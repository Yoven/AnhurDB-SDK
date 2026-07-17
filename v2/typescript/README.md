# anhurdb

TypeScript SDK for [AnhurDB](https://anhur.yoven.ai) — cognitive memory for AI agents.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point the SDK at `https://anhurdb.yoven.ai` (default).

Zero runtime dependencies. Works with Node 18+, Deno, Bun, and Cloudflare Workers.

## Quickstart

```typescript
import { Memory } from "anhurdb";

const mem = new Memory({ apiKey: "anhur_xxx" });
await mem.add("I'm a data scientist at Google working on NLP");
const results = await mem.search("what does this user do?");
```

## Installation

Tarballs ship on [GitHub Releases](https://github.com/Yoven/AnhurDB-SDK/releases) (`v2/typescript/vX.Y.Z`).

```bash
npm install \
  https://github.com/Yoven/AnhurDB-SDK/releases/download/v2/typescript/v2.0.4/anhurdb-2.0.4.tgz
```

## Usage

### Initialize

```typescript
import { Memory } from "anhurdb";

// Cloud (default)
const mem = new Memory({ apiKey: "anhur_xxx" });

// Self-hosted / OSS
const mem = new Memory({
  url: "http://localhost:8000",
  apiKey: "my-local-key",
});

// With user grouping and multi-tenant
const mem = new Memory({
  apiKey: "anhur_xxx",
  userId: "user-42",
  tenantId: "tenant-a",
});
```

### Core — Add, Search, Profile

```typescript
await mem.add("User prefers dark mode");
await mem.add("Revenue hit $1M this quarter", { score: 9, type: "fact" });

// Search across all sessions
const results = await mem.search("user preferences?", { limit: 5 });
for (const r of results) {
  console.log(`${r.record.summary} (similarity: ${r.similarity})`);
}

// Get user profile
const profile = await mem.profile();
console.log(profile.static);  // identity, preferences
```

### Search & Discovery

```typescript
// Filter by cognitive type
const facts = await mem.searchByType("fact", 50);

const smart = await mem.smartSearch("engineering experience", 10);

// Global recall
const broad = await mem.recall("engineering", 20);

// Most recent records
const latest = await mem.recent(5);
```

### Graph Traversal

```typescript
// BFS graph walk
const graph = await mem.walk(42, 3);

// Semantic walk (vector-weighted edges)
const semantic = await mem.walkSemantic(42, 3);

// Record context (1-hop neighbors)
const ctx = await mem.getContext(42);

// Full content
const content = await mem.readContent(42);
```

### Entity Knowledge Graph

> **Entity ≠ record type.** `record.type` (`episodic`, `fact`, …) classifies the memory node.
> Entities (`person`, `organization`, …) are Layer 2 for cross-cutting search; `linkRecordEntity` is the tag.

```typescript
// Search entities
const entities = await mem.searchEntities("Google", "organization");

// Create/update entity (idempotent)
const entity = await mem.upsertEntity("Google", {
  entityType: "organization",
  summary: "Technology company",
});

// Entity graph and timeline
const graph = await mem.entityGraph(entity.id, 2);
const timeline = await mem.entityTimeline(entity.id);

// Create typed relationships
await mem.upsertEntityEdge(1, 2, "works_at", {
  eventTime: "2024-01-15T00:00:00Z",
  confidence: 0.95,
});

// Cross-layer links
await mem.linkRecordEntity(42, entity.id, "mentions");
const linked = await mem.getRecordEntities(42);
```

### Batch Operations

```typescript
// Fetch content for multiple records (max 100, eliminates N+1)
const contents = await mem.batchReadContent([1, 2, 3, 4, 5]);

// Bulk status update
await mem.batchUpdateStatus([10, 11, 12], "archived");
```

### File Upload

```typescript
// Upload document for async ingestion (PDF, images, DOCX, etc.)
const upload = await mem.uploadFile("report.pdf", base64Content, "session-1");

// Poll processing status
const status = await mem.uploadStatus(upload.id);
console.log(status.status); // "processing" | "completed" | "failed"
```

### Temporal Versioning

```typescript
// Mark old record as superseded (keeps history, search prefers new)
await mem.supersede(42, 99);
```

### Session Management

```typescript
// Current session
console.log(mem.sessionId);

// Start new session
await mem.newSession();

// List all sessions with stats
const sessions = await mem.listSessions();

// Full session history (paginated)
const history = await mem.getSessionHistory("session-uuid", 50, 0);

// Thematic clusters within a session
const clusters = await mem.getSessionClusters("session-uuid");
```

### Record CRUD

```typescript
// Update fields
await mem.update(42, { summary: "Updated", score: 8, status: "archived" });

// Hard delete
await mem.delete(42);
```

### Query Builder (AST)

Structured filtering via `POST /api/v1/query`. Use `QueryBuilder` to compile an AST, then `Memory.query()` or `.execute()`:

```typescript
import { Memory, QueryBuilder } from "anhurdb";

const mem = new Memory({ apiKey: "anhur_xxx" });

// Fluent builder
const { records } = await new QueryBuilder()
  .where("type", "$eq", "risk")
  .where("score", "$gte", 7)
  .orderBy("weight", "desc")
  .limit(20)
  .execute(mem);

// Or build + query separately
const ast = new QueryBuilder()
  .whereEquals("status", "saved")
  .limit(10)
  .build();
const result = await mem.query(ast);
```

Supported operators: `$eq`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`.

## API Reference

### `new Memory(options)`

| Option     | Type     | Default                     | Description                     |
|------------|----------|-----------------------------|---------------------------------|
| `apiKey`   | `string` | *required*                  | Your AnhurDB API key            |
| `url`      | `string` | `https://anhurdb.yoven.ai`  | Server URL                      |
| `userId`   | `string` | derived from apiKey hash    | User/agent identifier           |
| `tenantId` | `string` | —                           | Tenant ID (multi-tenant setups) |

### Methods Summary

| Category | Methods |
|----------|---------|
| **Core** | `add`, `search`, `profile` |
| **Search** | `searchByType`, `smartSearch`, `recall`, `recent`, `query` |
| **Graph** | `walk`, `walkSemantic`, `getContext`, `readContent` |
| **Entity** | `searchEntities`, `upsertEntity`, `entityGraph`, `entityTimeline`, `upsertEntityEdge`, `linkRecordEntity`, `getRecordEntities` |
| **Batch** | `batchReadContent`, `batchUpdateStatus` |
| **Upload** | `uploadFile`, `uploadStatus` |
| **Temporal** | `supersede` |
| **Session** | `newSession`, `listSessions`, `getSessionHistory`, `getSessionClusters` |
| **CRUD** | `update`, `delete` |

## Types

All TypeScript interfaces are exported:

```typescript
import type {
  MemoryOptions, MemoryType, MemoryStatus,
  AddOptions, AddResult, SearchOptions, SearchResult, ProfileResult,
  MemoryRecord, WalkResult, ContextResult, SessionStats,
  EntityRecord, EntityEdge, EntityGraphResult, EntityTimelineResult,
  UpsertEntityOptions, UpsertEntityEdgeOptions,
  UploadResult, UploadStatusResult, BatchUpdateResult,
} from "anhurdb";
```

## Error Handling

```typescript
import { AnhurError, AnhurAuthError, AnhurQueryError, AnhurConnectionError } from "anhurdb";

try {
  await mem.add("something");
} catch (err) {
  if (err instanceof AnhurAuthError) {
    console.error("Bad API key");
  } else if (err instanceof AnhurConnectionError) {
    console.error("Server unreachable");
  } else if (err instanceof AnhurQueryError) {
    console.error("Invalid request:", err.message);
  }
}
```

## License

MIT
