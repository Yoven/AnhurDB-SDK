# AnhurDB REST API Reference

Public REST API reference for AnhurDB SDK users. All endpoints require the
`X-API-Key` header unless noted.

## Authentication

```
X-API-Key: your-api-key
X-Tenant-ID: my-tenant     (optional, multi-tenant)
```

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
| Body | `content` + `container_tag` (+ optional `session_id`) | Full create payload (`uuid`, `type`, `content`, …) |
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
| GET | `/api/v1/records/{id}/grounding` | Get provenance and anchors |
| PATCH | `/api/v1/records/{id}` | Update record fields |
| DELETE | `/api/v1/records/{id}` | Delete a record |

### Search

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/search` | **Canonical** hybrid search. Body field `scope`: `sessions` (default) \| `tenant_shared` \| `client_shared` \| `shared_all`. Optional `uuid` only with `scope=sessions`. |
| POST | `/api/v1/search/global` | **Deprecated alias** of `/api/v1/search` (same handler, same `scope` semantics). Prefer `/search`. |
| GET | `/api/v1/search/smart` | Smart search with cognitive weighting; query param `scope` (same enum, default `sessions`) |
| GET | `/api/v1/search/type` | Filter by memory type |
| POST | `/api/v1/query` | Structured query (SDK Query Builder) |

**Plane behaviour**

| `scope` | Store | What you get |
|---------|-------|--------------|
| `sessions` (omit/`""`) | `{client}_{tenant}` | Chat sessions only — excludes `shared-*` uuids |
| `tenant_shared` | `{client}_{tenant}` | Tenant Shared Data session only |
| `client_shared` | `{client}_shared` | Client-wide Shared Data session only |
| `shared_all` | both shared stores | Union with per-hit `provenance` |

### Graph

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/walk` | Graph traversal from a seed record |
| POST | `/api/v1/walk/semantic` | Semantic graph walk |

### Sessions and manifests

| Method | Path | Description |
|--------|------|-------------|
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
| POST | `/api/v1/ingest` | Platform path: episodic + async extraction (LLM + embed billed) |
| GET | `/api/v1/profile` | User or agent profile |

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
from anhurdb import Memory, CreateRequest

async with Memory(api_key="anhur_xxx", url="https://anhurdb.yoven.ai") as mem:
    await mem.add("text")
    await mem.search("query")
    await mem.search_session("session-uuid", "query")
    await mem.create(CreateRequest(uuid="s1", content="..."))
    await mem.get_grounding(record_id=42)
    await mem.search_entities(query="Google", entity_type="organization")
```

### TypeScript

```typescript
import { Memory } from "anhurdb";

const mem = new Memory({ apiKey: "anhur_xxx", url: "https://anhurdb.yoven.ai" });
await mem.add("text");
await mem.search("query");
await mem.searchSession("session-uuid", "query");
await mem.create("content", { type: "fact" });
await mem.getGrounding(42);
await mem.searchEntities("Google", "organization");
```

### Go

```go
import (
    "context"
    anhurdb "github.com/Yoven/AnhurDB-SDK/v2/golang/v2"
)

mem := anhurdb.NewMemory("anhur_xxx", anhurdb.WithURL("https://anhurdb.yoven.ai"))
ctx := context.Background()
mem.Add(ctx, "text")
mem.Search(ctx, "query")
mem.SearchSession(ctx, "session-uuid", "query")
mem.Create(ctx, "session-uuid", "content", anhurdb.WithCreateType("fact"))
mem.GetGrounding(ctx, 42, 0)
mem.SearchEntities(ctx, "Google", "organization", 20)
```
