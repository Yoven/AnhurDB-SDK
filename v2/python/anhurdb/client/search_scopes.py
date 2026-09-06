"""Scope and shortcut wrappers over the hybrid search port.

One domain: the thin helpers that pick a plane (``sessions`` /
``tenant_shared`` / ``client_shared`` / ``shared_all``) or a shortcut endpoint
(``/search/type``, ``/search/smart``) and then delegate. No knob is decided
here, and no response is parsed here beyond the bare-record envelope that
``/search/type`` uses.

Junior Tip [why these inherit instead of sitting beside]: every wrapper below
ends in ``self.search(...)``, so it is meaningless without the core port. The
inheritance says exactly that — ``SearchScopeMixin`` IS ``HybridSearchMixin``
plus conveniences — and it is also what lets a type checker resolve
``self.search`` without a second, hand-maintained declaration that could drift
away from the real signature.

ADR-0031 note: because every wrapper forwards ``**kwargs`` to ``search()``,
``mode``/``semantic_timeout_ms``/``debug_signals`` reach the wire through them
unchanged, and the cross-version check runs for them too. ``search_by_type``
and ``smart_search`` are the exceptions — they are different endpoints that
take no retrieval mode at all.

Junior Tip [why EVERY delegating wrapper takes ``**kwargs``, 2026-09-05]:
``recall`` and ``search_session`` used to declare a FIXED keyword set. They
still delegated to ``search()``, but a knob they did not name could not get
past their own signature — ``recall(q, s, 5, mode="semantic")`` raised
``TypeError`` in Python while the identical call worked in Go (``Recall``
forwards ``opts``) and in TypeScript (``recall`` spreads ``...options``). A
knob that exists in two SDKs and is a hard error in the third is the same
class of defect as a knob the server ignores: the caller's intent never
reaches the wire. Any new wrapper that ends in ``self.search(...)`` must
forward ``**kwargs`` for exactly this reason — a fixed list is a list that
goes stale the next time ``search()`` grows a parameter.
"""

from typing import Any, List, Optional, Sequence, Tuple

from .connection import HTTPConnection
from .search import HybridSearchMixin
from .search_parse import _parse_typed_records
from .session_filter import normalize_sessions
from ..models import SearchResult


class SearchScopeMixin(HybridSearchMixin):
    """Plane shortcuts and shortcut endpoints. Mixed into ``Memory``."""

    _connection: HTTPConnection

    # Junior Tip [why the session id is declared here too]: ``search_session``
    # falls back to the client's CURRENT write session when the caller passes
    # none. That attribute is owned and rotated by ``Memory``; declaring its
    # type (without assigning it) is what keeps the mixin honest to a type
    # checker instead of silently depending on a host attribute nobody
    # documented.
    _session_uuid: str

    # Junior Tip [why the plane PIN wins and a caller ``scope`` is POPPED,
    # 2026-09-06]: each wrapper below is NAMED for a plane, so answering from a
    # different plane would be a cross-plane lie invisible at the call site —
    # the same class of defect ADR-0014 exists to kill, and exactly the rule
    # ``search_session`` already applies. Without the pop, ``scope`` arrives
    # twice (once pinned, once in ``**kwargs``) and Python raises a bare
    # ``TypeError: search() got multiple values for keyword argument 'scope'``
    # — an error with no ``.kind`` and no ``.retryable``, so it escapes the
    # ``AnhurError`` contract every caller writes their handling against, while
    # Go and TypeScript quietly override. Overriding beats raising because the
    # wrapper already knows the answer. Widening is spelled
    # ``search(query, sessions, scope=...)``.

    async def search_sessions(
        self, query: str, sessions: Sequence[str], **kwargs: Any
    ) -> List[SearchResult]:
        """Search chat sessions only (``scope=sessions``).

        ``sessions`` is mandatory — see ``search``."""
        kwargs.pop("scope", None)
        return await self.search(query, sessions, scope="sessions", **kwargs)

    async def search_tenant_shared(
        self, query: str, sessions: Sequence[str], **kwargs: Any
    ) -> List[SearchResult]:
        """Search tenant-shared library docs (``scope=tenant_shared``).

        ``sessions`` is mandatory and selects inside the shared boundary."""
        kwargs.pop("scope", None)
        return await self.search(query, sessions, scope="tenant_shared", **kwargs)

    async def search_client_shared(
        self, query: str, sessions: Sequence[str], **kwargs: Any
    ) -> List[SearchResult]:
        """Search client-wide shared library (``scope=client_shared``).

        ``sessions`` is mandatory and selects inside the shared boundary."""
        kwargs.pop("scope", None)
        return await self.search(query, sessions, scope="client_shared", **kwargs)

    async def search_shared(
        self, query: str, sessions: Sequence[str], **kwargs: Any
    ) -> List[SearchResult]:
        """Search both shared planes (``scope=shared_all``).

        ``sessions`` is mandatory and selects inside both shared boundaries."""
        kwargs.pop("scope", None)
        return await self.search(query, sessions, scope="shared_all", **kwargs)

    async def search_by_type(
        self,
        memory_type: str,
        sessions: Sequence[str],
        limit: int = 20,
        query: Optional[str] = None,
    ) -> List[SearchResult]:
        """List/filter records by cognitive type in the tenant store.

        Faster than plane search when you know the exact type.

        Agent UX — not a plane switch: no ``scope`` parameter. Does **not**
        search Shared Data. For specialty docs use ``search_tenant_shared`` /
        ``search_client_shared`` / ``search_shared`` (or ``search(..., scope=...)``).

        ``sessions`` is MANDATORY (ADR-0014), exactly as in ``search``: this
        endpoint had no session argument at all before, so "give me the facts of
        this chat" quietly returned the facts of every chat.

        Args:
            memory_type: Type to filter (e.g. ``"fact"``, ``"risk"``).
            sessions:    Session filter (required) — ``sessions_all()`` or uuids.
            limit:       Maximum results (default 20).
            query:       Optional keyword search within the type.

        Returns:
            List of typed ``SearchResult`` objects (nested ``.record`` +
            ``.similarity``)."""
        resolved_sessions = normalize_sessions(sessions)
        params: List[Tuple[str, str]] = [
            ("type", memory_type),
            ("limit", str(limit)),
        ]
        params.extend(("sessions", session) for session in resolved_sessions)
        if query:
            params.append(("q", query))
        data = await self._connection.get(
            "/api/v1/search/type", params=params
        )
        return _parse_typed_records(data)

    async def search_session(
        self,
        query: str = "",
        *,
        session_uuid: Optional[str] = None,
        limit: int = 10,
        type_filter: Optional[str] = None,
        **kwargs: Any,
    ) -> List[SearchResult]:
        """Search within a single session (all record types, including recent).

        Sugar over ``search(query, [session_uuid])`` — the one-chat case
        expressed in the ADR-0014 grammar.

        Junior Tip [why the empty uuid stopped meaning "everything"]: this
        method used to send ``uuid: ""`` when there was no current session, and
        the server read that as "no session filter". A method named
        ``search_session`` silently searching every session is the exact defect
        ADR-0014 exists to kill. Widening is now spelled
        ``search(query, sessions_all())``.

        Args:
            query:        Natural language query.
            session_uuid: Session to search; ``None`` = current session.
            limit:        Maximum results (default 10).
            type_filter:  Optional memory type filter.
            **kwargs:     Any other ``search()`` keyword — ``mode``,
                ``semantic_timeout_ms``, ``debug_signals``, ``expand_related``,
                the ablation weights — forwarded verbatim. ``scope`` is the one
                exception: it is PINNED to ``sessions`` and a caller-supplied
                value is ignored, exactly as in Go and TypeScript.

        Returns:
            List of typed ``SearchResult`` objects (nested ``.record`` +
            ``.similarity``).

        Raises:
            AnhurError: ``INVALID_PARAM: ...`` when neither an explicit session
                nor a current session is available."""
        target_uuid = session_uuid if session_uuid is not None else self._session_uuid
        # Junior Tip [why the scope is PINNED]: a method named
        # ``search_session`` that answered from ``shared_all`` would read a
        # whole shared plane while its own name promised one chat — the same
        # class of silent lie ADR-0014 exists to kill, and invisible in the
        # results because the records come back looking perfectly normal.
        #
        # Junior Tip [why a caller-supplied scope is OVERRIDDEN, not refused]:
        # because the other two SDKs override it, and one knob with three
        # behaviours is the divergence this parity round exists to remove. Go
        # appends ``WithScope(searchScopeSessions)`` LAST so it beats a
        # caller-supplied ``WithScope`` (``client/parity.go``, SearchSession);
        # TypeScript writes ``scope: "sessions"`` AFTER the options spread
        # (``src/search.ts``, searchSession). Raising here would make Python
        # the odd one out for a call the other two accept.
        #
        # Junior Tip [why the pop is not cosmetic]: without it, ``scope``
        # arrives inside ``**kwargs``, collides with the pinned keyword below,
        # and Python raises a bare ``TypeError: search() got multiple values
        # for keyword argument 'scope'``. A bare TypeError has no ``.kind`` and
        # no ``.retryable``, so it escapes the ``AnhurError`` contract every
        # caller writes their error handling against — an uncatchable crash
        # where Go and TypeScript quietly do the right thing.
        kwargs.pop("scope", None)
        return await self.search(
            query,
            [target_uuid if target_uuid is not None else ""],
            limit=limit,
            type_filter=type_filter,
            scope="sessions",
            **kwargs,
        )

    async def smart_search(
        self,
        query: str,
        sessions: Sequence[str],
        *,
        limit: int = 10,
        memory_type: Optional[str] = None,
        scope: str = "sessions",
    ) -> Any:
        """Full-text search with cognitive weight boosting.

        Prefer this over ``search()`` for conceptual text queries (no
        embedding required). Ranks by text relevance × cognitive weight.
        Same memory-plane ``scope`` as ``search()`` (default ``sessions``).

        ``sessions`` is MANDATORY (ADR-0014), exactly as in ``search``.
        ``smart_search`` is one of the two paths that had no session argument at
        all before — it accepted the scope, dropped the chat filter, and
        answered from every conversation.

        Args:
            query:       Search query.
            sessions:    Session filter (required) — ``sessions_all()`` or uuids.
            limit:       Maximum results (default 10).
            memory_type: Optional type filter.
            scope:       Search plane (default ``sessions``).

        Returns:
            Search results ranked by cognitive relevance."""
        resolved_sessions = normalize_sessions(sessions)
        params: List[Tuple[str, str]] = [
            ("q", query),
            ("limit", str(limit)),
            ("scope", scope),
        ]
        params.extend(("sessions", session) for session in resolved_sessions)
        if memory_type:
            params.append(("type", memory_type))
        return await self._connection.get(
            "/api/v1/search/smart", params=params
        )

    async def recall(
        self,
        query: str,
        sessions: Sequence[str],
        limit: int = 10,
        *,
        scope: str = "sessions",
        **kwargs: Any,
    ) -> List[SearchResult]:
        """Recall memories via plane-aware search.

        Delegates directly to ``search()`` (``POST /api/v1/search``,
        default ``scope=sessions``). There is no server-side recall endpoint
        or fan-out — the name mirrors the MCP ``recall`` tool convention
        (whose 4-way fan-out + RRF lives in the MCP server, not the data
        plane). Identical across the three SDKs.

        ``sessions`` is MANDATORY (ADR-0014) — see ``search``.

        Args:
            query:     Natural language query.
            sessions:  Session filter (required) — ``sessions_all()`` or uuids.
            limit:     Maximum results (default 10).
            scope:     Search plane (default ``sessions``).
            **kwargs:  Any other ``search()`` keyword — ``mode``,
                ``semantic_timeout_ms``, ``debug_signals``, ``type_filter``,
                the ablation weights — forwarded verbatim.

        Returns:
            List of typed ``SearchResult`` objects (inherited from ``search``)."""
        return await self.search(query, sessions, limit=limit, scope=scope, **kwargs)
