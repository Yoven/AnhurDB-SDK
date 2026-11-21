"""Cross-session record reads that are not search: AST query, recent, counts.

One domain: reading records by structure rather than by relevance — the AST
filter (``POST /api/v1/query``), the newest records (``GET /api/v1/recent``),
the per-type tally walked over ``GET /api/v1/manifest``, and the local
taxonomy those types come from.

Split out of ``client/__init__.py`` in 4.0.0 (house 300-line cut). Relevance
search lives in ``search.py`` / ``search_scopes.py``.

Junior Tip [``search_with_ast`` is gone — 4.0.0]: it was a forwarding alias of
``query()`` kept after the cross-SDK rename (Go ``Query`` / TS ``query``).
Call ``query()``.
"""

from typing import Any, Dict, List, Optional

from .connection import HTTPConnection
from .envelope import envelope_list
from .query_argument import compile_query_argument
from ..models import MemoryType, Record


class RecordListingMixin:
    """AST query, recent records, per-type counts and the type taxonomy."""

    _connection: HTTPConnection

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
        # The server answers an empty result as {"records": null} (a nil Go
        # slice -> JSON null), so .get("records", []) returns None (key present)
        # and iterating it raises TypeError. `or []` coalesces null to [],
        # matching Go (wrapped.Records == nil -> []) and TS (records ?? []).
        records_data = (data.get("records") or []) if isinstance(data, dict) else []
        return [Record(**record_fields) for record_fields in records_data]

    async def count_by_type(
        self,
    ) -> Dict[str, int]:
        """Get aggregated record counts per cognitive type.

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
            records = envelope_list(data, "/api/v1/manifest", "records")
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
            List of typed ``Record`` objects ordered by creation time (newest first).

        Junior Tip [envelope only since 4.0.0]: the route always answers
        ``{"records", "count"}`` (``server/handler/record_session.go:455``).
        The bare-array branch for an older server is gone; ``records: null``
        (an empty result) still coalesces to ``[]`` — that is the current
        contract, not a fallback."""
        data = await self._connection.get(
            "/api/v1/recent",
            params={"limit": str(limit)},
        )
        # Typed Record objects (the FULL record) instead of raw dicts — matches
        # Go/TS recent(), which return the full typed record.
        records = envelope_list(data, "/api/v1/recent", "records")
        return [Record(**rec) for rec in records if isinstance(rec, dict)]

    def list_types(self) -> List[str]:
        """List the canonical cognitive memory types (LOCAL — no network call).

        Returns:
            List of type value strings, e.g.
            ``["episodic", "fact", "preference", ...]``."""
        return [member.value for member in MemoryType]


__all__ = ["RecordListingMixin"]
