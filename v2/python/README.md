# AnhurDB Python SDK

Official async Python client for [AnhurDB](https://anhur.yoven.ai) — cognitive memory for AI agents.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point the SDK at `https://anhurdb.yoven.ai` (default).

## Features

- **One client class**: `Memory` — dead-simple to start (`add`/`search`/`profile`) and carries the full 40+ endpoint surface. (`AnhurClient` remains as a deprecated alias for back-compat.)
- Full async support (aiohttp)
- Type-safe models (Pydantic v2)
- Fluent Query Builder (AST-based DSL for advanced filtering)
- Entity knowledge graph (search, upsert, relationships, timeline)
- Batch operations (the server caps a batch at 1000 ids)
- File upload with async ingestion (PDF, images, DOCX, etc.)
- Temporal versioning (supersede old facts)
- REST direct transport (the supported path — see [Transport Modes](#transport-modes))
- Session management with auto-generated container tags
- PEP 561 typed: ships `py.typed`, so the annotations are visible to your own mypy/pyright
- `anhurdb.__version__` reports the installed version — the same string the `User-Agent` sends

## Install

Wheels ship on [GitHub Releases](https://github.com/Yoven/AnhurDB-SDK/releases) (`v2/python/vX.Y.Z`).

```bash
pip install \
  https://github.com/Yoven/AnhurDB-SDK/releases/download/v2/python/v2.0.20/anhurdb-2.0.20-py3-none-any.whl
```

> The pin above is the newest wheel actually **published**. The source in this
> repository is already at `3.0.0` (`anhurdb.__version__`); the pin moves when the
> 3.0.0 release is cut, not before — a doc that pins a version nobody can download
> is worse than a stale one. (It was stale at `v2.0.12` — eight releases behind —
> until 2026-09-05.)
>
> **3.0.0 is a breaking release.** `create()` changed arity, twelve responses
> became typed models instead of dicts, and `get_entity_graph()` now defaults to
> the server's own depth of 1 instead of 2. Read `CHANGELOG.md` before upgrading.

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
    # A hit is a SearchResult model, NOT a dict: the record lives under
    # `.record` and the relevance under `.similarity`. Subscripting it
    # (`r['summary']`) raises TypeError — this snippet shipped broken until
    # 2026-09-13.
    for r in results:
        print(f"{r.record.summary} (similarity: {r.similarity:.2f})")

    # Get user profile — a ProfileResult model since 3.0.0, not a dict.
    profile = await mem.profile()
    print(profile.static.facts)
```

## Quick Start — AnhurClient (Full API)

```python
from anhurdb import AnhurClient, MemoryType, sessions_all

async with AnhurClient(api_key="anhur_xxx") as client:
    # Create a record. Since 3.0.0 the session and the content are required
    # POSITIONALS — POST /api/v1/records cannot succeed without either, so the
    # failure belongs in the signature, not in an HTTP 422.
    await client.create(
        "session-1",
        "Full conversation context here...",
        type=MemoryType.FACT,
        score=8,
    )

    # Search
    results = await client.search("data scientist", sessions_all(), limit=10)

    # Entity knowledge graph
    entity = await client.upsert_entity("Google", entity_type="organization")
    # depth omitted = the SERVER's default of 1. Pass depth=2 to widen it.
    graph = await client.get_entity_graph(entity.id)
    timeline = await client.entity_timeline(entity.id)

    # Batch operations
    contents = await client.batch_read_content([1, 2, 3])

    # File upload
    with open("report.pdf", "rb") as f:
        pdf_bytes = f.read()
    upload = await client.upload_file("report.pdf", pdf_bytes)
    status = await client.upload_status(upload.record_id)

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
    timeout=30.0,              # Per-request budget in SECONDS (default 30.0)
)
```

`timeout` is the whole wall clock a call gets — uploads included — because this
SDK performs **no client-side retry**. It is a constructor parameter and
deliberately not an environment variable: the right budget belongs to the
caller (a chat turn can wait 5 s; a 200 MB upload cannot), not to the machine.

### Core Methods

| Method | Description | Returns |
|--------|-------------|---------|
| `add(text, *, mode="ingest", session_id="", score=None, type=None, metadata=None)` | Store a memory. Keyword-only after `text`. Pinning `score` / `type` / `metadata` switches from `/ingest` to `/records` | `dict` with session_id, records, mode |
| `search(query, sessions, *, limit=10, type_filter=None, scope="sessions", mode=None, semantic_timeout_ms=None, debug_signals=False)` | Hybrid plane search (query → FTS `text`; prefer `smart_search` for conceptual RAG). `sessions` is **required** and positional: `sessions_all()` or up to 1000 uuids. Absent/empty/`["*", "uuid"]` → HTTP 400. See [Search controls](#search-controls-adr-0031) for the three retrieval knobs | `list[SearchResult]` |
| `profile(container_tag=None)` | Get the memory profile for a container tag. The tag is an IN-TENANT filter, never a tenant selector; an unknown tag is an EMPTY profile with HTTP 200, not a 404. An empty string is refused locally (guaranteed HTTP 400) | `ProfileResult` |

### Search & Discovery

| Method | Description |
|--------|-------------|
| `search_by_type(type, sessions, limit=20)` | Type filter in tenant store only — not a Shared Data plane switch |
| `smart_search(query, sessions, *, limit=10, memory_type=None, scope="sessions")` | Full-text + cognitive weight (prefer for conceptual text). `memory_type` is sent as `?type=`. Returns `SmartSearchResponse`, NOT `list[SearchResult]` — `results` is `None` (not `[]`) when nothing matched, and `relevance` is a LEXICAL score that must never be compared with `SearchResult.similarity` |
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
| `entity_graph(entity_id, depth=None)` | BFS entity relationship traversal. `depth=None` omits the parameter and lets the server apply its own default of **1** (it was 2 before 3.0.0) |
| `entity_timeline(entity_id)` | Temporal history of relationships |
| `upsert_entity_edge(src, dst, relation)` | Create/update typed relationship |
| `link_record_entity(record_id, entity_id)` | Cross-layer link |
| `get_record_entities(record_id)` | Entities linked to a record |

### Batch Operations

| Method | Description |
|--------|-------------|
| `batch_read_content(ids)` | Fetch content for many records in one call. The SDK does not cap the list; the **server** rejects more than **1000** ids with HTTP 400 `batch size exceeds maximum` (`server/handler/record_batch.go`, `maxBatchSize = 1000`, matched by gRPC `batch_service.go`) |
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
| `update(record_id, **fields)` | Partial update — **raises on `score`** (see below) |
| `set_score(record_id, score)` | Set importance 1-10 durably (`POST /records/set-score`) |
| `delete(record_id)` | **Soft delete** — archives the record (`archived=1`, `status='deleted'`); it disappears from reads but is not erased |

### Session Management

| Method | Description |
|--------|-------------|
| `create_session()` | Register a write session (`POST /api/v1/sessions`); omit id → server generates |
| `open_session()` | Local generate + register (new_session + create_session) |
| `new_session()` | Local id only — does **not** register |
| `list_sessions()` | All sessions with stats, paged to exhaustion → `list[SessionStats]` |
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

### Two things `QueryBuilder` refuses that the wire would accept

| Refused | Why it is not left to the server |
|---|---|
| `None` as an operand (`where(status=None)`, `where(weight__gt=None)`, a `None` inside `$in`) | The server takes null as a scalar and answers **HTTP 200 with zero rows**, on every input, forever — `col = NULL` is NULL, never true. And the grammar has no `$exists`, no `$ne` and no `IS NULL`, so the intent behind it cannot be expressed here at all. Filter on a real value, or test for null yourself after the rows come back |
| `$in []` (`where(type__in=[])`) | The server's answer is a fixed 400 the SDK already knows the text of, so the round trip buys nothing |

`QueryBuilder.semantic_search()` **now raises**. The server accepted the key and skipped
it, so the method returned a plain filter query while its name promised ranking — the
one failure mode with no symptom. Use `search()` / `POST /api/v1/search`. The pseudo-key
itself is still reachable by hand through `Filter` for anyone probing the server.

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

Query rejections are **one type on both sides of the wire**. A bad column, a
`None` operand, an empty `$in`, a bad sort direction, an out-of-range `limit`, a
non-AST argument and `execute()` without an executor all raise `AnhurQueryError`
with `kind == "invalid_request"` — the same class and kind the server's own HTTP
400 arrives as. `status_code` is what still tells you which side caught it:

```python
try:
    records = await mem.query(QueryBuilder().where(type="risk").limit(20))
except AnhurQueryError as bad_query:
    if bad_query.status_code is None:
        print(f"the SDK refused to send this: {bad_query}")
    else:
        print(f"the server rejected it (HTTP {bad_query.status_code}): {bad_query}")
```

Before 2.1.0 the client-side half raised `ValueError` / `TypeError` /
`RuntimeError` instead, so a caller who wrote only the obvious `except
AnhurQueryError` silently missed it.

## Search controls (ADR-0031)

Three opt-in knobs on `search()` and `search_with_retrieval()`. All three are
omitted from the request entirely when you do not set them, so the server's own
defaults apply exactly as before they existed.

| Knob | Values | Meaning |
|------|--------|---------|
| `mode` | `"fast"` \| `"balanced"` \| `"semantic"` | Retrieval budget. `None` (default) = the server's default, `balanced`. An unknown value is refused by the SDK **before** the request — the server normalises unknown modes to `balanced` on purpose, so it can never report your typo back to you |
| `semantic_timeout_ms` | `int >= 0` | Caps the Embed+HNSW wait. `None`/`0` = the server default (700 ms). Negative is refused |
| `debug_signals` | `bool` | Attaches the per-hit `SearchHitSignals` block (13 fields) and, on `search_with_retrieval()`, the `leg_scores` array |

```python
response = await mem.search_with_retrieval(
    "quarterly revenue risk",
    sessions_all(),
    mode="semantic",
    semantic_timeout_ms=1500,
    debug_signals=True,
)
print(response.retrieval.mode)          # what the server actually ran
print(response.leg_scores)              # per-leg score distributions
print(response.results[0].signals.hnsw_rank)
```

### `mode="semantic"` against an older server

`mode` is an additive wire field: a server that predates ADR-0031 does not know it,
drops it, runs `balanced`, and answers **HTTP 200 with lexical results** — while you
believe you asked for strict semantic retrieval. The SDK detects this from the
**response** (a current server always fills `retrieval.mode`) and **raises**
`AnhurError` with a `SERVER_TOO_OLD:` message rather than handing you results that
quietly are not what you asked for.

`semantic_timeout_ms` and `debug_signals` only cost you a budget or some debug
detail, so those raise a `RuntimeWarning` instead.

One blind spot, stated plainly: with `scope="shared_all"` a **current** server also
returns an empty `retrieval.mode` (a two-plane fan-out has no single honest mode to
report), so the check cannot run there. `mode="semantic"` + `scope="shared_all"`
warns that it could not be verified.

## Transport Modes

- **REST direct** (default): Calls AnhurDB REST endpoints directly. **Use this.**
- **MCP tunnel** (`mode="mcp"`): legacy/alternative transport. It rewrites exactly two
  paths — `POST /api/v1/records` → MCP tool `create_memory`, and `POST /api/v1/query`
  → MCP tool `query` — and posts them to `/api/v1/mcp/direct`. Every other call
  falls through to plain REST.

> ⚠️ **`mode="mcp"` is unusable against a normal deployment.** Corrected 2026-09-05:
> an earlier version of this note blamed the retired `execute_ast` tool. That cause was
> false — the SDK maps `/api/v1/query` to the live `query` tool
> (`client/connection.py`, `_MCP_TOOL_MAP`), and `tests/test_mcp_tunnel.py` pins it.
> The real blocker is the ENDPOINT: `/api/v1/mcp/direct` is registered only on the MCP
> server's metrics listener (`ANHUR_MCP_METRICS_PORT`, default 9092), never on the
> data-plane port that this SDK's `url` points at, so the tunnel 404s unless a
> deployment proxies it. Second, smaller trap: the 22-tool schemas are strict
> (`additionalProperties: false`), so any argument the tool does not declare is
> rejected server-side.
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

### Score is not writable through `update`

`PATCH /api/v1/records/{id}` has no `score` field. It answers **200 and drops
the key** — so `update(id, score=8)` used to report success and change nothing.
Measured 2026-08-15; the same shape as an earlier defect with `archived`.

Use `set_score` / `SetScore` / `setScore`, which posts to
`POST /api/v1/records/set-score` — a replicated command that also invalidates
the read cache. `update` now **raises** if given `score` rather than dropping
it, because a silent no-op is worse than an error.

Note that `add`/`create` **can** pin a score at write time; only changing it
afterwards needs the dedicated route. And a corrected score reaches ranking on
the next maintenance pass, not instantly: it feeds the value term of the
cognitive weight, which selects the record's embedding fidelity band.
