"""Sessions — identity, registration and the per-session reads.

One domain: the write session every record belongs to. Generating a local id,
registering it on the server (``POST /api/v1/sessions``), resolving which
session a write goes to, and the reads keyed by one session (records list,
full-text history, clusters) or by all of them (aggregate stats).

Split out of ``client/__init__.py`` in 4.0.0 (house 300-line cut). The
container tag is NOT a session and lives in ``container_tag.py``.
"""

import secrets
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from .connection import HTTPConnection
from .exceptions import AnhurQueryError
from .session_stats import fetch_all_session_stats
from ..models import SessionStats


def _utc_timestamp() -> str:
    """Return current UTC time as ``YYYYMMDD-HHMMSS``."""
    now = datetime.now(timezone.utc)
    return now.strftime("%Y%m%d-%H%M%S")


class SessionMixin:
    """Session lifecycle and session-scoped reads."""

    _connection: HTTPConnection
    _container_tag: str
    _session_uuid: str
    _session_registered: bool

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

        Junior Tip [sessionRegistered]: a local uuid is NOT enough to write.
        The container tag is never a session fallback either — substituting it
        would merge every conversation of this client into one session."""
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
            List of record dicts.

        Junior Tip [envelope only since 4.0.0]: the route always answers
        ``{"records", "count"}`` (``server/handler/record_session.go:184``);
        ``records`` is JSON ``null`` for an empty session (a nil Go slice), so
        ``or []`` is the current contract. The old ``data.get("records",
        data)`` that handed back a bare array or the whole envelope is gone."""
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
        records = data.get("records") if isinstance(data, dict) else None
        return list(records or [])

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


__all__ = ["SessionMixin"]
