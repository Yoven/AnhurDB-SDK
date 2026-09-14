"""Record-graph reads: topology, grounding and the two walks.

One domain: the four endpoints that answer with a piece of the RECORD graph
(Layer 1). Split out of ``client/__init__.py`` in 3.0.0 when these four
returns went from ``Dict[str, Any]`` to typed models — the file was already
past the 300-line house cut, so the domain moved before it grew.

The ENTITY graph (Layer 2) is a different graph with different edges and lives
in ``client/entities.py``.
"""

import base64
from typing import Any, Dict, List, Optional

from .connection import HTTPConnection
from ..models.graph import ContextResult, GroundingResult, WalkResult


class RecordGraphMixin:
    """Topology / grounding / walk reads over the record graph."""

    _connection: HTTPConnection

    async def get_context(
        self,
        record_id: int,
    ) -> ContextResult:
        """Get the topological context (1-hop neighbours) around a record.

        Returns the target record plus its parent, child, and sibling
        records in the knowledge graph.

        Args:
            record_id: The record ID to inspect.

        Returns:
            ``ContextResult`` — ``target`` is a full ``Record`` and
            ``neighbors`` is a list of full ``Record``s. Both are complete
            records, not id stubs."""
        data = await self._connection.get(
            f"/api/v1/records/{record_id}/topology",
        )
        return ContextResult.model_validate(data if isinstance(data, dict) else {})

    async def get_grounding(
        self,
        record_id: int,
        max_depth: int = 3,
    ) -> GroundingResult:
        """Get the provenance ("grounding") subgraph for a record — the episodic
        anchors and consolidated stars that this record was derived from.

        Performs a server-side BFS over main_ids/related_ids to surface WHERE a
        memory came from, with the anchors' raw chat snippets attached.

        Args:
            record_id: The record whose provenance you want.
            max_depth: BFS depth budget, integer 1..5 inclusive (default 3).

        Returns:
            ``GroundingResult``. ``found_count == 0`` with HTTP 200 is the
            legitimate answer for a record with no episodic ancestry — it is
            not an error and must not be treated as one.

        Raises:
            ValueError: If ``max_depth`` is outside 1..5 (fail fast locally
                        rather than round-trip to a guaranteed HTTP 400)."""
        # Validate locally so we fail loud and cheaply — the server enforces the
        # exact same 1..5 bound and would 400, but a clear ValueError is kinder.
        if not isinstance(max_depth, int) or max_depth < 1 or max_depth > 5:
            raise ValueError("max_depth must be an integer between 1 and 5")
        data = await self._connection.get(
            f"/api/v1/records/{record_id}/grounding",
            params={"max_depth": str(max_depth)},
        )
        return GroundingResult.model_validate(data if isinstance(data, dict) else {})

    async def walk(
        self,
        start_id: int,
        depth: int = 3,
    ) -> WalkResult:
        """BFS graph traversal from a seed record.

        Follows related_ids and main_ids edges in both directions up to the
        specified depth.

        Args:
            start_id:  Record ID to start from.
            depth:     Maximum hops (default 3).

        Returns:
            ``WalkResult`` — ``nodes`` are FULL records, ``edges`` are
            ``{source, target}`` pairs, and ``truncated`` tells you whether the
            server hit a budget and stopped early. Ignoring ``truncated`` is
            how a partial graph gets mistaken for the whole one."""
        data = await self._connection.post(
            "/api/v1/walk",
            {"seed_id": start_id, "depth": depth, "direction": "both"},
        )
        return WalkResult.model_validate(data if isinstance(data, dict) else {})

    async def walk_semantic(
        self,
        start_id: int,
        depth: int = 3,
        *,
        target: Optional[str] = None,
        goal_vector: Optional[bytes] = None,
        target_tag: Optional[str] = None,
        max_cost: Optional[float] = None,
    ) -> WalkResult:
        """Semantic graph walk — follows edges weighted by vector similarity.

        Unlike regular ``walk()``, this prioritises semantically related
        records rather than just following structural edges. By default the
        server runs a plain Dijkstra traversal (edge cost ``1 − similarity``).

        Passing ``target`` upgrades the walk to a goal-directed A* search that
        is steered toward the requested goal:

          - ``"semantic"``: pulls toward the ``goal_vector`` guide embedding
            (supply raw packed bytes; the SDK base64-encodes them for the wire).
          - ``"tag"``: pulls toward records carrying ``target_tag``.
          - ``"recency"``: pulls toward the newest records.

        Args:
            start_id:    Record ID to start from.
            depth:       Maximum hops (default 3). Retained for backward
                         compatibility; the semantic walk is bounded by
                         ``max_cost``/``max_nodes`` server-side.
            target:      Goal mode — ``"semantic"``, ``"tag"`` or ``"recency"``.
                         ``None`` (default) → plain Dijkstra.
            goal_vector: Guide embedding as raw bytes, required when
                         ``target="semantic"``; sent base64-encoded.
            target_tag:  Entity/tag name to steer toward, required when
                         ``target="tag"``.
            max_cost:    Optional cost budget (server default 2.0).

        Returns:
            ``WalkResult``, with ``truncated`` left as ``None``.

            Junior Tip [why ``truncated`` is ``None`` here and a bool on
            ``walk()``]: this endpoint answers ``{nodes, edges}`` and nothing
            else (``handler/record_search_graph.go:337-340``) — it never made a
            completeness claim. Reporting ``False`` would invent one."""
        # before, then attach only the goal fields the caller actually set. An
        # unset field is never serialized, so the server sees the identical
        # payload it received prior to the goal-directed feature.
        body: Dict[str, Any] = {"seed_id": start_id, "depth": depth}
        if max_cost is not None:
            body["max_cost"] = max_cost
        if target is not None:
            body["target"] = target
        if goal_vector is not None:
            body["vector"] = base64.b64encode(goal_vector).decode("ascii")
        if target_tag is not None:
            body["target_tag"] = target_tag
        data = await self._connection.post(
            "/api/v1/walk/semantic",
            body,
        )
        return WalkResult.model_validate(data if isinstance(data, dict) else {})


__all__ = ["RecordGraphMixin"]
