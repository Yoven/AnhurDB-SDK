"""
Data models for AnhurDB memory records.

These Pydantic models define the wire format for creating, reading, and
searching cognitive memory records. They match the Go server's JSON
serialisation exactly.

Junior Tip: ``CreateRequest`` intentionally exposes the full parameter
surface so power users can set score, weight, and related_ids. The
``Memory`` class wraps this with sane defaults for simple use cases.
"""

from typing import Any, Dict, List, Optional
from pydantic import AliasChoices, BaseModel, ConfigDict, Field
from datetime import datetime

from .enums import MemoryType, MemoryStatus


class CreateRequest(BaseModel):
    """
    Payload for creating a new memory record.

    Required:
        session_id (preferred) or uuid — both identify the write session from
        ``create_session`` / ``POST /api/v1/sessions``. The wire field sent to
        the server may be ``session_id`` or legacy ``uuid`` (same value).

    The server automatically computes embeddings and search indexes when
    ``dimension=0`` (default). Set ``dimension``, ``vector``, and ``prefix``
    only when providing a pre-computed embedding.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    # Junior Tip [session_id parity]: prefer session_id=...; uuid= remains for
    # older call sites. Both serialize — create() posts session_id when set.
    uuid: str = Field(default="", description="Legacy alias for session_id")
    session_id: str = Field(default="", description="Preferred session from create_session")
    type: MemoryType = Field(default=MemoryType.EPISODIC)

    summary: str = Field(default="")
    content: str = Field(default="")
    score: int = Field(default=5)
    weight: float = Field(default=0.5)
    status: str = Field(default="saved")
    related_ids: List[int] = Field(default_factory=list)
    metadata: str = Field(default="")

    # Advanced: pre-computed embedding fields.
    # Leave at defaults — the server handles embedding automatically.
    dimension: int = Field(default=0)
    vector: str = Field(default="")
    prefix: str = Field(default="")
    main_ids: List[int] = Field(default_factory=list)

    valid_from: Optional[str] = Field(default=None)
    valid_until: Optional[str] = Field(default=None)

    # Consolidation fields — set by the consolidation agent on summary records.
    consolidated: bool = Field(default=False)
    consolidate_id: int = Field(default=0)


class Record(BaseModel):
    """
    A cognitive memory record as returned by the AnhurDB API.

    This model covers the fields returned across different endpoints
    (search, topology, manifest, content).
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: Optional[int] = Field(default=0)
    uuid: str = ""
    # Junior Tip [read-model enum tolerance, 2026-07-04]: type/status are plain str on the
    # READ model (not MemoryType/MemoryStatus enums) so an out-of-taxonomy value the server
    # may legitimately hold (e.g. status="" on a transient/processing record) is preserved
    # verbatim instead of raising a pydantic ValidationError that would destroy the ENTIRE
    # search/recent/query response. Matches Go (type MemoryStatus string) and TS (status: string).
    type: str = Field(default="episodic")

    weight: float = Field(default=0.0)
    score: int = Field(default=5)

    # Graph edges — server JSON keys match field names: related_ids / main_ids.
    related_ids: List[int] = Field(default_factory=list)
    main_ids: List[int] = Field(default_factory=list)

    archived: bool = Field(default=False)
    consolidated: bool = Field(default=False)
    status: str = Field(default="saved")  # plain str — see the type/status Junior Tip above

    metadata: str = Field(default="")
    summary: str = Field(default="")

    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None

    # Temporal versioning.
    superseded_by: Optional[int] = None
    valid_from: Optional[datetime] = None
    valid_until: Optional[datetime] = None

    # Full payload content (only populated by content/topology endpoints).
    content: Optional[Any] = None


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


class DeleteFileResult(BaseModel):
    """
    Response of ``DELETE /api/v1/records/by-file`` — apagar TODO o rastro de um
    arquivo ingerido (root + capítulos + satélites) de uma sessão.

    ``matched_count`` é o que o prefixo ENCONTROU; ``deleted_count`` é o que o
    cluster realmente apagou. Em ``dry_run`` só ``matched_count`` é preenchido:
    nada foi escrito, então ``deleted_count`` fica 0 de propósito.

    Junior Tip [por que a contagem faz parte do contrato]: "apaguei 0 registros"
    tem de ser visível para o chamador. Um método que devolvesse apenas ``None``
    transformaria um prefixo errado em sucesso silencioso — e perda silenciosa é
    a falha número um deste projeto.

    ``deleted_ids`` e ``raft_index`` chegam com ``omitempty`` do servidor: em
    dry-run, ou quando nada casou, as chaves não existem no JSON. Ausência aqui
    é informação legítima, não erro de parse — por isso ambos têm default.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    session_uuid: str = Field(default="")
    ingest_key_prefix: str = Field(default="")
    matched_count: int = Field(default=0)
    deleted_count: int = Field(default=0)
    deleted_ids: List[int] = Field(default_factory=list)
    dry_run: bool = Field(default=False)
    raft_index: int = Field(default=0)


class EntityModel(BaseModel):
    """
    A named entity in the AnhurDB knowledge graph (Layer 2).

    Real-world objects (people, organisations, concepts) linked to memory
    records. ``entity_type`` is NOT ``record.type`` (episodic/fact/decision) —
    the cross-layer link (``link_record_entity``) is the tag.

    Wire key is ``entity_type`` (AnhurDB entityToResponse). ``type`` is accepted
    only as a legacy alias for older upsert responses.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: Optional[int] = None
    name: str = ""
    entity_type: str = Field(
        default="",
        validation_alias=AliasChoices("entity_type", "type"),
        serialization_alias="entity_type",
    )
    summary: str = ""
    attributes: Optional[Dict[str, Any]] = None
    dimension: Optional[int] = None
    first_seen: Optional[str] = None
    last_seen: Optional[str] = None
    mention_count: Optional[int] = None
    weight: Optional[float] = None


class EntityEdge(BaseModel):
    """
    A typed, temporal relationship between two entities.

    Examples: ``works_at``, ``knows``, ``part_of``, ``created_by``.
    Edges carry optional confidence scores and validity windows.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    source_id: int
    target_id: int
    relation: str
    event_time: Optional[str] = None
    valid_until: Optional[str] = None
    confidence: Optional[float] = None
    source_record_id: Optional[int] = None
