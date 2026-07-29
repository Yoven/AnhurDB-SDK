# AnhurDB REST API Reference

Public REST API reference for AnhurDB SDK users. All endpoints require the
`X-API-Key` header unless noted.

## Authentication

```
X-API-Key: your-api-key
X-Tenant-ID: my-tenant     (optional, multi-tenant)
```

## Error envelope

Every non-2xx answer on `/api/v1/*` is a JSON object with a single `error` key:

```json
{"error": "filter \"type\" has no operator: use one of $eq, $gt, $gte, $lt, $lte, $in"}
```

(`server/handler/response.go` → `writeError`.) The message is the actionable part —
match on it for diagnostics, not for control flow.

## Breaking-behaviour notice — 2026-07-28

Two contracts stopped accepting under-specified input **and started answering 400**.
Both used to succeed and return a **wider** result set than the caller asked for.

1. **`sessions` is mandatory on every search endpoint** (ADR-0014). The old singular
   `uuid` body field is gone. See [Session filter](#session-filter-adr-0014).
2. **`POST /api/v1/query` rejects malformed AST filters by name** instead of dropping
   them. See [POST /api/v1/query](#post-apiv1query--structured-ast-query).

If you build request bodies by hand, read both sections before deploying.

## Session-first writes

Every write path requires a **registered session UUID** before ingest, create, or
upload (chat mode):

1. **Create session** — `POST /api/v1/sessions` (optional `session_id`, optional
   `metadata` JSON object copied onto records in this session).
2. **Write** — pass the returned `session_id` on ingest/create/upload.

`container_tag` is a **recall/profile aggregation tag only** — it is stored in
record metadata and groups cross-session recall via `GET /api/v1/profile`. It is
**never** a session substitute.

Missing `session_id` on ingest/create returns **400**:

```
session_id is required — create a session first (POST /api/v1/sessions)
```

**SDK:** call `create_session()` / `CreateSession` / `createSession` **before**
`add(text, mode="ingest")` or `add(..., mode="regular")` / `create(...)`.
SDKs do **not** auto-register on first write — missing registration returns 400.
(The Claude Code memory plugin calls `CreateSession` before each persist.)

## Endpoints

### System

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/health` | Service health check |

### Write paths: `/ingest` vs `/records`

AnhurDB exposes **two** write contracts. They are not interchangeable — behavior
and **token billing** differ.

| | `POST /api/v1/ingest` | `POST /api/v1/records` |
|--|----------------------|------------------------|
| SDK | `add(text)` (default) | `create(...)` — also `add(...)` when `type` / `score` / `metadata` are set |
| MCP | `ingest_memory` | `create_memory` |
| Immediate write | **1 episodic** | **Exactly 1** typed record |
| Satellites (fact, preference, …) | **Platform** extraction agent (async NATS) | **Caller** only — no extraction job |
| Body | `content` + `container_tag` + **`session_id`** (required) | Typed payload with **`session_id`** (or legacy `uuid`) + `type`, `content`, … |
| Prerequisite | `POST /api/v1/sessions` first | `POST /api/v1/sessions` first — same `session_id` |
| Billing | Extraction **LLM** tokens + embed tokens for episodic **and** each satellite | **No** extraction LLM; embed tokens for that one record |

```
ingest:  text → episodic → extraction.create → agents create satellites
records: typed payload → one record → enrichment embed only
```

### Record CRUD

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/records` | Create one typed record (**no** extraction) |
| GET | `/api/v1/records/{id}` | Get record metadata |
| GET | `/api/v1/records/{id}/content` | Get full record content |
| GET | `/api/v1/records/{id}/topology` | Get record and nearby graph nodes |
| GET | `/api/v1/records/{id}/explain` | Provenance tree for a record. Query params `depth` (1–3, default 1) and `as_of` |
| GET | `/api/v1/records/{id}/grounding` | Get provenance and anchors |
| PATCH | `/api/v1/records/{id}` | Update record fields |
| DELETE | `/api/v1/records/{id}` | **Soft delete** — archives the record (`{"message":"record archived"}`), it is not erased |

`DELETE` is a tombstone, not an erase: the row stays with `archived=1`,
`status='deleted'`, and is hidden from every read path by default. Un-archiving is an
admin-only operation (`POST /api/v1/records/restore`, master/admin key), not part of
the SDK surface.

### Search

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/search` | **Canonical** hybrid search. Body: `text` (or `vector` / `query_embedding`), **`sessions` (required)**, `scope`, `limit`, `type_filter`. |
| POST | `/api/v1/search/global` | **Deprecated alias** of `/api/v1/search` — same handler, same `sessions` validation. Prefer `/search`. |
| GET | `/api/v1/search/smart` | Lexical search with cognitive weighting. Query params: `q` (required), **`sessions` (required)**, `scope`, `limit`, `type`. |
| GET | `/api/v1/search/type` | Records of one type, ordered by weight. Query params: `type` (required), **`sessions` (required)**, optional `q`, `limit` (default 20, capped 1000). |
| POST | `/api/v1/query` | Structured AST query — see [below](#post-apiv1query--structured-ast-query) |

#### Session filter (ADR-0014)

`sessions` is **required** on all four search endpoints above. It is orthogonal to
`scope`: `scope` picks the *store/plane*, `sessions` picks the *subset inside it*.

| Value sent | Result |
|------------|--------|
| `["*"]` | every session inside the scope boundary |
| `["uuid-a"]` | exactly that session |
| `["uuid-a","uuid-b"]` | exactly those, up to **1000** uuids (deduplicated first) |
| `[]` | **400** `INVALID_PARAM: 'sessions' cannot be empty; use ["*"] for every session in scope` |
| field absent | **400** `INVALID_PARAM: 'sessions' is required; use ["*"] for every session in scope` |
| `["*","uuid-a"]` | **400** — the wildcard must stand alone |
| `["uuid-a",""]` | **400** `INVALID_PARAM: 'sessions' contains an empty entry` |

Spelling per transport:

- `POST /search`, `POST /search/global` — JSON body: `"sessions": ["*"]`. If the body
  omits the key entirely, the query string is read as a fallback; the body wins when
  both carry it.
- `GET /search/smart`, `GET /search/type` — query string only: repeated key
  (`?sessions=a&sessions=b`) or comma form (`?sessions=a,b`).

`?sessions=` alone (key present, value blank) is a 400 — a caller who sent the argument
and left it empty is not a caller asking for everything. Both spellings run the same
validation, so neither door is the lenient one.

> **Removed:** the singular `uuid` body field on search. It was decoded into a struct
> field nobody read, so a caller who sent it got an **unfiltered** search that looked
> successful. There is no compatibility adapter — migrate `uuid: "x"` to
> `sessions: ["x"]`, and "I want everything" to `sessions: ["*"]`.
>
> All three SDKs already require it: `sessions_all()` (Python), `sessionsAll()`
> (TypeScript), `client.SessionsAll()` (Go).

**Plane behaviour** (`scope`)

| `scope` | Store | What you get |
|---------|-------|--------------|
| `sessions` (omit/`""`) | `{client}_{tenant}` | Chat sessions only — excludes `shared-*` uuids |
| `tenant_shared` | `{client}_{tenant}` | Tenant Shared Data session only |
| `client_shared` | `{client}_shared` | Client-wide Shared Data session only |
| `shared_all` | both shared stores | Union with per-hit `provenance` |

An unknown `scope` value is a 400. `type_filter=file` is rejected on the chat plane
(named 400, not an empty 200).

### POST /api/v1/query — structured AST query

Body is the AST emitted by the SDK query builders. Response envelope is unchanged:
`{"records": [...]|null, "count": N}` — zero hits marshal as `"records": null`, never `[]`.

```json
{
  "filters": {
    "type":  {"$eq":  "risk"},
    "score": {"$gte": 7},
    "status":{"$in":  ["saved","consolidated"]}
  },
  "sort":       [{"field": "weight", "order": "desc"}],
  "pagination": {"limit": 20, "offset": 0}
}
```

**Filter shape.** `filters` is an **object keyed by column**, and each value is an
**operator object**. It is *not* an array of `{field, op, value}` — that shape used to
be swallowed silently and turned the query into an unfiltered listing.

```
CORRECT   "filters": {"type": {"$eq": "episodic"}}
WRONG     "filters": [{"field":"type","op":"eq","value":"episodic"}]   → 400
WRONG     "filters": {"type": "episodic"}                              → 400 (bare value)
```

Operators — closed set, no others exist: `$eq`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`.
(`$neq`, `$nin`, `$like` were never implemented.)

Filterable / sortable columns — closed whitelist:
`id`, `uuid`, `type`, `dimension`, `weight`, `score`, `status`, `consolidated`,
`archived`, `created_at`, `updated_at`, `prefix`, `metadata`, `summary`,
`superseded_by`, `valid_from`, `valid_until`.

#### Behaviour changes — 2026-07-28

Each row below used to be **accepted and dropped**, producing HTTP 200 with a result
set wider than the one requested. All now answer **400** with a message naming the
offending field or operator.

| Input | Was | Now |
|-------|-----|-----|
| unknown operator — `{"type":{"$like":"fac%"}}` | operator dropped → unfiltered listing | 400 `filter "type": unsupported operator "$like" — supported operators are $eq, $gt, $gte, $lt, $lte, $in` |
| bare value — `{"type":"episodic"}` | filter dropped | 400 `filter "type" must be an object of operators (e.g. {"$eq": value}), got a bare value` |
| empty operator object — `{"type":{}}` | filter dropped | 400 `filter "type" has no operator: use one of $eq, $gt, $gte, $lt, $lte, $in` |
| empty `$in` — `{"type":{"$in":[]}}` | filter dropped | 400 `filter "type": $in requires a non-empty array of values` |
| array/object as an operator value | generic 500 | 400 naming the field and the expected scalar |
| `filters` sent as an array, or undecodable body | 400 `invalid json ast` | 400, same stable `invalid json ast` prefix, message now shows the correct shape |
| row scan / iteration failure mid-page | **200** with a silently truncated page | **500**, no records |

Unchanged (still accept-and-ignore — do **not** read these as fixed):

- `select` is accepted for wire compatibility and ignored; every hit is a full record.
- An unrecognised `sort.order` falls back to `DESC` without error.
- `limit` defaults to 50 and is silently clamped to **1000**; `offset` defaults to 0.
  Neither is echoed in the response, so a client cannot detect the clamp.
- `filters.semantic_search` is accepted and skipped — it is answered by the hybrid
  engine (`POST /api/v1/search`), never by this endpoint. Sending it here is a no-op,
  not an error.
- Superseded rows are never returned. Archived rows are hidden unless you filter on
  `archived` explicitly.

**Go SDK callers, read this.** `client.QueryOp` tags all six operator fields
`omitempty`, so `QueryOp{}`, `QueryOp{In: []interface{}{}}` and `QueryOp{Eq: nil}` all
marshal to `{}` — which is now the 400 above. Calls that used to return the whole
tenant page now fail. Two consequences worth knowing:

- an empty `$in` from Go surfaces as the *"has no operator"* message, not the
  *"$in requires a non-empty array"* one Python/TypeScript get;
- `$eq: null` is not expressible from Go, although the server accepts it.

The Go builder also has **no** client-side column whitelist and **no** limit/offset
guard — an unknown column reaches the server and 400s there, and an out-of-range limit
is clamped server-side with no signal. Python (`anhurdb.query.QueryBuilder`) and
TypeScript (`QueryBuilder`) both mirror the server whitelist and reject locally.

### Graph

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/walk` | Graph traversal from a seed record |
| POST | `/api/v1/walk/semantic` | Semantic graph walk |

### Sessions and manifests

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/sessions` | **Create a write session** (optional `session_id`, optional `metadata`). Required before ingest/create/upload. |
| GET | `/api/v1/sessions` | List all session UUIDs |
| GET | `/api/v1/sessions/stats` | Session statistics |
| GET | `/api/v1/sessions/{uuid}/history` | Paginated session history |
| GET | `/api/v1/sessions/{uuid}/clusters` | Thematic session clusters |
| GET | `/api/v1/chats/{uuid}` | Records in a session |
| GET | `/api/v1/chats/{uuid}/manifest` | Session manifest |
| GET | `/api/v1/manifest` | Global manifest |
| GET | `/api/v1/recent` | Recently updated records |

### Batch operations

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/records/batch-content` | Fetch content for multiple records |
| PATCH | `/api/v1/records/mark-consolidated` | Bulk status update |
| PATCH | `/api/v1/records/consolidate-ids` | Link consolidated children |
| PATCH | `/api/v1/records/append-main-ids` | Append main record links |
| PATCH | `/api/v1/records/append-related-ids` | Append related record links |
| POST | `/api/v1/records/supersede` | Temporal versioning (supersede) |

### Entity graph

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/entities` | Search entities (`q` query param) |
| GET | `/api/v1/entities/list` | List entities |
| POST | `/api/v1/entities` | Create or update entity |
| GET | `/api/v1/entities/{id}/graph` | Entity relationship graph |
| GET | `/api/v1/entities/{id}/timeline` | Entity timeline |
| POST | `/api/v1/entities/edges` | Create or update entity edge |
| POST | `/api/v1/entities/link` | Link record to entity |
| GET | `/api/v1/records/{id}/entities` | Entities linked to a record |

### Ingestion and profiles

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/ingest` | Platform path: episodic + async extraction (LLM + embed billed). Body: `content`, `container_tag`, **`session_id`** (required). |
| GET | `/api/v1/profile` | Aggregated profile for a `container_tag` (recall scope — not a session id) |

### File upload

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/upload` | Upload a document |
| GET | `/api/v1/upload/{id}/status` | Upload processing status |

## SDK mapping

All three SDKs use a single **`Memory`** client. See `v2/PARITY_SPEC.md` for the
full method list. Open Beta default URL: `https://anhurdb.yoven.ai`.

### Python

```python
from anhurdb import Memory, CreateRequest, sessions_all

async with Memory(api_key="anhur_xxx", url="https://anhurdb.yoven.ai") as mem:
    session_id = await mem.create_session()
    await mem.add("text", mode="ingest", session_id=session_id)
    await mem.search("query", sessions_all())              # sessions is required
    await mem.search("query", [session_id])                # one chat only
    await mem.search_session("query", session_uuid=session_id)
    await mem.create(CreateRequest(session_id=session_id, content="..."))
    await mem.get_grounding(record_id=42)
    await mem.search_entities(query="Google", entity_type="organization")
```

### TypeScript

```typescript
import { Memory, sessionsAll } from "anhurdb";

const mem = new Memory({ apiKey: "anhur_xxx", url: "https://anhurdb.yoven.ai" });
const sessionId = await mem.createSession();
await mem.add("text", { mode: "ingest", sessionId });
await mem.search("query", sessionsAll());   // sessions is required
await mem.search("query", [sessionId]);     // one chat only
await mem.searchSession("query", sessionId);
await mem.create("content", { type: "fact" });
await mem.getGrounding(42);
await mem.searchEntities("Google", "organization");
```

### Go

```go
import (
    "context"
    anhurdb "github.com/Yoven/AnhurDB-SDK/v2/golang/v2"
    "github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

mem := anhurdb.NewMemory("anhur_xxx", anhurdb.WithURL("https://anhurdb.yoven.ai"))
ctx := context.Background()
sessionID, _ := mem.CreateSession(ctx)
mem.Add(ctx, "text", anhurdb.WithSessionID(sessionID), anhurdb.WithMode("ingest"))
mem.Search(ctx, "query", client.SessionsAll())   // sessions is required
mem.Search(ctx, "query", []string{sessionID})    // one chat only
mem.SearchSession(ctx, sessionID, "query")
mem.Create(ctx, sessionID, "content", anhurdb.WithCreateType("fact"))
mem.GetGrounding(ctx, 42, 0)
mem.SearchEntities(ctx, "Google", "organization", 20)
```

`anhurdb.SessionsAll` is re-exported at the package root, so
`anhurdb.SessionsAll()` works too. The write-mode option is `WithMode` — there is no
`WithAddMode`.
