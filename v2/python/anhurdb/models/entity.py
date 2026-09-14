"""Entity-graph (Layer 2) response envelopes.

One domain: what the ``/api/v1/entities*`` routes answer with. The entity
itself (``EntityModel``) already lives in ``models/record.py`` and is NOT
duplicated here — only the envelopes that carry it, plus the edge shape the
graph and timeline routes emit.

Routes covered:

- ``GET /api/v1/entities/list``            -> ``EntitiesPage``   (HAS a cursor)
- ``GET /api/v1/entities``                 -> ``list[EntityModel]``
- ``GET /api/v1/records/{id}/entities``    -> ``list[EntityModel]``
- ``GET /api/v1/entities/{id}/graph``      -> ``EntityGraphResult``
- ``GET /api/v1/entities/{id}/timeline``   -> ``EntityTimelineResult``
"""

from typing import List, Optional

from pydantic import BaseModel, ConfigDict, Field

from .record import EntityModel


class EntityGraphEdge(BaseModel):
    """A typed, temporal relationship as the READ routes serialise it.

    Server truth: ``server/database/entity.go:40-51``.

    Junior Tip [why this is not ``models.record.EntityEdge``]: ``EntityEdge``
    is the shape a caller BUILDS for ``upsert_entity_edge`` — it has no ``id``
    (the server assigns it), no ``ingested_at`` (the server stamps it) and no
    ``weight`` (the server derives it), and its three identity fields are
    required because a write without them is meaningless. This type is what the
    server ANSWERS with: every field is present and every field has a default,
    because a read model that raises on a missing key destroys the whole
    response over one row. Same concept, opposite direction, so two types.

    ``event_time``, ``valid_until`` and ``source_record_id`` are ``omitempty``
    on the wire — their absence is information (never invalidated / not
    sourced from a record), not a parse failure.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: int = Field(default=0)
    source_id: int = Field(default=0)
    target_id: int = Field(default=0)
    relation: str = Field(default="")
    confidence: float = Field(default=0.0)
    weight: float = Field(default=0.0)
    event_time: Optional[str] = Field(default=None)
    ingested_at: str = Field(default="")
    valid_until: Optional[str] = Field(default=None)
    source_record_id: Optional[int] = Field(default=None)


class EntityGraphNode(BaseModel):
    """One node of the entity graph: the entity plus the edges leaving it."""

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    entity: EntityModel = Field(default_factory=EntityModel)
    edges: List[EntityGraphEdge] = Field(default_factory=list)


class EntityGraphResult(BaseModel):
    """Envelope of ``GET /api/v1/entities/{id}/graph``.

    ``depth`` is the depth the SERVER used, echoed back. When the SDK omits the
    query parameter the server applies its own default of **1**
    (``handler/entity.go:280``), and this field is how the caller learns that
    without having to know the default — which is exactly why the SDK omits the
    parameter instead of restating ``depth=1`` on the wire.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    entity_id: int = Field(default=0)
    depth: int = Field(default=0)
    node_count: int = Field(default=0)
    nodes: List[EntityGraphNode] = Field(default_factory=list)


class EntityTimelineResult(BaseModel):
    """Envelope of ``GET /api/v1/entities/{id}/timeline``.

    The timeline includes INVALIDATED edges on purpose — that is the whole
    point of asking for a history rather than for the graph. An edge whose
    ``valid_until`` is set is a relationship that ENDED, not a corrupt row.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    entity: EntityModel = Field(default_factory=EntityModel)
    timeline: List[EntityGraphEdge] = Field(default_factory=list)
    record_ids: List[int] = Field(default_factory=list)
    edge_count: int = Field(default=0)


class EntitiesPage(BaseModel):
    """Envelope of ``GET /api/v1/entities/list`` — the paged walk.

    Junior Tip [this route DOES carry a cursor, unlike the manifest]:
    ``handler/entity.go:201-207`` sends ``next_offset`` alongside ``has_more``.
    Follow the cursor rather than computing ``offset + count`` yourself: the
    walk is ordered by ``id ASC`` so that pages do not shift under concurrent
    inserts, and the server is the only side that knows where it actually
    stopped.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    entities: List[EntityModel] = Field(default_factory=list)
    count: int = Field(default=0)
    total: int = Field(default=0)
    limit: int = Field(default=0)
    offset: int = Field(default=0)
    has_more: bool = Field(default=False)
    next_offset: int = Field(default=0)
