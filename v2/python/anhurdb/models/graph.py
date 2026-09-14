"""Record-graph responses: walk, topology and grounding.

One domain: the three endpoints that answer with a piece of the RECORD graph
(Layer 1) rather than with a ranked list. The entity graph (Layer 2) is a
different graph with different edges and lives in ``models/entity.py``.

- ``POST /api/v1/walk`` and ``POST /api/v1/walk/semantic`` -> ``WalkResult``
- ``GET  /api/v1/records/{id}/topology``                   -> ``ContextResult``
- ``GET  /api/v1/records/{id}/grounding``                  -> ``GroundingResult``
"""

from typing import Dict, List, Optional

from pydantic import BaseModel, ConfigDict, Field

from .record import Record


class WalkEdge(BaseModel):
    """One traversed edge: nothing but the two endpoints.

    Junior Tip [there is no edge ``type`` here, verified 2026-09-14]: the
    handler builds an anonymous struct with exactly ``source`` and ``target``
    (``handler/record_search_graph.go:325-334``). A ``type`` field on this
    shape would decode as ``None`` forever and read to the next maintainer like
    a value the server merely forgot to fill.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    source: int = Field(default=0)
    target: int = Field(default=0)


class WalkResult(BaseModel):
    """Envelope of a graph walk: ``{nodes, edges, truncated}``.

    ``nodes`` are FULL records, not a lossy projection — the handler serialises
    ``*model.Record`` straight out of the database layer.

    Junior Tip [why ``truncated`` is Optional and not ``False``]: only
    ``POST /api/v1/walk`` emits it (``record_search_graph.go:222``);
    ``POST /api/v1/walk/semantic`` answers ``{nodes, edges}`` and nothing else
    (``:337-340``). Defaulting to ``False`` would tell a semantic-walk caller
    "the result is complete" on an endpoint that never made that promise —
    a silent guarantee the server did not give. ``None`` means "this door does
    not report truncation"; ``False`` means "this door reported complete".
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    nodes: List[Record] = Field(default_factory=list)
    edges: List[WalkEdge] = Field(default_factory=list)
    truncated: Optional[bool] = Field(default=None)


class ContextResult(BaseModel):
    """Envelope of ``GET /api/v1/records/{id}/topology``: target + 1-hop ring.

    Both blocks are full ``Record``s (``handler/record_graph.go:270-273``).
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    target: Optional[Record] = Field(default=None)
    neighbors: List[Record] = Field(default_factory=list)


class GroundingTarget(BaseModel):
    """The record whose provenance was asked for — identity only."""

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: int = Field(default=0)
    uuid: str = Field(default="")
    type: str = Field(default="")
    summary: str = Field(default="")


class GroundingAnchor(BaseModel):
    """An episodic turn the target was derived from.

    ``content`` is a PARSED object (``{"user": ..., "assistant": ...}``), not a
    JSON string — the server already unmarshals the episodic payload
    (``service/record_grounding.go:109-117``) precisely so the caller does not
    have to guess at the shape a second time. It is ``omitempty`` on the wire,
    so absence is normal for a legacy plain-text anchor.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: int = Field(default=0)
    uuid: str = Field(default="")
    type: str = Field(default="")
    summary: str = Field(default="")
    content: Optional[Dict[str, str]] = Field(default=None)
    hops_from_target: int = Field(default=0)
    session_position: Optional[int] = Field(default=None)


class GroundingConsolidation(BaseModel):
    """A consolidated star between the target and its anchors."""

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: int = Field(default=0)
    uuid: str = Field(default="")
    summary: str = Field(default="")
    hops_from_target: int = Field(default=0)


class GroundingResult(BaseModel):
    """Envelope of ``GET /api/v1/records/{id}/grounding``.

    ``depth_used`` is what the BFS actually spent and ``max_depth`` is what it
    was allowed to spend; they differ whenever the search closed early.
    ``found_count`` counts anchors, so a value of ``0`` with HTTP 200 is the
    legitimate answer for a record with no episodic ancestry — not a failure.

    ``anchors_capped`` / ``consolidations_capped`` arrive ``omitempty``: the
    key is simply absent when nothing was cut, which is why both default to
    ``False`` rather than ``None`` (the server's own zero value IS "not
    capped").
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    target: GroundingTarget = Field(default_factory=GroundingTarget)
    anchors: List[GroundingAnchor] = Field(default_factory=list)
    consolidations: List[GroundingConsolidation] = Field(default_factory=list)
    depth_used: int = Field(default=0)
    max_depth: int = Field(default=0)
    found_count: int = Field(default=0)
    anchors_capped: bool = Field(default=False)
    consolidations_capped: bool = Field(default=False)
