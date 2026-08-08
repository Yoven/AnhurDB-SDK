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


class SearchResult(BaseModel):
    """
    A single search hit combining a record with its relevance score.

    Returned by ``/api/v1/search``, ``/api/v1/search/type``,
    and ``/api/v1/search/smart``.
    """

    record: Record
    similarity: float = 0.0


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
