"""AnhurDB Python SDK — Memory client.

``Memory`` is the canonical client for the full REST API.
``AnhurClient`` is a deprecated alias with a localhost default URL.
"""

import base64
import hashlib
import json
import os
import secrets
import warnings
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
DEFAULT_CLOUD_URL = "https://anhurdb.yoven.ai"

# Historical self-hosted default. Kept ONLY for the deprecated ``AnhurClient``
# subclass alias so its constructor behaves exactly as it did before the
# single-class collapse (the old ``AnhurClient`` defaulted to localhost, not
# the cloud endpoint).
_LEGACY_LOCAL_URL = "http://localhost:8080"


# ---------------------------------------------------------------------------
# Helper: derive a stable container tag from the API key
# ---------------------------------------------------------------------------

def _derive_container_tag(api_key: str) -> str:
    """Derive a short, stable hex tag from the API key using SHA-256.

    The first 12 hex characters of the hash are used, prefixed with
    ``mem-``. This matches the algorithm in the TypeScript and Go SDKs
    so the same API key always produces the same container tag across
    all three languages.

    Args:
        api_key: The raw API key string.

    Returns:
        A container tag like ``mem-a1b2c3d4e5f6``."""
    digest = hashlib.sha256(api_key.encode("utf-8")).hexdigest()
    return f"mem-{digest[:12]}"


def _build_metadata_json(
    container_tag: str,
    extra_metadata: Optional[Dict[str, Any]] = None,
) -> str:
    """Wrap ``container_tag`` into the canonical metadata JSON envelope
    ``{"container_tag": "<tag>"}``.

    Returns ``"{}"`` when container_tag is empty so the column always holds a
    parseable JSON object."""
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


def _parse_search_results(data: Any) -> List[SearchResult]:
    """Parse a raw search-endpoint envelope into typed ``SearchResult`` objects.

    Returns an empty list when the payload is not the expected envelope object."""
    if not isinstance(data, dict):
        return []
    parsed: List[SearchResult] = []
    for hit in data.get("results", []):
        parsed.append(
            SearchResult(
                record=Record(**hit["record"]),
                similarity=hit.get("similarity", 0.0),
            )
        )
    return parsed


def _parse_typed_records(data: Any) -> List[SearchResult]:
    """Parse a BARE-record envelope (``{"records": [{...}, ...], "count": N}``) into
    typed ``SearchResult`` objects.

    Returns an empty list when the payload is not the expected envelope object."""
    if not isinstance(data, dict):
        return []
    return [
        SearchResult(record=Record(**record), similarity=0.0)
        for record in data.get("records", [])
    ]


# ---------------------------------------------------------------------------
# Memory — the single canonical client (simple ergonomics + full surface)
# ---------------------------------------------------------------------------

class Memory:
    """The one AnhurDB client. Dead-simple to start with, complete underneath.

    Handles session management, container tagging, and cloud/OSS fallback
    automatically (the simple ergonomics) while ALSO exposing every AnhurDB
    REST endpoint directly on the same object (the full surface). Mirrors the
    TypeScript ``Memory`` class and Go ``client.Memory`` struct
    method-for-method.

    Core methods:
        - ``add(text)``    — store a memory
        - ``search(query)`` — find relevant memories (default scope: sessions)
        - ``profile()``    — get user/agent profile

    Full surface matches the Go/TypeScript SDKs (see ``v2/PARITY_SPEC.md``):
    create/update/delete, search family, manifests, walk, entities, upload,
    batch ops, sessions, profile, grounding.

    Args:
        api_key:   AnhurDB API key (required). Falls back to
                   ``ANHUR_API_KEY`` environment variable.
        url:       Server URL (default: cloud endpoint).
        user_id:   Explicit container tag. When omitted, derived from
                   API key hash.
        tenant_id: Optional ``X-Tenant-ID`` header for multi-tenant.
        mode:      Transport — ``"rest"`` (default) or ``"mcp"``."""

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

        # Memory owns the single HTTPConnection (one TCP/HTTP pool). No facade
        # indirection — every method below issues its own request through this.
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

        # Session UUID: container_tag + UTC timestamp + 6 random hex chars.
        # timestamp alone (``%Y%m%d-%H%M%S``) has 1-second resolution, so two
        # Memory clients constructed in the same second under the same
        # container_tag would derive the SAME session_uuid and cross-write each
        # other's session. Appending 3 bytes of crypto-random hex
        # (``secrets.token_hex(3)`` → 6 lowercase hex chars) makes the default
        # session_uuid effectively unique. Format is byte-for-byte identical to
        # the Go and TS SDKs: ``<container_tag>-<YYYYMMDD-HHMMSS>-<6 hex>``.
        self._session_uuid = f"{self._container_tag}-{_utc_timestamp()}-{secrets.token_hex(3)}"

        # Derived add()/create() issue exactly one request. When a session has
        # no episodic yet, the server returns HTTP 422 ("create an episodic
        # record first"), surfaced as AnhurQueryError. Callers write the
        # episodic first — the client never fabricates a synthetic anchor.

        # Cloud ingest availability (None = untested).
        self._ingest_available: Optional[bool] = None

    # -- Lifecycle ----------------------------------------------------------

    async def connect(self) -> None:
        """Open the HTTP session (idempotent)."""
        await self._connection.connect()

    async def close(self) -> None:
        """Close the HTTP session and release resources."""
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

    # ── Health ─────────────────────────────────────────────────────

    async def health(self) -> Dict[str, Any]:
        """Check server health.

        Returns:
            Dict with ``status`` (``"healthy"``) and ``name`` fields.

        Raises:
            AnhurConnectionError: If the server is unreachable."""
        return await self._connection.get("/api/v1/health")

    # -- Core: add() --------------------------------------------------------

    async def add(
        self,
        text: str,
        *,
        score: Optional[int] = None,
        type: Optional[MemoryType] = None,
        metadata: Optional[Dict[str, Any]] = None,
        session_id: str = "",
    ) -> Dict[str, Any]:
        """Store a memory. Simplest way to save information.

        ``/api/v1/ingest`` accepts ONLY ``content`` + ``container_tag`` — it
        stores type as episodic and does not persist score/type/metadata.
        Sending them there drops them silently. So when the caller explicitly
        sets ``score``, ``type``, or ``metadata`` we MUST go straight to
        ``/api/v1/records``, which is the only write path that persists those
        columns. Plain ``add(text)`` with no options still prefers the ingest
        pipeline (auto-embedding + extraction).

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
                                   type=MemoryType.PREFERENCE)"""
        if not text:
            raise ValueError("text cannot be empty")

        # When score/type/metadata are explicitly requested, the ingest endpoint
        # cannot honour them — go directly to the records path which persists
        # all three. This is the parity contract shared with the Go/TS SDKs.
        wants_explicit_fields = (
            score is not None or type is not None or metadata is not None
        )

        if not wants_explicit_fields and self._ingest_available is not False:
            result = await self._try_ingest(text, score, type, metadata, session_id)
            if result is not None:
                return result

        # Direct record creation — honours score, type, and metadata.
        return await self._create_record(text, score, type, metadata)

    # ── Memory CRUD ────────────────────────────────────────────────

    async def create(self, req: CreateRequest) -> Dict[str, Any]:
        """Create a new memory record (full-fidelity, caller-owned).

        Unlike ``add()`` (which auto-manages the session/anchor), ``create()``
        sends the ``CreateRequest`` straight to ``POST /api/v1/records`` — the
        caller supplies ``uuid``, ``type``, ``score``, ``related_ids`` etc.

        Args:
            req: ``CreateRequest`` with at minimum ``uuid`` and ``content``.

        Returns:
            Server response dict (the created record). Includes ``id``."""
        # Inject the SDK-owned container_tag into metadata (same as add() and
        # the Go/TS create paths) so records stay visible to container-scoped
        # search/profile.
        payload = req.model_dump(exclude_none=True)
        # Seed weight from score/10 when the caller did not set weight, matching
        # add()/_create_record and the Go/TS create defaults.
        if "weight" not in req.model_fields_set:
            payload["weight"] = round((req.score if req.score is not None else 5) / 10, 4)
        caller_metadata: Dict[str, Any] = {}
        existing_metadata = payload.get("metadata")
        if isinstance(existing_metadata, dict):
            caller_metadata = existing_metadata
        elif isinstance(existing_metadata, str) and existing_metadata.strip() not in ("", "{}"):
            try:
                parsed_metadata = json.loads(existing_metadata)
                if isinstance(parsed_metadata, dict):
                    caller_metadata = parsed_metadata
            except (ValueError, TypeError):
                # Non-JSON metadata string: keep it under a key rather than drop it.
                caller_metadata = {"_raw": existing_metadata}
        payload["metadata"] = _build_metadata_json(self._container_tag, caller_metadata)

        # One request. Missing episodic anchor → HTTP 422, surfaced as
        # AnhurQueryError. Never fabricate a synthetic anchor client-side.
        return await self._connection.post("/api/v1/records", payload)

    async def get(
        self,
        record_id: int,
    ) -> Dict[str, Any]:
        """Get a record's metadata by ID.

        Args:
            record_id: The record ID.

        Returns:
            Record metadata dict."""
        return await self._connection.get(
            f"/api/v1/records/{record_id}"
        )

    async def update(self, record_id: int, **fields: Any) -> None:
        """Partially update a record.

        Args:
            record_id: The record ID to update.
            **fields:  Keyword arguments for fields to update
                       (e.g. ``summary="new"``, ``status="archived"``).

        Example::

            await mem.update(42, summary="Updated summary", score=8)"""
        await self._connection.patch(f"/api/v1/records/{record_id}", fields)

    async def delete(self, record_id: int) -> None:
        """Delete a record by ID (hard delete).

        For soft delete, use ``update(id, status="archived")`` instead.

        Args:
            record_id: The record ID to delete."""
        await self._connection.delete(f"/api/v1/records/{record_id}")

    async def read_content(
        self,
        record_id: int,
    ) -> Any:
        """Read the full content payload for a record.

        Args:
            record_id: The record ID to read.

        Returns:
            The content payload. Type depends on what was stored:
            a dict for structured records, a string for plain text."""
        return await self._connection.get(
            f"/api/v1/records/{record_id}/content",
            raw_text=True,
                    )

    async def get_context(
        self,
        record_id: int,
    ) -> Dict[str, Any]:
        """Get the topological context (1-hop neighbours) around a record.

        Returns the target record plus its parent, child, and sibling
        records in the knowledge graph.

        Args:
            record_id: The record ID to inspect.

        Returns:
            Dict with ``target`` and ``neighbors``."""
        return await self._connection.get(
            f"/api/v1/records/{record_id}/topology",
                    )

    async def get_grounding(
        self,
        record_id: int,
        max_depth: int = 3,
    ) -> Dict[str, Any]:
        """Get the provenance ("grounding") subgraph for a record — the episodic
        anchors and consolidated stars that this record was derived from.

        Performs a server-side BFS over main_ids/related_ids to surface WHERE a
        memory came from, with the anchors' raw chat snippets attached.

        Args:
            record_id: Target record id (must be > 0).
            max_depth: BFS depth budget, integer 1..5 inclusive (default 3).

        Returns:
            Dict with ``target``, ``anchors`` (each may carry whitelisted
            ``content`` keys ``user``/``assistant``/``full_text``),
            ``consolidations``, ``depth_used``, ``max_depth``, ``found_count``,
            and the ``anchors_capped`` / ``consolidations_capped`` flags.

        Raises:
            ValueError: If ``max_depth`` is outside 1..5 (fail fast locally
                        rather than round-trip to a guaranteed HTTP 400)."""
        # Validate locally so we fail loud and cheaply — the server enforces the
        # exact same 1..5 bound and would 400, but a clear ValueError is kinder.
        if not isinstance(max_depth, int) or max_depth < 1 or max_depth > 5:
            raise ValueError("max_depth must be an integer between 1 and 5")
        return await self._connection.get(
            f"/api/v1/records/{record_id}/grounding",
            params={"max_depth": str(max_depth)},
                    )

    # ── Search ─────────────────────────────────────────────────────

    async def search(
        self,
        query: str,
        *,
        limit: int = 10,
        type_filter: Optional[str] = None,
        scope: str = "sessions",
    ) -> List[SearchResult]:
        """Hybrid semantic search via ``POST /api/v1/search``.

        Default ``scope`` is ``sessions`` (all chat sessions for the tenant,
        excluding shared-library uuids). Use the scope helpers or pass
        ``tenant_shared``, ``client_shared``, or ``shared_all`` explicitly.

        Args:
            query:       Natural language query (required).
            limit:       Maximum results (default 10).
            type_filter: Optional memory type filter.
            scope:       Search plane (default ``sessions``).

        Returns:
            List of typed ``SearchResult`` objects (nested ``.record`` +
            ``.similarity``).

        Example::

            hits = await mem.search("what does this user do?", limit=5)"""
        payload: Dict[str, Any] = {"text": query, "limit": limit, "scope": scope}
        if type_filter:
            payload["type_filter"] = type_filter
        data = await self._connection.post("/api/v1/search", payload)
        return _parse_search_results(data)

    async def search_sessions(self, query: str, **kwargs: Any) -> List[SearchResult]:
        """Search chat sessions only (``scope=sessions``)."""
        return await self.search(query, scope="sessions", **kwargs)

    async def search_tenant_shared(
        self, query: str, **kwargs: Any
    ) -> List[SearchResult]:
        """Search tenant-shared library docs (``scope=tenant_shared``)."""
        return await self.search(query, scope="tenant_shared", **kwargs)

    async def search_client_shared(
        self, query: str, **kwargs: Any
    ) -> List[SearchResult]:
        """Search client-wide shared library (``scope=client_shared``)."""
        return await self.search(query, scope="client_shared", **kwargs)

    async def search_shared(self, query: str, **kwargs: Any) -> List[SearchResult]:
        """Search both shared planes (``scope=shared_all``)."""
        return await self.search(query, scope="shared_all", **kwargs)

    async def search_by_type(
        self,
        memory_type: str,
        limit: int = 20,
        query: Optional[str] = None,
    ) -> List[SearchResult]:
        """Search filtered by cognitive type with optional keyword query.

        Faster than semantic search when you know the exact type.

        Args:
            memory_type: Type to filter (e.g. ``"fact"``, ``"risk"``).
            limit:       Maximum results (default 20).
            query:       Optional keyword search within the type.

        Returns:
            List of typed ``SearchResult`` objects (nested ``.record`` +
            ``.similarity``)."""
        params: Dict[str, str] = {"type": memory_type, "limit": str(limit)}
        if query:
            params["q"] = query
        data = await self._connection.get(
            "/api/v1/search/type", params=params
        )
        return _parse_typed_records(data)

    async def search_session(
        self,
        query: str = "",
        *,
        session_uuid: Optional[str] = None,
        limit: int = 10,
        type_filter: Optional[str] = None,
    ) -> List[SearchResult]:
        """Search within a single session (all record types, including recent).

        Uses ``POST /api/v1/search`` with ``scope=sessions`` and a session
        ``uuid`` so results come from one chat only.

        Args:
            query:        Natural language query.
            session_uuid: Session to search; ``None`` = current session.
            limit:        Maximum results (default 10).
            type_filter:  Optional memory type filter.

        Returns:
            List of typed ``SearchResult`` objects (nested ``.record`` +
            ``.similarity``)."""
        target_uuid = session_uuid if session_uuid is not None else self._session_uuid
        payload: Dict[str, Any] = {
            "uuid": target_uuid,
            "text": query,
            "limit": limit,
            "scope": "sessions",
        }
        if type_filter:
            payload["type_filter"] = type_filter
        data = await self._connection.post("/api/v1/search", payload)
        return _parse_search_results(data)

    async def smart_search(
        self,
        query: str,
        *,
        limit: int = 10,
        memory_type: Optional[str] = None,
        scope: str = "sessions",
    ) -> Any:
        """Full-text search with cognitive weight boosting.

        Ranks results by a combination of text relevance and cognitive
        importance (score × weight). Same memory-plane ``scope`` as
        ``search()`` (default ``sessions``).

        Args:
            query:       Search query.
            limit:       Maximum results (default 10).
            memory_type: Optional type filter.
            scope:       Search plane (default ``sessions``).

        Returns:
            Search results ranked by cognitive relevance."""
        params: Dict[str, str] = {
            "q": query,
            "limit": str(limit),
            "scope": scope,
        }
        if memory_type:
            params["type"] = memory_type
        return await self._connection.get(
            "/api/v1/search/smart", params=params
        )

    async def recall(
        self,
        query: str,
        limit: int = 10,
        *,
        scope: str = "sessions",
    ) -> List[SearchResult]:
        """Recall memories via plane-aware search.

        Delegates directly to ``search()`` (``POST /api/v1/search``,
        default ``scope=sessions``). There is no server-side recall endpoint
        or fan-out — the name mirrors the MCP ``recall`` tool convention
        (whose 4-way fan-out + RRF lives in the MCP server, not the data
        plane). Identical across the three SDKs.

        Args:
            query:     Natural language query.
            limit:     Maximum results (default 10).
            scope:     Search plane (default ``sessions``).

        Returns:
            List of typed ``SearchResult`` objects (inherited from ``search``)."""
        return await self.search(query, limit=limit, scope=scope)

    async def query(
        self,
        ast: Any,
        session_uuid: Optional[str] = None,
    ) -> List[Record]:
        """Execute an AST query against AnhurDB (``POST /api/v1/query``).

        If ``session_uuid`` is provided it is injected as a ``uuid`` filter so
        results are scoped to that session. The server expects the AST FLAT at the
        top level of the body (filters, pagination, sort, select) — NOT wrapped in
        a ``{"query": ...}`` key.

        Args:
            ast:          A compiled AST dict, or a QueryBuilder/Filter instance.
            session_uuid: Optional session UUID to scope results.

        Returns:
            List of ``Record`` objects matching the query.

        Example::

            from anhurdb.query import QueryBuilder
            qb = QueryBuilder().where(type="risk", score__gte=7).limit(20)
            records = await mem.query(qb, session_uuid="s1")"""
        # Accept a raw AST dict or a QueryBuilder (.build_ast()) / Filter (.ast()).
        if isinstance(ast, dict):
            compiled_ast = dict(ast)
        elif hasattr(ast, "build_ast"):
            compiled_ast = ast.build_ast()
        elif hasattr(ast, "ast"):
            compiled_ast = ast.ast()
        else:
            raise TypeError(
                "query() needs an AST dict or a QueryBuilder/Filter "
                "(exposing build_ast()/ast())."
            )

        # The server does NOT accept session_uuid as a separate field — it must be
        # a regular filter in the AST's filters dict.
        if session_uuid:
            compiled_ast.setdefault("filters", {})["uuid"] = {"$eq": session_uuid}

        # Server expects the AST flat at top-level. Do NOT wrap in {"query": ast}.
        data = await self._connection.post(
            "/api/v1/query", compiled_ast
        )
        # result as {"records": null} (a nil Go slice -> JSON null), so .get("records", [])
        # returns None (key present) and iterating it raises TypeError. `or []` coalesces
        # null/None to [], matching Go (wrapped.Records == nil -> []) and TS (records ?? []).
        records_data = (data.get("records") or []) if isinstance(data, dict) else []
        return [Record(**record_fields) for record_fields in records_data]

    async def search_with_ast(
        self,
        filter_builder: Any,
        session_uuid: Optional[str] = None,
    ) -> List[Record]:
        """Deprecated: use :meth:`query` instead.

        Forwarding alias kept so existing callers keep working after the canonical
        rename to ``query`` (matching Go ``Query`` / TS ``query``)."""
        warnings.warn(
            "search_with_ast() is deprecated; use query().",
            DeprecationWarning,
            stacklevel=2,
        )
        return await self.query(filter_builder, session_uuid)

    # ── Batch Operations ───────────────────────────────────────────

    async def batch_read_content(
        self,
        ids: List[int],
    ) -> Dict[str, Any]:
        """Fetch full content for multiple records in a single call (max 100).

        Eliminates the N+1 pattern of calling ``read_content`` in a loop.

        Args:
            ids:       List of record IDs (max 100).

        Returns:
            Dict mapping ``record_id → content_payload``."""
        data = await self._connection.post(
            "/api/v1/records/batch-content",
            {"ids": ids},
                    )
        return data if isinstance(data, dict) else {}

    async def batch_update_status(self, ids: List[int], status: str) -> Dict[str, Any]:
        """Update the status for a batch of records at once.

        Args:
            ids:    List of record IDs to update.
            status: New status (e.g. consolidated, hubbed, processing,
                    completed, failed).

        Returns:
            Confirmation dict with count of updated records."""
        return await self._connection.patch(
            "/api/v1/records/mark-consolidated",
            {"ids": ids, "status": status},
        )

    async def mark_consolidated(self, ids: List[int]) -> Dict[str, Any]:
        """Deprecated: use :meth:`batch_update_status` instead.

        Kept as a forwarding alias so existing callers keep working after the canonical rename."""
        warnings.warn(
            "mark_consolidated() is deprecated; use batch_update_status(ids, status).",
            DeprecationWarning,
            stacklevel=2,
        )
        # Historical behavior: mark_consolidated always meant status="consolidated".
        return await self.batch_update_status(ids, "consolidated")

    async def link_consolidated(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """Set the parent consolidated record for a batch of child records.

        Links child records to their summary ("star") record after
        consolidation. Batched so N children pointing at the same star cost
        ONE server round-trip instead of N.

        Args:
            ids:             List of child record IDs.
            consolidate_id:  ID of the summary (parent) record.

        Returns:
            Confirmation dict (empty when ``ids`` is empty — no-op)."""
        if not ids:
            return {}
        if consolidate_id <= 0:
            raise AnhurError("link_consolidated: consolidate_id must be > 0")
        return await self._connection.patch(
            "/api/v1/records/consolidate-ids",
            {"ids": ids, "consolidate_id": consolidate_id},
        )

    async def link_to_consolidated(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """Deprecated: use :meth:`link_consolidated` instead.

        Kept as a forwarding alias so existing callers keep working after the
        canonical rename."""
        warnings.warn(
            "link_to_consolidated() is deprecated; use link_consolidated().",
            DeprecationWarning,
            stacklevel=2,
        )
        return await self.link_consolidated(ids, consolidate_id)

    async def update_consolidate_ids(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """Deprecated: use :meth:`link_consolidated` instead.

        Kept as a forwarding alias so existing callers keep working after the canonical rename."""
        warnings.warn(
            "update_consolidate_ids() is deprecated; use link_consolidated().",
            DeprecationWarning,
            stacklevel=2,
        )
        return await self.link_consolidated(ids, consolidate_id)

    async def append_main_ids(
        self,
        record_id: int,
        main_ids: List[int],
    ) -> Dict[str, Any]:
        """Append parent record IDs to the ``main_ids`` array of a single record.

        Server-side this reads, deduplicates, and writes back — idempotent on
        Go ``Memory.AppendMainIDs`` and TS ``appendMainIds``.

        Args:
            record_id: Child record that receives the parents.
            main_ids:  Parent IDs to append.

        Returns:
            Confirmation dict (empty when ``main_ids`` is empty — no-op)."""
        if record_id <= 0:
            raise AnhurError("append_main_ids: record_id must be > 0")
        if not main_ids:
            return {}
        payload = {"ids": [record_id], "main_ids_to_append": main_ids}
        return await self._connection.patch("/api/v1/records/append-main-ids", payload)

    async def append_related_ids(
        self,
        record_id: int,
        related_ids: List[int],
    ) -> Dict[str, Any]:
        """Append related record IDs to the ``related_ids`` array of a single record.

        Server-side this reads, deduplicates, and writes back — idempotent on
        mirror of ``append_main_ids`` on the sibling REST route
        ``PATCH /api/v1/records/append-related-ids`` (payload key
        ``related_ids_to_append``); keeps the Go/Python/TS SDK trio in lockstep
        (parity invariant #13). Append, never replace.

        Args:
            record_id:   Record that receives the related links.
            related_ids: Related IDs to append.

        Returns:
            Confirmation dict (empty when ``related_ids`` is empty — no-op)."""
        if record_id <= 0:
            raise AnhurError("append_related_ids: record_id must be > 0")
        if not related_ids:
            return {}
        payload = {"ids": [record_id], "related_ids_to_append": related_ids}
        return await self._connection.patch("/api/v1/records/append-related-ids", payload)

    async def append_main_links(
        self,
        ids: List[int],
        main_ids_to_append: List[int],
    ) -> Dict[str, Any]:
        """Append parent record IDs to a BATCH of records (non-destructive).

        Does NOT replace existing ``main_ids`` — only adds new links. Use this
        to build parent-child relationships in the knowledge graph across many
        records at once. For a single record, prefer ``append_main_ids``.

        Args:
            ids:                 Records to update.
            main_ids_to_append:  Parent IDs to add to each record's ``main_ids``.

        Returns:
            Confirmation dict."""
        return await self._connection.patch(
            "/api/v1/records/append-main-ids",
            {"ids": ids, "main_ids_to_append": main_ids_to_append},
        )

    # ── Graph Traversal ────────────────────────────────────────────

    async def walk(
        self,
        start_id: int,
        depth: int = 3,
    ) -> Dict[str, Any]:
        """BFS graph traversal from a seed record.

        Follows related_ids and main_ids edges in both directions up to the
        specified depth.

        Args:
            start_id:  Record ID to start from.
            depth:     Maximum hops (default 3).

        Returns:
            Dict with ``nodes`` and ``edges``."""
        return await self._connection.post(
            "/api/v1/walk",
            {"seed_id": start_id, "depth": depth, "direction": "both"},
        )

    async def walk_semantic(
        self,
        start_id: int,
        depth: int = 3,
        *,
        target: Optional[str] = None,
        goal_vector: Optional[bytes] = None,
        target_tag: Optional[str] = None,
        max_cost: Optional[float] = None,
    ) -> Dict[str, Any]:
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
            Dict with ``nodes`` and ``edges``."""
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
        return await self._connection.post(
            "/api/v1/walk/semantic",
            body,
                    )

    # ── Session Management ─────────────────────────────────────────

    def new_session(self) -> str:
        """Start a new session (generates a fresh UUID).

        All subsequent ``add()`` calls will be grouped under this session.

        Returns:
            The new session UUID."""
        # default session_uuid — <container_tag>-<YYYYMMDD-HHMMSS UTC>-<6 random hex>.
        # The random suffix (secrets.token_hex(3)) is what stops two rotations in the
        # same UTC second from colliding onto one session (cross-contamination), and it
        # keeps this path byte-for-byte with Go NewSession and TS newSession.
        self._session_uuid = f"{self._container_tag}-{_utc_timestamp()}-{secrets.token_hex(3)}"
        return self._session_uuid

    async def list_sessions(
        self,
    ) -> List[Dict[str, Any]]:
        """List all sessions with aggregate statistics.

        Args:

        Returns:
            List of dicts with ``uuid``, ``record_count``, ``types``,
            ``last_activity``."""
        data = await self._connection.get(
            "/api/v1/sessions/stats"
        )
        return data.get("sessions", data) if isinstance(data, dict) else data

    async def list_chat(
        self,
        session_uuid: str,
        *,
        consolidated: Optional[bool] = None,
        status: Optional[str] = None,
    ) -> List[Dict[str, Any]]:
        """List all records in a specific session (metadata only, no .gz body).

        Args:
            session_uuid: The session UUID.
            consolidated: Tri-state filter. ``None`` = all; ``True`` = only
                          consolidated; ``False`` = only non-consolidated.
            status:       Optional exact status filter (e.g. ``"saved"``).

        Returns:
            List of record dicts."""
        params: Dict[str, str] = {}
        if consolidated is not None:
            # Server parses (val == "true"); send the canonical lowercased form.
            params["consolidated"] = "true" if consolidated else "false"
        if status:
            params["status"] = status
        data = await self._connection.get(
            f"/api/v1/chats/{session_uuid}",
            params=params or None,
                    )
        return data.get("records", data) if isinstance(data, dict) else data

    async def get_session_history(
        self,
        session_uuid: str,
        limit: int = 50,
        offset: int = 0,
    ) -> Dict[str, Any]:
        """Get paginated full-text history for a session.

        Returns actual message content from the filesystem, unlike
        ``list_chat`` which returns metadata only.

        Args:
            session_uuid: The session UUID.
            limit:        Max records per page (default 50).
            offset:       Pagination offset.

        Returns:
            Dict with ``records``, ``total_records``, ``returned_count``."""
        return await self._connection.get(
            f"/api/v1/sessions/{session_uuid}/history",
            params={"limit": str(limit), "offset": str(offset)},
                    )

    async def get_session_clusters(
        self,
        session_uuid: str,
    ) -> Dict[str, Any]:
        """Get mathematically clustered topological groups for a session.

        Uses vector similarity and clustering to identify thematic groups
        within the session's records.

        Args:
            session_uuid: The session UUID.

        Returns:
            Dict with cluster assignments."""
        return await self._connection.get(
            f"/api/v1/sessions/{session_uuid}/clusters",
                    )

    async def manifest_global(
        self,
        limit: int = 50,
        offset: int = 0,
        query: Optional[str] = None,
        *,
        as_of: Optional[str] = None,
        since: Optional[str] = None,
        until: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Cross-session overview of all knowledge, ranked by importance.

        Best tool for RAG context injection — returns the most important
        records across all sessions.

        Args:
            limit:     Max records (default 50).
            offset:    Pagination offset.
            query:     Optional keyword filter.
            as_of:     Optional RFC3339 UTC snapshot instant. Mutually
                       exclusive with ``since``/``until`` (server rejects the
                       combination with HTTP 400).
            since:     Optional RFC3339 UTC lower bound (created_at >= since).
            until:     Optional RFC3339 UTC upper bound (created_at <= until).

        Returns:
            Dict with ``count``, ``has_more``, ``records``, ``limit``,
            ``offset``."""
        params: Dict[str, str] = {"limit": str(limit), "offset": str(offset)}
        if query:
            params["q"] = query
        if as_of:
            params["as_of"] = as_of
        if since:
            params["since"] = since
        if until:
            params["until"] = until
        return await self._connection.get(
            "/api/v1/manifest", params=params
        )

    async def manifest_session(
        self,
        session_uuid: str,
        query: Optional[str] = None,
        *,
        limit: int = 500,
        offset: int = 0,
        as_of: Optional[str] = None,
        since: Optional[str] = None,
        until: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Get the manifest for a single session (records with metadata).

        Args:
            session_uuid: The session UUID.
            query:        Optional keyword filter (sent as ``q``).
            limit:        Max records (default 500).
            offset:       Pagination offset.
            as_of:        Optional RFC3339 UTC snapshot instant. Mutually
                          exclusive with ``since``/``until``.
            since:        Optional RFC3339 UTC lower bound.
            until:        Optional RFC3339 UTC upper bound.

        Returns:
            Dict with ``records``, ``count``, ``limit``, ``offset``,
            ``has_more``."""
        params: Dict[str, str] = {"limit": str(limit), "offset": str(offset)}
        if query:
            params["q"] = query
        if as_of:
            params["as_of"] = as_of
        if since:
            params["since"] = since
        if until:
            params["until"] = until
        return await self._connection.get(
            f"/api/v1/chats/{session_uuid}/manifest",
            params=params,
        )

    async def count_by_type(
        self,
    ) -> Dict[str, int]:
        """Get aggregated record counts per cognitive type.

        Args:

        Returns:
            Dict mapping ``type → count`` across all (non-archived) records."""
        counts: Dict[str, int] = {}
        page_size = 1000  # server hard cap; minimises round-trips.
        offset = 0
        while True:
            data = await self._connection.get(
                "/api/v1/manifest",
                params={"limit": str(page_size), "offset": str(offset)},
                            )
            records = data.get("records") if isinstance(data, dict) else None
            if not records:
                break
            for record_fields in records:
                record_type = record_fields.get("type", "") if isinstance(record_fields, dict) else ""
                counts[record_type] = counts.get(record_type, 0) + 1
            # ``has_more`` can false-positive on an exactly-full last page, so we
            # also stop when the page came back short — whichever fires first.
            has_more = bool(data.get("has_more")) if isinstance(data, dict) else False
            if not has_more or len(records) < page_size:
                break
            offset += page_size
        return counts

    async def recent(
        self,
        limit: int = 20,
    ) -> List[Record]:
        """Get recently updated records.

        Args:
            limit:     Maximum records (default 20).

        Returns:
            List of typed ``Record`` objects ordered by creation time (newest first)."""
        data = await self._connection.get(
            "/api/v1/recent",
            params={"limit": str(limit)},
                    )
        # objects (the FULL record) instead of returning raw dicts — matches Go/TS recent()
        # which return the full typed record, and mirrors the typed SearchResult parsing.
        records = data if isinstance(data, list) else data.get("records", [])
        return [Record(**rec) for rec in records if isinstance(rec, dict)]

    # ── Taxonomy (local, no REST round-trip) ───────────────────────

    def list_types(self) -> List[str]:
        """List the canonical cognitive memory types (LOCAL — no network call).

        Returns:
            List of type value strings, e.g.
            ``["episodic", "fact", "preference", ...]``."""
        return [member.value for member in MemoryType]

    # ── File Upload ────────────────────────────────────────────────

    async def upload_file(
        self,
        filename: str,
        content: bytes,
        session_id: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Upload a document for async ingestion.

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

            with open("report.pdf", "rb") as handle:
                result = await mem.upload_file("report.pdf", handle.read())
            record_id = result["record_id"]"""
        extra: Dict[str, str] = {}
        if session_id:
            extra["session_id"] = session_id
            # creates a second root and leaves the upload status stuck at processing.
            extra["mode"] = "chat"
        return await self._connection.post_multipart(
            "/api/v1/upload",
            file_field="file",
            file_data=content,
            filename=filename,
            extra_fields=extra or None,
        )

    async def upload_status(
        self,
        upload_id: int,
    ) -> Dict[str, Any]:
        """Check the processing status of a file upload.

        Args:
            upload_id: The upload ID returned by ``upload_file()``.

        Returns:
            Dict with ``status`` (``"processing"``, ``"completed"``,
            ``"failed"``)."""
        return await self._connection.get(
            f"/api/v1/upload/{upload_id}/status"
        )

    # ── Temporal Versioning ────────────────────────────────────────

    async def supersede(self, old_id: int, new_id: int) -> Dict[str, Any]:
        """Mark an old record as superseded by a new one.

        This implements temporal versioning — the old record remains in the
        graph but is annotated with ``superseded_by`` pointing to the new
        record. Search results prefer the newer version.

        Args:
            old_id: The record being superseded.
            new_id: The replacement record.

        Returns:
            Confirmation dict."""
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
        """Search named entities (people, organisations, concepts).

        Args:
            query:       Name or keyword search.
            entity_type: Filter by entity type (e.g. ``"person"``).
            limit:       Maximum results (default 20).

        Returns:
            List of entity dicts."""
        params: Dict[str, str] = {"limit": str(limit)}
        if query:
            params["q"] = query
        if entity_type:
            params["type"] = entity_type
        data = await self._connection.get(
            "/api/v1/entities", params=params
        )
        return data.get("entities", data) if isinstance(data, dict) else data

    async def list_entities(
        self,
        limit: int = 200,
        offset: int = 0,
    ) -> Dict[str, Any]:
        """Paginated walk of ALL entities for the tenant, ordered by id ASC.

        Unlike :meth:`search_entities` (keyword LIKE filter, limited match
        set), this walks every row with a stable cursor — pages never shift
        under concurrent inserts. Use for analytics, normalization sweeps,
        exports, or admin dashboards.

        Args:
            limit:     Page size (default 200, server-clamped to [1, 500]).
            offset:    0-based offset (default 0).

        Returns:
            Dict with ``entities``, ``count``, ``total``, ``limit``,
            ``offset``, ``has_more``, ``next_offset``."""
        if limit <= 0:
            limit = 200
        if limit > 500:
            limit = 500
        if offset < 0:
            offset = 0
        params = {"limit": str(limit), "offset": str(offset)}
        return await self._connection.get(
            "/api/v1/entities/list", params=params
        )

    async def upsert_entity(
        self,
        name: str,
        entity_type: str = "",
        summary: str = "",
        attributes: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        """Create or update a named entity (idempotent by name).

        Args:
            name:        Entity name (required).
            entity_type: Entity type (e.g. ``"person"``, ``"organization"``).
            summary:     Short description.
            attributes:  Arbitrary key-value metadata.

        Returns:
            Dict with entity ``id``."""
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
        """BFS traversal of entity relationships.

        Starting from an entity, discovers connected entities through
        typed edges (``works_at``, ``knows``, ``part_of``, etc.).

        Args:
            entity_id: The starting entity ID.
            depth:     How many hops to follow (default 2, max 5).

        Returns:
            Dict with ``entity``, ``nodes``, ``node_count``."""
        params: Dict[str, str] = {"depth": str(depth)}
        return await self._connection.get(
            f"/api/v1/entities/{entity_id}/graph",
            params=params,
                    )

    async def entity_graph(
        self,
        entity_id: int,
        depth: int = 2,
    ) -> Dict[str, Any]:
        """Alias of :meth:`get_entity_graph` using the canonical ``entity_graph``
        name (matches the MCP tool ``get_entity_graph`` exposed to the SDKs as
        ``entity_graph`` / Go ``EntityGraph`` / TS ``entityGraph``)."""
        return await self.get_entity_graph(entity_id, depth=depth)

    async def entity_timeline(
        self,
        entity_id: int,
    ) -> Dict[str, Any]:
        """Get the full temporal history of an entity's relationships.

        Shows ALL edges including invalidated ones, ordered by event time.
        Use to understand how an entity's context evolved over time.

        Args:
            entity_id: The entity ID.

        Returns:
            Dict with ``entity``, ``timeline``, ``record_ids``."""
        return await self._connection.get(
            f"/api/v1/entities/{entity_id}/timeline",
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
        """Create or update a typed relationship between two entities.

        Args:
            source_id:        Source entity ID.
            target_id:        Target entity ID.
            relation:         Relationship type (e.g. ``"works_at"``).
            event_time:       ISO 8601 timestamp when this became true.
            confidence:       Confidence score (0.0-1.0).
            source_record_id: Memory record that evidences this relationship.

        Returns:
            Confirmation dict."""
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
            Confirmation dict."""
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
    ) -> List[Dict[str, Any]]:
        """Get entities linked to a specific memory record.

        Args:
            record_id: The record ID.

        Returns:
            List of entity dicts."""
        data = await self._connection.get(
            f"/api/v1/records/{record_id}/entities",
                    )
        return data.get("entities", data) if isinstance(data, dict) else data

    # ── Caller-owned session writes ────────────────────────────────

    async def create_in_session(
        self,
        text: str,
        session_uuid: str,
    ) -> Dict[str, Any]:
        """Store ``text`` directly as an episodic record under ``session_uuid``,
        bypassing the auto-session assignment of the ingest path.

        Args:
            text:         Record text (stored in both summary and content).
            session_uuid: The session UUID to place the record under (required).

        Returns:
            Dict with ``session_id`` and ``records`` (the new episodic anchor)."""
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

    # ── Profile ────────────────────────────────────────────────────

    async def profile(
        self,
        container_tag: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Get the memory profile for a container tag (user/agent).

        Returns profile information including static facts, dynamic state,
        and aggregate statistics. If the server doesn't support profiles
        (OSS without agents), returns an empty profile rather than raising.

        Args:
            container_tag: User/agent identifier; ``None`` = this Memory's tag.

        Returns:
            Dict with ``static``, ``dynamic``, ``stats`` keys.

        Example::

            prof = await mem.profile()
            print(prof["static"])  # identity facts"""
        target_tag = container_tag if container_tag is not None else self._container_tag
        try:
            data = await self._connection.get(
                "/api/v1/profile",
                params={"tag": target_tag},
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
                    "tag": target_tag,
                    "status": "not_available",
                }
            raise

    async def forget(self, memory_id: Optional[int] = None) -> None:
        """Forget a specific memory or trigger cognitive decay.

        Not yet implemented — placeholder for the decay API.

        Args:
            memory_id: If provided, forget this specific memory.

        Raises:
            NotImplementedError: Always (until the API is available)."""
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
        session_id: str = "",
    ) -> Optional[Dict[str, Any]]:
        """Attempt cloud ingest at ``/api/v1/ingest``.

        Returns None if the endpoint doesn't exist (404), allowing the
        caller to fall back to direct record creation."""
        payload = {"content": text, "container_tag": self._container_tag}
        if session_id:
            payload["session_id"] = session_id

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
        """Create a record directly via ``POST /api/v1/records``.

        This is the only write path that persists ``score`` and ``type``
        (the ingest endpoint drops them — see ``add()``). Without server-side
        embedding, text is stored in both ``summary`` (for keyword search) and
        ``content`` (for full retrieval)."""
        summary = text[:200] + "..." if len(text) > 200 else text

        effective_score = 5 if score is None else score
        effective_type = MemoryType.EPISODIC if mem_type is None else mem_type

        # longer probes/creates a session anchor before writing. It issues ONE
        # request and sends NO synthetic related_ids link; the server auto-links
        # the REAL episodic anchor (server auto-link on create),
        # Rule 3a). On a session that genuinely has no episodic yet, the server
        # returns an honest HTTP 422 that surfaces to the caller as a typed
        # AnhurQueryError (write the episodic first). Fabricating an anchor from
        req = CreateRequest(
            uuid=self._session_uuid,
            type=effective_type,
            summary=summary,
            content=text,
            score=effective_score,
            weight=effective_score / 10,
            metadata=_build_metadata_json(self._container_tag, metadata),
        )

        data = await self._connection.post(
            "/api/v1/records",
            req.model_dump(exclude_none=True),
        )

        return {
            "session_id": self._session_uuid,
            "records": [{"id": data.get("id", 0), "type": effective_type.value,
                          "summary": summary}],
            "mode": "oss",
        }

    @staticmethod
    def _flatten_search_results(data: Any) -> List[Dict[str, Any]]:
        """Flatten nested search response into simple dicts."""
        results = []
        for item in (data.get("results", []) if isinstance(data, dict) else []):
            record_fields = item.get("record", {}) if isinstance(item, dict) else {}
            results.append({
                "id": record_fields.get("id", 0),
                "type": record_fields.get("type", ""),
                "summary": record_fields.get("summary", ""),
                "score": item.get("similarity", 0),
                "metadata": record_fields.get("metadata"),
                "content": record_fields.get("content"),
            })
        return results

    def __repr__(self) -> str:
        return (
            f"Memory(container_tag={self._container_tag!r}, "
            f"session={self._session_uuid!r})"
        )


# ---------------------------------------------------------------------------
# AnhurClient — DEPRECATED back-compat alias (thin subclass of Memory)
# ---------------------------------------------------------------------------

class AnhurClient(Memory):
    """Deprecated: use :class:`Memory` instead.

    Historically AnhurDB shipped two clients — a thin ``Memory`` facade and a
    full ``AnhurClient``. Per the canonical parity spec (PARITY_SPEC.md) the two
    collapsed into a SINGLE ``Memory`` class that carries every method. This
    subclass is kept ONLY so existing imports (``from anhurdb import
    AnhurClient``) keep working unchanged.

    The single behavioural difference it preserves is the historical default
    ``url``: the old ``AnhurClient`` defaulted to ``http://localhost:8080``
    (self-hosted), whereas ``Memory`` defaults to the cloud endpoint. New code
    should construct ``Memory(url=...)`` explicitly."""

    def __init__(
        self,
        url: str = _LEGACY_LOCAL_URL,
        api_key: Optional[str] = None,
        tenant_id: str = "",
        mode: str = "rest",
    ):
        # (not an error) so the thousands of existing AnhurClient(...) call sites
        # verbatim from Memory; we only re-order kwargs to match the OLD
        # AnhurClient signature (url first) and swap in the localhost default.
        warnings.warn(
            "AnhurClient is deprecated; use Memory (it now carries the full "
            "API surface). AnhurClient remains as a thin alias only.",
            DeprecationWarning,
            stacklevel=2,
        )
        super().__init__(
            api_key=api_key,
            url=url,
            tenant_id=tenant_id,
            mode=mode,
        )
