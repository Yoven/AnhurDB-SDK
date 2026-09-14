"""AnhurDB Python SDK — Memory client.

``Memory`` is the canonical client for the full REST API.
``AnhurClient`` is a deprecated alias with a localhost default URL.
"""

import base64
import hashlib
import json
import os
import secrets
import warnings
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional, Union

from .connection import HTTPConnection
from .exceptions import AnhurError, AnhurQueryError, AnhurUploadWaitTimeout
from .query_argument import compile_query_argument
from .search_parse import (
    _parse_search_response,
    _parse_search_results,
    _parse_typed_records,
)
from .entities import EntityMixin
from .manifests import ManifestMixin
from .profile import ProfileMixin
from .record_graph import RecordGraphMixin
from .search_scopes import SearchScopeMixin
from .session_stats import fetch_all_session_stats
from .uploads import UploadMixin
from ..models import (
    AddResult,
    CreateRequest,
    DeleteFileResult,
    EntityEdge,
    EntityModel,
    MemoryType,
    Record,
    RecordSummary,
    SearchResult,
    SessionStats,
)
from ..query import QueryBuilder
from ..version import USER_AGENT

# Junior Tip [why these three are imported but not called here]: the search
# family moved to ``search.py``/``search_scopes.py``/``search_parse.py`` when
# this file was split by domain, but ``from anhurdb.client import
# _parse_search_results`` was already an import path in use (this repo's own
# ``tests/test_search_expand_related.py`` uses it). Re-exporting keeps the
# split invisible to every existing importer — a file split must never become
# a breaking change.
__all__ = [
    "Memory",
    "AnhurClient",
    "DEFAULT_CLOUD_URL",
    "_parse_search_results",
    "_parse_search_response",
    "_parse_typed_records",
]


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Default cloud endpoint. Self-hosted users pass ``url`` explicitly.
DEFAULT_CLOUD_URL = "https://anhurdb.yoven.ai"

# Historical self-hosted default. Kept ONLY for the deprecated ``AnhurClient``
# subclass alias so its constructor behaves exactly as it did before the
# single-class collapse (the old ``AnhurClient`` defaulted to localhost, not
# the cloud endpoint).
_LEGACY_LOCAL_URL = "http://localhost:8080"

# Impersonation refresh skew (seconds before expires_at).
_IMPERSONATE_REFRESH_SKEW_SECONDS = 60


async def _impersonate_tenant(
    *,
    base_url: str,
    client_key: str,
    tenant_id: str,
    expires_in: int = 3600,
) -> Dict[str, Any]:
    """Mint a short-lived tenant token via POST /api/v1/client/impersonate."""
    import aiohttp

    if expires_in < 1:
        expires_in = 1
    if expires_in > 86400:
        expires_in = 86400
    url = base_url.rstrip("/") + "/api/v1/client/impersonate"
    headers = {
        "X-API-Key": client_key,
        "Content-Type": "application/json",
        "User-Agent": USER_AGENT,
    }
    payload = {"tenant_id": tenant_id, "expires_in": expires_in}
    timeout = aiohttp.ClientTimeout(total=30)
    async with aiohttp.ClientSession(timeout=timeout) as session:
        async with session.post(url, json=payload, headers=headers, allow_redirects=False) as response:
            body = await response.read()
            if response.status >= 400:
                detail = body.decode("utf-8", errors="replace")[:400]
                raise AnhurError(
                    f"impersonate failed HTTP {response.status}: {detail}"
                )
            return json.loads(body.decode("utf-8"))


# ---------------------------------------------------------------------------
# Helper: derive a stable container tag from the API key
# ---------------------------------------------------------------------------

def _derive_container_tag(api_key: str) -> str:
    """Derive a short, stable hex tag from the API key using SHA-256.

    The first 12 hex characters of the hash are used, prefixed with
    ``mem-``. This matches the algorithm in the TypeScript and Go SDKs
    so the same API key always produces the same container tag across
    all three languages.

    Args:
        api_key: The raw API key string.

    Returns:
        A container tag like ``mem-a1b2c3d4e5f6``."""
    digest = hashlib.sha256(api_key.encode("utf-8")).hexdigest()
    return f"mem-{digest[:12]}"


def _build_metadata_json(
    container_tag: str,
    extra_metadata: Optional[Dict[str, Any]] = None,
) -> str:
    """Wrap ``container_tag`` into the canonical metadata JSON envelope
    ``{"container_tag": "<tag>"}``.

    Returns ``"{}"`` when container_tag is empty so the column always holds a
    parseable JSON object."""
    envelope: Dict[str, Any] = {}
    if extra_metadata:
        envelope.update(extra_metadata)
    if container_tag:
        envelope["container_tag"] = container_tag
    if not envelope:
        return "{}"
    return json.dumps(envelope)


def _utc_timestamp() -> str:
    """Return current UTC time as ``YYYYMMDD-HHMMSS``."""
    now = datetime.now(timezone.utc)
    return now.strftime("%Y%m%d-%H%M%S")


def _add_result_from_record(
    data: Any,
    *,
    session_id: str,
    record_type: str,
    summary: str,
) -> AddResult:
    """Normalise a ``POST /api/v1/records`` response into an ``AddResult``.

    Junior Tip [why the SDK synthesises ``records`` here]: ``/records`` echoes
    the CREATED RECORD, not a write receipt — there is no ``records`` array in
    its body. ``/ingest`` does send one. Without this adapter the same public
    method answered with two different shapes depending on which door it took,
    and ``result["records"][0]`` was a ``KeyError`` waiting for the first
    caller who pinned a score. The summary and type are the ones WE sent, so
    the synthesised entry is a faithful description of what was written — not a
    guess.
    """
    record = data if isinstance(data, dict) else {}
    record_id = record.get("id", 0)
    return AddResult(
        session_id=record.get("session_id") or session_id,
        records=[
            RecordSummary(
                id=record_id if isinstance(record_id, int) else 0,
                type=record.get("type") or record_type,
                summary=record.get("summary") or summary,
            )
        ],
        mode="oss",
        id=record_id if isinstance(record_id, int) else None,
        status=record.get("status"),
    )


# ---------------------------------------------------------------------------
# Memory — the single canonical client (simple ergonomics + full surface)
# ---------------------------------------------------------------------------

class Memory(
    SearchScopeMixin,
    RecordGraphMixin,
    ManifestMixin,
    UploadMixin,
    EntityMixin,
    ProfileMixin,
):
    """The one AnhurDB client. Dead-simple to start with, complete underneath.

    Handles session management, container tagging, and cloud/OSS fallback
    automatically. **Session-first writes:** call ``await create_session()``
    before ``add()`` / ``create()``. ``container_tag`` aggregates recall/profile
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
        # where the caller can see it. 30.0 is the exact default
        # ``HTTPConnection`` already hardcoded, so nothing changes for anyone
        # who does not pass it.
        key = api_key or os.environ.get("ANHUR_API_KEY", "")
        if not key:
            raise ValueError(
                "api_key is required. Pass it directly or set ANHUR_API_KEY."
            )

        # Memory owns the single HTTPConnection (one TCP/HTTP pool). No facade
        # indirection — every method below issues its own request through this.
        self._connection = HTTPConnection(
            base_url=url,
            api_key=key,
            tenant_id=tenant_id,
            timeout=timeout,
            mode=mode,
        )

        # Container tag: explicit user_id or SHA-256 derived from API key.
        if user_id:
            self._container_tag = user_id
        else:
            self._container_tag = _derive_container_tag(key)

        # Local session id (container_tag + UTC timestamp + 6 random hex).
        # Junior Tip [sessionRegistered]: a local uuid is NOT enough to write.
        # create_session() / open_session() flip _session_registered after the
        # server ledger accepts it. container_tag is never a session fallback.
        self._session_uuid = self._generate_session_id()
        self._session_registered = False

        # Derived add()/create() issue exactly one request. When a session has
        # no episodic yet, the server returns HTTP 422 ("create an episodic
        # record first"), surfaced as AnhurQueryError. Callers write the
        # episodic first — the client never fabricates a synthetic anchor.

        # Cloud ingest availability (None = untested).
        self._ingest_available: Optional[bool] = None

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
        data-plane calls use the impersonation token with auto-refresh.
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
        expires_raw = minted.get("expires_at") or ""
        try:
            memory._impersonate_expires_at = datetime.fromisoformat(
                expires_raw.replace("Z", "+00:00")
            )
        except ValueError:
            memory._impersonate_expires_at = datetime.now(timezone.utc)
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

    async def _ensure_impersonation_fresh(self) -> None:
        """Re-mint when within skew of expires_at."""
        if not self._client_key or not self._impersonate_tenant_id:
            return
        now = datetime.now(timezone.utc)
        expires_at = self._impersonate_expires_at
        if expires_at is not None and expires_at.tzinfo is None:
            expires_at = expires_at.replace(tzinfo=timezone.utc)
        if expires_at is not None:
            remaining = (expires_at - now).total_seconds()
            if remaining > _IMPERSONATE_REFRESH_SKEW_SECONDS:
                return
        minted = await _impersonate_tenant(
            base_url=self._connection.base_url,
            client_key=self._client_key,
            tenant_id=self._impersonate_tenant_id,
            expires_in=self._impersonate_expires_in,
        )
        temp_key = minted.get("api_key") or ""
        if not temp_key.startswith("anhur_"):
            raise AnhurError("impersonate refresh missing api_key")
        self._connection.set_api_key(temp_key)
        expires_raw = minted.get("expires_at") or ""
        try:
            self._impersonate_expires_at = datetime.fromisoformat(
                expires_raw.replace("Z", "+00:00")
            )
        except ValueError:
            self._impersonate_expires_at = datetime.now(timezone.utc)

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

    # ── Health ─────────────────────────────────────────────────────

    async def health(self) -> Dict[str, Any]:
        """Check server health.

        Returns:
            Dict with ``status`` (``"healthy"``) and ``name`` fields.

        Raises:
            AnhurConnectionError: If the server is unreachable."""
        return await self._connection.get("/api/v1/health")

    # -- Core: add() --------------------------------------------------------

    async def add(
        self,
        text: str,
        *,
        mode: str = "ingest",
        score: Optional[int] = None,
        type: Optional[MemoryType] = None,
        metadata: Optional[Dict[str, Any]] = None,
        session_id: str = "",
    ) -> AddResult:
        """Store text in the current session (session-first write contract).

        Call ``await create_session()`` (or ``await open_session()``) before the
        first write. ``session_id`` is always sent — from the ``session_id``
        argument or ``self.session_id``.

        Agent UX — pick the write path explicitly via ``mode``:
        - ``mode="ingest"`` (default) → ``POST /api/v1/ingest`` with
          ``content``, ``container_tag``, ``session_id`` (extraction LLM).
        - ``mode="regular"`` → ``POST /api/v1/records`` (typed create;
          default type episodic). Use when you need ``score`` / ``type`` /
          ``metadata`` persisted without extraction.

        Trap (PARITY_SPEC.md ~L84): pinning ``score``, ``type``, or a
        non-empty ``metadata`` also forces ``/records`` even under
        ``mode="ingest"`` — the ingest endpoint's body has no field for any
        of the three, so a plain ingest call would silently drop the pin.
        Mirrors Go (``client.go`` ``Add`` / ``forceRecordsPath``) and
        TypeScript (``memory.ts`` ``add`` / ``forceRecordsPath``); all three
        SDKs now agree. Plain ``add(text)`` (no pins) prefers ingest; 404
        falls back to ``/records`` (OSS).

        Args:
            text:       The text to remember (required, non-empty).
            mode:       ``"ingest"`` or ``"regular"``.
            score:      Importance 1-10. Pinning it (any mode) forces the
                        ``/records`` path.
            type:       Memory type. Pinning it (any mode) forces the
                        ``/records`` path.
            metadata:   Optional metadata. A non-empty dict (any mode)
                        forces the ``/records`` path; an empty dict or
                        ``None`` does not (matches Go's
                        ``len(cfg.metadata) > 0`` guard).
            session_id: Override session; defaults to ``self.session_id``.

        Returns:
            ``AddResult``. ``mode`` tells you WHICH door ran: ``"cloud"``
            means the server's extraction pipeline accepted the text and
            ``records`` may hold MORE than one row; ``"oss"`` means the SDK
            wrote exactly one record directly. Reading ``records`` without
            reading ``mode`` is how "the extractor found three facts" gets
            mistaken for "I wrote three records".

        Raises:
            ValueError: If ``text`` is empty or ``mode`` is invalid.

        Example::

            session_id = await mem.create_session()
            await mem.add("User prefers dark mode", mode="ingest",
                          session_id=session_id)
            # Pinned score forces /records even though mode="ingest" — the
            # score is honored, not silently dropped.
            await mem.add("Pinned fact", mode="ingest", score=8,
                          type=MemoryType.PREFERENCE)"""
        if not text:
            raise ValueError("text cannot be empty")
        if mode not in ("ingest", "regular"):
            raise ValueError('mode must be "ingest" or "regular"')

        # PARITY_SPEC.md ~L84 / L22: pinning score, type, OR metadata forces
        # the synchronous /records create path even under mode="ingest" —
        # mirrors Go (client.go Add, forceRecordsPath) and TypeScript
        # (memory.ts add, forceRecordsPath). An empty metadata dict is NOT a
        # pin, matching Go's `len(cfg.metadata) > 0` check — only a
        # non-empty dict counts.
        #
        # Junior Tip [never let a pin evaporate]: POST /api/v1/ingest's body
        # is only {content, container_tag, session_id} — score, type, and
        # metadata have no field to land in on that endpoint. Before this
        # fix, the routing decision only looked at `mode`, so
        # add(text, mode="ingest", score=8) still called _try_ingest()
        # first; whenever ingest succeeded (the common case), the pinned
        # score was silently discarded — no exception, no warning, the
        # caller just got back a record that quietly did not have the
        # importance it asked for. That is exactly the class of bug this
        # project treats as the worst kind: silent data loss (see
        # CLAUDE.md "fail loud, never silently lose"). Route pinned calls
        # straight to _create_record(), the only path that actually
        # persists all three fields.
        pinned = score is not None or type is not None or bool(metadata)

        if mode == "ingest" and not pinned:
            if self._ingest_available is not False:
                ingest_result = await self._try_ingest(text, session_id)
                if ingest_result is not None:
                    return ingest_result

        return await self._create_record(text, score, type, metadata, session_id)

    # ── Memory CRUD ────────────────────────────────────────────────

    async def create(
        self,
        session_id: str,
        content: str,
        *,
        type: Optional[Union[MemoryType, str]] = None,
        score: Optional[int] = None,
        status: Optional[str] = None,
        related_ids: Optional[List[int]] = None,
        valid_from: Optional[str] = None,
        valid_until: Optional[str] = None,
        metadata: Optional[Dict[str, Any]] = None,
    ) -> AddResult:
        """Create exactly one typed record (no extraction).

        Agent UX — write path: use when you already know ``type`` + content.
        For raw text use ``add(text, mode="ingest")`` / MCP ``ingest_memory``.
        Hits ``POST /api/v1/records``.

        BREAKING in 3.0.0 — this used to take a single ``CreateRequest``. The
        migration is mechanical::

            await mem.create(req)
            # becomes
            await mem.create(
                req.session_id or req.uuid,
                req.content,
                **req.optional_fields(),
            )

        Junior Tip [why session and content are required POSITIONALS]:
        ``POST /api/v1/records`` cannot succeed without an anchor session and a
        body — a call missing either is an HTTP 422 every single time. Making
        them positional moves that failure from a network round trip to the
        function signature, and it is the ONE convention all three SDKs now
        share (Go ``Create(ctx, sessionUUID, content, opts...)``, TypeScript
        ``create(sessionUuid, text, options?)``). The previous
        ``session_id``-or-legacy-``uuid`` guess is gone on purpose: a client
        that picks between two fields for you is a client that can pick wrong
        and never tell you.

        Args:
            session_id:  Session from ``create_session()`` (required).
            content:     The record body (required).
            type:        Memory type — a ``MemoryType`` or its plain string
                         value. Server default ``episodic`` when unset.
            score:       Importance 1-10. Server default 5 when unset.
            status:      Record status. Server default ``saved`` when unset.
            related_ids: Graph edges to sibling records.
            valid_from:  RFC3339 start of the validity window. Delivered as a
                         METADATA key — this route ignores the top-level field.
            valid_until: RFC3339 end of the validity window, same delivery.
            metadata:    Caller metadata, merged under the SDK container tag.

        Returns:
            ``AddResult`` — ``id``/``status`` come from the created record,
            ``records`` carries the one summary, ``mode`` is ``"oss"`` (this is
            the direct ``/records`` door, never the extraction pipeline).

        Raises:
            ValueError: If ``session_id`` or ``content`` is empty.
            AnhurQueryError: HTTP 422 when the anchor does not exist. Never
                fabricate a synthetic anchor client-side — 422 stays 422."""
        resolved_session_id = (session_id or "").strip()
        if not resolved_session_id:
            raise ValueError(
                "session_id is required — create a session first "
                "(await create_session())"
            )
        if not content:
            raise ValueError("content cannot be empty")

        # Junior Tip [why the payload is still built through CreateRequest]:
        # the model owns the server's own defaults (weight seeding, the
        # embedding fields that must be sent as zero values). Rebuilding the
        # dict by hand here would fork that knowledge into a second place.
        request_fields: Dict[str, Any] = {
            "session_id": resolved_session_id,
            "content": content,
            "summary": content[:200] + "..." if len(content) > 200 else content,
        }
        if type is not None:
            request_fields["type"] = type
        if score is not None:
            request_fields["score"] = score
            request_fields["weight"] = round(score / 10, 4)
        if status is not None:
            request_fields["status"] = status
        if related_ids is not None:
            request_fields["related_ids"] = related_ids

        request = CreateRequest(**request_fields)
        payload = request.model_dump(exclude_none=True)

        # Junior Tip [the bi-temporal window travels in METADATA on this route]:
        # ``service/record_create.go`` only reads ``valid_from``/``valid_until``
        # from the metadata JSON; the top-level fields of the REST create body
        # are never filled by this handler. A caller who sent them as top-level
        # keys got HTTP 200 and a record with NO validity window — the pin
        # evaporated with no error to read. Go folds them into the envelope for
        # exactly this reason (``golang/client/parity.go:83-98``); Python now
        # matches, so the same call produces the same record in both arms.
        window_metadata: Dict[str, Any] = dict(metadata) if metadata else {}
        if valid_from is not None:
            window_metadata["valid_from"] = valid_from
        if valid_until is not None:
            window_metadata["valid_until"] = valid_until

        # Junior Tip [the legacy ``uuid`` wire alias is GONE, 2026-09-14]: the
        # server reads ``session_id`` (server/handler/record_create.go); the
        # duplicate ``uuid`` key was belt-and-braces for a server generation
        # that no longer exists. Confirmed by a live create against
        # https://anhurdb.yoven.ai in a disposable ``paridade-`` session before
        # removal. Sending both is how a stale field survives for years.
        payload.pop("uuid", None)
        payload["session_id"] = resolved_session_id

        # Inject the SDK-owned container_tag into metadata (same as add() and
        # the Go/TS create paths) so records stay visible to container-scoped
        # search/profile.
        payload["metadata"] = _build_metadata_json(
            self._container_tag, window_metadata or None
        )

        # One request. Missing episodic anchor → HTTP 422, surfaced as
        # AnhurQueryError. Never fabricate a synthetic anchor client-side.
        data = await self._connection.post("/api/v1/records", payload)
        # Junior Tip [read the type back off the VALIDATED model]: callers pass
        # either ``MemoryType.FACT`` or the plain string ``"fact"`` (this repo's
        # own AST harness passes strings), and ``type.value`` on a ``str`` is an
        # AttributeError that only fires on the string path. ``CreateRequest``
        # has already coerced both into the enum by here, so there is exactly
        # one shape left to read.
        resolved_type = request.type
        return _add_result_from_record(
            data,
            session_id=resolved_session_id,
            record_type=getattr(resolved_type, "value", str(resolved_type)),
            summary=request_fields["summary"],
        )

    async def get(
        self,
        record_id: int,
    ) -> Dict[str, Any]:
        """Get a record's metadata by ID.

        Args:
            record_id: The record ID.

        Returns:
            Record metadata dict."""
        return await self._connection.get(
            f"/api/v1/records/{record_id}"
        )

    async def update(self, record_id: int, **fields: Any) -> None:
        """Partially update a record.

        Args:
            record_id: The record ID to update.
            **fields:  Keyword arguments for fields to update
                       (e.g. ``summary="new"``, ``status="archived"``).

        Raises:
            ValueError: if ``score`` is among the fields — use
                :meth:`set_score`, which reaches the endpoint that
                actually persists it.

        Example::

            await mem.update(42, summary="Updated summary")
            await mem.set_score(42, 8)"""
        if "score" in fields:
            raise ValueError(
                "update() cannot change score: PATCH /api/v1/records/{id} has no "
                "score field and answers 200 while dropping the key. Use "
                "set_score(record_id, score), which posts to "
                "/api/v1/records/set-score (a replicated command). Measured "
                "2026-08-15; this call used to succeed and write nothing."
            )
        await self._connection.patch(f"/api/v1/records/{record_id}", fields)

    async def set_score(self, record_id: int, score: int) -> None:
        """Set a record's importance score (1-10) durably.

        ``update(record_id, score=...)`` cannot do this: the PATCH handler has
        no score field, so it returns success and changes nothing — the same
        shape as an earlier defect with ``archived``. This method posts to
        ``/api/v1/records/set-score``, which dispatches a replicated command
        and invalidates the read cache.

        The score feeds the value term of the cognitive weight, which in turn
        selects the record's embedding fidelity band — so a correction here
        propagates to ranking on the next maintenance pass, not instantly.

        Args:
            record_id: The record ID.
            score:     Importance, 1-10.

        Raises:
            ValueError: if ``score`` is outside 1-10.

        Example::

            await mem.set_score(42, 8)"""
        if score < 1 or score > 10:
            raise ValueError("score must be between 1 and 10, got %r" % (score,))
        await self._connection.post(
            "/api/v1/records/set-score", {"ids": [record_id], "score": score}
        )

    async def delete(self, record_id: int) -> None:
        """Archive a record by ID (soft delete — the server sets ``archived=1``
        and answers "record archived": the record becomes invisible to default
        queries but is NOT physically removed; no hard-delete route exists for
        a record id).

        Args:
            record_id: The record ID to archive."""
        await self._connection.delete(f"/api/v1/records/{record_id}")

    async def delete_file(
        self,
        session_uuid: str,
        ingest_key_prefix: str,
        *,
        dry_run: bool = False,
    ) -> DeleteFileResult:
        """Remove EVERY record produced by one ingested file inside a session —
        root, chapters and satellites — via ``DELETE /api/v1/records/by-file``.

        Junior Tip [por que este método existe — o delete que mentia]:
        ``delete(id)`` apaga UM record. Um arquivo ingerido vira centenas deles
        (root + capítulos + satélites), então apagar a ficha do arquivo deixava
        o CONHECIMENTO vivo: a busca continuava respondendo com o livro
        "apagado". Sem ``delete_file`` não existe "trocar a edição velha pela
        nova" — as duas coexistiriam para sempre, disputando a mesma busca.

        A identidade do arquivo é o prefixo ``<checksum16>`` que o file_ingestor
        grava em ``metadata.ingest_key`` (root: ``"<c16>:root"``; capítulo:
        ``"<c16>:<total>:<índice>"``) e em ``metadata.chapter_ingest_key``
        (satélite). Um prefixo alcança as três formas.

        A remoção é soft-delete REPLICADO (tombstone via Raft): o registro fica
        archived/status=deleted, some da busca e do tier analítico, e continua
        restaurável — a auditoria não é perdida.

        Junior Tip [dry_run é a rede de segurança, não um detalhe de debug]: com
        ``dry_run=True`` o servidor resolve o MESMO conjunto e devolve
        ``matched_count`` sem escrever nada. A interface mostra "isto vai apagar
        511 registros" ANTES de o usuário confirmar; apagar um documento inteiro
        é operação de mão pesada, e a contagem prévia é o que separa "removi a
        edição velha da lei" de "removi a biblioteca".

        Args:
            session_uuid:      Session that owns the file's records.
            ingest_key_prefix: The file's ``<checksum16>`` identity.
            dry_run:           Count only, write nothing (default: False).

        Returns:
            ``DeleteFileResult`` with ``matched_count`` / ``deleted_count`` —
            a contagem real, para que "apaguei 0" nunca se disfarce de sucesso.

        Raises:
            ValueError:      If either argument is empty/blank (fails locally,
                             without a round trip).
            AnhurQueryError: HTTP 400 when the prefix is shorter than 8
                             characters or carries invalid characters — essa
                             regra vive no servidor, fonte única da verdade.

        Example::

            preview = await mem.delete_file(session, "ef9976f1ef5d5176", dry_run=True)
            if preview.matched_count and confirmed:
                await mem.delete_file(session, "ef9976f1ef5d5176")"""
        # Validação local mínima: o vazio nunca merece uma ida ao servidor, e o
        # erro local nomeia o argumento do SDK. Tamanho mínimo e conjunto de
        # caracteres do prefixo continuam a ser do servidor — uma fonte da
        # verdade só, para os três SDKs não divergirem dela.
        trimmed_session_uuid = (session_uuid or "").strip()
        if not trimmed_session_uuid:
            raise ValueError("delete_file: session_uuid is required")
        trimmed_ingest_key_prefix = (ingest_key_prefix or "").strip()
        if not trimmed_ingest_key_prefix:
            raise ValueError("delete_file: ingest_key_prefix is required")

        response = await self._connection.delete(
            "/api/v1/records/by-file",
            params={
                "session": trimmed_session_uuid,
                "ingest_key_prefix": trimmed_ingest_key_prefix,
                "dry_run": "true" if dry_run else "false",
            },
        )
        return DeleteFileResult.model_validate(response or {})

    async def read_content(
        self,
        record_id: int,
    ) -> Any:
        """Read the full content payload for a record.

        Args:
            record_id: The record ID to read.

        Returns:
            The content payload. Type depends on what was stored:
            a dict for structured records, a string for plain text."""
        return await self._connection.get(
            f"/api/v1/records/{record_id}/content",
            raw_text=True,
                    )

    async def query(
        self,
        ast: Any,
        session_uuid: Optional[str] = None,
    ) -> List[Record]:
        """Execute an AST query against AnhurDB (``POST /api/v1/query``).

        If ``session_uuid`` is provided it is injected as a ``uuid`` filter so
        results are scoped to that session. The server expects the AST FLAT at the
        top level of the body (filters, pagination, sort, select) — NOT wrapped in
        a ``{"query": ...}`` key.

        Args:
            ast:          A compiled AST dict, or a QueryBuilder/Filter instance.
            session_uuid: Optional session UUID to scope results.

        Returns:
            List of ``Record`` objects matching the query.

        Example::

            from anhurdb.query import QueryBuilder
            qb = QueryBuilder().where(type="risk", score__gte=7).limit(20)
            records = await mem.query(qb, session_uuid="s1")"""
        compiled_ast = compile_query_argument(ast, session_uuid)

        # Server expects the AST flat at top-level. Do NOT wrap in {"query": ast}.
        data = await self._connection.post(
            "/api/v1/query", compiled_ast
        )
        # result as {"records": null} (a nil Go slice -> JSON null), so .get("records", [])
        # returns None (key present) and iterating it raises TypeError. `or []` coalesces
        # null/None to [], matching Go (wrapped.Records == nil -> []) and TS (records ?? []).
        records_data = (data.get("records") or []) if isinstance(data, dict) else []
        return [Record(**record_fields) for record_fields in records_data]

    async def search_with_ast(
        self,
        filter_builder: Any,
        session_uuid: Optional[str] = None,
    ) -> List[Record]:
        """Deprecated: use :meth:`query` instead.

        Forwarding alias kept so existing callers keep working after the canonical
        rename to ``query`` (matching Go ``Query`` / TS ``query``)."""
        warnings.warn(
            "search_with_ast() is deprecated; use query().",
            DeprecationWarning,
            stacklevel=2,
        )
        return await self.query(filter_builder, session_uuid)

    # ── Batch Operations ───────────────────────────────────────────

    async def batch_read_content(
        self,
        ids: List[int],
    ) -> Dict[str, Any]:
        """Fetch full content for multiple records in a single call (max 100).

        Eliminates the N+1 pattern of calling ``read_content`` in a loop.

        Args:
            ids:       List of record IDs (max 100).

        Returns:
            Dict mapping ``record_id → content_payload``."""
        data = await self._connection.post(
            "/api/v1/records/batch-content",
            {"ids": ids},
                    )
        return data if isinstance(data, dict) else {}

    async def batch_update_status(self, ids: List[int], status: str) -> Dict[str, Any]:
        """Update the status for a batch of records at once.

        Args:
            ids:    List of record IDs to update.
            status: New status (e.g. consolidated, hubbed, processing,
                    completed, failed).

        Returns:
            Confirmation dict with count of updated records."""
        return await self._connection.patch(
            "/api/v1/records/mark-consolidated",
            {"ids": ids, "status": status},
        )

    async def mark_consolidated(self, ids: List[int]) -> Dict[str, Any]:
        """Deprecated: use :meth:`batch_update_status` instead.

        Kept as a forwarding alias so existing callers keep working after the canonical rename."""
        warnings.warn(
            "mark_consolidated() is deprecated; use batch_update_status(ids, status).",
            DeprecationWarning,
            stacklevel=2,
        )
        # Historical behavior: mark_consolidated always meant status="consolidated".
        return await self.batch_update_status(ids, "consolidated")

    async def link_consolidated(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """Set the parent consolidated record for a batch of child records.

        Links child records to their summary ("star") record after
        consolidation. Batched so N children pointing at the same star cost
        ONE server round-trip instead of N.

        Args:
            ids:             List of child record IDs.
            consolidate_id:  ID of the summary (parent) record.

        Returns:
            Confirmation dict (empty when ``ids`` is empty — no-op)."""
        if not ids:
            return {}
        if consolidate_id <= 0:
            raise AnhurError("link_consolidated: consolidate_id must be > 0")
        return await self._connection.patch(
            "/api/v1/records/consolidate-ids",
            {"ids": ids, "consolidate_id": consolidate_id},
        )

    async def link_to_consolidated(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """Deprecated: use :meth:`link_consolidated` instead.

        Kept as a forwarding alias so existing callers keep working after the
        canonical rename."""
        warnings.warn(
            "link_to_consolidated() is deprecated; use link_consolidated().",
            DeprecationWarning,
            stacklevel=2,
        )
        return await self.link_consolidated(ids, consolidate_id)

    async def update_consolidate_ids(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """Deprecated: use :meth:`link_consolidated` instead.

        Kept as a forwarding alias so existing callers keep working after the canonical rename."""
        warnings.warn(
            "update_consolidate_ids() is deprecated; use link_consolidated().",
            DeprecationWarning,
            stacklevel=2,
        )
        return await self.link_consolidated(ids, consolidate_id)

    async def append_main_ids(
        self,
        record_id: int,
        main_ids: List[int],
    ) -> Dict[str, Any]:
        """Append parent record IDs to the ``main_ids`` array of a single record.

        Server-side this reads, deduplicates, and writes back — idempotent on
        Go ``Memory.AppendMainIDs`` and TS ``appendMainIds``.

        Args:
            record_id: Child record that receives the parents.
            main_ids:  Parent IDs to append.

        Returns:
            Confirmation dict (empty when ``main_ids`` is empty — no-op)."""
        if record_id <= 0:
            raise AnhurError("append_main_ids: record_id must be > 0")
        if not main_ids:
            return {}
        payload = {"ids": [record_id], "main_ids_to_append": main_ids}
        return await self._connection.patch("/api/v1/records/append-main-ids", payload)

    async def append_related_ids(
        self,
        record_id: int,
        related_ids: List[int],
    ) -> Dict[str, Any]:
        """Append related record IDs to the ``related_ids`` array of a single record.

        Server-side this reads, deduplicates, and writes back — idempotent on
        mirror of ``append_main_ids`` on the sibling REST route
        ``PATCH /api/v1/records/append-related-ids`` (payload key
        ``related_ids_to_append``); keeps the Go/Python/TS SDK trio in lockstep
        (parity invariant #13). Append, never replace.

        Args:
            record_id:   Record that receives the related links.
            related_ids: Related IDs to append.

        Returns:
            Confirmation dict (empty when ``related_ids`` is empty — no-op)."""
        if record_id <= 0:
            raise AnhurError("append_related_ids: record_id must be > 0")
        if not related_ids:
            return {}
        payload = {"ids": [record_id], "related_ids_to_append": related_ids}
        return await self._connection.patch("/api/v1/records/append-related-ids", payload)

    async def append_main_links(
        self,
        ids: List[int],
        main_ids_to_append: List[int],
    ) -> Dict[str, Any]:
        """Append parent record IDs to a BATCH of records (non-destructive).

        Does NOT replace existing ``main_ids`` — only adds new links. Use this
        to build parent-child relationships in the knowledge graph across many
        records at once. For a single record, prefer ``append_main_ids``.

        Args:
            ids:                 Records to update.
            main_ids_to_append:  Parent IDs to add to each record's ``main_ids``.

        Returns:
            Confirmation dict."""
        return await self._connection.patch(
            "/api/v1/records/append-main-ids",
            {"ids": ids, "main_ids_to_append": main_ids_to_append},
        )

    # ── Session Management ─────────────────────────────────────────

    def _generate_session_id(self) -> str:
        """Generate a local session id (does not register on the server).

        Format: ``<container_tag>-<YYYYMMDD-HHMMSS>-<6hex>``, byte-for-byte
        identical to Go ``NewSession`` and TS ``newSession``."""
        return (
            f"{self._container_tag}-{_utc_timestamp()}-{secrets.token_hex(3)}"
        )

    def _resolve_write_session_id(self, session_id: str = "") -> str:
        """Return the session id to attach to write payloads.

        Explicit ``session_id`` is passed through (server validates registration).
        Otherwise the client session must already be registered via
        ``create_session`` / ``open_session``.
        """
        explicit_session_id = (session_id or "").strip()
        if explicit_session_id:
            return explicit_session_id
        if not self._session_registered or not self._session_uuid:
            raise ValueError(
                "session_id is required — create a session first "
                "(POST /api/v1/sessions)"
            )
        return self._session_uuid

    async def create_session(
        self,
        metadata: Optional[Dict[str, Any]] = None,
        session_id: Optional[str] = None,
    ) -> str:
        """Register a write session with the server (required before writes).

        Posts ``POST /api/v1/sessions`` with optional ``session_id`` / ``metadata``.
        When ``session_id`` is omitted the server generates one (same as TypeScript
        ``createSession()``, Go ``CreateSession``, MCP ``create_session``).
        Sets ``self.session_id`` from the response.

        To register a caller-chosen id after ``new_session()``::

            await mem.create_session(session_id=mem.new_session())

        Or use ``open_session()`` (local generate + register in one call).

        Args:
            metadata:   Optional JSON object copied onto session records.
            session_id: Optional uuid to register (e.g. from ``new_session()``).

        Returns:
            The registered session id."""
        payload: Dict[str, Any] = {}
        if session_id:
            payload["session_id"] = session_id
        if metadata is not None:
            payload["metadata"] = metadata

        response_data = await self._connection.post(
            "/api/v1/sessions",
            payload,
        )
        registered_session_id = str(response_data.get("session_id", ""))
        if not registered_session_id:
            raise AnhurQueryError(
                "create_session: server returned empty session_id"
            )
        self._session_uuid = registered_session_id
        self._session_registered = True
        return registered_session_id

    async def open_session(
        self,
        metadata: Optional[Dict[str, Any]] = None,
    ) -> str:
        """Generate a fresh session id locally and register it on the server.

        Convenience wrapper around ``new_session()`` + ``create_session()``.

        Returns:
            The registered session id."""
        local_session_id = self._generate_session_id()
        return await self.create_session(
            metadata=metadata,
            session_id=local_session_id,
        )

    def new_session(self) -> str:
        """Generate a fresh local session id (does NOT register on the server).

        Prefer ``await create_session()`` or ``await open_session()`` before
        writes. To reuse this id, pass it to ``create_session(session_id=...)``:

        ``await mem.create_session(session_id=mem.new_session())``

        Returns:
            The new local session UUID."""
        self._session_uuid = self._generate_session_id()
        self._session_registered = False
        return self._session_uuid

    async def list_sessions(
        self,
    ) -> List[SessionStats]:
        """List ALL sessions with aggregate statistics, following pagination.

        The endpoint defaults to ``limit=50`` and reports the truncation in
        ``has_more``; this used to ignore it, so a 99-session tenant answered
        50 and looked complete. Loop and runaway brakes, with the WHY, live in
        ``session_stats.py``.

        Returns:
            One ``SessionStats`` per session — the tenant, never a single page.

            Junior Tip [the key is ``last_activity``, not ``last_active``]:
            the session row and the PROFILE stats block use two different
            spellings for the same idea, and both are correct on the wire
            (``database/list_sessions.go:38`` vs ``handler/profile.go:49``).
            TypeScript had this one wrong and read an always-undefined field."""
        rows = await fetch_all_session_stats(self._connection)
        return [SessionStats.model_validate(row) for row in rows]

    async def list_chat(
        self,
        session_uuid: str,
        *,
        consolidated: Optional[bool] = None,
        status: Optional[str] = None,
    ) -> List[Dict[str, Any]]:
        """List all records in a specific session (metadata only, no .gz body).

        Args:
            session_uuid: The session UUID.
            consolidated: Tri-state filter. ``None`` = all; ``True`` = only
                          consolidated; ``False`` = only non-consolidated.
            status:       Optional exact status filter (e.g. ``"saved"``).

        Returns:
            List of record dicts."""
        params: Dict[str, str] = {}
        if consolidated is not None:
            # Server parses (val == "true"); send the canonical lowercased form.
            params["consolidated"] = "true" if consolidated else "false"
        if status:
            params["status"] = status
        data = await self._connection.get(
            f"/api/v1/chats/{session_uuid}",
            params=params or None,
                    )
        return data.get("records", data) if isinstance(data, dict) else data

    async def get_session_history(
        self,
        session_uuid: str,
        limit: int = 50,
        offset: int = 0,
    ) -> Dict[str, Any]:
        """Get paginated full-text history for a session.

        Returns actual message content from the filesystem, unlike
        ``list_chat`` which returns metadata only.

        Args:
            session_uuid: The session UUID.
            limit:        Max records per page (default 50).
            offset:       Pagination offset.

        Returns:
            Dict with ``records``, ``total_records``, ``returned_count``."""
        return await self._connection.get(
            f"/api/v1/sessions/{session_uuid}/history",
            params={"limit": str(limit), "offset": str(offset)},
                    )

    async def get_session_clusters(
        self,
        session_uuid: str,
    ) -> Dict[str, Any]:
        """Get mathematically clustered topological groups for a session.

        Uses vector similarity and clustering to identify thematic groups
        within the session's records.

        Args:
            session_uuid: The session UUID.

        Returns:
            Dict with cluster assignments."""
        return await self._connection.get(
            f"/api/v1/sessions/{session_uuid}/clusters",
                    )

    async def count_by_type(
        self,
    ) -> Dict[str, int]:
        """Get aggregated record counts per cognitive type.

        Args:

        Returns:
            Dict mapping ``type → count`` across all (non-archived) records.

        Junior Tip [never trust the page size you asked for — 2026-08-20]:
        this method used to request ``limit=1000``, advance ``offset`` by that
        same 1000, and stop as soon as a page came back "short". Every one of
        those three steps assumed the server honours the requested limit. It
        does not: ``/api/v1/manifest`` clamps to 500 rows in the database layer
        (``ListAllPaginated``), while the handler computes ``has_more`` as
        ``len(records) == requested_limit`` — so a request for 1000 returns 500
        rows AND ``has_more=false``. The first page therefore looked short and
        final, and **every tenant holding more than 500 records was reported as
        holding exactly 500**. It is a silent undercount: no error, no warning,
        a plausible number. It was caught only because two unrelated benchmark
        tenants both reported exactly 500 — one of which genuinely held 1,503.

        The fix is to stop predicting the server's page size. We advance by the
        rows we actually received and stop only on a genuinely empty page, so
        the walk stays correct whatever cap the server applies. It costs one
        extra round trip per call; an undercount costs a wrong answer. This
        mirrors the same fix already applied server-side in
        ``cluster/regression_worker.go`` (page-skip fix, 2026-07-04), where the
        identical assumption was silently skipping half the graph each cycle."""
        counts: Dict[str, int] = {}
        page_size = 500  # documented server cap; correctness no longer depends on it.
        offset = 0
        while True:
            data = await self._connection.get(
                "/api/v1/manifest",
                params={"limit": str(page_size), "offset": str(offset)},
                            )
            records = data.get("records") if isinstance(data, dict) else None
            if not records:
                break
            for record_fields in records:
                record_type = record_fields.get("type", "") if isinstance(record_fields, dict) else ""
                counts[record_type] = counts.get(record_type, 0) + 1
            # Advance by what the server ACTUALLY returned, never by what we
            # asked for: the two differ whenever the server clamps, and the gap
            # is exactly the set of records that would be skipped.
            offset += len(records)
        return counts

    async def recent(
        self,
        limit: int = 20,
    ) -> List[Record]:
        """Get recently updated records.

        Args:
            limit:     Maximum records (default 20).

        Returns:
            List of typed ``Record`` objects ordered by creation time (newest first)."""
        data = await self._connection.get(
            "/api/v1/recent",
            params={"limit": str(limit)},
                    )
        # objects (the FULL record) instead of returning raw dicts — matches Go/TS recent()
        # which return the full typed record, and mirrors the typed SearchResult parsing.
        records = data if isinstance(data, list) else data.get("records", [])
        return [Record(**rec) for rec in records if isinstance(rec, dict)]

    # ── Taxonomy (local, no REST round-trip) ───────────────────────

    def list_types(self) -> List[str]:
        """List the canonical cognitive memory types (LOCAL — no network call).

        Returns:
            List of type value strings, e.g.
            ``["episodic", "fact", "preference", ...]``."""
        return [member.value for member in MemoryType]

    # ── Temporal Versioning ────────────────────────────────────────

    async def supersede(self, old_id: int, new_id: int) -> Dict[str, Any]:
        """Mark an old record as superseded by a new one.

        This implements temporal versioning — the old record remains in the
        graph but is annotated with ``superseded_by`` pointing to the new
        record. Search results prefer the newer version.

        Args:
            old_id: The record being superseded.
            new_id: The replacement record.

        Returns:
            Confirmation dict."""
        return await self._connection.post(
            "/api/v1/records/supersede",
            {"old_id": old_id, "new_id": new_id},
        )

    # ── Caller-owned session writes ────────────────────────────────

    async def create_in_session(
        self,
        text: str,
        session_uuid: str,
    ) -> AddResult:
        """Store ``text`` directly as an episodic record under ``session_uuid``.

        The session must be registered via ``create_session()`` / POST
        ``/api/v1/sessions`` before calling this on session-first servers.

        Args:
            text:         Record text (stored in both summary and content).
            session_uuid: The session UUID to place the record under (required).

        Returns:
            Dict with ``session_id`` and ``records`` (the new episodic anchor)."""
        if not session_uuid:
            raise AnhurError("create_in_session: session_uuid is required")
        summary = text[:200] + "..." if len(text) > 200 else text
        payload: Dict[str, Any] = {
            "uuid": session_uuid,
            "type": "episodic",
            "dimension": 0,
            "prefix": "",
            "weight": 0.5,
            "score": 5,
            "vector": "",
            "related_ids": [],
            "main_ids": [],
            "consolidate_id": 0,
            "metadata": _build_metadata_json(self._container_tag),
            "summary": summary,
            "content": text,
            "consolidated": False,
            "status": "saved",
        }
        data = await self._connection.post("/api/v1/records", payload)
        return _add_result_from_record(
            data,
            session_id=session_uuid,
            record_type="episodic",
            summary=summary,
        )

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

    # -- Internal helpers ---------------------------------------------------

    async def _try_ingest(
        self,
        text: str,
        session_id: str = "",
    ) -> Optional[AddResult]:
        """Attempt cloud ingest at ``/api/v1/ingest``.

        Always sends ``session_id``. Returns None if the endpoint doesn't
        exist (404), allowing the caller to fall back to direct record creation."""
        effective_session_id = self._resolve_write_session_id(session_id)
        payload = {
            "content": text,
            "container_tag": self._container_tag,
            "session_id": effective_session_id,
        }

        try:
            data = await self._connection.post("/api/v1/ingest", payload)
            self._ingest_available = True

            records = data.get("records", [{"id": data.get("id", 0),
                                             "type": "episodic",
                                             "summary": text[:200]}])
            return AddResult(
                session_id=data.get("session_id", effective_session_id),
                records=[RecordSummary.model_validate(row) for row in records],
                mode="cloud",
            )
        except AnhurQueryError as exc:
            if "404" in str(exc):
                self._ingest_available = False
                return None
            raise

    async def _create_record(
        self,
        text: str,
        score: Optional[int],
        mem_type: Optional[MemoryType],
        metadata: Optional[Dict[str, Any]],
        session_id: str = "",
    ) -> AddResult:
        """Create a record directly via ``POST /api/v1/records``.

        This is the only write path that persists ``score`` and ``type``
        (the ingest endpoint drops them — see ``add()``). Without server-side
        embedding, text is stored in both ``summary`` (for keyword search) and
        ``content`` (for full retrieval)."""
        summary = text[:200] + "..." if len(text) > 200 else text

        effective_score = 5 if score is None else score
        effective_type = MemoryType.EPISODIC if mem_type is None else mem_type
        effective_session_id = self._resolve_write_session_id(session_id)

        req = CreateRequest(
            uuid=effective_session_id,
            type=effective_type,
            summary=summary,
            content=text,
            score=effective_score,
            weight=effective_score / 10,
            metadata=_build_metadata_json(self._container_tag, metadata),
        )

        data = await self._connection.post(
            "/api/v1/records",
            req.model_dump(exclude_none=True),
        )

        return _add_result_from_record(
            data,
            session_id=effective_session_id,
            record_type=effective_type.value,
            summary=summary,
        )

    @staticmethod
    def _flatten_search_results(data: Any) -> List[Dict[str, Any]]:
        """Flatten nested search response into simple dicts."""
        results = []
        for item in (data.get("results", []) if isinstance(data, dict) else []):
            record_fields = item.get("record", {}) if isinstance(item, dict) else {}
            results.append({
                "id": record_fields.get("id", 0),
                "type": record_fields.get("type", ""),
                "summary": record_fields.get("summary", ""),
                "score": item.get("similarity", 0),
                "metadata": record_fields.get("metadata"),
                "content": record_fields.get("content"),
            })
        return results

    def __repr__(self) -> str:
        return (
            f"Memory(container_tag={self._container_tag!r}, "
            f"session={self._session_uuid!r})"
        )


# ---------------------------------------------------------------------------
# AnhurClient — DEPRECATED back-compat alias (thin subclass of Memory)
# ---------------------------------------------------------------------------

class AnhurClient(Memory):
    """Deprecated: use :class:`Memory` instead.

    Historically AnhurDB shipped two clients — a thin ``Memory`` facade and a
    full ``AnhurClient``. Per the canonical parity spec (PARITY_SPEC.md) the two
    collapsed into a SINGLE ``Memory`` class that carries every method. This
    subclass is kept ONLY so existing imports (``from anhurdb import
    AnhurClient``) keep working unchanged.

    The single behavioural difference it preserves is the historical default
    ``url``: the old ``AnhurClient`` defaulted to ``http://localhost:8080``
    (self-hosted), whereas ``Memory`` defaults to the cloud endpoint. New code
    should construct ``Memory(url=...)`` explicitly."""

    def __init__(
        self,
        url: str = _LEGACY_LOCAL_URL,
        api_key: Optional[str] = None,
        tenant_id: str = "",
        mode: str = "rest",
        timeout: float = 30.0,
    ):
        # (not an error) so the thousands of existing AnhurClient(...) call sites
        # verbatim from Memory; we only re-order kwargs to match the OLD
        # AnhurClient signature (url first) and swap in the localhost default.
        warnings.warn(
            "AnhurClient is deprecated; use Memory (it now carries the full "
            "API surface). AnhurClient remains as a thin alias only.",
            DeprecationWarning,
            stacklevel=2,
        )
        super().__init__(
            api_key=api_key,
            url=url,
            tenant_id=tenant_id,
            mode=mode,
            timeout=timeout,
        )
