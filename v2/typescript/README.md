# anhurdb

TypeScript SDK for [AnhurDB](https://anhur.yoven.ai) — cognitive memory for AI agents.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point the SDK at `https://anhurdb.yoven.ai` (default).

Zero runtime dependencies. Works with Node 18+, Deno, Bun, and Cloudflare Workers.

## Quickstart

```typescript
import { Memory, sessionsAll } from "anhurdb";

const mem = new Memory({ apiKey: "anhur_xxx" });
const sessionId = await mem.createSession();
await mem.add("I'm a data scientist at Google working on NLP", {
  mode: "ingest",
  sessionId,
});
// Reads do not need createSession, but `sessions` is mandatory (ADR-0014):
// sessionsAll() = every session in scope, or pass [sessionId] for one chat.
const results = await mem.search("what does this user do?", sessionsAll());
```

## Installation

Tarballs ship on [GitHub Releases](https://github.com/Yoven/AnhurDB-SDK/releases) (`v2/typescript/vX.Y.Z`).

```bash
npm install \
  https://github.com/Yoven/AnhurDB-SDK/releases/download/v2/typescript/v4.0.0/anhurdb-4.0.0.tgz
```

> The pin above names the `v2/typescript/v4.0.0` tarball, which matches
> `SDK_VERSION` in this tree. Go and Python ship the same number.
>
> **4.0.0 is breaking (legacy removal).** `updateConsolidateIds` → `linkConsolidated`;
> `AddResult.mode` is `"ingest" | "records"` (was `"cloud" | "oss"`); `add()` no
> longer falls back to `/records` when `/ingest` answers 404; typed refusals are
> recognised by their `code` only; `DeleteFileResult.raft_index`,
> `SearchSessionPayload` and the `"pending"` record status are gone. See `CHANGELOG.md`.
>
> **3.0.0 is breaking.** `create()` takes the session as its first argument,
> `SessionStats.last_active` is really `last_activity`, and `UploadResult`,
> `WalkResult` and `ProfileResult` gained the fields the server actually sends.
> Every break is compile-time. See `CHANGELOG.md`.

## Usage

### Initialize

```typescript
import { Memory, sessionsAll } from "anhurdb";

// Cloud (default)
const mem = new Memory({ apiKey: "anhur_xxx" });

// Self-hosted
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

### Core — Session, Add, Search, Profile

```typescript
const sessionId = await mem.createSession(); // required before writes
await mem.add("User prefers dark mode", { mode: "ingest", sessionId });
await mem.add("Revenue hit $1M this quarter", {
  mode: "regular",
  score: 9,
  type: "fact",
  sessionId,
});

// Search across every session in scope (reads do not need createSession)
const results = await mem.search("user preferences?", sessionsAll(), { limit: 5 });
for (const r of results) {
  console.log(`${r.record.summary} (similarity: ${r.similarity})`);
}

// Get user profile (SDK sends GET /profile?tag=<container_tag>)
const profile = await mem.profile();
console.log(profile.static.facts);   // string[]
console.log(profile.stats.sessions); // number

// `tag` filters WITHIN your tenant — it never selects another one. An unknown
// tag is an EMPTY profile with HTTP 200, not an error.
const other = await mem.profile("some-other-agent");
```

### Search & Discovery

```typescript
// Tenant type filter only — not a Shared Data plane switch
const facts = await mem.searchByType("fact", sessionsAll(), 50);

// Prefer for conceptual text (weight-boosted FTS; search() sends query as FTS text)
const smart = await mem.smartSearch("engineering experience", sessionsAll(), 10);

// Plane-aware recall (SDK alias of search; default scope=sessions)
const broad = await mem.recall("engineering", sessionsAll(), 20);

// Most recent records
const latest = await mem.recent(5);
```

#### Ranking effort — `mode`, `semanticTimeoutMs`, `debugSignals` (2.1.0)

```typescript
// "fast" never embeds the query; "balanced" (the default) degrades to the
// lexical legs when the embedder is down; "semantic" refuses to answer
// without semantics.
const strict = await mem.search("quarterly risk", sessionsAll(), {
  mode: "semantic",
  semanticTimeoutMs: 1500,   // per-query budget; 0/unset = server default (700 ms)
});

// Research instrumentation: per-hit signals + pre-fusion per-leg scores.
const { results, retrieval, legScores } = await mem.searchWithRetrieval(
  "quarterly risk", sessionsAll(), { debugSignals: true });
console.log(retrieval?.mode, results[0]?.signals?.hnsw_rank, legScores);
```

`mode: "semantic"` is a promise the SERVER has to keep. An AnhurDB older than
ADR-0031 ignores the field, runs balanced and answers **HTTP 200 with lexical
results**. The SDK detects that from the response (`retrieval.mode`, which a
current server always fills) and **throws** rather than hand you degraded
results dressed as semantic ones. `semanticTimeoutMs` and `debugSignals`
degrade honestly, so those only log one warning.

An unknown `mode` is rejected before the request leaves the process — the
server folds anything it does not recognise into `balanced`, so a typo would
otherwise be invisible.

One exception: on `scope: "shared_all"` the check can only warn. A current
server deliberately leaves `retrieval.mode` empty when it merges two planes,
so there is nothing to compare against. Use a single scope when you need the
strict-semantic guarantee verified.

### Graph Traversal

```typescript
// BFS graph walk
const graph = await mem.walk(42, 3);

// Semantic walk (vector-weighted edges). `graph.nodes` are FULL records;
// `graph.truncated` is sent by /walk only, so it is undefined here.
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
// Omit `depth` to get the SERVER's default of 1 — the SDK sends no param.
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
// The server acks with a message only — it does NOT report a row count.
const ack = await mem.batchUpdateStatus([10, 11, 12], "archived");
console.log(ack.message); // "marked consolidated"
```

### File Upload

```typescript
// Upload document for async ingestion (PDF, images, DOCX, etc.)
// Chat: session + linked episodic (has_file=true on the episodic)
const upload = await mem.uploadFile("report.pdf", fileBytes, {
  sessionId,
  linkedEpisodicId: episodicId,
});
// Shared Data (tenant):
await mem.uploadFile("handbook.pdf", fileBytes, { mode: "tenant_shared" });

// Poll processing status. The polling key is `record_id` — there is no `id`
// on the upload ack (handler/upload.go:109-119 sends a fixed nine-key map).
// `upload.scope` is the plane the file landed in (SDK-filled: tenant_shared by
// default, client_shared, or sessions for chat) — record ids are per store.
const status = await mem.uploadStatus(upload.record_id!, { scope: upload.scope });
console.log(status.status); // "processing" | "completed" | "failed"
const final = await mem.waitForUpload(upload.record_id!, { scope: upload.scope });
```

Without a scope the server looks in every plane you can upload to and throws
`AnhurUploadStatusAmbiguousError` (HTTP 409) when the id is a file in more than one.

### Temporal Versioning

```typescript
// Mark old record as superseded (keeps history, search prefers new)
await mem.supersede(42, 99);
```

### Session Management

```typescript
// Register a write session (required before add/create)
const sessionId = await mem.createSession();
console.log(mem.sessionId); // server-registered id

// Local rotate + register in one call
await mem.openSession();

// Local id only (does NOT register — call createSession after)
await mem.newSession();

// List all sessions with stats (auto-paged). The freshness key is
// `last_activity`; `types` is a per-type histogram.
const sessions = await mem.listSessions();
console.log(sessions[0].last_activity, sessions[0].types);

// Full session history (paginated)
const history = await mem.getSessionHistory("session-uuid", 50, 0);

// Thematic clusters within a session
const clusters = await mem.getSessionClusters("session-uuid");
```

### Typed create (POST /records, no extraction)

```typescript
// The session is the FIRST argument and is REQUIRED — 3.0.0 removed the
// silent fallback to the client's ambient session.
const created = await mem.create(sessionId, "Ship the parity release", {
  type: "decision",
  score: 9,
  status: "saved",
  relatedIds: [anchorEpisodicId],
  validFrom: "2026-09-14T00:00:00Z",
  metadata: { origin: "release-notes" },
});
```

### Record CRUD

```typescript
// Update fields
await mem.update(42, { summary: "Updated", status: "archived" });
// metadata is a merge patch: only the named keys change, null deletes a key.
await mem.update(42, { metadata: { reviewed: true, draft_of: null } });
await mem.setScore(42, 8); // score NAO passa pelo update — ver nota abaixo

// Archive (soft delete)
await mem.delete(42);
```

`metadata` on `update` is an RFC 7396 JSON merge patch (AnhurDB ADR-0035): keys
present overwrite, a `null` value deletes the key (`undefined` does not — JSON
drops it), keys you do not name are left untouched, nested objects merge,
arrays are replaced whole. Pass a JSON string or a plain object (encoded with
`null` kept; `NaN`/`Infinity` throw instead of silently becoming a delete). A
patch that is not a JSON object — or stored metadata that is not one — is
refused with 400. There is no need to read, merge and write back the whole
object.

### By-id scope (records from a shared plane, ADR-0029)

A record id is a rowid **per store**. An id that came from a shared-plane
search or upload must be read and changed in THAT store, or the call addresses
an unrelated record with the same number in your chat store. Every by-id method
takes `{ scope }` as its last argument: `get`, `readContent`, `getContext`,
`getGrounding`, `batchReadContent`, `update`, `delete`, `supersede`,
`uploadStatus` (and `waitForUpload`). Omitted = your own store (unchanged).

| `scope` | Store |
|---|---|
| `"sessions"` | your chat store, shared-* rows excluded |
| `"tenant_shared"` | tenant library `{c}_{t}_lib` (uploads after the L2 switch) |
| `"tenant_shared_legacy"` | pre-migration library rows in `{c}_{t}` |
| `"client_shared"` | client library `{c}_shared` |

```typescript
import { sessionsAll, tenantSharedScopeForHit } from "anhurdb";

const hits = await mem.search("refund policy", sessionsAll(), { scope: "tenant_shared" });
const hit = hits[0].record;
// one tenant_shared search reads BOTH library stores: pick the store per hit
const body = await mem.readContent(hit.id, { scope: tenantSharedScopeForHit(hit.uuid) });
```

`shared_all` (or any unknown value) is refused locally (`AnhurQueryError`,
`invalid_request`). A row outside the named plane is HTTP 404, exactly like a
missing id; `batchReadContent` omits it.

`deleteFile(session, prefix, { scope: "tenant_shared" })` deletes in both
library stores (session may be empty) and fills `result.legs`;
`{ scope: "client_shared" }` targets the client library. A failure in one
library store throws `AnhurLibraryDeletePartialError` with `.legs`; retrying the
same call is safe (idempotent).

When nothing matched (`matched_count === 0`), `result.alternative_sessions`
lists the sessions of the same store that DO hold that prefix
(`FileSessionCandidate { session_uuid, matched_count, root_checksum?, root_filename? }`)
— check `root_checksum` and re-issue `deleteFile` with the right session.
`purge` tells whether the delete is final (true, shared plane) or a restorable
tombstone (always sent).

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

Supported operators: `$eq`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`. There is no
`$ne`, `$or`, `$and` or `$not`: `filters` is flat and every predicate is ANDed.

The builder refuses, before sending anything, what the server would otherwise
answer misleadingly:

| Rejected client-side | Why |
|---|---|
| a column or operator outside the whitelist | server 400, caught a round trip earlier |
| a sort direction other than `asc`/`desc` | the server does **not** reject it — it silently sorts `DESC` and answers 200 |
| a `null` filter value (including inside `$in`) | SQL never matches `col = NULL`, so the query returns zero rows for every input while answering 200 — and the grammar has no `$exists`/`$ne`/`IS NULL` to express it instead |
| `$in` with an empty array | server 400, caught a round trip earlier |
| `limit` outside 1..1000, negative `offset` | server clamps silently |

Every one of these throws `AnhurQueryError` with `kind: "invalid_request"` and
`retryable: false` — the same shape a server HTTP 400 arrives as, so one `catch`
block handles both. Client-side rejections carry no `statusCode`, because no
request was sent.

## API Reference

### `new Memory(options)`

| Option     | Type     | Default                     | Description                     |
|------------|----------|-----------------------------|---------------------------------|
| `apiKey`   | `string` | *required*                  | Your AnhurDB API key            |
| `url`      | `string` | `https://anhurdb.yoven.ai`  | Server URL                      |
| `userId`   | `string` | derived from apiKey hash    | User/agent identifier           |
| `tenantId` | `string` | —                           | Tenant ID (multi-tenant setups); `[A-Za-z0-9_-]`, ≤ 64, else the constructor throws `AnhurQueryError` |

### Methods Summary

| Category | Methods |
|----------|---------|
| **Core** | `add`, `search`, `searchWithRetrieval`, `profile` |
| **Search** | `searchSessions`, `searchTenantShared`, `searchClientShared`, `searchShared`, `searchSession`, `searchByType`, `smartSearch`, `recall`, `recent`, `query` |
| **Identity** | `sessionId`, `getSessionId`, `containerTag`, `getContainerTag` |
| **Graph** | `walk`, `walkSemantic`, `getContext`, `readContent` |
| **Entity** | `searchEntities`, `upsertEntity`, `entityGraph`, `entityTimeline`, `upsertEntityEdge`, `linkRecordEntity`, `getRecordEntities` |
| **Batch** | `batchReadContent`, `batchUpdateStatus` |
| **Upload** | `uploadFile`, `uploadStatus` |
| **Temporal** | `supersede` |
| **Session** | `createSession`, `openSession`, `newSession`, `listSessions`, `getSessionHistory`, `getSessionClusters` |
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
  SearchMode, RetrievalMeta, SearchHitSignals, LegScoreSummary,
  SearchWithRetrievalResult, WalkTarget, WalkSemanticOptions,
  AnhurErrorKind, QueryParams,
} from "anhurdb";
```

## Client key API (child clients, agent profile, impersonation — ADR-0034)

`ClientAPI` takes a **client key** (kind=client), never a tenant key. It talks
only to `/api/v1/client/*` and **never sends `X-Tenant-ID`** (the tenant
travels in the body).

```ts
import { ClientAPI } from "anhurdb";

const api = new ClientAPI({ clientKey, url: "http://localhost:8000" });
const child = await api.createChild(accountRef); // created=true once, then false with the same id
await api.createChildTenant(child.child_client_id, {
  tenantId: "contabilidade",
  profile: { config: { search_top_k: "8" }, maxRpm: 1200 },
});
const token = await api.impersonateChild(child.child_client_id, {
  tenantId: "contabilidade", scope: "write", l1Only: true, ttlSeconds: 3600,
});
// token.api_key, token.key_id, token.scope, token.l1_only, token.expires_at (Date)
await api.revokeImpersonation(token.key_id);
```

| Method | Route |
|---|---|
| `createChild(externalRef)` | `POST /api/v1/client/children` |
| `listChildren()` | `GET /api/v1/client/children` |
| `suspendChild(childId)` / `resumeChild(childId)` | `POST /api/v1/client/children/{child_id}/suspend` / `resume` |
| `listTenants()` / `listChildTenants(childId)` | `GET /api/v1/client/tenants` / `GET /api/v1/client/children/{child_id}/tenants` |
| `createTenant(request)` / `createChildTenant(childId, request)` | `POST /api/v1/client/tenants` / `POST /api/v1/client/children/{child_id}/tenants` |
| `impersonate(request)` / `impersonateChild(childId, request)` | `POST /api/v1/client/impersonate` / `POST /api/v1/client/children/{child_id}/impersonate` |
| `revokeImpersonation(keyId)` | `POST /api/v1/client/impersonate/revoke` |

**Trust the echo, not the request.** The server lowers a scope above the
client key's ceiling without an error, so always read `token.scope` and
`token.l1_only`. The SDK fails closed when the echo is WEAKER than requested:
`AnhurClientAPIResponseError` with `code` `l1_only_not_honoured` or
`scope_not_honoured` (and `keyId` of the minted token, so you can revoke it).
A lower scope than requested is accepted.

**Errors.** A refusal is `AnhurClientAPIError` (extends `AnhurError`) with
`statusCode`, `code` (= the server's `body.error`: `child_suspended`,
`child_not_found`, `child_limit`, `tenant_limit`, `invalid_scope`,
`invalid_tenant_id`, `invalid_profile`, `invalid_child_id`, `key_not_found`,
`key_not_owned`, …), `detail`, `errorType` and `isNotLeader`. `kind` follows
the status; `child_limit`/`tenant_limit` are hard caps (`retryable` false).
The SDK never retries. Inputs that can never succeed (empty ref/tenant/key id,
tenant `*`, non-canonical child id, unknown scope, child impersonate without
scope, non-integer or negative TTL, `maxRpm < 1`) throw `AnhurQueryError`
(400) before any request.

## Error Handling

```typescript
import { AnhurError, AnhurAuthError, AnhurQueryError, AnhurConnectionError } from "anhurdb";

try {
  const sessionId = await mem.createSession();
  await mem.add("something", { mode: "ingest", sessionId });
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

Every 401/403 throws `AnhurAuthRefusedError` — it extends `AnhurAuthError`, so
`instanceof AnhurAuthError` keeps working — with `statusCode`, `code` (the
server's `body.error`, verbatim, read whatever the Content-Type), `detail` and
`errorType`. A scope refusal no longer reads as an invalid key:

```typescript
import { AnhurAuthRefusedError } from "anhurdb";

try {
  await mem.add("turn", { mode: "ingest", sessionId });
} catch (err) {
  if (err instanceof AnhurAuthRefusedError) {
    if (err.isL1Only) { /* 403 l1_only: token restricted to L1 sessions */ }
    else if (err.isChildSuspended) { /* 403 child_suspended */ }
    else if (err.isInsufficientScope) { /* 403 "insufficient scope — ..." */ }
    else if (err.isImpersonationRequired) { /* 403 client_key_requires_impersonation */ }
    else if (err.isInvalidKey) { /* 401 "invalid or inactive API key" */ }
  }
}
```

Message: `Authentication failed (HTTP <s>): <code>[: <detail>]` (identical in Go
and Python). A body that is not a JSON object with a string `error` keeps
`Authentication failed (HTTP <s>)`, with `code === ""`. Not retryable. This SDK
has no gRPC transport.

A write into a full session throws `AnhurSessionRecordLimitError` (extends
`AnhurError`, HTTP 409, `retryable === false`) when the server answers
`"code": "session_record_limit"`; `sessionUuid`, `limit` and `count` are always
set. A 409 without that code is a plain conflict `AnhurError`. Keep the data and
retry later — never write it into another session to dodge the cap:

```typescript
import { AnhurSessionRecordLimitError } from "anhurdb";

try {
  await mem.add("turn", { mode: "ingest", sessionId });
} catch (err) {
  if (err instanceof AnhurSessionRecordLimitError) {
    console.error(`session ${err.sessionUuid} is full: ${err.count}/${err.limit}`);
  }
}
```

### Tenant library collision (`AnhurTenantLibraryCollisionError`)

When your tenant library store id collides with another registered tenant's
store (a legacy tenant named `<t>_lib`), the server refuses every tenant_shared
search, by-id call, upload and delete: HTTP 409 with `"code":
"tenant_library_collision"`. The SDK recognises it by that code only and throws
`AnhurTenantLibraryCollisionError`, **non-retryable**; it heals only when an
operator renames one of the tenants. It is never the session-record-limit 409.
An unreadable tenant registry is HTTP 503 (upload included) — the ordinary
retryable `AnhurError` with kind `unavailable`.

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
