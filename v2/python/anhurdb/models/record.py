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
from pydantic import BaseModel, ConfigDict, Field
from datetime import datetime

from .enums import MemoryType, MemoryStatus


class CreateRequest(BaseModel):
    """
    Payload for creating a new memory record.

    Required:
        uuid: Session identifier.

    The server automatically computes embeddings and search indexes when
    ``dimension=0`` (default). Set ``dimension``, ``vector``, and ``prefix``
    only when providing a pre-computed embedding.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    uuid: str
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

    Returned by ``/api/v1/search/global``, ``/api/v1/search/type``,
    and ``/api/v1/search/smart``.
    """

    record: Record
    similarity: float = 0.0


class EntityModel(BaseModel):
    """
    A named entity in the AnhurDB knowledge graph (Layer 2).

    Real-world objects (people, organisations, concepts) linked to memory
    records. ``entity_type`` is NOT ``record.type`` (episodic/fact/decision) —
    the cross-layer link (``link_record_entity``) is the tag.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: Optional[int] = None
    name: str = ""
    entity_type: str = Field(default="", alias="type")
    summary: str = ""
    attributes: Optional[Dict[str, Any]] = None
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None


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
