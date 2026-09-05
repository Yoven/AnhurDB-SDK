"""The hybrid search port: ``POST /api/v1/search``.

One domain: build the search request, send it, and hold the server to the
contract it answered with. The scope convenience wrappers live next door in
``search_scopes.py``; the response parsing lives in ``search_parse.py``; the
mode vocabulary and the cross-version check live in ``search_mode.py``.

Junior Tip [why a mixin and not a helper module]: ``Memory`` is deliberately
ONE class with the whole surface on it (PARITY_SPEC.md), so ``mem.search(...)``
must keep working exactly as before. A mixin moves the CODE out of a 2483-line
file without moving the METHOD off the class — the public surface is
byte-identical, only the file it is typed into changed.
"""

from typing import Any, Dict, List, Optional, Sequence

from .connection import HTTPConnection
from .search_mode import (
    enforce_search_mode_contract,
    validate_search_mode,
    validate_semantic_timeout_ms,
)
from .search_parse import _parse_search_response, _parse_search_results
from .session_filter import normalize_sessions
from ..models import SearchResponse, SearchResult


class HybridSearchMixin:
    """The two calls that talk to ``POST /api/v1/search``.

    Mixed into ``Memory``; never instantiated on its own. The annotation below
    is what lets a type checker see the connection the mixin borrows from its
    host class — it declares the contract, it does not create the attribute.
    """

    _connection: HTTPConnection

    # ── Search ─────────────────────────────────────────────────────

    def _build_search_payload(
        self,
        query: str,
        sessions: Sequence[str],
        *,
        limit: int,
        type_filter: Optional[str],
        scope: str,
        skip_query_embed: bool,
        skip_cognitive_rerank: bool,
        expand_related: bool,
        astar_weight: Optional[float],
        entity_jaccard_weight: Optional[float],
        mode: str,
        semantic_timeout_ms: int,
        debug_signals: bool,
    ) -> Dict[str, Any]:
        """Build the ``POST /api/v1/search`` body shared by ``search()`` and
        ``search_with_retrieval()`` — one place so the two request paths
        cannot silently diverge on which knobs get sent.

        Every optional knob is OMITTED from the payload rather than sent at
        its "off" value, matching the server's own ``omitempty`` wire
        contract (``server/model/record.go`` ``SearchRequest``) and the
        ``skip_query_embed`` precedent (commit ``ff7f803``): a caller who
        never touched a knob gets the exact payload shape that existed
        before the knob was added, so the server's own default (not the
        SDK's) decides the behaviour."""
        resolved_sessions = normalize_sessions(sessions)
        payload: Dict[str, Any] = {
            "text": query,
            "limit": limit,
            "scope": scope,
            "sessions": resolved_sessions,
        }
        if type_filter:
            payload["type_filter"] = type_filter
        # Knobs de ablação (paridade REST/MCP/Go/TS, 2026-08-07): omitidos
        # quando False para preservar o wire default do servidor.
        if skip_query_embed:
            payload["skip_query_embed"] = True
        if skip_cognitive_rerank:
            payload["skip_cognitive_rerank"] = True
        # ADR-0021 (2026-08-10): expand_related is bool/opt-in, same
        # omit-when-false discipline as the ablation knobs above.
        if expand_related:
            payload["expand_related"] = True
        # astar_weight / entity_jaccard_weight: None means "caller never set
        # this", NOT "set it to 0.0". The server distinguishes the two with
        # a *float64 (nil vs 0.0) for exactly this reason — see
        # server/model/record.go SearchRequest.AstarWeight's Junior Tip. A
        # plain float default of 0.0 here would make "explicitly asked for
        # 0.0" and "never asked" indistinguishable on the wire, silently
        # forcing every unset caller into an explicit-zero override.
        if astar_weight is not None:
            payload["astar_weight"] = astar_weight
        if entity_jaccard_weight is not None:
            payload["entity_jaccard_weight"] = entity_jaccard_weight
        # ADR-0031 (2026-09-05): the three retrieval controls. Same
        # omit-when-unset discipline as every knob above — an empty ``mode``,
        # a zero budget and a false ``debug_signals`` are all left OFF the
        # wire so the server's own defaults (balanced / 700ms / no debug
        # block) apply, byte-for-byte as before the knobs existed.
        if mode:
            payload["mode"] = mode
        if semantic_timeout_ms > 0:
            payload["semantic_timeout_ms"] = semantic_timeout_ms
        if debug_signals:
            payload["debug_signals"] = True
        return payload

    async def search(
        self,
        query: str,
        sessions: Sequence[str],
        *,
        limit: int = 10,
        type_filter: Optional[str] = None,
        scope: str = "sessions",
        skip_query_embed: bool = False,
        skip_cognitive_rerank: bool = False,
        expand_related: bool = False,
        astar_weight: Optional[float] = None,
        entity_jaccard_weight: Optional[float] = None,
        mode: Optional[str] = None,
        semantic_timeout_ms: Optional[int] = None,
        debug_signals: bool = False,
    ) -> List[SearchResult]:
        """Hybrid plane search via ``POST /api/v1/search``.

        Default ``scope`` is ``sessions`` (all chat sessions for the tenant,
        excluding shared-library uuids). Use the scope helpers or pass
        ``tenant_shared``, ``client_shared``, or ``shared_all`` explicitly.

        ``sessions`` is MANDATORY (ADR-0014): pass ``sessions_all()`` for every
        session inside the scope, or the explicit uuids to confine the query to
        those chats. ``None`` and ``[]`` are errors, never "all".

        Junior Tip [scope vs sessions]: the two are orthogonal. ``scope`` picks
        the BOUNDARY (which store/plane is reachable at all); ``sessions`` picks
        the SUBSET inside that boundary. ``["*"]`` means "everything in this
        boundary" — it is not a way to cross into a shared plane.

        Agent UX — text is not semantic: ``query`` is sent as body ``text``
        (FTS5 exact-word matching), not an embedding. For conceptual RAG
        without a vector, prefer ``smart_search`` (or MCP ``recall``).

        Each hit's ``.provenance`` / ``.scope`` / ``.signals`` are populated
        straight from the server response (fixed 2026-08-10 — previously
        silently discarded by the SDK parser). ``.related_nodes`` is
        populated when ``expand_related=True`` and the walk found neighbours
        within budget; ``None`` means either the flag was off or nothing was
        found — see ``docs/decisions/ADR-0021-search-expand-related.md``
        (implemented on REST/gRPC/MCP/all 3 SDKs as of 2026-08-11). Session/
        plane admission is enforced server-side for every neighbour, the
        same guarantee the rest of a hit's own visibility already has.

        Args:
            query:                 Query string sent as FTS ``text`` (required).
            sessions:               Session filter (required) — ``sessions_all()`` or uuids.
            limit:                  Maximum results (default 10).
            type_filter:            Optional memory type filter.
            scope:                  Search plane (default ``sessions``).
            expand_related:         ADR-0021 opt-in — ask the server to attach a
                                    bounded ``related_nodes`` summary to each
                                    surviving top-K hit. Default ``False``,
                                    omitted from the wire when unset.
            astar_weight:           Per-request override of
                                    ``ANHUR_SEARCH_ASTAR_WEIGHT`` for this query
                                    only. ``None`` (default) = use the server's
                                    configured weight; omitted from the wire.
                                    Pass ``0.0`` explicitly to disable the A*
                                    arm's contribution for just this query —
                                    that is NOT the same as leaving this unset.
            entity_jaccard_weight:  Per-request override of
                                    ``ANHUR_ENTITY_JACCARD_WEIGHT`` for this
                                    query only. Same ``None``-vs-``0.0`` contract
                                    as ``astar_weight``.
            mode:                   ADR-0031 retrieval budget: ``"fast"`` |
                                    ``"balanced"`` | ``"semantic"``. ``None``
                                    (default) omits the field so the server's
                                    default (balanced) applies. An unknown value
                                    is rejected here, before the round trip.
                                    ``"semantic"`` is a PROMISE — see Raises.
            semantic_timeout_ms:    ADR-0031 cap on the Embed+HNSW wait, in
                                    milliseconds. ``None``/``0`` = the server
                                    default (700 ms). Negative is rejected.
            debug_signals:          ADR-0031 opt-in for the per-hit
                                    ``SearchHitSignals`` block (13 fields) and,
                                    on ``search_with_retrieval``, the
                                    ``leg_scores`` array. Off by default —
                                    it makes the response bigger, never better.

        Returns:
            List of typed ``SearchResult`` objects (nested ``.record`` +
            ``.similarity`` + ``.provenance``/``.scope``/``.signals``/
            ``.related_nodes``).

        Raises:
            AnhurError: ``INVALID_PARAM: ...`` when the session filter is
                absent, empty, contradictory, or above the cap, or when
                ``mode``/``semantic_timeout_ms`` carry a value this SDK
                refuses to send.
            AnhurError: ``SERVER_TOO_OLD: ...`` when ``mode="semantic"`` was
                requested and the server answered with a different
                ``retrieval.mode`` — see ``client/search_mode.py`` for why the
                response, not the request, is the only honest detector.

        Example::

            hits = await mem.search(
                "what does this user do?", sessions_all(), limit=5
            )"""
        resolved_mode = validate_search_mode(mode)
        resolved_semantic_timeout_ms = validate_semantic_timeout_ms(semantic_timeout_ms)
        payload = self._build_search_payload(
            query,
            sessions,
            limit=limit,
            type_filter=type_filter,
            scope=scope,
            skip_query_embed=skip_query_embed,
            skip_cognitive_rerank=skip_cognitive_rerank,
            expand_related=expand_related,
            astar_weight=astar_weight,
            entity_jaccard_weight=entity_jaccard_weight,
            mode=resolved_mode,
            semantic_timeout_ms=resolved_semantic_timeout_ms,
            debug_signals=debug_signals,
        )
        data = await self._connection.post("/api/v1/search", payload)
        enforce_search_mode_contract(
            resolved_mode,
            scope,
            data,
            semantic_timeout_ms=resolved_semantic_timeout_ms,
            debug_signals=debug_signals,
        )
        return _parse_search_results(data)

    async def search_with_retrieval(
        self,
        query: str,
        sessions: Sequence[str],
        *,
        limit: int = 10,
        type_filter: Optional[str] = None,
        scope: str = "sessions",
        skip_query_embed: bool = False,
        skip_cognitive_rerank: bool = False,
        expand_related: bool = False,
        astar_weight: Optional[float] = None,
        entity_jaccard_weight: Optional[float] = None,
        mode: Optional[str] = None,
        semantic_timeout_ms: Optional[int] = None,
        debug_signals: bool = False,
    ) -> SearchResponse:
        """Identical request to ``search()``, but returns the full envelope
        including the ADR-0012 ``retrieval`` block (which search arms ran,
        whether semantic degraded, the RESOLVED astar/entity-jaccard
        weights actually used).

        Added instead of changing ``search()``'s return type so existing
        callers of ``search()`` (which every SDK caller today is) keep
        their ``List[SearchResult]`` unchanged. Use this method only when
        you specifically need ``.retrieval``; otherwise prefer ``search()``.

        Args:
            (identical to ``search()`` — see its docstring for each.)

        Returns:
            ``SearchResponse`` with ``.results`` (same as ``search()``'s
            return value), ``.retrieval`` (``None`` if the server did not
            attach the block) and ``.leg_scores`` (``None`` unless
            ``debug_signals=True`` on a single-plane scope)."""
        resolved_mode = validate_search_mode(mode)
        resolved_semantic_timeout_ms = validate_semantic_timeout_ms(semantic_timeout_ms)
        payload = self._build_search_payload(
            query,
            sessions,
            limit=limit,
            type_filter=type_filter,
            scope=scope,
            skip_query_embed=skip_query_embed,
            skip_cognitive_rerank=skip_cognitive_rerank,
            expand_related=expand_related,
            astar_weight=astar_weight,
            entity_jaccard_weight=entity_jaccard_weight,
            mode=resolved_mode,
            semantic_timeout_ms=resolved_semantic_timeout_ms,
            debug_signals=debug_signals,
        )
        data = await self._connection.post("/api/v1/search", payload)
        enforce_search_mode_contract(
            resolved_mode,
            scope,
            data,
            semantic_timeout_ms=resolved_semantic_timeout_ms,
            debug_signals=debug_signals,
        )
        return _parse_search_response(data)
