"""
Search wire models for the AnhurDB Python SDK.

One domain: everything that comes back from ``POST /api/v1/search`` — the
per-hit debug signals, the expanded neighbours, the retrieval-arm metadata,
the per-leg score distributions, and the two envelopes that carry them.

Junior Tip [why these left ``record.py``]: ``record.py`` answers "what is a
memory record on the wire". These types answer "what did a SEARCH say about
those records" — a different contract, versioned by different ADRs
(ADR-0012 signals, ADR-0021 expand_related, ADR-0031 mode/leg_scores), and
the one that grows every time a retrieval leg is added. Keeping them in the
record file made a 322-line file that grew for two unrelated reasons.
"""

from typing import List, Optional

from pydantic import BaseModel, ConfigDict, Field

from .record import Record


class SearchHitSignals(BaseModel):
    """
    Per-hit ablation debug signals (ADR-0012), mirroring Go
    ``model.SearchHitSignals`` (``server/model/record.go``).

    Only populated by the server when the request asked for debug signals
    (``debug_signals``); otherwise ``SearchResult.signals`` is ``None`` and
    this model is never constructed. All fields use Go's zero-value ==
    "not reported" convention (every Go field carries ``omitempty``), so a
    plain scalar default here is the correct mirror — there is no wire
    distinction between "rank is 0" and "rank omitted" to preserve.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    fts_rank: int = Field(default=0)
    semantic_rank: int = Field(default=0)
    simhash_rank: int = Field(default=0)
    simhash_hamming: int = Field(default=0)
    rrf_score: float = Field(default=0.0)
    semantic_cosine: float = Field(default=0.0)

    # ADR-0031 (2026-09-05) — the 7 signals that completed the set of 13.
    #
    # Junior Tip [why the legs had to be named one by one]: before these, the
    # four base legs were COLLAPSED into two fields (fts_rank/semantic_rank),
    # so a bimodal rrf_score could not be explained from outside the server —
    # you could not tell whether a hit was ranked by HNSW or by the BSQ scan,
    # nor whether A* and entity-Jaccard had contributed at all, because those
    # two legs had no field whatsoever. ``active_leg_weight_sum`` is the RRF
    # DENOMINATOR (the summed weight of the legs that produced a rank for THIS
    # hit) — without it, two rrf_scores from the same query are not comparable,
    # because they may have been divided by different numbers of active legs.
    #
    # All seven are ``omitempty`` on the Go side and debug-only: they arrive
    # solely when the request set ``debug_signals``. Zero therefore means "leg
    # did not rank this hit" and "server did not report", indistinguishably —
    # that ambiguity is the server's wire contract, not a loss introduced here.
    hnsw_rank: int = Field(default=0)
    bsq_rank: int = Field(default=0)
    parquet_rank: int = Field(default=0)
    fts5_rank: int = Field(default=0)
    astar_rank: int = Field(default=0)
    entity_jaccard_rank: int = Field(default=0)
    active_leg_weight_sum: float = Field(default=0.0)


class RelatedNode(BaseModel):
    """
    A bounded, summary-only neighbour of a search hit (ADR-0021
    ``expand_related``).

    Junior Tip [why this is not a ``Record``]: ADR-0021 is explicit that
    ``related_nodes`` is a SUMMARY projection (id/type/summary/weight), not
    the full ``Record`` — no ``content``, no internal fields — precisely so
    expansion cannot multiply the response payload by N. Do not widen this
    model to carry more of ``Record`` without re-reading the ADR's "Formato
    de resposta proposto" section first.

    Server-side status (2026-08-11): implemented and live on REST and gRPC
    (server/model/record.go has ``RelatedNode`` and ``SearchRequest.ExpandRelated``;
    the MCP ``search`` tool and this SDK's ``search()``/``search_with_retrieval()``
    both send ``expand_related`` and parse ``related_nodes`` back). Session/plane
    admission is enforced server-side (reuses the same ``WalkAdmission`` the A*
    leg uses) — this model itself does not, and should not, decide what's safe
    to show; it only carries whatever bounded summary the server already vetted.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: Optional[int] = Field(default=0)
    type: str = Field(default="")
    summary: str = Field(default="")
    weight: float = Field(default=0.0)


class RetrievalMeta(BaseModel):
    """
    Retrieval-arm metadata (ADR-0012), mirroring Go ``model.RetrievalMeta``
    (``server/model/record.go``) — which search arms ran, whether semantic
    degraded, and the RESOLVED astar/entity-jaccard weights actually used
    for the query (after any per-request override was applied).

    Attached to the response envelope only, never per-hit (see
    ``SearchResponse.retrieval``) — the server omits the whole ``retrieval``
    key when it has nothing to report (``bundle.go``:
    ``if retrieval != nil { payload["retrieval"] = retrieval }``).
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    mode: str = Field(default="")
    signals_used: List[str] = Field(default_factory=list)
    semantic_attempted: bool = Field(default=False)
    semantic_used: bool = Field(default=False)
    degraded: bool = Field(default=False)
    reason: str = Field(default="")
    elapsed_ms: int = Field(default=0)
    content_simhash_enabled: bool = Field(default=False)
    content_simhash_weight: float = Field(default=0.0)
    astar_enabled: bool = Field(default=False)
    astar_weight: float = Field(default=0.0)
    entity_jaccard_enabled: bool = Field(default=False)
    entity_jaccard_weight: float = Field(default=0.0)


class LegScoreSummary(BaseModel):
    """
    Sufficient statistics of ONE retrieval leg's raw score distribution,
    mirroring Go ``model.LegScoreSummary`` (``server/model/search_leg_scores.go``).

    Junior Tip [what this is for, and what it is NOT]: these are QPP
    (query-performance-prediction) numbers — how many candidates a leg
    produced and how its raw scores were spread — used to explain WHY a
    ranking looks the way it does. They are raw per-leg scores, never
    calibrated similarities, so comparing ``mean`` across legs (or across
    stores) is exactly the mistake that makes "sometimes it finds it"
    undebuggable. Compare a leg against ITSELF over time, not against
    another leg.

    Only sent when the request asked for ``debug_signals``. Never sent for
    ``scope="shared_all"``: two legs over two stores produce two
    distributions, and there is no honest single array for them.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    leg: str = Field(default="")
    candidates: int = Field(default=0)
    top_scores: List[float] = Field(default_factory=list)
    mean: float = Field(default=0.0)
    stddev: float = Field(default=0.0)


class SearchResult(BaseModel):
    """
    A single search hit combining a record with its relevance score.

    Returned by ``/api/v1/search``, ``/api/v1/search/type``,
    and ``/api/v1/search/smart``.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    record: Record
    similarity: float = 0.0

    # Junior Tip [2026-08-10 — the débito técnico ADR-0021 flagged]: these three
    # existed on the server wire (server/model/record.go SearchResult) long
    # before this SDK ever read them — the SDK's manual field-by-field
    # construction in ``_parse_search_results`` simply never picked them up,
    # so every SDK caller was silently blind to which plane answered a
    # shared_all hit, and to debug signals, even though the server always
    # sent them. Fixed alongside expand_related in the same batch (ADR-0021
    # "Decisão pendente do dono" #2 — resolved: yes, same batch).
    provenance: str = Field(default="")
    scope: str = Field(default="")
    signals: Optional[SearchHitSignals] = Field(default=None)
    # None = server did not send the key (expand_related was not requested,
    # or the server predates ADR-0021). Empty list = expand_related WAS
    # requested and the hit legitimately has zero neighbours. That
    # distinction is real information — collapsing it to a single default
    # would erase "no neighbours" vs "didn't ask" in exactly the way this
    # codebase's own doc string above already condemns for other fields.
    related_nodes: Optional[List[RelatedNode]] = Field(default=None)


class SearchResponse(BaseModel):
    """
    Full envelope from ``POST /api/v1/search``: typed hits plus the optional
    ADR-0012 ``retrieval`` block.

    Junior Tip [why this is a NEW type instead of changing search()'s return]:
    ``Memory.search()`` has returned ``List[SearchResult]`` since before this
    change, and every existing caller (docs, PARITY_SPEC.md, this repo's own
    tests) iterates that list directly. Retyping search() to return an
    envelope would be a breaking change for all of them for a field
    (``retrieval``) most callers never asked for. ``Memory.search_with_retrieval()``
    issues the identical request and returns this richer envelope instead —
    additive, not a replacement.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    results: List[SearchResult] = Field(default_factory=list)
    retrieval: Optional[RetrievalMeta] = Field(default=None)

    # ADR-0031 (2026-09-05). ``None`` = the server sent no ``leg_scores`` key:
    # either the request did not set ``debug_signals``, or the scope was
    # ``shared_all`` (where neither port emits the array), or the server
    # predates ADR-0031 Stage 2. An EMPTY list means the key arrived with no
    # legs in it. Keeping the two apart is the same discipline
    # ``SearchResult.related_nodes`` already applies — a default of ``[]``
    # would erase "the server never told us" into "no legs ran".
    leg_scores: Optional[List[LegScoreSummary]] = Field(default=None)
