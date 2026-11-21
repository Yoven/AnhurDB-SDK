"""AnhurDB Python SDK — the ``Memory`` client.

``Memory`` is the one client for the full REST API. This file holds only what
makes a ``Memory`` a ``Memory``: its construction (constructor and the two
factories), its lifecycle (``connect``/``close``/``async with``) and its
identity properties. Every endpoint family lives in its own module and comes
back in as a mixin:

  - ``write_add.py``          ``add()`` and the ingest / records doors
  - ``record_create.py``      ``create()``, ``create_in_session()``
  - ``record_links.py``       batch status + graph-link PATCH routes
  - ``record_listing.py``     ``query()``, ``recent()``, ``count_by_type()``
  - ``sessions.py``           session identity, registration, session reads
  - ``impersonation.py``      client-key token mint + refresh
  - ``search_scopes.py``, ``record_by_id.py``, ``record_delete_file.py``,
    ``record_graph.py``, ``manifests.py``, ``uploads.py``, ``entities.py``,
    ``profile.py`` — split earlier, unchanged.

Junior Tip [4.0.0 — why this file shrank from 1375 lines]: it was four times
the house 300-line cut, and the legacy removal (``AnhurClient``, the
deprecated method aliases, the ingest-404 fallback) touched it. House law: a
file over the cut is split before it is changed. Public names and signatures
are unchanged apart from the removals listed in CHANGELOG 4.0.0.
"""

import os
from datetime import datetime
from typing import Any, Dict, Optional

from .connection import HTTPConnection
from .container_tag import _derive_container_tag
from .entities import EntityMixin
from .exceptions import AnhurError
from .impersonation import ImpersonationMixin, _impersonate_tenant, _parse_expires_at
from .manifests import ManifestMixin
from .profile import ProfileMixin
from .record_by_id import RecordByIdMixin
from .record_create import RecordCreateMixin
from .record_delete_file import DeleteFileMixin
from .record_graph import RecordGraphMixin
from .record_links import RecordLinkMixin
from .record_listing import RecordListingMixin
from .search_parse import (
    _parse_search_response,
    _parse_search_results,
    _parse_typed_records,
)
from .search_scopes import SearchScopeMixin
from .sessions import SessionMixin
from .uploads import UploadMixin
from .write_add import AddMixin

# Junior Tip [why the three parsers are re-exported]: the search family moved
# to ``search.py``/``search_scopes.py``/``search_parse.py`` when this file was
# first split, but ``from anhurdb.client import _parse_search_results`` was
# already an import path in use (``tests/test_search_expand_related.py``).
# Re-exporting keeps a file split from becoming a breaking change.
__all__ = [
    "Memory",
    "DEFAULT_CLOUD_URL",
    "_parse_search_results",
    "_parse_search_response",
    "_parse_typed_records",
]

# Default cloud endpoint. Self-hosted users pass ``url`` explicitly.
DEFAULT_CLOUD_URL = "https://anhurdb.yoven.ai"


class Memory(
    AddMixin,
    RecordCreateMixin,
    RecordLinkMixin,
    RecordListingMixin,
    SessionMixin,
    ImpersonationMixin,
    SearchScopeMixin,
    RecordByIdMixin,
    DeleteFileMixin,
    RecordGraphMixin,
    ManifestMixin,
    UploadMixin,
    EntityMixin,
    ProfileMixin,
):
    """The one AnhurDB client. Dead-simple to start with, complete underneath.

    Handles session management and container tagging automatically.
    **Session-first writes:** call ``await create_session()`` before
    ``add()`` / ``create()``. ``container_tag`` aggregates recall/profile
    only — it is never a session substitute.

    Core methods:
        - ``create_session()`` — register a write session (required before writes)
        - ``add(text, mode="ingest")`` — store raw text (episodic + async extraction)
        - ``search(query)`` — find relevant memories (default scope: sessions)
        - ``profile()``    — get user/agent profile (``GET /profile?tag=``)

    Full surface matches the Go/TypeScript SDKs (see ``v2/PARITY_SPEC.md``):
    create/update/delete, search family, manifests, walk, entities, upload,
    batch ops, sessions, profile, grounding.

    Args:
        api_key:   AnhurDB API key (required). Falls back to
                   ``ANHUR_API_KEY`` environment variable.
        url:       Server URL (default: cloud endpoint).
        user_id:   Explicit container tag. When omitted, derived from
                   API key hash.
        tenant_id: Optional ``X-Tenant-ID`` header for multi-tenant.
        mode:      Transport — ``"rest"`` (default) or ``"mcp"``.
        timeout:   Per-request budget in seconds (default 30.0), applied to
                   every call this client makes, uploads included. There is no
                   client-side retry, so this is the whole wall clock a call
                   gets before ``AnhurConnectionError``."""

    def __init__(
        self,
        api_key: Optional[str] = None,
        url: str = DEFAULT_CLOUD_URL,
        user_id: Optional[str] = None,
        tenant_id: str = "",
        mode: str = "rest",
        timeout: float = 30.0,
    ):
        # Junior Tip [why a constructor parameter and never an env var]: the
        # right request budget is a property of the CALLER (a chat turn can
        # wait 5s; a 200 MB upload cannot), not of the machine the process
        # happens to run on. An env var would apply one number to every
        # Memory in the process and would be invisible at the call site — and
        # this project's rule is that a new knob is a debt: the value belongs
        # where the caller can see it.
        key = api_key or os.environ.get("ANHUR_API_KEY", "")
        if not key:
            raise ValueError(
                "api_key is required. Pass it directly or set ANHUR_API_KEY."
            )

        # Memory owns the single HTTPConnection (one TCP/HTTP pool). Every
        # mixin method issues its own request through this.
        self._connection = HTTPConnection(
            base_url=url,
            api_key=key,
            tenant_id=tenant_id,
            timeout=timeout,
            mode=mode,
        )

        # Container tag: explicit user_id or SHA-256 derived from API key.
        self._container_tag = user_id if user_id else _derive_container_tag(key)

        # Local session id (container_tag + UTC timestamp + 6 random hex).
        # create_session() / open_session() flip _session_registered after the
        # server ledger accepts it (see SessionMixin._resolve_write_session_id).
        self._session_uuid = self._generate_session_id()
        self._session_registered = False

        # Derived add()/create() issue exactly one request. When a session has
        # no episodic yet, the server returns HTTP 422 ("create an episodic
        # record first"), surfaced as AnhurQueryError. Callers write the
        # episodic first — the client never fabricates a synthetic anchor.

        # Client-key impersonation state (optional — set by from_client_key).
        self._client_key: Optional[str] = None
        self._impersonate_tenant_id: Optional[str] = None
        self._impersonate_expires_at: Optional[datetime] = None
        self._impersonate_expires_in: int = 3600

    @classmethod
    async def from_client_key(
        cls,
        client_key: str,
        tenant_id: str,
        *,
        expires_in: int = 3600,
        url: str = DEFAULT_CLOUD_URL,
        user_id: Optional[str] = None,
        mode: str = "rest",
        timeout: float = 30.0,
    ) -> "Memory":
        """Build a Memory by minting a short-lived tenant token from a client key.

        Junior Tip [STS]: the long-lived client_key only hits /api/v1/client/*;
        data-plane calls use the impersonation token with auto-refresh
        (``ImpersonationMixin._ensure_impersonation_fresh``).
        """
        if not client_key or not tenant_id:
            raise ValueError("client_key and tenant_id are required")
        minted = await _impersonate_tenant(
            base_url=url,
            client_key=client_key,
            tenant_id=tenant_id,
            expires_in=expires_in,
        )
        temp_key = minted.get("api_key") or ""
        if not temp_key.startswith("anhur_"):
            raise AnhurError("impersonate response missing api_key")
        memory = cls(
            api_key=temp_key,
            url=url,
            user_id=user_id,
            mode=mode,
            timeout=timeout,
        )
        memory._client_key = client_key
        memory._impersonate_tenant_id = tenant_id
        memory._impersonate_expires_in = int(minted.get("expires_in") or expires_in)
        memory._impersonate_expires_at = _parse_expires_at(minted.get("expires_at") or "")
        memory._connection._before_request = memory._ensure_impersonation_fresh
        return memory

    @classmethod
    def from_api_key(
        cls,
        api_key: str,
        *,
        url: str = DEFAULT_CLOUD_URL,
        user_id: Optional[str] = None,
        tenant_id: str = "",
        mode: str = "rest",
        timeout: float = 30.0,
    ) -> "Memory":
        """Explicit factory for tenant (or already-minted impersonation) keys."""
        return cls(
            api_key=api_key,
            url=url,
            user_id=user_id,
            tenant_id=tenant_id,
            mode=mode,
            timeout=timeout,
        )

    # -- Lifecycle ----------------------------------------------------------

    async def connect(self) -> None:
        """Open the HTTP session (idempotent)."""
        await self._ensure_impersonation_fresh()
        await self._connection.connect()

    async def close(self) -> None:
        """Close the HTTP session and release resources."""
        await self._connection.close()

    async def __aenter__(self) -> "Memory":
        await self.connect()
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.close()

    # -- Properties ---------------------------------------------------------

    @property
    def session_id(self) -> str:
        """Current session UUID."""
        return self._session_uuid

    @property
    def container_tag(self) -> str:
        """Container tag for recall/profile aggregation only.

        This is NOT a session identifier and is never substituted for
        ``session_id`` on write paths."""
        return self._container_tag

    # -- Health and placeholders ---------------------------------------------

    async def health(self) -> Dict[str, Any]:
        """Check server health.

        Returns:
            Dict with ``status`` (``"healthy"``) and ``name`` fields.

        Raises:
            AnhurConnectionError: If the server is unreachable."""
        return await self._connection.get("/api/v1/health")

    async def forget(self, memory_id: Optional[int] = None) -> None:
        """Forget a specific memory or trigger cognitive decay.

        Not yet implemented — placeholder for the decay API.

        Args:
            memory_id: If provided, forget this specific memory.

        Raises:
            NotImplementedError: Always (until the API is available)."""
        raise NotImplementedError(
            "forget() is not yet available. "
            "Use delete() for hard removal or update(id, status='archived') "
            "for soft delete."
        )

    def __repr__(self) -> str:
        return (
            f"Memory(container_tag={self._container_tag!r}, "
            f"session={self._session_uuid!r})"
        )
