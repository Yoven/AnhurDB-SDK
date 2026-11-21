# AnhurDB Python SDK

Official async Python client for [AnhurDB](https://anhur.yoven.ai) — cognitive memory for AI agents.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point the SDK at `https://anhurdb.yoven.ai` (default).

## Features

- **One client class**: `Memory` — dead-simple to start (`add`/`search`/`profile`) and carries the full 40+ endpoint surface. (`AnhurClient`, its deprecated alias, was removed in 4.0.0 — use `Memory`.)
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
  https://github.com/Yoven/AnhurDB-SDK/releases/download/v2/python/v4.0.0/anhurdb-4.0.0-py3-none-any.whl
```

> The pin above matches `anhurdb.__version__` in this tree (`4.0.0`). Go and
> TypeScript ship the same number. Until the `v2/python/v4.0.0` release is
> published, the newest published wheel is `v2/python/v3.0.1`.
>
> **4.0.0 is a breaking release (legacy removal).** `AnhurClient`,
> `search_with_ast`, `mark_consolidated`, `link_to_consolidated`,
> `update_consolidate_ids`, `CreateRequest(uuid=)`, `DeleteFileResult.raft_index`,
> `MemoryStatus.PENDING` and every old-server fallback are gone, and
> `AddResult.mode` is now `"ingest"` / `"records"`. See the migration table in
> `CHANGELOG.md`.
>
> **3.0.x is a breaking release.** `create()` changed arity, twelve responses
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

## Quick Start — Memory (Full API)

```python
from anhurdb import Memory, MemoryType, sessions_all

async with Memory(api_key="anhur_xxx") as client:
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
    # upload.scope = the plane it landed in (tenant_shared by default)
    status = await client.upload_status(upload.record_id, scope=upload.scope)

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
    tenant_id="tenant-a",      # Optional X-Tenant-ID: [A-Za-z0-9_-], <= 64, else AnhurQueryError
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
| `smart_search(query, sessions, *, limit=10, memory_type=None, scope="sessions")` | **Lexical only** — full-text relevance × cognitive weight, no embedding. Use it for keywords and identifiers; for conceptual queries use `search()`. `memory_type` is sent as `?type=`. Returns `SmartSearchResponse`, NOT `list[SearchResult]` — `results` is `None` (not `[]`) when nothing matched, and `relevance` is a LEXICAL score that must never be compared with `SearchResult.similarity` |
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
| `batch_update_status(ids, status)` | Set one status on many records (replaces `mark_consolidated`, removed in 4.0.0: pass `"consolidated"`) |
| `link_consolidated(ids, consolidate_id)` | Link children to a consolidated star (replaces `link_to_consolidated`/`update_consolidate_ids`, removed in 4.0.0) |

### File Upload

| Method | Description |
|--------|-------------|
| `upload_file(filename, content)` | Upload document for async ingestion |
| `upload_status(upload_id, scope=upload.scope)` | Poll file ingestion status in the plane the file landed in |
| `wait_for_upload(upload_id, scope=upload.scope)` | Poll until terminal |

`upload_file` fills `UploadResult.scope` (`tenant_shared` by default,
`client_shared`, or `sessions` for chat). Without `scope` the server looks in
every plane you can upload to and raises `AnhurUploadStatusAmbiguousError`
(HTTP 409) when the id is a file in more than one — record ids are per store.

### Temporal Versioning

| Method | Description |
|--------|-------------|
| `supersede(old_id, new_id)` | Mark old record as superseded |

### Record CRUD

| Method | Description |
|--------|-------------|
| `update(record_id, **fields)` | Partial update — **raises on `score`** (see below). `metadata` is an RFC 7396 merge patch (AnhurDB ADR-0035): a JSON `str` or a `dict`; `None` deletes a key, keys you do not name stay, nested objects merge, lists are replaced; a non-object patch is refused with 400 |
| `set_score(record_id, score)` | Set importance 1-10 durably (`POST /records/set-score`) |
| `delete(record_id)` | **Soft delete** — archives the record (`archived=1`, `status='deleted'`); it disappears from reads but is not erased |

### By-id scope (records from a shared plane, ADR-0029)

A record id is a rowid **per store**. An id that came from a shared-plane
search or upload must be read and changed in THAT store, or the call addresses
an unrelated record with the same number in your chat store. Every by-id
method takes a keyword-only `scope=`: `get`, `read_content`, `get_context`,
`get_grounding`, `batch_read_content`, `update`, `delete`, `supersede`,
`upload_status`, `wait_for_upload`. `None` = your own store (unchanged).

| `scope` | Store |
|---|---|
| `"sessions"` | your chat store, shared-* rows excluded |
| `"tenant_shared"` | tenant library `{c}_{t}_lib` (uploads after the L2 switch) |
| `"tenant_shared_legacy"` | pre-migration library rows in `{c}_{t}` |
| `"client_shared"` | client library `{c}_shared` |

```python
from anhurdb import sessions_all, tenant_shared_scope_for_hit

hits = await mem.search("refund policy", sessions_all(), scope="tenant_shared")
hit = hits[0].record
# one tenant_shared search reads BOTH library stores: pick the store per hit
body = await mem.read_content(hit.id, scope=tenant_shared_scope_for_hit(hit.uuid))
```

`shared_all` (or any unknown value) is refused locally (`AnhurQueryError`,
`invalid_request`). A row outside the named plane is HTTP 404, exactly like a
missing id; `batch_read_content` omits it.

`delete_file(session, prefix, scope="tenant_shared")` deletes in both library
stores (the session may be empty) and fills `DeleteFileResult.legs`;
`scope="client_shared"` targets the client library. A failure in one library
store raises `AnhurLibraryDeletePartialError` with `.legs` (every leg); retrying
the same call is safe (idempotent).

When nothing matched (`matched_count == 0`), `DeleteFileResult.alternative_sessions`
lists the sessions of the same store that DO hold that prefix
(`FileSessionCandidate(session_uuid, matched_count, root_checksum, root_filename)`)
— check `root_checksum` and re-issue `delete_file` with the right session.
`purge` tells whether the delete is final (True, shared plane) or a restorable
tombstone. There is no `raft_index` (removed in 4.0.0 — the server never sends it).

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
- **Search**: `search`, `search_session`, `search_by_type`, `smart_search`, `recall`, `query`
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

> `search_with_ast()` was removed in 4.0.0 — call `query()`; it takes the same arguments.

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

## Client key API (child clients, agent profile, impersonation — ADR-0034)

`ClientAPI` takes a **client key** (kind=client), never a tenant key. It talks
only to `/api/v1/client/*` and **never sends `X-Tenant-ID`** (the tenant
travels in the body).

```python
from anhurdb import AgentProfile, ClientAPI, KeyScope

async with ClientAPI(client_key, url="http://localhost:8000") as api:
    child = await api.create_child(account_ref)          # created=True once, then False with the same id
    await api.create_child_tenant(child.child_client_id, "contabilidade",
                                  profile=AgentProfile(config={"search_top_k": "8"}, max_rpm=1200))
    token = await api.impersonate_child(child.child_client_id, "contabilidade",
                                        scope=KeyScope.WRITE, l1_only=True, ttl_seconds=3600)
    # token.api_key, token.key_id, token.scope, token.l1_only, token.expires_at
    await api.revoke_impersonation(token.key_id)
```

| Method | Route |
|---|---|
| `create_child(external_ref)` | `POST /api/v1/client/children` |
| `list_children()` | `GET /api/v1/client/children` |
| `suspend_child(child_id)` / `resume_child(child_id)` | `POST /api/v1/client/children/{child_id}/suspend` / `resume` |
| `list_tenants()` / `list_child_tenants(child_id)` | `GET /api/v1/client/tenants` / `GET /api/v1/client/children/{child_id}/tenants` |
| `create_tenant(tenant_id, description, profile)` / `create_child_tenant(child_id, …)` | `POST /api/v1/client/tenants` / `POST /api/v1/client/children/{child_id}/tenants` |
| `impersonate(tenant_id, scope, l1_only, description, ttl_seconds)` / `impersonate_child(child_id, …)` | `POST /api/v1/client/impersonate` / `POST /api/v1/client/children/{child_id}/impersonate` |
| `revoke_impersonation(key_id)` | `POST /api/v1/client/impersonate/revoke` |

**Trust the echo, not the request.** The server lowers a scope above the
client key's ceiling without an error, so always read `token.scope` and
`token.l1_only`. The SDK fails closed when the echo is WEAKER than requested:
`AnhurClientAPIResponseError` with `code` `l1_only_not_honoured` or
`scope_not_honoured` (and `key_id` of the minted token, so you can revoke it).
A lower scope than requested is accepted.

**Errors.** A refusal is `AnhurClientAPIError` (subclass of `AnhurError`) with
`status_code`, `code` (= the server's `body.error`: `child_suspended`,
`child_not_found`, `child_limit`, `tenant_limit`, `invalid_scope`,
`invalid_tenant_id`, `invalid_profile`, `invalid_child_id`, `key_not_found`,
`key_not_owned`, …), `detail`, `error_type` and `is_not_leader`. `kind`
follows the status; `child_limit`/`tenant_limit` are hard caps
(`retryable` False). The SDK never retries. Inputs that can never succeed
(empty ref/tenant/key id, tenant `*`, non-canonical child id, unknown scope,
child impersonate without scope, negative TTL, `max_rpm < 1`) raise
`AnhurQueryError` (400) before any request.

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

Every 401/403 raises `AnhurAuthRefusedError` — a subclass of `AnhurAuthError`,
so `except AnhurAuthError` keeps working — with `status_code`, `code` (the
server's `body.error`, verbatim, read whatever the Content-Type), `detail` and
`error_type`. A scope refusal no longer reads as an invalid key:

```python
from anhurdb import AnhurAuthRefusedError

try:
    await mem.add("turn", mode="ingest", session_id=session_id)
except AnhurAuthRefusedError as refused:
    if refused.is_l1_only:                  # 403 l1_only: token restricted to L1 sessions
        ...
    elif refused.is_child_suspended:        # 403 child_suspended
        ...
    elif refused.is_insufficient_scope:     # 403 "insufficient scope — ..."
        ...
    elif refused.is_impersonation_required: # 403 client_key_requires_impersonation
        ...
    elif refused.is_invalid_key:            # 401 "invalid or inactive API key"
        ...
```

Message: `Authentication failed (HTTP <s>): <code>[: <detail>]` (identical in Go
and TypeScript). A body that is not a JSON object with a string `error` keeps
`Authentication failed (HTTP <s>)`, with `code == ""`. Not retryable. This SDK
has no gRPC transport.

A write into a full session raises `AnhurSessionRecordLimitError` (a subclass
of `AnhurQueryError`, HTTP 409, `retryable == False`) when the server answers
409 with `"code": "session_record_limit"`. `session_uuid` (`str`), `limit` and
`count` (`int`) are always set — the server always sends them. A 409 without
that code (including the pre-ADR-0033 text-only sentence) is the generic
`AnhurQueryError`. Keep the data and retry later — never write it into another
session to dodge the cap:

```python
from anhurdb import AnhurSessionRecordLimitError

try:
    await mem.add("turn", mode="ingest", session_id=session_id)
except AnhurSessionRecordLimitError as full:
    print(f"session {full.session_uuid} is full: {full.count}/{full.limit}")
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

### Tenant library collision (`AnhurTenantLibraryCollisionError`)

When your tenant library store id collides with another registered tenant's
store (a legacy tenant named `<t>_lib`), the server refuses every tenant_shared
search, by-id call, upload and delete: HTTP 409 with `"code":
"tenant_library_collision"`, upload included. The code is the only signal: since
4.0.0 the sentence alone, or a 400, is the generic error. The SDK raises
`AnhurTenantLibraryCollisionError` (an `AnhurQueryError`), **non-retryable**; it
heals only when an operator renames one of the tenants. It is never the
session-record-limit 409. An unreadable tenant registry is HTTP 503 (upload
included) — the ordinary retryable `AnhurError` with kind `unavailable`.

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

## Transport

REST, directly against AnhurDB — the same single transport as the Go and
TypeScript SDKs. The Python-only `mode="mcp"` tunnel was removed in 4.0.0: it
posted to `/api/v1/mcp/direct`, which is served only on the MCP server's
metrics listener, never on the data-plane port `url` points at.

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
