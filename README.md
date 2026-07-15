# AnhurDB SDK v2 — Cognitive Memory for AI Agents

**3 lines. That's all you need.**

```python
from anhurdb import Memory

async with Memory(api_key="anhur_xxx") as mem:
    await mem.add("I'm a data scientist at Google working on NLP")
    context = await mem.search("what does this user do?")
```

Available in **Python**, **TypeScript**, and **Go**. Same API. Same endpoints. Zero heavy dependencies.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point every SDK at `https://anhurdb.yoven.ai` (now the default).

---

## Install

Packages ship on **[GitHub Releases](https://github.com/Yoven/AnhurDB-SDK/releases)** (Yoven).

```bash
# Python — wheel from the Python SDK release assets
pip install \
  https://github.com/Yoven/AnhurDB-SDK/releases/download/v2/python/v2.0.1/anhurdb-2.0.1-py3-none-any.whl

# TypeScript — tarball from the TypeScript SDK release assets
npm install \
  https://github.com/Yoven/AnhurDB-SDK/releases/download/v2/typescript/v2.0.0/anhurdb-2.0.0.tgz

# Go — module tag v2/golang/vX.Y.Z on this repo
go get github.com/Yoven/AnhurDB-SDK/v2/golang/v2@v2.0.1
```

> Pin versions to the tag you want on the [releases page](https://github.com/Yoven/AnhurDB-SDK/releases).

---

## How It Works

```
Your App                    AnhurDB
========                    =======

mem.add("text")
    |  POST /api/v1/ingest
    +---------------------->  Store memory, extract structured facts
    <----------------------+  Return session_id and created records

mem.search("query")
    |  POST /api/v1/search/global
    +---------------------->  Hybrid search across sessions
    <----------------------+  Ranked results with similarity scores
```

---

## Full API Reference

All 3 SDKs share the same methods. Names follow each language's convention.

### Core Methods

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `add(text)` | Store a memory with auto-extraction | POST /api/v1/ingest |
| `search(query)` | Find relevant memories (global, cross-session) | POST /api/v1/search/global |
| `profile()` | Get structured user profile | GET /api/v1/profile |

### Search & Discovery

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `search_by_type(type)` | Filter by memory type (fact, preference, etc.) | GET /api/v1/search/type |
| `smart_search(query)` | Full-text search with cognitive weight boosting | GET /api/v1/search/smart |
| `recall(query)` | Broad fan-out search (alias for global search) | POST /api/v1/search/global |
| `recent(limit)` | Most recent memories | GET /api/v1/recent |

### Graph Traversal

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `walk(seed_id, depth)` | BFS graph traversal | POST /api/v1/walk |
| `walk_semantic(seed_id, depth)` | Semantic graph walk (vector-weighted edges) | POST /api/v1/walk/semantic |
| `get_context(record_id)` | Get record + 1-hop neighbors | GET /api/v1/records/{id}/topology |

### Record CRUD

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `read_content(record_id)` | Full content payload of a record | GET /api/v1/records/{id}/content |
| `update(record_id, ...)` | Modify record fields | PATCH /api/v1/records/{id} |
| `delete(record_id)` | Hard-delete a record | DELETE /api/v1/records/{id} |

### Batch Operations

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `batch_read_content(ids)` | Fetch content for up to 100 records in one call | POST /api/v1/records/batch-content |
| `batch_update_status(ids, status)` | Bulk status update (consolidated, archived, etc.) | PATCH /api/v1/records/mark-consolidated |

### Entity Knowledge Graph (Layer 2)

> **Entity ≠ record type.**  
> `record.type` (`episodic`, `fact`, `decision`, …) classifies the **memory node**.  
> An **entity** is a named real-world node (`person`, `organization`, `location`, `product`, `concept`, `event`) for **cross-cutting search**.  
> Linking a record to an entity (`link_record_entity`) is the “tag”; the entity itself is the registry entry. Use `organization`, not `org`.

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `search_entities(query)` | Search named entities (people, orgs, concepts) | GET /api/v1/entities |
| `upsert_entity(name, type)` | Create or update an entity (idempotent) | POST /api/v1/entities |
| `entity_graph(entity_id, depth)` | BFS traversal of entity relationships | GET /api/v1/entities/{id}/graph |
| `entity_timeline(entity_id)` | Temporal history of entity relationships | GET /api/v1/entities/{id}/timeline |
| `upsert_entity_edge(src, dst, rel)` | Create/update typed relationship between entities | POST /api/v1/entities/edges |
| `link_record_entity(rec_id, ent_id)` | Cross-layer link: memory record to entity | POST /api/v1/entities/link |
| `get_record_entities(record_id)` | Get entities linked to a record | GET /api/v1/records/{id}/entities |

### File Upload

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `upload_file(filename, content)` | Upload document for async ingestion (PDF, images, etc.) | POST /api/v1/upload |
| `upload_status(upload_id)` | Poll file ingestion status | GET /api/v1/upload/{id}/status |

### Temporal Versioning

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `supersede(old_id, new_id)` | Mark old record as superseded by new one | POST /api/v1/records/supersede |

### Session Management

| Method | What it does | Endpoint |
|--------|-------------|----------|
| `new_session()` | Start a fresh session UUID | — |
| `session_id` | Current session ID | — |
| `container_tag` | User/agent identifier (derived from API key) | — |
| `list_sessions()` | List all sessions with stats | GET /api/v1/sessions/stats |
| `get_session_history(uuid)` | Paginated full-text session history | GET /api/v1/sessions/{uuid}/history |
| `get_session_clusters(uuid)` | Thematic clusters within a session | GET /api/v1/sessions/{uuid}/clusters |

---

## Examples by Language

### Python

```python
from anhurdb import Memory

async with Memory(api_key="anhur_xxx") as mem:
    # Core
    result = await mem.add("I'm a senior engineer. I prefer Go over Python.")
    results = await mem.search("what language does this user prefer?")
    profile = await mem.profile()

    # Search & discovery
    facts = await mem.search_by_type("fact", limit=50)
    smart = await mem.smart_search("engineering experience", limit=10)
    latest = await mem.recent(limit=5)

    # Graph traversal
    graph = await mem.walk(start_id=42, depth=3)
    semantic = await mem.walk_semantic(start_id=42, depth=3)
    context = await mem.get_context(record_id=42)
    content = await mem.read_content(record_id=42)

    # Entity knowledge graph
    entities = await mem.search_entities(query="Google")
    entity = await mem.upsert_entity("Google", entity_type="organization")
    graph = await mem.entity_graph(entity["id"], depth=2)
    timeline = await mem.entity_timeline(entity["id"])
    await mem.upsert_entity_edge(1, 2, "works_at", confidence=0.95)
    await mem.link_record_entity(42, entity["id"], role="mentions")
    linked = await mem.get_record_entities(42)

    # Batch operations
    contents = await mem.batch_read_content([1, 2, 3])
    await mem.batch_update_status([10, 11], status="archived")

    # File upload
    upload = await mem.upload_file("report.pdf", base64_content)
    status = await mem.upload_status(upload["id"])

    # Temporal versioning
    await mem.supersede(old_id=42, new_id=99)

    # Sessions
    sessions = await mem.list_sessions()
    history = await mem.get_session_history("session-uuid", limit=50)
    clusters = await mem.get_session_clusters("session-uuid")
    mem.new_session()
    print(mem.session_id, mem.container_tag)

    # Mutate
    await mem.update(42, summary="Updated summary", score=8)
    await mem.delete(42)

    # AST query (QueryBuilder)
    from anhurdb.query import QueryBuilder
    qb = QueryBuilder().where(type="risk", score__gte=7).limit(20)
    records = await mem.query(qb, session_uuid="session-uuid")
```

### TypeScript

```typescript
import { Memory } from 'anhurdb';

const mem = new Memory({ apiKey: 'anhur_xxx' });

// Core
const result = await mem.add("I'm a senior engineer.");
const results = await mem.search("what language?");
const profile = await mem.profile();

// Extended search
const facts = await mem.searchByType("fact", 50);
const smart = await mem.smartSearch("engineering", 10);
const latest = await mem.recent(5);

// Graph traversal
const graph = await mem.walk(42, 3);
const semantic = await mem.walkSemantic(42, 3);
const ctx = await mem.getContext(42);
const content = await mem.readContent(42);

// Entity knowledge graph
const entities = await mem.searchEntities("Google");
const entity = await mem.upsertEntity("Google", { entityType: "organization" });
const entityGraph = await mem.entityGraph(entity.id, 2);
const timeline = await mem.entityTimeline(entity.id);
await mem.upsertEntityEdge(1, 2, "works_at", { confidence: 0.95 });
await mem.linkRecordEntity(42, entity.id, "mentions");
const linked = await mem.getRecordEntities(42);

// Batch operations
const contents = await mem.batchReadContent([1, 2, 3]);
await mem.batchUpdateStatus([10, 11], "archived");

// File upload
const upload = await mem.uploadFile("report.pdf", base64Content);
const status = await mem.uploadStatus(upload.id);

// Temporal versioning
await mem.supersede(42, 99);

// Sessions
const sessions = await mem.listSessions();
const history = await mem.getSessionHistory("session-uuid");
const clusters = await mem.getSessionClusters("session-uuid");
await mem.newSession();

// AST query (QueryBuilder)
import { QueryBuilder } from "anhurdb";
const { records } = await new QueryBuilder()
  .where("type", "$eq", "risk")
  .where("score", "$gte", 7)
  .limit(20)
  .execute(mem);

// Mutate
await mem.update(42, { summary: "Updated" });
await mem.delete(42);
```

### Go

```go
package main

import (
    "context"
    "fmt"
    anhurdb "github.com/Yoven/AnhurDB-SDK/v2/golang/v2"
    "github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

func main() {
    ctx := context.Background()

    // Connect
    mem := anhurdb.NewMemory("anhur_xxx")
    // or: anhurdb.NewMemory("key", anhurdb.WithURL("http://localhost:8000"))
    // or: anhurdb.NewMemory("key", anhurdb.WithUserID("user-123"))

    // Core
    result, _ := mem.Add(ctx, "I'm a senior engineer.")
    hits, _ := mem.Search(ctx, "what language?")
    profile, _ := mem.Profile(ctx)

    // Extended search
    facts, _ := mem.SearchByType(ctx, "fact", 50)
    smart, _ := mem.SmartSearch(ctx, "engineering", 10)
    latest, _ := mem.RecentMemories(ctx, 5)

    // Graph traversal
    graph, _ := mem.Walk(ctx, 42, 3)
    semantic, _ := mem.WalkSemantic(ctx, 42, 3)
    topo, _ := mem.GetContext(ctx, 42)
    content, _ := mem.ReadContent(ctx, 42)

    // Entity knowledge graph
    entities, _ := mem.SearchEntities(ctx, "Google", "", 20)
    entity, _ := mem.UpsertEntity(ctx, "Google", "organization", "", nil)
    entityGraph, _ := mem.EntityGraph(ctx, entity.ID, 2)
    timeline, _ := mem.EntityTimeline(ctx, entity.ID)
    _ = mem.UpsertEntityEdge(ctx, 1, 2, "works_at",
        client.WithConfidence(0.95))
    _ = mem.LinkRecordEntity(ctx, 42, entity.ID, "mentions")
    linked, _ := mem.GetRecordEntities(ctx, 42)

    // Batch operations
    contents, _ := mem.BatchReadContent(ctx, []int64{1, 2, 3})
    _ = mem.BatchUpdateStatus(ctx, []int64{10, 11}, "archived")

    // File upload
    upload, _ := mem.UploadFile(ctx, "report.pdf", base64Content, "")
    status, _ := mem.UploadStatus(ctx, upload.ID)

    // Temporal versioning
    _ = mem.Supersede(ctx, 42, 99)

    // Sessions
    sessions, _ := mem.ListSessions(ctx)
    history, _ := mem.GetSessionHistory(ctx, "session-uuid", 50, 0)
    clusters, _ := mem.GetSessionClusters(ctx, "session-uuid")
    mem.NewSession()
    fmt.Println(mem.SessionID(), mem.ContainerTag())

    // AST query (NewQuery fluent builder)
    req := client.NewQuery().
        Where("type", client.QueryOp{Eq: "risk"}).
        Where("score", client.QueryOp{Gte: 7}).
        Limit(20)
    records, _ := mem.Query(ctx, req)

    // Mutate
    _ = mem.Update(ctx, 42, map[string]interface{}{"summary": "Updated"})
    _ = mem.Delete(ctx, 42)
}
```

---

## Authentication

All SDKs use the `X-API-Key` header. Get your key from [ControlPlane](https://anhur.yoven.ai/app).

For multi-tenant apps, the API key already contains the tenant scope. No extra configuration needed.

---

## Self-Hosted (OSS)

AnhurDB OSS runs as a single-node Docker container:

```bash
docker compose up -d
```

Then point the SDK to your local instance:

```python
async with Memory(url="http://localhost:8000", api_key="your-local-key") as mem:
    await mem.add("hello")
```

OSS includes the REST API, search, and graph features. Cloud adds auto-extraction,
profiles, and managed cognitive processing.

---

## Memory Types

> These values are **`record.type` only**. Entity types (`person`, `organization`, …) are a separate Layer-2 whitelist — see [Entity Knowledge Graph](#entity-knowledge-graph-layer-2).

AnhurDB classifies memories into 12 cognitive types:

| Type | Description | Example |
|------|-------------|---------|
| `episodic` | Raw conversation turns | "User asked about Redis" |
| `fact` | Verifiable information | "Senior engineer at Google" |
| `preference` | Likes, dislikes | "Prefers dark mode" |
| `decision` | Choices made | "Team chose PostgreSQL" |
| `task` | Action items | "Deploy auth service by Friday" |
| `risk` | Concerns, warnings | "No rollback strategy" |
| `emotion` | Feelings expressed | "Frustrated with build times" |
| `reasoning` | Chain of thought | "Chose Redis because..." |
| `idea` | Proposals | "Could use event sourcing" |
| `consolidated` | Agent-synthesized summary | (auto-created) |
| `hub` | Cross-session cluster | (auto-created) |
| `file` | Uploaded document root | (from file upload endpoint) |

---

## Project Structure

```
AnhurDB-SDK/
+-- v2/
|   +-- python/              Python SDK (async, Pydantic models)
|   |   +-- anhurdb/
|   |   |   +-- client/      Memory client
|   |   |   +-- models/      Record and entity types
|   |   |   +-- query/       Query Builder
|   |   +-- pyproject.toml
|   |   +-- tests/
|   |
|   +-- typescript/          TypeScript SDK (zero runtime deps)
|   |   +-- src/
|   |   |   +-- memory.ts    Memory class (30+ methods)
|   |   |   +-- query.ts     QueryBuilder (AST-based DSL)
|   |   |   +-- client.ts    HTTP client (native fetch)
|   |   |   +-- types.ts     All interfaces + error classes
|   |   |   +-- index.ts     Public exports
|   |   +-- package.json     ESM + CJS dual output
|   |
|   +-- golang/              Go SDK (zero external deps)
|       +-- client/
|       |   +-- client.go    Memory struct (30+ methods)
|       |   +-- parity.go    Memory.Query + NewQuery() AST builder
|       |   +-- connection.go REST HTTP client (stdlib only)
|       |   +-- types.go     Response types, Entity, Upload, QueryRequest
|       |   +-- errors.go    Typed error constants
|       +-- models/          Record and session types
|       +-- go.mod           github.com/Yoven/AnhurDB-SDK/v2/golang/v2
|
+-- docs/
|   +-- general/ARCHITECTURE.md
|   +-- api/REST_API.md
|   +-- claude/CLAUDE_ANHURDB_INTEGRATION.md
```

---

## SDK vs MCP

| | SDK (REST) | MCP |
|---|---|---|
| **For** | Application developers | AI IDE integrations |
| **Protocol** | HTTP REST | MCP over HTTP/SSE |
| **Auth** | `X-API-Key` header | API key in tool arguments |
| **Best for** | Production applications | Claude, Cursor, and similar tools |

Use the **SDK** for application code. Use **MCP** for IDE plugin integrations.

---

## What Makes AnhurDB Different

- **12 cognitive memory types** — beyond flat key-value storage
- **Entity knowledge graph** — named entities with typed relationships
- **Temporal versioning** — supersede old facts without losing history
- **Graph traversal** — walk and explore memory connections
- **Smart search** — relevance boosted by cognitive importance
- **File ingestion** — upload documents for async processing

---

## Links

- [ControlPlane](https://anhur.yoven.ai/app) — Create API keys, manage projects
- [Open Beta Docs](https://anhur.yoven.ai) — Product docs and SDK guides
- [GitHub](https://github.com/Yoven/AnhurDB-SDK) — Source code, issues, contributions
