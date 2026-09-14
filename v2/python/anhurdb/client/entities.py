"""Entity knowledge graph (Layer 2) — reads and writes.

One domain: everything under ``/api/v1/entities*`` plus the two cross-layer
routes that join a record to an entity. Split out of ``client/__init__.py`` in
3.0.0 when these returns became typed models; the file was already past the
300-line house cut.

Layer 1 is records (episodic / fact / decision). Layer 2 is entities (people,
organisations, concepts). ``entity_type`` is NEVER ``record.type`` — keeping
the two vocabularies apart is why the wire key is ``entity_type`` and not
``type``.
"""

from typing import Any, Dict, List, Optional

from .connection import HTTPConnection
from ..models.entity import (
    EntitiesPage,
    EntityGraphResult,
    EntityTimelineResult,
)
from ..models.record import EntityModel


class EntityMixin:
    """Entity search, upsert, graph, timeline and record links."""

    _connection: HTTPConnection

    async def search_entities(
        self,
        query: Optional[str] = None,
        entity_type: Optional[str] = None,
        limit: int = 20,
    ) -> List[EntityModel]:
        """Search named entities (people, organisations, concepts).

        Args:
            query:       Name or keyword search.
            entity_type: Filter by entity type (e.g. ``"person"``).
            limit:       Maximum results (default 20).

        Returns:
            List of ``EntityModel``. Use :meth:`list_entities` when you need
            the page metadata as well."""
        params: Dict[str, str] = {"limit": str(limit)}
        if query:
            params["q"] = query
        if entity_type:
            params["type"] = entity_type
        data = await self._connection.get("/api/v1/entities", params=params)
        return _entities_from(data)

    async def list_entities(
        self,
        limit: int = 200,
        offset: int = 0,
    ) -> EntitiesPage:
        """Paginated walk of ALL entities for the tenant, ordered by id ASC.

        Unlike :meth:`search_entities` (keyword LIKE filter, limited match
        set), this walks every row with a stable cursor — pages never shift
        under concurrent inserts. Use for analytics, normalization sweeps,
        exports, or admin dashboards.

        Args:
            limit:     Page size (default 200, server-clamped to [1, 500]).
            offset:    0-based offset (default 0).

        Returns:
            ``EntitiesPage``. Follow ``next_offset`` while ``has_more`` — this
            route sends a real cursor, unlike the manifest routes."""
        if limit <= 0:
            limit = 200
        if limit > 500:
            limit = 500
        if offset < 0:
            offset = 0
        params = {"limit": str(limit), "offset": str(offset)}
        data = await self._connection.get("/api/v1/entities/list", params=params)
        return EntitiesPage.model_validate(data if isinstance(data, dict) else {})

    async def upsert_entity(
        self,
        name: str,
        entity_type: str = "",
        summary: str = "",
        attributes: Optional[Dict[str, Any]] = None,
    ) -> EntityModel:
        """Create or update a named entity (idempotent by name).

        Args:
            name:        Entity name (required).
            entity_type: Entity type (e.g. ``"person"``, ``"organization"``).
            summary:     Short description.
            attributes:  Arbitrary key-value metadata.

        Returns:
            ``EntityModel`` carrying ``id``, ``name`` and ``entity_type``.

            Junior Tip [this response is a PARTIAL entity, by design]: the
            handler answers with exactly those three keys
            (``handler/entity.go:438-442``) — it does not echo back
            ``summary``, ``attributes`` or the counters. Every other field on
            the model therefore holds its default here; that is the server
            declining to repeat what you just sent, not data loss. Re-read with
            :meth:`search_entities` if you need the full row."""
        payload: Dict[str, Any] = {"name": name}
        if entity_type:
            payload["entity_type"] = entity_type
        if summary:
            payload["summary"] = summary
        if attributes:
            payload["attributes"] = attributes
        data = await self._connection.post("/api/v1/entities", payload)
        return EntityModel.model_validate(data if isinstance(data, dict) else {})

    async def get_entity_graph(
        self,
        entity_id: int,
        depth: Optional[int] = None,
    ) -> EntityGraphResult:
        """BFS traversal of entity relationships.

        Starting from an entity, discovers connected entities through
        typed edges (``works_at``, ``knows``, ``part_of``, etc.).

        Args:
            entity_id: The starting entity ID.
            depth:     How many hops to follow. ``None`` (default) omits the
                       query parameter entirely and lets the SERVER apply its
                       own default of **1**.

        Returns:
            ``EntityGraphResult``. ``depth`` echoes what the server actually
            used, so an omitted request still tells you the depth you got.

        Junior Tip [why the default is not 2 any more — BREAKING in 3.0.0]:
        this SDK defaulted ``depth=2`` and SENT it, while the handler's own
        default is 1 (``handler/entity.go:280``) and Go/TypeScript both omitted
        the parameter. Live proof 2026-09-14: omitted -> ``depth:1,
        node_count:1``; ``?depth=2`` -> ``depth:2, node_count:2``. So the same
        call returned a strictly larger graph in Python than in the other two
        arms, and a caller comparing SDKs saw "Python finds more entities" when
        the truth was "Python silently asked a different question". Omitting
        beats restating the default: when the server changes its default, the
        SDK follows instead of pinning yesterday's value."""
        params: Dict[str, str] = {}
        if depth is not None:
            params["depth"] = str(depth)
        data = await self._connection.get(
            f"/api/v1/entities/{entity_id}/graph",
            params=params or None,
        )
        return EntityGraphResult.model_validate(data if isinstance(data, dict) else {})

    async def entity_graph(
        self,
        entity_id: int,
        depth: Optional[int] = None,
    ) -> EntityGraphResult:
        """Alias of :meth:`get_entity_graph` using the canonical ``entity_graph``
        name (matches the MCP tool ``get_entity_graph`` exposed to the SDKs as
        ``entity_graph``)."""
        return await self.get_entity_graph(entity_id, depth=depth)

    async def entity_timeline(
        self,
        entity_id: int,
    ) -> EntityTimelineResult:
        """Get the full temporal history of an entity's relationships.

        Shows ALL edges including invalidated ones, ordered by event time.
        Use to understand how an entity's context evolved over time.

        Args:
            entity_id: The entity ID.

        Returns:
            ``EntityTimelineResult`` with ``entity``, ``timeline``,
            ``record_ids`` and ``edge_count``."""
        data = await self._connection.get(
            f"/api/v1/entities/{entity_id}/timeline",
        )
        return EntityTimelineResult.model_validate(data if isinstance(data, dict) else {})

    async def upsert_entity_edge(
        self,
        source_id: int,
        target_id: int,
        relation: str,
        event_time: Optional[str] = None,
        confidence: Optional[float] = None,
        source_record_id: Optional[int] = None,
    ) -> Dict[str, Any]:
        """Create or update a typed relationship between two entities.

        Args:
            source_id:        Source entity ID.
            target_id:        Target entity ID.
            relation:         Relationship type (e.g. ``"works_at"``).
            event_time:       ISO 8601 timestamp when this became true.
            confidence:       Confidence score (0.0-1.0).
            source_record_id: Memory record that evidences this relationship.

        Returns:
            Confirmation dict. Deliberately left untyped — it is a
            ``{"message": ...}`` write ack, and all three SDKs agree on leaving
            those untyped in 3.0.0."""
        payload: Dict[str, Any] = {
            "source_id": source_id,
            "target_id": target_id,
            "relation": relation,
        }
        if event_time:
            payload["event_time"] = event_time
        if confidence is not None:
            payload["confidence"] = confidence
        if source_record_id is not None:
            payload["source_record_id"] = source_record_id
        return await self._connection.post("/api/v1/entities/edges", payload)

    async def link_record_entity(
        self,
        record_id: int,
        entity_id: int,
        role: str = "",
    ) -> Dict[str, Any]:
        """Link a memory record to an entity (cross-layer connection).

        Args:
            record_id: Memory record ID.
            entity_id: Entity ID.
            role:      Optional role description.

        Returns:
            Confirmation dict — an untyped write ack, same as
            :meth:`upsert_entity_edge`."""
        payload: Dict[str, Any] = {
            "record_id": record_id,
            "entity_id": entity_id,
        }
        if role:
            payload["role"] = role
        return await self._connection.post("/api/v1/entities/link", payload)

    async def get_record_entities(
        self,
        record_id: int,
    ) -> List[EntityModel]:
        """Get entities linked to a specific memory record.

        Args:
            record_id: The record ID.

        Returns:
            List of ``EntityModel``."""
        data = await self._connection.get(
            f"/api/v1/records/{record_id}/entities",
        )
        return _entities_from(data)


def _entities_from(data: Any) -> List[EntityModel]:
    """Decode the two UNPAGED entity envelopes into typed entities.

    Junior Tip [why a bare list is still accepted]: the routes answer
    ``{count, entities[]}`` today, but this SDK has always tolerated a bare
    array because an older server generation sent one. Keeping the tolerance
    costs one ``isinstance`` and removes a whole class of "works against prod,
    explodes against the OSS build" report. What it must NOT do is silently
    return ``[]`` for a shape it does not recognise — an unknown shape raises
    through pydantic instead.
    """
    rows = data.get("entities", []) if isinstance(data, dict) else data
    if not isinstance(rows, list):
        return []
    return [EntityModel.model_validate(row) for row in rows]


__all__ = ["EntityMixin"]
