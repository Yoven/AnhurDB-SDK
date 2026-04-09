"""
AnhurDB Python SDK — main client module.

Provides two client classes:

- **Memory**: Dead-simple 3-method API (``add``, ``search``, ``profile``)
  that matches the TypeScript and Go SDKs exactly. Best for quick
  integration where you just want memory to work.

- **AnhurClient**: Full-featured async client exposing every AnhurDB
  endpoint — CRUD, batch operations, entity graph, file upload,
  cognitive search, and temporal versioning.

Usage::

    # Simple API (recommended for most users)
    from anhurdb import Memory

    async with Memory(api_key="anhur_xxx") as mem:
        await mem.add("User is a data scientist at Google")
        results = await mem.search("what does this user do?")
        profile = await mem.profile()

    # Full API (for power users)
    from anhurdb import AnhurClient

    async with AnhurClient(api_key="anhur_xxx") as client:
        await client.create(CreateRequest(uuid="session-1", content="..."))
        results = await client.search("query", limit=20)
        entities = await client.search_entities(query="Google")

Junior Tip: Both classes use ``X-API-Key`` authentication, matching the
Go server's middleware. The ``Memory`` class auto-generates session UUIDs
and container tags from the API key hash, just like the TypeScript and
Go SDKs.
"""

import hashlib
import os
import secrets
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from .connection import HTTPConnection
from .exceptions import AnhurError, AnhurQueryError
from ..models import (
    CreateRequest,
    EntityEdge,
    EntityModel,
    MemoryType,
    Record,
    SearchResult,
    SessionStats,
)
from ..query import QueryBuilder, SemanticMode


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Default cloud endpoint. Self-hosted users pass ``url`` explicitly.
DEFAULT_CLOUD_URL = "https://api.anhurdb.com"


# ---------------------------------------------------------------------------
# Helper: derive a stable container tag from the API key
# ---------------------------------------------------------------------------

def _derive_container_tag(api_key: str) -> str:
    """
    Derive a short, stable hex tag from the API key using SHA-256.

    The first 12 hex characters of the hash are used, prefixed with
    ``mem-``. This matches the algorithm in the TypeScript and Go SDKs
    so the same API key always produces the same container tag across
    all three languages.

    Args:
        api_key: The raw API key string.

    Returns:
        A container tag like ``mem-a1b2c3d4e5f6``.
    """
    digest = hashlib.sha256(api_key.encode("utf-8")).hexdigest()
    return f"mem-{digest[:12]}"


def _utc_timestamp() -> str:
    """Return current UTC time as ``YYYYMMDD-HHMMSS``."""
    now = datetime.now(timezone.utc)
    return now.strftime("%Y%m%d-%H%M%S")


# ---------------------------------------------------------------------------
# Memory — simple 3-method API (matches TypeScript/Go)
# ---------------------------------------------------------------------------

class Memory:
    """
    Dead-simple memory interface for AnhurDB.

    Handles session management, container tagging, and cloud/OSS fallback
    automatically. Mirrors the TypeScript ``Memory`` class and Go
    ``client.Memory`` struct method-for-method.

    Core methods:
        - ``add(text)``    — store a memory
        - ``search(query)`` — find relevant memories
        - ``profile()``    — get user/agent profile

    Extended methods:
        - ``search_by_type``, ``recall``, ``walk``, ``list_sessions``,
          ``get_context``, ``read_content``, ``recent``, ``update``,
          ``delete``, ``new_session``

    Args:
        api_key:   AnhurDB API key (required). Falls back to
                   ``ANHUR_API_KEY`` environment variable.
        url:       Server URL (default: cloud endpoint).
        user_id:   Explicit container tag. When omitted, derived from
                   API key hash.
        tenant_id: Optional ``X-Tenant-ID`` header for multi-tenant.
        mode:      Transport — ``"rest"`` (default) or ``"mcp"``.
    """

    def __init__(
        self,
        api_key: Optional[str] = None,
        url: str = DEFAULT_CLOUD_URL,
        user_id: Optional[str] = None,
        tenant_id: str = "",
        mode: str = "rest",
    ):
        key = api_key or os.environ.get("ANHUR_API_KEY", "")
        if not key:
            raise ValueError(
                "api_key is required. Pass it directly or set ANHUR_API_KEY."
            )

        self._connection = HTTPConnection(
            base_url=url,
            api_key=key,
            tenant_id=tenant_id,
            mode=mode,
        )

        # Container tag: explicit user_id or SHA-256 derived from API key.
        if user_id:
            self._container_tag = user_id
        else:
            self._container_tag = _derive_container_tag(key)

        # Session UUID: container_tag + UTC timestamp.
        self._session_uuid = f"{self._container_tag}-{_utc_timestamp()}"

        # Cloud ingest availability (None = untested).
        self._ingest_available: Optional[bool] = None

    # -- Lifecycle ----------------------------------------------------------

    async def connect(self) -> None:
        """Open the HTTP session."""
        await self._connection.connect()

    async def close(self) -> None:
        """Close the HTTP session."""
        await self._connection.close()

    async def __aenter__(self) -> "Memory":
        await self.connect()
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.close()

    # -- Properties ---------------------------------------------------------

    @property
    def session_id(self) -> str:
        """Current session UUID."""
        return self._session_uuid

    @property
    def container_tag(self) -> str:
        """Container tag identifying this user/agent."""
        return self._container_tag

    # -- Core: add() --------------------------------------------------------

    async def add(
        self,
        text: str,
        *,
        score: int = 5,
        type: MemoryType = MemoryType.EPISODIC,
    ) -> Dict[str, Any]:
        """
        Store a memory. Simplest way to save information.

        Tries the cloud ``/api/v1/ingest`` endpoint first (auto-embedding
        + entity extraction). If that returns 404, falls back to direct
        record creation via ``/api/v1/records`` (OSS mode, FTS5 only).

        Args:
            text:  The text to remember (required, non-empty).
            score: Importance rating 1-10 (default 5).
            type:  Memory type (default ``episodic``).

        Returns:
            Dict with ``session_id``, ``records``, and ``mode``
            (``"cloud"`` or ``"oss"``).

        Raises:
            ValueError: If ``text`` is empty.

        Example::

            result = await mem.add("User prefers dark mode", score=8,
                                   type=MemoryType.PREFERENCE)
        """
        if not text:
            raise ValueError("text cannot be empty")

        # Try cloud ingest (has auto-embedding + extraction).
        if self._ingest_available is not False:
            result = await self._try_ingest(text, score, type)
            if result is not None:
                return result

        # Fallback: direct record creation (OSS / self-hosted).
        return await self._create_record(text, score, type)

    # -- Core: search() -----------------------------------------------------

    async def search(
        self,
        query: str,
        *,
        limit: int = 10,
        type_filter: Optional[str] = None,
    ) -> List[Dict[str, Any]]:
        """
        Search for relevant memories using hybrid (vector + full-text) search.

        Uses global search (not session-scoped) so it finds facts across
        ALL sessions for this user.

        Args:
            query:       Natural language query (required).
            limit:       Maximum results (default 10).
            type_filter: Optional filter by memory type.

        Returns:
            List of dicts with ``id``, ``type``, ``summary``, ``score``,
            ``metadata``, ``content``.

        Example::

            hits = await mem.search("what does this user do?", limit=5)
            for hit in hits:
                print(hit["summary"], hit["score"])
        """
        if not query:
            raise ValueError("query cannot be empty")

        payload: Dict[str, Any] = {
            "query": query,
            "text": query,
            "limit": limit,
        }
        if type_filter:
            payload["type_filter"] = type_filter

        data = await self._connection.post("/api/v1/search/global", payload)
        return self._flatten_search_results(data)

    # -- Core: profile() ----------------------------------------------------

    async def profile(self) -> Dict[str, Any]:
        """
        Get the memory profile for this container tag (user/agent).

        Returns profile information including static facts, dynamic state,
        and aggregate statistics. If the server doesn't support profiles
        (OSS without agents), returns an empty profile rather than raising.

        Returns:
            Dict with ``static``, ``dynamic``, ``stats`` keys.

        Example::

            prof = await mem.profile()
            print(prof["static"])  # identity facts
        """
        try:
            data = await self._connection.get(
                "/api/v1/profile",
                params={"tag": self._container_tag},
            )
            return {
                "static": data.get("static", {}),
                "dynamic": data.get("dynamic", {}),
                "stats": data.get("stats", {}),
            }
        except AnhurQueryError as exc:
            # 404 = server doesn't support profiles (OSS mode).
            if "404" in str(exc):
                return {
                    "static": {},
                    "dynamic": {},
                    "stats": {},
                    "tag": self._container_tag,
                    "status": "not_available",
                }
            raise

    # -- Extended: search_by_type() -----------------------------------------

    async def search_by_type(
        self,
        memory_type: str,
        limit: int = 20,
    ) -> List[Dict[str, Any]]:
        """
        Search for memories filtered by cognitive type.

        Faster than semantic search when you know the exact type.

        Args:
            memory_type: Type to filter (e.g. ``"fact"``, ``"risk"``).
            limit:       Maximum results (default 20).

        Returns:
            List of search result dicts.
        """
        params = {"type": memory_type, "limit": str(limit)}
        data = await self._connection.get("/api/v1/search/type", params=params)
        return data.get("results", [])

    # -- Extended: recall() -------------------------------------------------

    async def recall(self, query: str, limit: int = 10) -> List[Dict[str, Any]]:
        """
        Recall memories via global search (alias for ``search``).

        Named to match the MCP ``recall`` tool which performs a cognitive
        fan-out across smart_search + fact search + consolidated search.

        Args:
            query: Natural language query.
            limit: Maximum results (default 10).

        Returns:
            List of search result dicts.
        """
        return await self.search(query, limit=limit)

    # -- Extended: walk() ---------------------------------------------------

    async def walk(self, start_id: int, depth: int = 3) -> Dict[str, Any]:
        """
        Walk the memory graph starting from a given record.

        Performs BFS traversal following related_ids and main_ids edges
        in both directions up to the specified depth.

        Args:
            start_id: Record ID to start from.
            depth:    How many hops to traverse (default 3).

        Returns:
            Dict with ``nodes`` and ``edges`` arrays.
        """
        payload = {"seed_id": start_id, "depth": depth, "direction": "both"}
        return await self._connection.post("/api/v1/walk", payload)

    # -- Extended: list_sessions() ------------------------------------------

    async def list_sessions(self) -> List[Dict[str, Any]]:
        """
        List all sessions with aggregate statistics.

        Returns:
            List of dicts with ``uuid``, ``record_count``, ``types``,
            ``last_activity``.
        """
        data = await self._connection.get("/api/v1/sessions/stats")
        return data.get("sessions", data) if isinstance(data, dict) else data

    # -- Extended: get_context() --------------------------------------------

    async def get_context(self, record_id: int) -> Dict[str, Any]:
        """
        Get the topological context (1-hop neighbours) around a record.

        Returns the target record plus its parent, child, and sibling
        records in the knowledge graph.

        Args:
            record_id: The record ID to inspect.

        Returns:
            Dict with ``target`` and ``neighbors``.
        """
        return await self._connection.get(
            f"/api/v1/records/{record_id}/topology"
        )

    # -- Extended: read_content() -------------------------------------------

    async def read_content(self, record_id: int) -> str:
        """
        Read the full content payload for a record.

        Records store a summary for search indexing, but the full content
        may be much larger. This returns the complete gzip-decompressed text.

        Args:
            record_id: The record ID to read.

        Returns:
            The raw content string.
        """
        data = await self._connection.get(
            f"/api/v1/records/{record_id}/content"
        )
        if isinstance(data, dict):
            return data.get("content", str(data))
        return str(data)

    # -- Extended: recent() -------------------------------------------------

    async def recent(self, limit: int = 20) -> List[Dict[str, Any]]:
        """
        Fetch the most recent records from the global manifest.

        Args:
            limit: Maximum records (default 20).

        Returns:
            List of record dicts ordered by creation time (newest first).
        """
        data = await self._connection.get(
            "/api/v1/manifest",
            params={"limit": str(limit)},
        )
        return data.get("records", []) if isinstance(data, dict) else data

    # -- Extended: update() -------------------------------------------------

    async def update(self, record_id: int, **fields: Any) -> None:
        """
        Update fields on an existing record.

        Args:
            record_id: The record ID to update.
            **fields:  Keyword arguments for fields to update
                       (e.g. ``summary="new"``, ``status="archived"``).

        Example::

            await mem.update(42, summary="Updated summary", score=8)
        """
        await self._connection.patch(f"/api/v1/records/{record_id}", fields)

    # -- Extended: delete() -------------------------------------------------

    async def delete(self, record_id: int) -> None:
        """
        Delete a record by ID (hard delete).

        For soft delete, use ``update(id, status="archived")`` instead.

        Args:
            record_id: The record ID to delete.
        """
        await self._connection.delete(f"/api/v1/records/{record_id}")

    # -- Extended: new_session() --------------------------------------------

    def new_session(self) -> str:
        """
        Start a new session (generates a fresh UUID).

        All subsequent ``add()`` calls will be grouped under this session.

        Returns:
            The new session UUID.
        """
        self._session_uuid = f"{self._container_tag}-{_utc_timestamp()}"
        return self._session_uuid

    # -- Extended: smart_search() -------------------------------------------

    async def smart_search(
        self,
        query: str,
        *,
        limit: int = 10,
        memory_type: Optional[str] = None,
    ) -> Any:
        """
        Full-text search with cognitive weight boosting.

        Uses the DuckDB-backed smart search engine that ranks results
        by a combination of text relevance and cognitive importance.

        Args:
            query:       Search query.
            limit:       Maximum results (default 10).
            memory_type: Optional type filter.

        Returns:
            Search results ranked by cognitive relevance.
        """
        params: Dict[str, str] = {"q": query, "limit": str(limit)}
        if memory_type:
            params["type"] = memory_type
        return await self._connection.get("/api/v1/search/smart", params=params)

    # -- Extended: walk_semantic() ------------------------------------------

    async def walk_semantic(self, start_id: int, depth: int = 3) -> Dict[str, Any]:
        """
        Semantic graph walk — follows edges weighted by vector similarity.

        Unlike regular ``walk()``, this prioritises semantically related
        records rather than just following structural edges.

        Args:
            start_id: Record ID to start from.
            depth:    Maximum hops (default 3).

        Returns:
            Dict with ``nodes`` and ``edges``.
        """
        return await self._connection.post(
            "/api/v1/walk/semantic",
            {"seed_id": start_id, "depth": depth},
        )

    # -- Extended: batch_read_content() -------------------------------------

    async def batch_read_content(self, ids: List[int]) -> Dict[str, Any]:
        """
        Fetch full content for multiple records in a single call (max 100).

        Eliminates the N+1 pattern of calling ``read_content`` in a loop.

        Args:
            ids: List of record IDs (max 100).

        Returns:
            Dict mapping ``record_id → content_payload``.
        """
        return await self._connection.post(
            "/api/v1/records/batch-content", {"ids": ids}
        )

    # -- Extended: batch_update_status() ------------------------------------

    async def batch_update_status(
        self, ids: List[int], status: str
    ) -> Dict[str, Any]:
        """
        Update status for multiple records at once.

        Args:
            ids:    List of record IDs.
            status: Target status (e.g. ``"consolidated"``, ``"archived"``).

        Returns:
            Confirmation dict with count of updated records.
        """
        return await self._connection.patch(
            "/api/v1/records/mark-consolidated", {"ids": ids, "status": status}
        )

    # -- Extended: supersede() ----------------------------------------------

    async def supersede(self, old_id: int, new_id: int) -> Dict[str, Any]:
        """
        Mark an old record as superseded by a new one.

        The old record remains in the graph but search results prefer the
        newer version. Implements temporal versioning.

        Args:
            old_id: The record being superseded.
            new_id: The replacement record.

        Returns:
            Confirmation dict.
        """
        return await self._connection.post(
            "/api/v1/records/supersede", {"old_id": old_id, "new_id": new_id}
        )

    # -- Extended: upload_file() --------------------------------------------

    async def upload_file(
        self,
        filename: str,
        content: str,
        session_id: Optional[str] = None,
    ) -> Dict[str, Any]:
        """
        Upload a document for async ingestion.

        Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown,
        HTML, DOCX.

        Args:
            filename:   Original filename (format detection).
            content:    Base64-encoded file content.
            session_id: Optional session UUID to associate with.

        Returns:
            Dict with ``id`` for status polling.
        """
        payload: Dict[str, Any] = {"filename": filename, "content": content}
        if session_id:
            payload["session_id"] = session_id
        return await self._connection.post("/api/v1/upload", payload)

    # -- Extended: upload_status() ------------------------------------------

    async def upload_status(self, upload_id: int) -> Dict[str, Any]:
        """
        Check the processing status of a file upload.

        Args:
            upload_id: The upload ID returned by ``upload_file()``.

        Returns:
            Dict with ``status`` (``"processing"``, ``"completed"``,
            ``"failed"``).
        """
        return await self._connection.get(f"/api/v1/upload/{upload_id}/status")

    # -- Extended: get_session_history() ------------------------------------

    async def get_session_history(
        self,
        session_uuid: str,
        limit: int = 50,
        offset: int = 0,
    ) -> Dict[str, Any]:
        """
        Get paginated full-text history for a session.

        Returns actual message content, unlike ``list_sessions`` which
        returns metadata only.

        Args:
            session_uuid: The session UUID.
            limit:        Max records per page (default 50).
            offset:       Pagination offset.

        Returns:
            Dict with ``records``, ``total_records``, ``returned_count``.
        """
        return await self._connection.get(
            f"/api/v1/sessions/{session_uuid}/history",
            params={"limit": str(limit), "offset": str(offset)},
        )

    # -- Extended: get_session_clusters() -----------------------------------

    async def get_session_clusters(self, session_uuid: str) -> Dict[str, Any]:
        """
        Get thematic clusters within a session.

        Uses BSQ vectors and DBSCAN to identify topic groups.

        Args:
            session_uuid: The session UUID.

        Returns:
            Dict with cluster assignments.
        """
        return await self._connection.get(
            f"/api/v1/sessions/{session_uuid}/clusters"
        )

    # -- Entity: search_entities() ------------------------------------------

    async def search_entities(
        self,
        query: Optional[str] = None,
        entity_type: Optional[str] = None,
        limit: int = 20,
    ) -> List[Dict[str, Any]]:
        """
        Search named entities (people, organisations, concepts).

        Args:
            query:       Name or keyword search.
            entity_type: Filter by entity type (e.g. ``"person"``).
            limit:       Maximum results (default 20).

        Returns:
            List of entity dicts.
        """
        params: Dict[str, str] = {"limit": str(limit)}
        if query:
            params["query"] = query
        if entity_type:
            params["type"] = entity_type
        data = await self._connection.get("/api/v1/entities", params=params)
        return data.get("entities", data) if isinstance(data, dict) else data

    # -- Entity: upsert_entity() --------------------------------------------

    async def upsert_entity(
        self,
        name: str,
        entity_type: str = "",
        summary: str = "",
        attributes: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """
        Create or update a named entity (idempotent by name).

        Args:
            name:        Entity name (required).
            entity_type: Entity type (e.g. ``"person"``, ``"org"``).
            summary:     Short description.
            attributes:  Arbitrary key-value metadata.

        Returns:
            Dict with entity ``id``.
        """
        payload: Dict[str, Any] = {"name": name}
        if entity_type:
            payload["entity_type"] = entity_type
        if summary:
            payload["summary"] = summary
        if attributes:
            payload["attributes"] = attributes
        return await self._connection.post("/api/v1/entities", payload)

    # -- Entity: entity_graph() ---------------------------------------------

    async def entity_graph(
        self, entity_id: int, depth: int = 2
    ) -> Dict[str, Any]:
        """
        BFS traversal of entity relationships.

        Args:
            entity_id: The starting entity ID.
            depth:     How many hops (default 2, max 5).

        Returns:
            Dict with ``entity``, ``nodes``, ``node_count``.
        """
        return await self._connection.get(
            f"/api/v1/entities/{entity_id}/graph",
            params={"depth": str(depth)},
        )

    # -- Entity: entity_timeline() ------------------------------------------

    async def entity_timeline(self, entity_id: int) -> Dict[str, Any]:
        """
        Full temporal history of an entity's relationships.

        Shows ALL edges including invalidated ones, ordered by event time.

        Args:
            entity_id: The entity ID.

        Returns:
            Dict with ``entity``, ``timeline``, ``record_ids``.
        """
        return await self._connection.get(
            f"/api/v1/entities/{entity_id}/timeline"
        )

    # -- Entity: upsert_entity_edge() ---------------------------------------

    async def upsert_entity_edge(
        self,
        source_id: int,
        target_id: int,
        relation: str,
        event_time: Optional[str] = None,
        confidence: Optional[float] = None,
        source_record_id: Optional[int] = None,
    ) -> Dict[str, Any]:
        """
        Create or update a typed relationship between two entities.

        Args:
            source_id:        Source entity ID.
            target_id:        Target entity ID.
            relation:         Relationship type (e.g. ``"works_at"``).
            event_time:       ISO 8601 timestamp.
            confidence:       Confidence score (0.0-1.0).
            source_record_id: Record that evidences this relationship.

        Returns:
            Confirmation dict.
        """
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

    # -- Entity: link_record_entity() ---------------------------------------

    async def link_record_entity(
        self, record_id: int, entity_id: int, role: str = ""
    ) -> Dict[str, Any]:
        """
        Link a memory record to an entity (cross-layer connection).

        Args:
            record_id: Memory record ID.
            entity_id: Entity ID.
            role:      Optional role description.

        Returns:
            Confirmation dict.
        """
        payload: Dict[str, Any] = {
            "record_id": record_id,
            "entity_id": entity_id,
        }
        if role:
            payload["role"] = role
        return await self._connection.post("/api/v1/entities/link", payload)

    # -- Entity: get_record_entities() --------------------------------------

    async def get_record_entities(self, record_id: int) -> List[Dict[str, Any]]:
        """
        Get entities linked to a specific memory record.

        Args:
            record_id: The record ID.

        Returns:
            List of entity dicts.
        """
        data = await self._connection.get(
            f"/api/v1/records/{record_id}/entities"
        )
        return data.get("entities", data) if isinstance(data, dict) else data

    # -- Stub: forget() -----------------------------------------------------

    async def forget(self, memory_id: Optional[int] = None) -> None:
        """
        Forget a specific memory or trigger cognitive decay.

        Not yet implemented — placeholder for the decay API.

        Args:
            memory_id: If provided, forget this specific memory.

        Raises:
            NotImplementedError: Always (until the API is available).
        """
        raise NotImplementedError(
            "forget() is not yet available. "
            "Use delete() for hard removal or update(id, status='archived') "
            "for soft delete."
        )

    # -- Internal helpers ---------------------------------------------------

    async def _try_ingest(
        self,
        text: str,
        score: int,
        mem_type: MemoryType,
    ) -> Optional[Dict[str, Any]]:
        """
        Attempt cloud ingest at ``/api/v1/ingest``.

        Returns None if the endpoint doesn't exist (404), allowing the
        caller to fall back to direct record creation.
        """
        payload = {"content": text, "container_tag": self._container_tag}

        try:
            data = await self._connection.post("/api/v1/ingest", payload)
            self._ingest_available = True

            records = data.get("records", [{"id": data.get("id", 0),
                                             "type": "episodic",
                                             "summary": text[:200]}])
            return {
                "session_id": self._session_uuid,
                "records": records,
                "mode": "cloud",
            }
        except AnhurQueryError as exc:
            if "404" in str(exc):
                self._ingest_available = False
                return None
            raise

    async def _create_record(
        self,
        text: str,
        score: int,
        mem_type: MemoryType,
    ) -> Dict[str, Any]:
        """
        Create a record directly via ``POST /api/v1/records`` (OSS mode).

        Without server-side embedding, text is stored in both ``summary``
        (for FTS5 search) and ``content`` (for full retrieval).
        """
        summary = text[:200] + "..." if len(text) > 200 else text

        req = CreateRequest(
            uuid=self._session_uuid,
            type=mem_type,
            summary=summary,
            content=text,
            score=score,
            weight=score / 10,
            metadata=self._container_tag,
        )

        data = await self._connection.post(
            "/api/v1/records",
            req.model_dump(exclude_none=True),
        )

        return {
            "session_id": self._session_uuid,
            "records": [{"id": data.get("id", 0), "type": mem_type.value,
                          "summary": summary}],
            "mode": "oss",
        }

    @staticmethod
    def _flatten_search_results(data: Any) -> List[Dict[str, Any]]:
        """Flatten nested search response into simple dicts."""
        results = []
        for item in (data.get("results", []) if isinstance(data, dict) else []):
            rec = item.get("record", {}) if isinstance(item, dict) else {}
            results.append({
                "id": rec.get("id", 0),
                "type": rec.get("type", ""),
                "summary": rec.get("summary", ""),
                "score": item.get("similarity", 0),
                "metadata": rec.get("metadata"),
                "content": rec.get("content"),
            })
        return results

    def __repr__(self) -> str:
        return (
            f"Memory(container_tag={self._container_tag!r}, "
            f"session={self._session_uuid!r})"
        )


# ---------------------------------------------------------------------------
# AnhurClient — full-featured client
# ---------------------------------------------------------------------------

class AnhurClient:
    """
    Full-featured async client for AnhurDB V2.

    Exposes the complete AnhurDB REST API surface including:
      - Memory CRUD (create, read, update, delete)
      - Batch operations (batch_read_content, batch_update_status)
      - Search (global, by type, smart, AST query)
      - Graph traversal (walk, semantic walk)
      - Entity knowledge graph (search, upsert, edges, timeline)
      - File upload and ingestion status
      - Temporal versioning (supersede)
      - Session management and manifests

    Unlike ``Memory``, this client does NOT auto-manage sessions or
    container tags — the caller is responsible for providing UUIDs.

    Args:
        url:       Server URL (default: ``http://localhost:8080``).
        api_key:   API key (required). Falls back to ``ANHUR_API_KEY`` env.
        tenant_id: Optional tenant ID for multi-tenant deployments.
        mode:      Transport — ``"rest"`` (default) or ``"mcp"``.

    Example::

        async with AnhurClient(api_key="anhur_xxx") as client:
            await client.create(CreateRequest(uuid="s1", content="hello"))
            results = await client.search("hello")
            await client.upload_file("doc.pdf", pdf_bytes, session_id="s1")
    """

    def __init__(
        self,
        url: str = "http://localhost:8080",
        api_key: Optional[str] = None,
        tenant_id: str = "",
        mode: str = "rest",
    ):
        key = api_key or os.environ.get("ANHUR_API_KEY", "")
        if not key:
            raise ValueError(
                "api_key is required. Pass it directly or set ANHUR_API_KEY."
            )
        self._connection = HTTPConnection(
            base_url=url,
            api_key=key,
            tenant_id=tenant_id,
            mode=mode,
        )

    async def __aenter__(self) -> "AnhurClient":
        await self._connection.connect()
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self._connection.close()

    # ── Memory CRUD ────────────────────────────────────────────────

    async def create(self, req: CreateRequest) -> Dict[str, Any]:
        """
        Create a new memory record.

        Args:
            req: ``CreateRequest`` with at minimum ``uuid`` and ``content``.

        Returns:
            Server response dict (includes ``id``, ``uuid``, ``status``).
        """
        return await self._connection.post(
            "/api/v1/records",
            req.model_dump(exclude_none=True),
        )

    async def get(self, record_id: int) -> Dict[str, Any]:
        """
        Get a record's metadata by ID.

        Args:
            record_id: The record ID.

        Returns:
            Record metadata dict.
        """
        return await self._connection.get(f"/api/v1/records/{record_id}")

    async def read_content(self, record_id: int) -> str:
        """
        Read the full content payload for a record.

        Args:
            record_id: The record ID.

        Returns:
            The decompressed content string.
        """
        data = await self._connection.get(
            f"/api/v1/records/{record_id}/content"
        )
        if isinstance(data, dict):
            return data.get("content", str(data))
        return str(data)

    async def get_context(self, record_id: int) -> Dict[str, Any]:
        """
        Get the topological context (1-hop neighbours) around a record.

        Args:
            record_id: The record ID to inspect.

        Returns:
            Dict with ``target`` and ``neighbors``.
        """
        return await self._connection.get(
            f"/api/v1/records/{record_id}/topology"
        )

    async def update(self, record_id: int, **fields: Any) -> None:
        """
        Partially update a record.

        Args:
            record_id: The record ID.
            **fields:  Fields to update (summary, status, score, etc.).
        """
        await self._connection.patch(f"/api/v1/records/{record_id}", fields)

    async def delete(self, record_id: int) -> None:
        """
        Hard delete a record.

        Args:
            record_id: The record ID.
        """
        await self._connection.delete(f"/api/v1/records/{record_id}")

    # ── Search ─────────────────────────────────────────────────────

    async def search(
        self,
        query: str,
        *,
        limit: int = 10,
        type_filter: Optional[str] = None,
    ) -> List[Dict[str, Any]]:
        """
        Global hybrid search (vector + FTS5) across all sessions.

        Args:
            query:       Natural language query.
            limit:       Maximum results (default 10).
            type_filter: Optional memory type filter.

        Returns:
            List of search result dicts.
        """
        payload: Dict[str, Any] = {"text": query, "limit": limit}
        if type_filter:
            payload["type_filter"] = type_filter
        data = await self._connection.post("/api/v1/search/global", payload)
        return data.get("results", []) if isinstance(data, dict) else []

    async def search_by_type(
        self,
        memory_type: str,
        limit: int = 20,
        query: Optional[str] = None,
    ) -> List[Dict[str, Any]]:
        """
        Search filtered by cognitive type with optional keyword query.

        Args:
            memory_type: Type to filter (e.g. ``"fact"``).
            limit:       Maximum results (default 20).
            query:       Optional keyword search within the type.

        Returns:
            List of search result dicts.
        """
        params: Dict[str, str] = {"type": memory_type, "limit": str(limit)}
        if query:
            params["query"] = query
        data = await self._connection.get("/api/v1/search/type", params=params)
        return data.get("results", []) if isinstance(data, dict) else []

    async def smart_search(
        self,
        query: str,
        *,
        limit: int = 10,
        memory_type: Optional[str] = None,
    ) -> List[Dict[str, Any]]:
        """
        Full-text search with cognitive weight boosting.

        Uses the DuckDB-backed smart search engine that ranks results
        by a combination of text relevance and cognitive importance (score).

        Args:
            query:       Search query.
            limit:       Maximum results (default 10).
            memory_type: Optional type filter.

        Returns:
            List of search result dicts.
        """
        params: Dict[str, str] = {"q": query, "limit": str(limit)}
        if memory_type:
            params["type"] = memory_type
        return await self._connection.get("/api/v1/search/smart", params=params)

    async def recall(
        self,
        query: str,
        limit: int = 10,
    ) -> List[Dict[str, Any]]:
        """
        Cognitive fan-out search combining multiple search strategies.

        The server-side ``recall`` performs smart_search + fact search +
        consolidated search in parallel and merges results.

        Args:
            query: Natural language query.
            limit: Maximum results (default 10).

        Returns:
            List of search result dicts ranked by cognitive relevance.
        """
        return await self.search(query, limit=limit)

    async def search_with_ast(
        self,
        filter_builder: Any,
        session_uuid: Optional[str] = None,
    ) -> List[Record]:
        """
        Execute an AST query built by ``QueryBuilder`` or ``Filter``.

        The filter_builder should expose an ``.ast()`` method returning
        the compiled JSON AST dict.

        If ``session_uuid`` is provided, it is injected as an additional
        ``uuid`` filter so results are scoped to that session. The server
        expects the AST at the top level of the request body (not nested
        under a ``query`` key).

        Args:
            filter_builder: A ``QueryBuilder`` or ``Filter`` instance.
            session_uuid:   Optional session UUID to scope results.

        Returns:
            List of ``Record`` objects matching the query.

        Example::

            from anhurdb.query import QueryBuilder
            qb = QueryBuilder().where(type="risk", score__gte=7).limit(20)
            records = await client.search_with_ast(qb, session_uuid="s1")
        """
        # Support both QueryBuilder (.build_ast()) and Filter (.ast()).
        if hasattr(filter_builder, "build_ast"):
            ast = filter_builder.build_ast()
        elif hasattr(filter_builder, "ast"):
            ast = filter_builder.ast()
        else:
            raise TypeError(
                "filter_builder must have a build_ast() or ast() method. "
                "Use QueryBuilder or Filter."
            )

        # Inject session_uuid as a uuid filter if provided.
        # The server does NOT accept session_uuid as a separate field —
        # it must be a regular filter in the AST's filters dict.
        if session_uuid:
            ast.setdefault("filters", {})["uuid"] = {"$eq": session_uuid}

        # Server expects the AST flat at top-level (filters, pagination,
        # sort, select). Do NOT wrap in {"query": ast}.
        data = await self._connection.post("/api/v1/query", ast)
        records_data = data.get("records", []) if isinstance(data, dict) else []
        return [Record(**r) for r in records_data]

    # ── Batch Operations ───────────────────────────────────────────

    async def batch_read_content(self, ids: List[int]) -> Dict[int, Any]:
        """
        Fetch full content for multiple records in a single call (max 100).

        Eliminates the N+1 pattern of calling ``read_content`` in a loop.

        Args:
            ids: List of record IDs (max 100).

        Returns:
            Dict mapping ``record_id → content_payload``.
        """
        data = await self._connection.post(
            "/api/v1/records/batch-content",
            {"ids": ids},
        )
        return data if isinstance(data, dict) else {}

    async def batch_update_status(
        self,
        ids: List[int],
        status: str,
    ) -> Dict[str, Any]:
        """
        Update status for multiple records at once.

        Useful for bulk operations like marking records as consolidated,
        archived, or hubbed.

        Args:
            ids:    List of record IDs.
            status: Target status (e.g. ``"consolidated"``, ``"archived"``).

        Returns:
            Confirmation dict with count of updated records.
        """
        return await self._connection.patch(
            "/api/v1/records/mark-consolidated",
            {"ids": ids, "status": status},
        )

    # ── Graph Traversal ────────────────────────────────────────────

    async def walk(self, start_id: int, depth: int = 3) -> Dict[str, Any]:
        """
        BFS graph traversal from a seed record.

        Args:
            start_id: Record ID to start from.
            depth:    Maximum hops (default 3).

        Returns:
            Dict with ``nodes`` and ``edges``.
        """
        return await self._connection.post(
            "/api/v1/walk",
            {"seed_id": start_id, "depth": depth, "direction": "both"},
        )

    async def walk_semantic(self, start_id: int, depth: int = 3) -> Dict[str, Any]:
        """
        Semantic graph walk — follows edges weighted by vector similarity.

        Unlike regular ``walk()``, this prioritises semantically related
        records rather than just following structural edges.

        Args:
            start_id: Record ID to start from.
            depth:    Maximum hops (default 3).

        Returns:
            Dict with ``nodes`` and ``edges``.
        """
        return await self._connection.post(
            "/api/v1/walk/semantic",
            {"seed_id": start_id, "depth": depth},
        )

    # ── Session Management ─────────────────────────────────────────

    async def list_sessions(self) -> List[Dict[str, Any]]:
        """
        List all sessions with aggregate statistics.

        Returns:
            List of dicts with ``uuid``, ``record_count``, ``types``,
            ``last_activity``.
        """
        data = await self._connection.get("/api/v1/sessions/stats")
        return data.get("sessions", data) if isinstance(data, dict) else data

    async def list_chat(self, session_uuid: str) -> List[Dict[str, Any]]:
        """
        List all records in a specific session.

        Args:
            session_uuid: The session UUID.

        Returns:
            List of record dicts.
        """
        data = await self._connection.get(f"/api/v1/chats/{session_uuid}")
        return data.get("records", data) if isinstance(data, dict) else data

    async def get_session_history(
        self,
        session_uuid: str,
        limit: int = 50,
        offset: int = 0,
    ) -> Dict[str, Any]:
        """
        Get paginated full-text history for a session.

        Returns actual message content from the filesystem, unlike
        ``list_chat`` which returns metadata only.

        Args:
            session_uuid: The session UUID.
            limit:        Max records per page (default 50).
            offset:       Pagination offset.

        Returns:
            Dict with ``records``, ``total_records``, ``returned_count``.
        """
        return await self._connection.get(
            f"/api/v1/sessions/{session_uuid}/history",
            params={"limit": str(limit), "offset": str(offset)},
        )

    async def get_session_clusters(
        self,
        session_uuid: str,
    ) -> Dict[str, Any]:
        """
        Get mathematically clustered topological groups for a session.

        Uses BSQ vectors and DBSCAN to identify thematic clusters
        within the session's records.

        Args:
            session_uuid: The session UUID.

        Returns:
            Dict with cluster assignments.
        """
        return await self._connection.get(
            f"/api/v1/sessions/{session_uuid}/clusters"
        )

    async def manifest_global(
        self,
        limit: int = 50,
        offset: int = 0,
        query: Optional[str] = None,
    ) -> Dict[str, Any]:
        """
        Cross-session overview of all knowledge, ranked by importance.

        Best tool for RAG context injection — returns the most important
        records across all sessions.

        Args:
            limit:  Max records (default 50).
            offset: Pagination offset.
            query:  Optional keyword filter.

        Returns:
            Dict with ``count``, ``has_more``, ``records``.
        """
        params: Dict[str, str] = {"limit": str(limit), "offset": str(offset)}
        if query:
            params["query"] = query
        return await self._connection.get("/api/v1/manifest", params=params)

    async def manifest_session(
        self,
        session_uuid: str,
        query: Optional[str] = None,
    ) -> Dict[str, Any]:
        """
        Get the manifest for a single session (records with metadata).

        Args:
            session_uuid: The session UUID.
            query:        Optional keyword filter.

        Returns:
            Dict with session records.
        """
        params: Dict[str, str] = {}
        if query:
            params["query"] = query
        return await self._connection.get(
            f"/api/v1/chats/{session_uuid}/manifest",
            params=params or None,
        )

    async def recent(self, limit: int = 20) -> List[Dict[str, Any]]:
        """
        Get recently updated records.

        Args:
            limit: Maximum records (default 20).

        Returns:
            List of record dicts.
        """
        data = await self._connection.get(
            "/api/v1/recent",
            params={"limit": str(limit)},
        )
        return data if isinstance(data, list) else data.get("records", [])

    # ── File Upload ────────────────────────────────────────────────

    async def upload_file(
        self,
        filename: str,
        content: str,
        session_id: Optional[str] = None,
    ) -> Dict[str, Any]:
        """
        Upload a document for async ingestion.

        Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown,
        HTML, DOCX.

        The server processes the file asynchronously — use
        ``upload_status()`` to poll for completion.

        Args:
            filename:   Original filename (used for format detection).
            content:    Base64-encoded file content.
            session_id: Optional session UUID to associate with.

        Returns:
            Dict with ``id`` for status polling.
        """
        payload: Dict[str, Any] = {"filename": filename, "content": content}
        if session_id:
            payload["session_id"] = session_id
        return await self._connection.post("/api/v1/upload", payload)

    async def upload_status(self, upload_id: int) -> Dict[str, Any]:
        """
        Check the processing status of a file upload.

        Args:
            upload_id: The upload ID returned by ``upload_file()``.

        Returns:
            Dict with ``status`` (``"processing"``, ``"completed"``,
            ``"failed"``).
        """
        return await self._connection.get(f"/api/v1/upload/{upload_id}/status")

    # ── Temporal Versioning ────────────────────────────────────────

    async def supersede(self, old_id: int, new_id: int) -> Dict[str, Any]:
        """
        Mark an old record as superseded by a new one.

        This implements temporal versioning — the old record remains
        in the graph but is annotated with ``superseded_by`` pointing
        to the new record. Search results prefer the newer version.

        Args:
            old_id: The record being superseded.
            new_id: The replacement record.

        Returns:
            Confirmation dict.
        """
        return await self._connection.post(
            "/api/v1/records/supersede",
            {"old_id": old_id, "new_id": new_id},
        )

    # ── Entity Knowledge Graph (Layer 2) ───────────────────────────

    async def search_entities(
        self,
        query: Optional[str] = None,
        entity_type: Optional[str] = None,
        limit: int = 20,
    ) -> List[Dict[str, Any]]:
        """
        Search named entities (people, organisations, concepts).

        Args:
            query:       Name or keyword search.
            entity_type: Filter by entity type (e.g. ``"person"``).
            limit:       Maximum results (default 20).

        Returns:
            List of entity dicts.
        """
        params: Dict[str, str] = {"limit": str(limit)}
        if query:
            params["query"] = query
        if entity_type:
            params["type"] = entity_type
        data = await self._connection.get("/api/v1/entities", params=params)
        return data.get("entities", data) if isinstance(data, dict) else data

    async def upsert_entity(
        self,
        name: str,
        entity_type: str = "",
        summary: str = "",
        attributes: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """
        Create or update a named entity (idempotent by name).

        Args:
            name:        Entity name (required).
            entity_type: Entity type (e.g. ``"person"``, ``"org"``).
            summary:     Short description.
            attributes:  Arbitrary key-value metadata.

        Returns:
            Dict with entity ``id``.
        """
        payload: Dict[str, Any] = {"name": name}
        if entity_type:
            payload["entity_type"] = entity_type
        if summary:
            payload["summary"] = summary
        if attributes:
            payload["attributes"] = attributes
        return await self._connection.post("/api/v1/entities", payload)

    async def get_entity_graph(
        self,
        entity_id: int,
        depth: int = 2,
    ) -> Dict[str, Any]:
        """
        BFS traversal of entity relationships.

        Starting from an entity, discovers connected entities through
        typed edges (``works_at``, ``knows``, ``part_of``, etc.).

        Args:
            entity_id: The starting entity ID.
            depth:     How many hops to follow (default 2, max 5).

        Returns:
            Dict with ``entity``, ``nodes``, ``node_count``.
        """
        params: Dict[str, str] = {"depth": str(depth)}
        return await self._connection.get(
            f"/api/v1/entities/{entity_id}/graph",
            params=params,
        )

    async def entity_timeline(self, entity_id: int) -> Dict[str, Any]:
        """
        Get the full temporal history of an entity's relationships.

        Shows ALL edges including invalidated ones, ordered by event time.
        Use to understand how an entity's context evolved over time.

        Args:
            entity_id: The entity ID.

        Returns:
            Dict with ``entity``, ``timeline``, ``record_ids``.
        """
        return await self._connection.get(
            f"/api/v1/entities/{entity_id}/timeline"
        )

    async def upsert_entity_edge(
        self,
        source_id: int,
        target_id: int,
        relation: str,
        event_time: Optional[str] = None,
        confidence: Optional[float] = None,
        source_record_id: Optional[int] = None,
    ) -> Dict[str, Any]:
        """
        Create or update a typed relationship between two entities.

        Args:
            source_id:        Source entity ID.
            target_id:        Target entity ID.
            relation:         Relationship type (e.g. ``"works_at"``).
            event_time:       ISO 8601 timestamp when this became true.
            confidence:       Confidence score (0.0-1.0).
            source_record_id: Memory record that evidences this relationship.

        Returns:
            Confirmation dict.
        """
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
        """
        Link a memory record to an entity (cross-layer connection).

        Args:
            record_id: Memory record ID.
            entity_id: Entity ID.
            role:      Optional role description.

        Returns:
            Confirmation dict.
        """
        payload: Dict[str, Any] = {
            "record_id": record_id,
            "entity_id": entity_id,
        }
        if role:
            payload["role"] = role
        return await self._connection.post("/api/v1/entities/link", payload)

    async def get_record_entities(self, record_id: int) -> List[Dict[str, Any]]:
        """
        Get entities linked to a specific memory record.

        Args:
            record_id: The record ID.

        Returns:
            List of entity dicts.
        """
        data = await self._connection.get(
            f"/api/v1/records/{record_id}/entities"
        )
        return data.get("entities", data) if isinstance(data, dict) else data

    # ── Utility ────────────────────────────────────────────────────

    async def count_by_type(self) -> Dict[str, int]:
        """
        Get aggregated record counts per type.

        Returns:
            Dict mapping type name → count
            (e.g. ``{"episodic": 120, "fact": 45}``).
        """
        # Uses the manifest endpoint with a type aggregation.
        data = await self._connection.get("/api/v1/manifest", params={"limit": "0"})
        # The server may return counts differently; return raw.
        return data if isinstance(data, dict) else {}

    async def profile(self, container_tag: str) -> Dict[str, Any]:
        """
        Get the memory profile for a specific container tag.

        Args:
            container_tag: The user/agent identifier.

        Returns:
            Dict with ``static``, ``dynamic``, ``stats``.
        """
        return await self._connection.get(
            "/api/v1/profile",
            params={"tag": container_tag},
        )
