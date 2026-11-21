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
from pydantic import BaseModel, ConfigDict, Field, model_validator
from datetime import datetime

from .enums import MemoryType, MemoryStatus

# Junior Tip [re-export, 4.0.0]: the delete-by-file models moved to
# ``delete_file.py`` when this file crossed the house 300-line cut;
# ``from anhurdb.models.record import DeleteFileResult`` keeps working.
from .delete_file import DeleteFileLeg, DeleteFileResult, FileSessionCandidate  # noqa: F401


class CreateRequest(BaseModel):
    """
    Payload for creating a new memory record.

    Required:
        session_id — the write session from ``create_session`` /
        ``POST /api/v1/sessions``.

    The server automatically computes embeddings and search indexes when
    ``dimension=0`` (default). Set ``dimension``, ``vector``, and ``prefix``
    only when providing a pre-computed embedding.

    Junior Tip [why there is no ``uuid`` field any more — 4.0.0]: the record
    column is called ``uuid``, and this model used to carry a ``uuid=`` "legacy
    alias for session_id". Two fields for one value is how a caller ends up
    setting the wrong one: the server copies ``session_id`` into the column
    itself (``server/model/record.go:137-140``, ``handler/record_crud.go:49-52``),
    so ``session_id`` is the only name the SDK speaks. ``CreateRequest(uuid=...)``
    RAISES instead of being dropped by ``extra="ignore"``: a silently ignored
    session would surface later as "session_id is required" far from the line
    that caused it.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    session_id: str = Field(default="", description="Write session from create_session")

    @model_validator(mode="before")
    @classmethod
    def _refuse_removed_uuid_alias(cls, data: Any) -> Any:
        """Fail loud on the ``uuid=`` alias removed in 4.0.0 (see class doc)."""
        if isinstance(data, dict) and "uuid" in data:
            raise ValueError(
                "CreateRequest(uuid=...) was removed in anhurdb 4.0.0; "
                "pass session_id=... instead"
            )
        return data
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

    def optional_fields(self) -> Dict[str, Any]:
        """The keyword arguments ``Memory.create()`` accepts, as a dict.

        This is the migration bridge for the 3.0.0 signature change. Where a
        caller used to write ``await mem.create(req)`` they now write::

            await mem.create(
                req.session_id,
                req.content,
                **req.optional_fields(),
            )

        Junior Tip [why only the fields the caller actually SET are returned]:
        every field on this model has a default, so an unconditional dump would
        send ``score=5``, ``status="saved"`` and ``type="episodic"`` on a call
        where the caller pinned none of them — turning three server-side
        defaults into three client-side ones. The day the server changes a
        default, that call would keep writing the old value and nobody would
        see a diff. ``model_fields_set`` is pydantic's record of what was
        passed explicitly, so an unset field stays unset all the way to the
        wire.
        """
        settable = (
            "type",
            "score",
            "status",
            "related_ids",
            "valid_from",
            "valid_until",
            "metadata",
        )
        return {
            field_name: getattr(self, field_name)
            for field_name in settable
            if field_name in self.model_fields_set
        }


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


class EntityModel(BaseModel):
    """
    A named entity in the AnhurDB knowledge graph (Layer 2).

    Real-world objects (people, organisations, concepts) linked to memory
    records. ``entity_type`` is NOT ``record.type`` (episodic/fact/decision) —
    the cross-layer link (``link_record_entity``) is the tag.

    Wire key is ``entity_type`` on every entity route — search, list, graph,
    timeline and upsert (``server/handler/entity.go:89`` entityToResponse,
    ``:438-441`` upsert). The ``type`` read alias was removed in 4.0.0: no
    server answer carries it, and accepting it let a record-shaped dict
    (``type: "fact"``) masquerade as an entity.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: Optional[int] = None
    name: str = ""
    entity_type: str = Field(default="")
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
