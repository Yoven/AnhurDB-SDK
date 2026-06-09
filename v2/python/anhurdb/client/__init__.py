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
import json
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


def _build_metadata_json(
    container_tag: str,
    extra_metadata: Optional[Dict[str, Any]] = None,
) -> str:
    """
    Wrap ``container_tag`` into the canonical metadata JSON envelope
    ``{"container_tag": "<tag>"}``.

    Junior Tip [metadata corruption parity, 2026-05-22]: every record-create
    path historically wrote ``metadata`` as the bare container_tag string
    (``"mem-3f9..."``) instead of a JSON object. On the server this poisoned
    every downstream agent that runs ``json.loads(metadata)`` — entity taggers
    logged ``tagged_no_entities`` because the parse failed at the first step,
    and a one-shot repair had to fix 516 corrupted records. The Go SDK was
    fixed first (buildMetadataJSON); this is the Python parity. The TypeScript
    SDK carries the same fix. ALL THREE SDKs MUST stay byte-identical here —
    see the SDK-sync rule in project memory.

    Returns ``"{}"`` when container_tag is empty so the column always holds a
    parseable JSON object.

    Junior Tip [caller metadata merge, 2026-06-08]: ``extra_metadata`` lets
    ``add()`` carry user-supplied metadata WITHOUT clobbering the
    ``container_tag`` envelope key the agents rely on. The container_tag is
    written last so it always wins over a colliding caller key — losing the
    tag would re-break the entity taggers (the 2026-05-22 corruption mode).
    """
    envelope: Dict[str, Any] = {}
    if extra_metadata:
        envelope.update(extra_metadata)
    if container_tag:
        envelope["container_tag"] = container_tag
    if not envelope:
        return "{}"
    return json.dumps(envelope)


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

    Junior Tip [refactor 2026-04-25]: This class is now a THIN WRAPPER over
    ``AnhurClient``. The 27 generic API calls (search, walk, upload, entity_*,
    etc.) delegate directly to the same method on the underlying client, so
    we maintain ONE implementation of each call. Memory adds value only in:
      1. Auto-deriving the container_tag from api_key (SHA-256).
      2. Auto-creating session_uuid (container_tag + UTC timestamp).
      3. The cloud→OSS fallback for ``add()`` (try /api/v1/ingest, fall
         back to direct /api/v1/records on 404).
      4. Defaulting session-scoped methods (profile, get_session_history,
         get_session_clusters) to the current session.

    Core methods:
        - ``add(text)``    — store a memory
        - ``search(query)`` — find relevant memories
        - ``profile()``    — get user/agent profile

    Extended methods (delegate to AnhurClient):
        - ``search_by_type``, ``recall``, ``walk``, ``list_sessions``,
          ``get_context``, ``read_content``, ``recent``, ``update``,
          ``delete``, ``new_session``, plus all entity_*/upload_*/batch_*
          methods.

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

        # Compose an AnhurClient — Memory delegates 27 API methods to it.
        # We share its HTTPConnection via the _connection alias below so the
        # private helpers (_try_ingest, _create_record) can keep using
        # `self._connection` directly.
        self._client = AnhurClient(
            url=url,
            api_key=key,
            tenant_id=tenant_id,
            mode=mode,
        )
        # Alias — same underlying HTTPConnection, single TCP/HTTP pool.
        self._connection = self._client._connection

        # Container tag: explicit user_id or SHA-256 derived from API key.
        if user_id:
            self._container_tag = user_id
        else:
            self._container_tag = _derive_container_tag(key)

        # Session UUID: container_tag + UTC timestamp.
        self._session_uuid = f"{self._container_tag}-{_utc_timestamp()}"

        # Cloud ingest availability (None = untested).
        self._ingest_available: Optional[bool] = None

        # ID of the episodic anchor created for the current session, if any.
        # Junior Tip [session anchor invariant, 2026-06-08]: the server rejects
        # a derived record (fact/preference/task/…) when the session has no
        # episodic anchor yet ("cannot create preference without an episodic
        # anchor"). The ingest path creates that anchor implicitly; the direct
        # records path does not, so when add() routes a typed record straight
        # to /api/v1/records we lazily create the anchor once per session and
        # cache its ID here so subsequent typed adds auto-link to it.
        self._session_anchor_id: Optional[int] = None

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
        score: Optional[int] = None,
        type: Optional[MemoryType] = None,
        metadata: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """
        Store a memory. Simplest way to save information.

        Routing (Junior Tip [score/type drop, 2026-06-08]): the cloud
        ``/api/v1/ingest`` endpoint accepts ONLY ``content`` + ``container_tag``
        — it hardcodes ``type=episodic`` and never reads score/type/metadata
        (see server handler ``record_ingest.go``). Sending them there drops
        them silently. So when the caller explicitly sets ``score``, ``type``,
        or ``metadata`` we MUST go straight to ``/api/v1/records``, which is the
        only write path that persists those columns. Plain ``add(text)`` with no
        options still prefers the ingest pipeline (auto-embedding + extraction).

        Args:
            text:     The text to remember (required, non-empty).
            score:    Importance rating 1-10. ``None`` = let the server/pipeline
                      decide (defaults to 5 on the direct path).
            type:     Memory type. ``None`` = ``episodic``.
            metadata: Optional caller metadata merged into the record's
                      metadata JSON alongside the ``container_tag`` envelope.

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

        # When score/type/metadata are explicitly requested, the ingest endpoint
        # cannot honour them — go directly to the records path which persists
        # all three. This is the parity contract shared with the Go/TS SDKs.
        wants_explicit_fields = (
            score is not None or type is not None or metadata is not None
        )

        if not wants_explicit_fields and self._ingest_available is not False:
            result = await self._try_ingest(text, score, type, metadata)
            if result is not None:
                return result

        # Direct record creation — honours score, type, and metadata.
        return await self._create_record(text, score, type, metadata)

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
        return await self._client.search(query, limit=limit, type_filter=type_filter)

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

    async def search_session(
        self,
        query: str,
        *,
        limit: int = 10,
        type_filter: Optional[str] = None,
    ) -> List[Dict[str, Any]]:
        """
        Search within the current session (all types, including recent memories).

        Unlike ``search()``, this returns ALL memory types so you can find
        recent episodic records and in-progress tasks within this session.

        Args:
            query:       Natural language query.
            limit:       Maximum results (default 10).
            type_filter: Optional memory type filter.

        Returns:
            List of search result dicts.

        Example::

            hits = await mem.search_session("what did we discuss about payments?")
        """
        return await self._client.search_session(
            self._session_uuid, query, limit=limit, type_filter=type_filter
        )

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
        return await self._client.search_by_type(memory_type, limit=limit)

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
        return await self._client.recall(query, limit)

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
        return await self._client.walk(start_id, depth)

    async def list_sessions(self) -> List[Dict[str, Any]]:
        """
        List all sessions with aggregate statistics.

        Returns:
            List of dicts with ``uuid``, ``record_count``, ``types``,
            ``last_activity``.
        """
        return await self._client.list_sessions()

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
        return await self._client.get_context(record_id)

    async def read_content(self, record_id: int) -> Any:
        """
        Read the full content payload for a record.

        Returns the complete decompressed content. Type depends on what
        was stored — a dict for structured records, a string for plain text.

        Args:
            record_id: The record ID to read.

        Returns:
            Content payload (dict or string).
        """
        return await self._client.read_content(record_id)

    async def recent(self, limit: int = 20) -> List[Dict[str, Any]]:
        """
        Fetch the most recent records from the global manifest.

        Args:
            limit: Maximum records (default 20).

        Returns:
            List of record dicts ordered by creation time (newest first).
        """
        return await self._client.recent(limit)

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
        await self._client.update(record_id, **fields)

    async def delete(self, record_id: int) -> None:
        """
        Delete a record by ID (hard delete).

        For soft delete, use ``update(id, status="archived")`` instead.

        Args:
            record_id: The record ID to delete.
        """
        await self._client.delete(record_id)

    def new_session(self) -> str:
        """
        Start a new session (generates a fresh UUID).

        All subsequent ``add()`` calls will be grouped under this session.

        Returns:
            The new session UUID.
        """
        self._session_uuid = f"{self._container_tag}-{_utc_timestamp()}"
        # New session → no anchor yet; force re-creation on the next typed add.
        self._session_anchor_id = None
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

        Ranks results by a combination of text relevance and cognitive
        importance (score × weight).

        Args:
            query:       Search query.
            limit:       Maximum results (default 10).
            memory_type: Optional type filter.

        Returns:
            Search results ranked by cognitive relevance.
        """
        return await self._client.smart_search(query, limit=limit, memory_type=memory_type)

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
        return await self._client.walk_semantic(start_id, depth)

    async def batch_read_content(self, ids: List[int]) -> Dict[str, Any]:
        """
        Fetch full content for multiple records in a single call (max 100).

        Eliminates the N+1 pattern of calling ``read_content`` in a loop.

        Args:
            ids: List of record IDs (max 100).

        Returns:
            Dict mapping ``record_id → content_payload``.
        """
        return await self._client.batch_read_content(ids)

    async def mark_consolidated(self, ids: List[int]) -> Dict[str, Any]:
        """
        Mark a batch of records as consolidated.

        Args:
            ids: List of record IDs to mark.

        Returns:
            Confirmation dict with count of updated records.
        """
        return await self._client.mark_consolidated(ids)

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
        return await self._client.supersede(old_id, new_id)

    async def upload_file(
        self,
        filename: str,
        content: bytes,
        session_id: Optional[str] = None,
    ) -> Dict[str, Any]:
        """
        Upload a document for async ingestion.

        Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown,
        HTML, DOCX.

        Args:
            filename:   Original filename (format detection).
            content:    Raw file bytes.
            session_id: Optional session UUID to associate with.

        Returns:
            Dict with ``record_id``, ``uuid``, ``filename``, ``status``.
        """
        return await self._client.upload_file(filename, content, session_id=session_id)

    async def upload_status(self, upload_id: int) -> Dict[str, Any]:
        """
        Check the processing status of a file upload.

        Args:
            upload_id: The upload ID returned by ``upload_file()``.

        Returns:
            Dict with ``status`` (``"processing"``, ``"completed"``,
            ``"failed"``).
        """
        return await self._client.upload_status(upload_id)

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

        Uses vector similarity and clustering to identify thematic groups
        within the session's records.

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
        return await self._client.search_entities(query=query, entity_type=entity_type, limit=limit)

    async def list_entities(self, limit: int = 200, offset: int = 0) -> Dict[str, Any]:
        """
        Paginated walk of ALL entities for the tenant (id ASC, stable cursor).

        Loop until ``has_more`` is false to consume the full set. See the
        low-level :meth:`AnhurClient.list_entities` for the full contract.
        """
        return await self._client.list_entities(limit=limit, offset=offset)

    async def create_in_session(self, text: str, session_uuid: str) -> Dict[str, Any]:
        """
        Store ``text`` as an episodic record under a CALLER-OWNED session uuid.
        See :meth:`AnhurClient.create_in_session`.
        """
        return await self._client.create_in_session(text, session_uuid)

    async def append_main_ids(self, record_id: int, main_ids: List[int]) -> Dict[str, Any]:
        """Append parent IDs to ``record_id``'s main_ids. See :meth:`AnhurClient.append_main_ids`."""
        return await self._client.append_main_ids(record_id, main_ids)

    async def update_consolidate_ids(self, ids: List[int], consolidate_id: int) -> Dict[str, Any]:
        """Set ``consolidate_id`` on a batch of children. See :meth:`AnhurClient.update_consolidate_ids`."""
        return await self._client.update_consolidate_ids(ids, consolidate_id)

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
        return await self._client.upsert_entity(name, entity_type=entity_type, summary=summary, attributes=attributes)

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
        return await self._client.get_entity_graph(entity_id, depth=depth)

    async def entity_timeline(self, entity_id: int) -> Dict[str, Any]:
        """
        Full temporal history of an entity's relationships.

        Shows ALL edges including invalidated ones, ordered by event time.

        Args:
            entity_id: The entity ID.

        Returns:
            Dict with ``entity``, ``timeline``, ``record_ids``.
        """
        return await self._client.entity_timeline(entity_id)

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
        return await self._client.upsert_entity_edge(source_id, target_id, relation, event_time=event_time, confidence=confidence, source_record_id=source_record_id)

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
        return await self._client.link_record_entity(record_id, entity_id, role=role)

    async def get_record_entities(self, record_id: int) -> List[Dict[str, Any]]:
        """
        Get entities linked to a specific memory record.

        Args:
            record_id: The record ID.

        Returns:
            List of entity dicts.
        """
        return await self._client.get_record_entities(record_id)

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
        score: Optional[int],
        mem_type: Optional[MemoryType],
        metadata: Optional[Dict[str, Any]],
    ) -> Optional[Dict[str, Any]]:
        """
        Attempt cloud ingest at ``/api/v1/ingest``.

        Returns None if the endpoint doesn't exist (404), allowing the
        caller to fall back to direct record creation.

        Junior Tip [ingest field set, 2026-06-08]: the server ingest handler
        only reads ``content`` + ``container_tag`` and hardcodes the episodic
        type. ``add()`` therefore never routes here when score/type/metadata
        are set — they would be dropped. The extra parameters are accepted only
        to keep a uniform internal signature.
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
        score: Optional[int],
        mem_type: Optional[MemoryType],
        metadata: Optional[Dict[str, Any]],
    ) -> Dict[str, Any]:
        """
        Create a record directly via ``POST /api/v1/records``.

        This is the only write path that persists ``score`` and ``type``
        (the ingest endpoint drops them — see ``add()``). Without server-side
        embedding, text is stored in both ``summary`` (for keyword search) and
        ``content`` (for full retrieval).

        Junior Tip [score/type defaults, 2026-06-08]: ``None`` score/type mean
        "caller didn't care" — we apply the historical defaults (5 / episodic)
        so the record is always well-formed, while still letting an explicit
        ``score=8`` or ``type=preference`` flow through to the DB columns.
        """
        summary = text[:200] + "..." if len(text) > 200 else text

        effective_score = 5 if score is None else score
        effective_type = MemoryType.EPISODIC if mem_type is None else mem_type

        # Ensure the session has an episodic anchor before writing a derived
        # type. The server rejects orphan derived records (see the
        # ``_session_anchor_id`` Junior Tip on __init__). Episodic records are
        # themselves the anchor, so they skip this step.
        related_ids: List[int] = []
        if effective_type != MemoryType.EPISODIC:
            anchor_id = await self._ensure_session_anchor()
            if anchor_id:
                related_ids = [anchor_id]

        req = CreateRequest(
            uuid=self._session_uuid,
            type=effective_type,
            summary=summary,
            content=text,
            score=effective_score,
            weight=effective_score / 10,
            related_ids=related_ids,
            metadata=_build_metadata_json(self._container_tag, metadata),
        )

        data = await self._connection.post(
            "/api/v1/records",
            req.model_dump(exclude_none=True),
        )

        # If this add itself created the episodic anchor, remember its ID so
        # later typed adds in the same session can link to it without a probe.
        if effective_type == MemoryType.EPISODIC and self._session_anchor_id is None:
            self._session_anchor_id = data.get("id")

        return {
            "session_id": self._session_uuid,
            "records": [{"id": data.get("id", 0), "type": effective_type.value,
                          "summary": summary}],
            "mode": "oss",
        }

    async def _ensure_session_anchor(self) -> Optional[int]:
        """
        Return the episodic anchor ID for the current session, creating one if
        none exists yet.

        Junior Tip [anchor caching, 2026-06-08]: cached per session in
        ``_session_anchor_id`` so a burst of typed adds creates exactly one
        anchor, not one per call. The anchor is a minimal episodic record — the
        same shape the ingest endpoint produces — so downstream agents treat
        these sessions identically whether they arrived via ingest or direct.
        """
        if self._session_anchor_id is not None:
            return self._session_anchor_id

        anchor_req = CreateRequest(
            uuid=self._session_uuid,
            type=MemoryType.EPISODIC,
            summary="session start",
            content="session start",
            score=5,
            weight=0.5,
            metadata=_build_metadata_json(self._container_tag),
        )
        anchor_data = await self._connection.post(
            "/api/v1/records",
            anchor_req.model_dump(exclude_none=True),
        )
        self._session_anchor_id = anchor_data.get("id")
        return self._session_anchor_id

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
      - Batch operations (batch_read_content, mark_consolidated, decay)
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

    # ── Health ─────────────────────────────────────────────────────

    async def health(self) -> Dict[str, Any]:
        """
        Check server health.

        Returns:
            Dict with ``status`` (``"healthy"``) and ``name`` fields.

        Raises:
            AnhurConnectionError: If the server is unreachable.
        """
        return await self._connection.get("/api/v1/health")

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

    async def read_content(self, record_id: int) -> Any:
        """
        Read the full content payload for a record.

        Args:
            record_id: The record ID.

        Returns:
            The content payload. Type depends on what was stored:
            a dict for structured records, a string for plain text.

        Junior Tip [plain-text unwrap, 2026-06-08]: the server returns the raw
        content body (often plain text, not JSON) for this endpoint. Passing
        ``raw_text=True`` makes a non-JSON body come back as the verbatim
        string instead of being wrapped in ``{"message": <text[:1000]>}`` —
        the old behaviour both mislabelled the payload AND silently truncated
        it at 1000 chars.
        """
        return await self._connection.get(
            f"/api/v1/records/{record_id}/content",
            raw_text=True,
        )

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
        Global semantic search across all sessions (safe memory types only).

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
            params["q"] = query
        data = await self._connection.get("/api/v1/search/type", params=params)
        return data.get("results", []) if isinstance(data, dict) else []

    async def search_session(
        self,
        session_uuid: str,
        query: str = "",
        *,
        limit: int = 10,
        type_filter: Optional[str] = None,
    ) -> List[Dict[str, Any]]:
        """
        Search within a specific session (all record types).

        Unlike ``search()`` (global, safe types only), this returns ALL types
        including recent episodic records for the given session.

        Args:
            session_uuid: UUID of the session to search within.
            query:        Natural language query.
            limit:        Maximum results (default 10).
            type_filter:  Optional memory type filter.

        Returns:
            List of search result dicts.
        """
        payload: Dict[str, Any] = {"uuid": session_uuid, "text": query, "limit": limit}
        if type_filter:
            payload["type_filter"] = type_filter
        data = await self._connection.post("/api/v1/search", payload)
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

        Ranks results by a combination of text relevance and cognitive
        importance (score × weight).

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
        Global search alias for backward compatibility.

        Delegates directly to ``search()`` (``POST /api/v1/search/global``).
        There is no server-side recall endpoint or fan-out — the name mirrors
        the MCP ``recall`` tool convention.

        Args:
            query: Natural language query.
            limit: Maximum results (default 10).

        Returns:
            List of search result dicts.
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

    async def mark_consolidated(self, ids: List[int]) -> Dict[str, Any]:
        """
        Mark a batch of records as consolidated.

        Flags the given records as having been included in a summary record.
        Use ``link_to_consolidated()`` afterward to set the parent record pointer.

        Args:
            ids: List of record IDs to mark.

        Returns:
            Confirmation dict with count of updated records.
        """
        return await self._connection.patch(
            "/api/v1/records/mark-consolidated",
            {"ids": ids},
        )

    async def link_to_consolidated(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """
        Set the parent consolidated record for a batch of records.

        Links child records to their summary record after consolidation.

        Args:
            ids:             List of child record IDs.
            consolidate_id:  ID of the summary (parent) record.

        Returns:
            Confirmation dict.
        """
        return await self._connection.patch(
            "/api/v1/records/consolidate-ids",
            {"ids": ids, "consolidate_id": consolidate_id},
        )

    async def append_main_links(
        self,
        ids: List[int],
        main_ids_to_append: List[int],
    ) -> Dict[str, Any]:
        """
        Append parent record IDs to a batch of records (non-destructive).

        Does NOT replace existing ``main_ids`` — only adds new links.
        Use this to build parent-child relationships in the knowledge graph.

        Args:
            ids:                 Records to update.
            main_ids_to_append:  Parent IDs to add to each record's ``main_ids``.

        Returns:
            Confirmation dict.
        """
        return await self._connection.patch(
            "/api/v1/records/append-main-ids",
            {"ids": ids, "main_ids_to_append": main_ids_to_append},
        )

    async def append_related_links(
        self,
        ids: List[int],
        related_ids_to_append: List[int],
    ) -> Dict[str, Any]:
        """
        Append lateral record links to a batch of records (non-destructive).

        .. note::
            The server-side ``PATCH /api/v1/records/append-related-ids`` route
            is not yet registered. Use ``create()`` with ``related_ids`` set on
            the ``CreateRequest``, or build lateral links via the topology rules
            in the AnhurDB pipeline. This method will raise ``AnhurQueryError``
            until the server exposes the route.

        Args:
            ids:                    Records to update.
            related_ids_to_append:  Lateral record IDs to add.

        Returns:
            Confirmation dict.

        Raises:
            AnhurQueryError: Always — the server route is not yet available.
        """
        raise AnhurQueryError(
            "append_related_links: server route PATCH /api/v1/records/append-related-ids "
            "is not registered. Use CreateRequest.related_ids on create, or rely on "
            "topology rules to build lateral links automatically."
        )

    async def decay(
        self,
        ids: List[int],
        target_weight: float = 0.05,
        target_dimension: int = 64,
    ) -> Dict[str, Any]:
        """
        Apply memory decay to a batch of records.

        Decayed records are downgraded to floor values and archived.
        Use this to prune low-importance memories over time.

        Args:
            ids:              List of record IDs to decay.
            target_weight:    Floor weight after decay (default 0.05).
            target_dimension: Target embedding dimension after decay (default 64).

        Returns:
            Confirmation dict with count of decayed records.
        """
        return await self._connection.patch(
            "/api/v1/records/decay",
            {"ids": ids, "target_weight": target_weight, "target_dimension": target_dimension},
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
            {"seed_id": start_id, "depth": depth},
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

    async def graph(self, archived: bool = False) -> Dict[str, Any]:
        """
        Fetch the full knowledge graph (all nodes and edges).

        Returns every record node and relationship edge in the database.
        Use ``walk()`` for targeted traversal from a specific record.

        Args:
            archived: Include archived records (default False).

        Returns:
            Dict with ``nodes`` (list of records) and ``edges`` (list of links).
        """
        params: Dict[str, str] = {}
        if archived:
            params["archived"] = "1"
        return await self._connection.get("/api/v1/graph", params=params or None)

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

        Uses vector similarity and clustering to identify thematic groups
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
            params["q"] = query
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
            params["q"] = query
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
        content: bytes,
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
            content:    Raw file bytes.
            session_id: Optional session UUID to associate with.

        Returns:
            Dict with ``record_id``, ``uuid``, ``filename``, ``status``.

        Example::

            with open("report.pdf", "rb") as f:
                result = await client.upload_file("report.pdf", f.read())
            record_id = result["record_id"]
        """
        extra: Dict[str, str] = {}
        if session_id:
            extra["session_id"] = session_id
        return await self._connection.post_multipart(
            "/api/v1/upload",
            file_field="file",
            file_data=content,
            filename=filename,
            extra_fields=extra or None,
        )

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
            params["q"] = query
        if entity_type:
            params["type"] = entity_type
        data = await self._connection.get("/api/v1/entities", params=params)
        return data.get("entities", data) if isinstance(data, dict) else data

    async def list_entities(
        self,
        limit: int = 200,
        offset: int = 0,
    ) -> Dict[str, Any]:
        """
        Paginated walk of ALL entities for the tenant, ordered by id ASC.

        Unlike :meth:`search_entities` (keyword LIKE filter, limited match
        set), this walks every row with a stable cursor — pages never shift
        under concurrent inserts. Use for analytics, normalization sweeps,
        exports, or admin dashboards.

        Junior Tip [SDK parity, 2026-05-22]: mirrors Go ``Memory.ListEntities``
        and TS ``listEntities``. The response carries ``has_more`` +
        ``next_offset``; loop until ``has_more`` is false to consume the set.

        Args:
            limit:  Page size (default 200, server-clamped to [1, 500]).
            offset: 0-based offset (default 0).

        Returns:
            Dict with ``entities``, ``count``, ``total``, ``limit``,
            ``offset``, ``has_more``, ``next_offset``.
        """
        if limit <= 0:
            limit = 200
        if limit > 500:
            limit = 500
        if offset < 0:
            offset = 0
        params = {"limit": str(limit), "offset": str(offset)}
        return await self._connection.get("/api/v1/entities/list", params=params)

    async def create_in_session(
        self,
        text: str,
        session_uuid: str,
    ) -> Dict[str, Any]:
        """
        Store ``text`` directly as an episodic record under ``session_uuid``,
        bypassing the auto-session assignment of the ingest path.

        Junior Tip [SDK parity, 2026-05-22]: mirrors Go ``Memory.CreateInSession``
        and TS ``createInSession``. Used by agents that must place a record in
        a CALLER-OWNED session (e.g. consolidation writing a consolidated star
        into the chat session it summarised). Metadata is wrapped through the
        canonical JSON envelope to avoid the container_tag corruption bug.

        Args:
            text:         Record text (stored in both summary and content).
            session_uuid: The session UUID to place the record under (required).

        Returns:
            Dict with ``session_id`` and ``records`` (the new episodic anchor).
        """
        if not session_uuid:
            raise AnhurError("create_in_session: session_uuid is required")
        summary = text[:200] + "..." if len(text) > 200 else text
        payload: Dict[str, Any] = {
            "uuid": session_uuid,
            "type": "episodic",
            "dimension": 0,
            "prefix": "",
            "weight": 0.5,
            "score": 5,
            "vector": "",
            "related_ids": [],
            "main_ids": [],
            "consolidate_id": 0,
            "metadata": _build_metadata_json(self._container_tag),
            "summary": summary,
            "content": text,
            "consolidated": False,
            "status": "saved",
        }
        data = await self._connection.post("/api/v1/records", payload)
        return {
            "session_id": session_uuid,
            "records": [{"id": data.get("id", 0), "type": "episodic", "summary": summary}],
            "mode": "oss",
        }

    async def append_main_ids(
        self,
        record_id: int,
        main_ids: List[int],
    ) -> Dict[str, Any]:
        """
        Append parent record IDs to the ``main_ids`` array of a single record.

        Server-side this reads, deduplicates, and writes back — idempotent on
        the union of existing + supplied IDs. Junior Tip [SDK parity]: mirrors
        Go ``Memory.AppendMainIDs`` and TS ``appendMainIds``.

        Args:
            record_id: Child record that receives the parents.
            main_ids:  Parent IDs to append.

        Returns:
            Confirmation dict (empty when ``main_ids`` is empty — no-op).
        """
        if record_id <= 0:
            raise AnhurError("append_main_ids: record_id must be > 0")
        if not main_ids:
            return {}
        payload = {"ids": [record_id], "main_ids_to_append": main_ids}
        return await self._connection.patch("/api/v1/records/append-main-ids", payload)

    async def update_consolidate_ids(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """
        Set ``consolidate_id`` on a batch of child records (judge → star link).

        Junior Tip [SDK parity]: mirrors Go ``Memory.UpdateConsolidateIDs`` and
        TS ``updateConsolidateIds``. Batched so N children pointing at the same
        star cost ONE Raft round-trip instead of N.

        Args:
            ids:            Child record IDs.
            consolidate_id: The consolidated star's ID.

        Returns:
            Confirmation dict (empty when ``ids`` is empty — no-op).
        """
        if not ids:
            return {}
        if consolidate_id <= 0:
            raise AnhurError("update_consolidate_ids: consolidate_id must be > 0")
        payload = {"ids": ids, "consolidate_id": consolidate_id}
        return await self._connection.patch("/api/v1/records/consolidate-ids", payload)

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
            Raw manifest metadata dict from the server (limit=0 probe).
            May include ``count``, ``has_more``, ``records`` keys.
            For actual per-type counts, use ``search_by_type()`` per type
            or ``manifest_global()`` with a small limit and inspect ``count``.
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

    async def explain(self, record_id: int) -> Dict[str, Any]:
        """
        Get a human-readable explanation of why a record scored the way it did.

        Returns the cognitive weight breakdown, decay factors, and the reasoning
        behind the record's current score and status.

        Args:
            record_id: The record ID to explain.

        Returns:
            Dict with weight breakdown, decay factors, and cognitive rationale.
        """
        return await self._connection.get(f"/api/v1/records/{record_id}/explain")

    async def access_stats(self) -> Dict[str, Any]:
        """
        Get access frequency statistics for records.

        Returns aggregated access counts used by the decay and hub-growth agents
        to calibrate weight decay and identify high-traffic hubs.

        Returns:
            Dict with per-record access counts and aggregated statistics.
        """
        return await self._connection.get("/api/v1/stats/access")

    async def get_engine_config(self) -> Dict[str, Any]:
        """
        Get the current tenant's cognitive engine configuration.

        Returns the effective tuning parameters (decay rates, consolidation
        thresholds, hub growth limits) that the agents are operating with.

        Returns:
            Dict with engine configuration parameters.
        """
        return await self._connection.get("/api/v1/tenant/engine-config")
