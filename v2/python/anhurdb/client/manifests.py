"""The two manifest reads — importance-ranked slices of records.

One domain: ``GET /api/v1/manifest`` (cross-session) and
``GET /api/v1/chats/{uuid}/manifest`` (one session). They share an envelope,
they share the temporal-filter trio, and they differ only in scope — so they
belong in one file and nowhere else.

Split out of ``client/__init__.py`` in 3.0.0 when the return type went from
``Dict[str, Any]`` to ``ManifestResult``; the file was already past the
300-line house cut.
"""

from typing import Dict, Optional

from .connection import HTTPConnection
from ..models.manifest import ManifestResult


class ManifestMixin:
    """Manifest reads: the RAG-context door."""

    _connection: HTTPConnection

    async def manifest_global(
        self,
        limit: int = 50,
        offset: int = 0,
        query: Optional[str] = None,
        *,
        as_of: Optional[str] = None,
        since: Optional[str] = None,
        until: Optional[str] = None,
    ) -> ManifestResult:
        """Cross-session overview of all knowledge, ranked by importance.

        Best tool for RAG context injection — returns the most important
        records across all sessions.

        Args:
            limit:     Max records (default 50).
            offset:    Pagination offset.
            query:     Optional keyword filter.
            as_of:     Optional RFC3339 UTC snapshot instant. Mutually
                       exclusive with ``since``/``until`` (server rejects the
                       combination with HTTP 400).
            since:     Optional RFC3339 UTC lower bound (created_at >= since).
            until:     Optional RFC3339 UTC upper bound (created_at <= until).

        Returns:
            ``ManifestResult``. Page with ``offset + count`` while
            ``has_more`` — this route sends no cursor, on purpose (see the
            model's Junior Tip)."""
        params: Dict[str, str] = {"limit": str(limit), "offset": str(offset)}
        if query:
            params["q"] = query
        if as_of:
            params["as_of"] = as_of
        if since:
            params["since"] = since
        if until:
            params["until"] = until
        data = await self._connection.get("/api/v1/manifest", params=params)
        return ManifestResult.model_validate(data if isinstance(data, dict) else {})

    async def manifest_session(
        self,
        session_uuid: str,
        query: Optional[str] = None,
        *,
        limit: int = 500,
        offset: int = 0,
        as_of: Optional[str] = None,
        since: Optional[str] = None,
        until: Optional[str] = None,
    ) -> ManifestResult:
        """Get the manifest for a single session (records with metadata).

        Args:
            session_uuid: The session UUID.
            query:        Optional keyword filter (sent as ``q``).
            limit:        Max records (default 500).
            offset:       Pagination offset.
            as_of:        Optional RFC3339 UTC snapshot instant. Mutually
                          exclusive with ``since``/``until``.
            since:        Optional RFC3339 UTC lower bound.
            until:        Optional RFC3339 UTC upper bound.

        Returns:
            ``ManifestResult`` — same envelope as ``manifest_global``."""
        params: Dict[str, str] = {"limit": str(limit), "offset": str(offset)}
        if query:
            params["q"] = query
        if as_of:
            params["as_of"] = as_of
        if since:
            params["since"] = since
        if until:
            params["until"] = until
        data = await self._connection.get(
            f"/api/v1/chats/{session_uuid}/manifest",
            params=params,
        )
        return ManifestResult.model_validate(data if isinstance(data, dict) else {})


__all__ = ["ManifestMixin"]
