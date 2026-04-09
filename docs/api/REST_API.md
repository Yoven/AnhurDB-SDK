# AnhurDB REST API Reference

Complete reference for the AnhurDB REST API. All endpoints require the `X-API-Key` header.

## Authentication

```
X-API-Key: your-api-key
X-Tenant-ID: my-tenant     (optional, for multi-tenant)
```

## Endpoints

### System & Observability

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/health` | Server health check (always public) |
| GET | `/api/v1/healthz/topology` | Topology health (graph invariants) |
| GET | `/metrics` | Prometheus metrics (always public) |

### Record CRUD

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/records` | Create a new memory record |
| GET | `/api/v1/records/{id}` | Get record metadata |
| GET | `/api/v1/records/{id}/content` | Get decompressed .gz content |
| GET | `/api/v1/records/{id}/topology` | Get record + 1-hop graph neighbors |
| PATCH | `/api/v1/records/{id}` | Update record fields |
| DELETE | `/api/v1/records/{id}` | Delete a record |

### Search & Analysis

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/search` | Session-scoped search (vector + text hybrid) |
| POST | `/api/v1/search/global` | Cross-session search (global, safe types only) |
| GET | `/api/v1/search/smart` | Full-text + cognitive weight boosting (DuckDB) |
| GET | `/api/v1/search/type` | Filter by memory type with optional keyword |
| POST | `/api/v1/query` | Execute raw AST query (used by SDK Query Builder) |
| GET | `/api/v1/tenant/engine-config` | Get search engine configuration |

### Graph Traversal

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/walk` | BFS graph traversal from seed record |
| POST | `/api/v1/walk/semantic` | Semantic walk (vector-weighted edges) |
| GET | `/api/v1/graph` | Full graph for viewer |

### Session & Manifest

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/sessions` | List all session UUIDs |
| GET | `/api/v1/sessions/stats` | Sessions with aggregate stats |
| GET | `/api/v1/sessions/{uuid}/history` | Paginated full-text session history |
| GET | `/api/v1/sessions/{uuid}/clusters` | Thematic clusters (DBSCAN on BSQ vectors) |
| GET | `/api/v1/chats/{uuid}` | List all records in a session |
| GET | `/api/v1/chats/{uuid}/manifest` | Session manifest with metadata |
| GET | `/api/v1/manifest` | Global manifest (cross-session, ranked) |
| GET | `/api/v1/recent` | Recently updated records |

### Batch Operations

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/records/batch-content` | Fetch content for multiple records (max 100) |
| PATCH | `/api/v1/records/mark-consolidated` | Bulk status update |
| PATCH | `/api/v1/records/consolidate-ids` | Link children to consolidation anchor |
| PATCH | `/api/v1/records/append-main-ids` | Append to main_ids array |
| PATCH | `/api/v1/records/decay` | Apply Ebbinghaus memory decay |
| POST | `/api/v1/records/supersede` | Mark record as superseded (temporal versioning) |

### Entity Knowledge Graph (Layer 2)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/entities` | Search entities by name/type |
| POST | `/api/v1/entities` | Create/upsert entity (idempotent by name) |
| GET | `/api/v1/entities/{id}/graph` | BFS entity relationship traversal |
| GET | `/api/v1/entities/{id}/timeline` | Temporal history of entity relationships |
| POST | `/api/v1/entities/edges` | Create/upsert typed entity relationship |
| POST | `/api/v1/entities/link` | Link memory record to entity (cross-layer) |
| GET | `/api/v1/records/{id}/entities` | Get entities linked to a record |

### Ingestion & Profiles

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/ingest` | Synchronous text ingestion with auto-extraction |
| GET | `/api/v1/profile` | Aggregated user profile (facts + preferences + stats) |

### File Upload

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/upload` | Upload document for async ingestion |
| GET | `/api/v1/upload/{id}/status` | Poll file ingestion status |

### Admin (requires master key)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/admin/keys` | List all API keys |
| POST | `/api/v1/admin/keys` | Create API key |
| DELETE | `/api/v1/admin/keys/{key}` | Revoke API key |
| GET | `/api/v1/admin/clients` | List tenants/clients |
| GET | `/api/v1/admin/tenants` | List all tenants |
| PATCH | `/api/v1/admin/clients/{client_id}` | Set dedicated resources |
| POST | `/api/v1/admin/graph/reset` | Reset tenant's knowledge graph |
| POST | `/api/v1/admin/config` | Set tenant configuration |
| GET | `/api/v1/admin/config` | Get tenant configurations |
| POST | `/api/v1/admin/parquet/rebuild` | Rebuild Parquet analytics |

### Backup & Recovery (admin only)

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/admin/backup` | Create backup |
| GET | `/api/v1/admin/backups` | List backups |

### Cluster Management (internal)

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/cluster/join` | Join Raft cluster |
| POST | `/api/v1/internal/multiraft/join` | Join per-tenant Raft group |
| POST | `/internal/lock/acquire` | Acquire distributed lock |
| POST | `/internal/lock/release` | Release distributed lock |

## SDK Mapping

### Python

```python
from anhurdb import Memory, AnhurClient

# Simple API
async with Memory(api_key="key") as mem:
    await mem.add("text")                          # POST /ingest (cloud) or /records (OSS)
    await mem.search("query")                      # POST /search/global
    await mem.profile()                            # GET /profile

# Full API
async with AnhurClient(api_key="key") as client:
    await client.create(req)                       # POST /records
    await client.search("query")                   # POST /search/global
    await client.search_entities(query="Google")   # GET /entities
    await client.batch_read_content([1,2,3])       # POST /records/batch-content
    await client.upload_file("f.pdf", content)     # POST /upload
    await client.supersede(42, 99)                 # POST /records/supersede
```

### TypeScript

```typescript
import { Memory } from "anhurdb";

const mem = new Memory({ apiKey: "key" });
await mem.add("text");                             // POST /ingest or /records
await mem.search("query");                         // POST /search/global
await mem.searchEntities("Google");                // GET /entities
await mem.batchReadContent([1, 2, 3]);             // POST /records/batch-content
await mem.uploadFile("f.pdf", content);            // POST /upload
await mem.supersede(42, 99);                       // POST /records/supersede
```

### Go

```go
mem := anhurdb.NewMemory("key")
mem.Add(ctx, "text")                               // POST /ingest or /records
mem.Search(ctx, "query")                           // POST /search/global
mem.SearchEntities(ctx, "Google", "", 20)           // GET /entities
mem.BatchReadContent(ctx, []int64{1, 2, 3})        // POST /records/batch-content
mem.UploadFile(ctx, "f.pdf", content, "")          // POST /upload
mem.Supersede(ctx, 42, 99)                         // POST /records/supersede
```
