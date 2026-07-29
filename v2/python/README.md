# AnhurDB Python SDK

Official async Python client for [AnhurDB](https://anhur.yoven.ai) — cognitive memory for AI agents.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point the SDK at `https://anhurdb.yoven.ai` (default).

## Features

- **One client class**: `Memory` — dead-simple to start (`add`/`search`/`profile`) and carries the full 40+ endpoint surface. (`AnhurClient` remains as a deprecated alias for back-compat.)
- Full async support (aiohttp)
- Type-safe models (Pydantic v2)
- Fluent Query Builder (AST-based DSL for advanced filtering)
- Entity knowledge graph (search, upsert, relationships, timeline)
- Batch operations (read/update up to 100 records at once)
- File upload with async ingestion (PDF, images, DOCX, etc.)
- Temporal versioning (supersede old facts)
- REST direct transport (the supported path — see [Transport Modes](#transport-modes))
- Session management with auto-generated container tags

## Install

Wheels ship on [GitHub Releases](https://github.com/Yoven/AnhurDB-SDK/releases) (`v2/python/vX.Y.Z`).

```bash
pip install \
  https://github.com/Yoven/AnhurDB-SDK/releases/download/v2/python/v2.0.12/anhurdb-2.0.12-py3-none-any.whl
```

## Quick Start — Memory (Simple API)

```python
from anhurdb import Memory, sessions_all

async with Memory(api_key="anhur_xxx", url="https://anhurdb.yoven.ai") as mem:
    # 1) Register a write session (required before ingest/create)
    session_id = await mem.create_session()
    # 2) add(mode="ingest") → /ingest: episodic + platform extraction (LLM billed)
    #    create() → /records: one typed record, no extraction LLM
    await mem.add(
        "I'm a data scientist at Google working on NLP",
        mode="ingest",
        session_id=session_id,
    )

    # Search — `sessions` is mandatory: sessions_all() is every session in
    # scope, or pass [session_id] to confine the query to one chat (ADR-0014).
    results = await mem.search("what does this user do?", sessions_all())
    for r in results:
        print(f"{r['summary']} (score: {r['score']:.2f})")

    # Get user profile
    profile = await mem.profile()
    print(profile["static"])
```

## Quick Start — AnhurClient (Full API)

```python
from anhurdb import AnhurClient, CreateRequest, MemoryType, sessions_all

async with AnhurClient(api_key="anhur_xxx") as client:
    # Create a record
    await client.create(CreateRequest(
        uuid="session-1",
        type=MemoryType.FACT,
        summary="User is a data scientist",
        content="Full conversation context here...",
        score=8,
    ))

    # Search
    results = await client.search("data scientist", sessions_all(), limit=10)

    # Entity knowledge graph
    entity = await client.upsert_entity("Google", entity_type="organization")
    graph = await client.get_entity_graph(entity["id"], depth=2)
    timeline = await client.entity_timeline(entity["id"])

    # Batch operations
    contents = await client.batch_read_content([1, 2, 3])

    # File upload
    with open("report.pdf", "rb") as f:
        pdf_bytes = f.read()
    upload = await client.upload_file("report.pdf", pdf_bytes)
    status = await client.upload_status(upload["record_id"])

    # Temporal versioning
    await client.supersede(old_id=42, new_id=99)
```

## API Reference — Memory Class

### Constructor

```python
Memory(
    api_key="anhur_xxx",       # Required (or set ANHUR_API_KEY env)
    url="https://anhurdb.yoven.ai",  # Open Beta data plane (default)
    user_id="user-123",        # Optional explicit container tag
    tenant_id="tenant-a",      # Optional multi-tenant header
    mode="rest",               # "rest" (default) or "mcp" (tunnel)
)
```

### Core Methods

| Method | Description | Returns |
|--------|-------------|---------|
| `add(text, *, mode="ingest", session_id="", score=None, type=None, metadata=None)` | Store a memory. Keyword-only after `text`. Pinning `score` / `type` / `metadata` switches from `/ingest` to `/records` | `dict` with session_id, records, mode |
| `search(query, sessions, *, limit=10, type_filter=None, scope="sessions")` | Hybrid plane search (query → FTS `text`; prefer `smart_search` for conceptual RAG). `sessions` is **required** and positional: `sessions_all()` or up to 1000 uuids. Absent/empty/`["*", "uuid"]` → HTTP 400 | `list[SearchResult]` |
| `profile()` | Get user/agent memory profile | `dict` with static, dynamic, stats |

### Search & Discovery

| Method | Description |
|--------|-------------|
| `search_by_type(type, sessions, limit=20)` | Type filter in tenant store only — not a Shared Data plane switch |
| `smart_search(query, sessions, limit=10, scope="sessions")` | Full-text + cognitive weight (prefer for conceptual text) |
| `recall(query, sessions, limit=10)` | Same engine as `search`, MCP naming |
| `recent(limit=20)` | Most recent records |

### Graph Traversal

| Method | Description |
|--------|-------------|
| `walk(start_id, depth=3)` | BFS graph traversal |
| `walk_semantic(start_id, depth=3)` | Vector-weighted semantic walk |
| `get_context(record_id)` | Record + 1-hop neighbors |
| `read_content(record_id)` | Full content payload |

### Entity Knowledge Graph

> **Entity ≠ record type.** `record.type` (`episodic`, `fact`, …) classifies the memory node.
> Entities (`person`, `organization`, …) are Layer 2 for cross-cutting search; `link_record_entity` is the tag.

| Method | Description |
|--------|-------------|
| `search_entities(query, entity_type, limit)` | Search named entities |
| `upsert_entity(name, entity_type, summary)` | Create/update entity |
| `entity_graph(entity_id, depth)` | BFS entity relationship traversal |
| `entity_timeline(entity_id)` | Temporal history of relationships |
| `upsert_entity_edge(src, dst, relation)` | Create/update typed relationship |
| `link_record_entity(record_id, entity_id)` | Cross-layer link |
| `get_record_entities(record_id)` | Entities linked to a record |

### Batch Operations

| Method | Description |
|--------|-------------|
| `batch_read_content(ids)` | Fetch content for up to 100 records |
| `batch_update_status(ids)` | Mark records as consolidated (was `mark_consolidated`, now a deprecated alias) |
| `link_consolidated(ids, consolidate_id)` | Link children to a consolidated star (was `link_to_consolidated`/`update_consolidate_ids`, now deprecated aliases) |

### File Upload

| Method | Description |
|--------|-------------|
| `upload_file(filename, content)` | Upload document for async ingestion |
| `upload_status(upload_id)` | Poll file ingestion status |

### Temporal Versioning

| Method | Description |
|--------|-------------|
| `supersede(old_id, new_id)` | Mark old record as superseded |

### Record CRUD

| Method | Description |
|--------|-------------|
| `update(record_id, **fields)` | Partial update |
| `delete(record_id)` | **Soft delete** — archives the record (`archived=1`, `status='deleted'`); it disappears from reads but is not erased |

### Session Management

| Method | Description |
|--------|-------------|
| `create_session()` | Register a write session (`POST /api/v1/sessions`); omit id → server generates |
| `open_session()` | Local generate + register (new_session + create_session) |
| `new_session()` | Local id only — does **not** register |
| `list_sessions()` | All sessions with stats |
| `get_session_history(uuid, limit, offset)` | Paginated session history |
| `get_session_clusters(uuid)` | Thematic clusters |

### Properties

| Property | Description |
|----------|-------------|
| `session_id` | Current session UUID |
| `container_tag` | Recall/profile aggregation tag |

## API Reference — full surface (on `Memory`)

`Memory` exposes the public AnhurDB surface (see `v2/PARITY_SPEC.md`):

- **CRUD**: `create`, `get`, `read_content`, `get_context`, `get_grounding`, `update`, `delete`
- **Search**: `search`, `search_session`, `search_by_type`, `smart_search`, `recall`, `query` (`search_with_ast` deprecated)
- **Manifests / taxonomy**: `manifest_global`, `manifest_session`, `list_chat`, `count_by_type`, `list_types`, `recent`
- **Batch**: `batch_read_content`, `batch_update_status`, `link_consolidated`, `append_main_ids`, `append_related_ids`
- **Graph**: `walk`, `walk_semantic`
- **Entity**: `search_entities`, `upsert_entity`, `entity_graph`, `entity_timeline`, `upsert_entity_edge`, `link_record_entity`, `get_record_entities`
- **Upload**: `upload_file`, `upload_status`
- **Temporal**: `supersede`
- **Session**: `create_session`, `open_session`, `new_session`, `list_sessions`, `get_session_history`, `get_session_clusters`
- **Profile**: `profile`

## Query Builder

The Python SDK includes a fluent query builder for advanced filtering. Execute via `Memory.query()` (canonical across all SDKs):

```python
from anhurdb import Memory
from anhurdb.query import QueryBuilder, Filter

async with Memory(api_key="anhur_xxx") as mem:
    # Fluent builder with Django-style kwargs
    qb = QueryBuilder()
    qb.where(type="risk", score__gte=7).order_by("weight", "desc").limit(10)
    records = await mem.query(qb)

    # Scope to a specific session
    records = await mem.query(qb, session_uuid="session-123")

    # Filter shorthand for simple cases
    records = await mem.query(
        Filter({"type": {"$eq": "risk"}, "score": {"$gt": 7}}),
    )
```

> **Deprecated:** `search_with_ast()` still works but emits a `DeprecationWarning`. Use `query()` instead.

Supported operators: `$eq`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`. There are no others —
`$neq`, `$nin` and `$like` were never implemented server-side.

`QueryBuilder` validates the column name and the operator suffix locally against the
server whitelist (`id`, `uuid`, `type`, `dimension`, `weight`, `score`, `status`,
`consolidated`, `archived`, `created_at`, `updated_at`, `prefix`, `metadata`,
`summary`, `superseded_by`, `valid_from`, `valid_until`), so a typo raises `ValueError`
before a request is sent.

### Server-side validation tightened on 2026-07-28

`POST /api/v1/query` used to **drop** malformed filters and answer 200 with an
unfiltered listing. It now answers **400** with a message naming the offending field or
operator:

| Input | Now |
|-------|-----|
| unknown operator (`{"type": {"$like": "..."}}`) | 400, names `$like` and lists the supported set |
| bare value (`{"type": "episodic"}`) | 400 — must be an operator object |
| empty operator object (`{"type": {}}`) | 400 — `has no operator` |
| empty `$in` (`{"type": {"$in": []}}`) | 400 — `$in requires a non-empty array of values` |
| `filters` as an array of `{field, op, value}` | 400 — stable `invalid json ast` prefix |
| row-scan failure mid-page | 500, no records (was 200 with a truncated page) |

`QueryBuilder` cannot emit any of those through its fluent API. **`Filter` can** — it
copies the dict you hand it verbatim with no validation, so `Filter({"type": {}})` and
`Filter({"type": {"$in": []}})` now raise `AnhurQueryError` instead of quietly
returning everything. Audit any hand-built `Filter` payload before upgrading.

Still accepted and ignored (do not read these as fixed): `select` is a no-op (every hit
is a full record), an unrecognised sort order silently falls back to `DESC`, `limit` is
clamped to 1000 without an echo in the response, and `filters["semantic_search"]` is
skipped — it is answered by `search()`, never by this endpoint.

## Error Handling

```python
from anhurdb import AnhurError, AnhurAuthError, AnhurQueryError, AnhurConnectionError

try:
    session_id = await mem.create_session()
    await mem.add("something", mode="ingest", session_id=session_id)
except ValueError as e:
    print(f"Client contract: {e}")  # e.g. missing create_session
except AnhurAuthError:
    print("Invalid API key")
except AnhurConnectionError:
    print("Server unreachable")
except AnhurQueryError as e:
    print(f"Bad request: {e}")
```

## Transport Modes

- **REST direct** (default): Calls AnhurDB REST endpoints directly. **Use this.**
- **MCP tunnel** (`mode="mcp"`): legacy/alternative transport. It rewrites exactly two
  paths — `POST /api/v1/records` → MCP tool `create_memory`, and `POST /api/v1/query`
  → MCP tool `execute_ast` — and posts them to `/api/v1/mcp/direct`. Every other call
  falls through to plain REST.

> ⚠️ **`mode="mcp"` is broken for `query()` as of 2026-07-28.** The MCP surface was cut
> from 47 tools to 22 and `execute_ast` no longer exists — it was absorbed into
> `query(ast=)`. `/api/v1/mcp/direct` dispatches by tool name against the registered
> set, so the tunnel gets **HTTP 404 / `Tool not found`**. The tunnel's `create_memory`
> mapping still names a live tool, but the 22-tool schemas are strict
> (`additionalProperties: false`, `api_key` declared as a required argument), so a REST
> record body forwarded verbatim may still be rejected as an undeclared argument.
>
> `/api/v1/mcp/direct` is also served only on the MCP server's metrics listener
> (`ANHUR_MCP_METRICS_PORT`, default 9092), not on the data-plane port the SDK's `url`
> points at — a deployment has to proxy it for the tunnel to be reachable at all.
>
> **Use `mode="rest"` (the default).** The REST surface is unaffected by the MCP cut.

```python
# REST direct — the supported transport
async with Memory(api_key="key") as mem:
    session_id = await mem.create_session()
    await mem.add("text", mode="ingest", session_id=session_id)
```

## License

MIT
