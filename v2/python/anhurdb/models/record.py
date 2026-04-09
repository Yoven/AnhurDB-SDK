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
    Payload for ``POST /api/v1/records`` — creates a new memory record.

    Required fields:
        uuid: Session identifier that groups related records.

    Optional fields mirror the Go server's ``RecordCreateRequest``:
        type, summary, content, score, weight, status, related_ids,
        metadata, valid_from, valid_until.

    Fields like ``dimension``, ``vector``, and ``main_ids`` default to
    zero/empty because the server's regression agent populates them
    asynchronously after creation.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    uuid: str
    type: MemoryType = Field(default=MemoryType.EPISODIC)

    # Summary is indexed by FTS5 for keyword search (max ~500 chars).
    summary: str = Field(default="")

    # Content is the full payload stored on disk (gzip-compressed).
    content: str = Field(default="")

    # Cognitive weight (0-10). Higher = more important in search ranking.
    score: int = Field(default=5)

    # Search weight (0.0-1.5). Controls ranking in hybrid search results.
    weight: float = Field(default=0.5)

    # Initial lifecycle status. Agents set "saved", regression promotes.
    status: str = Field(default="saved")

    # Graph edges: IDs of parent records this record is linked to.
    # For episodic → link to previous episodic (auto-chain if omitted).
    # For derived types → link to the episodic they orbit.
    related_ids: List[int] = Field(default_factory=list)

    # Metadata string (e.g. container_tag for user identification).
    metadata: str = Field(default="")

    # Vector dimension in bits (0 = no vector, server handles embedding).
    dimension: int = Field(default=0)

    # Binary vector for Hamming search (base64-encoded). Usually empty
    # on creation — the regression agent computes it asynchronously.
    vector: str = Field(default="")

    # Legacy/internal fields (kept for wire compatibility).
    prefix: str = Field(default="")
    main_ids: List[int] = Field(default_factory=list)
    consolidate_id: int = Field(default=0)
    consolidated: bool = Field(default=False)

    # Temporal versioning (v6) — optional validity window.
    valid_from: Optional[str] = Field(default=None)
    valid_until: Optional[str] = Field(default=None)


class Record(BaseModel):
    """
    A complete cognitive memory record as returned by the AnhurDB API.

    This model covers every field the server may return across different
    endpoints (search, topology, manifest, content). Fields that don't
    apply to a particular endpoint will use their defaults.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: Optional[int] = Field(default=0)
    uuid: str = ""
    type: MemoryType = Field(default=MemoryType.EPISODIC)

    # Vector metadata (populated by regression agent, not by SDK callers).
    dimension: int = Field(default=0)
    prefix: str = Field(default="")
    weight: float = Field(default=0.0)
    score: int = Field(default=5)

    # Graph edges (JSON arrays in the database, hence the alias).
    related_ids: List[int] = Field(default_factory=list, alias="related_json")
    main_ids: List[int] = Field(default_factory=list, alias="main_json")

    # Consolidation pointers.
    consolidate_id: int = Field(default=0)
    consolidated: bool = Field(default=False)
    archived: bool = Field(default=False)
    status: MemoryStatus = Field(default=MemoryStatus.SAVED)

    # Content fields.
    metadata: str = Field(default="")
    summary: str = Field(default="")

    # Storage references.
    file_path: str = Field(default="")
    checksum: str = Field(default="")

    # Timestamps.
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None

    # Temporal versioning (v6).
    superseded_by: Optional[int] = None
    valid_from: Optional[datetime] = None
    valid_until: Optional[datetime] = None

    # Binary vector (not usually returned in plain queries).
    vector: Optional[str] = None

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

    Entities represent real-world objects (people, organisations, concepts)
    that are extracted from or linked to memory records.
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
