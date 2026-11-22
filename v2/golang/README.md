# AnhurDB Go SDK

Official Go client for [AnhurDB](https://anhur.yoven.ai) — cognitive memory for AI agents.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point the SDK at `https://anhurdb.yoven.ai` (default).

**Zero external dependencies.** Uses only `net/http`, `crypto/sha256`, `encoding/json`, and other stdlib packages.

## Install

Module tags ship on [GitHub Releases](https://github.com/Yoven/AnhurDB-SDK/releases) (`v2/golang/vX.Y.Z`).

```bash
go get github.com/Yoven/AnhurDB-SDK/v2/golang/v4@v4.1.0
```

> This tree is **4.1.0** (`client.Version`); TypeScript and Python ship the same
> number. `v2/golang/v4.0.0` (2026-11-21) is the previous tag on the same `/v4`
> path; `v2/golang/v3.0.1` is the last tag on the old `/v3` path.
>
> **The module path moved to `/v4` in 4.0.0** (it moved to `/v3` in 3.0.0). It
> contains TWO `v2`-looking segments: `github.com/Yoven/AnhurDB-SDK/v2/golang/v4`.
> The first `v2` is this repository's directory layout — it never changes. The
> trailing one is the Go major-version suffix. Bumping only the version,
> `.../golang/v3@v4.1.0`, resolves to nothing: Go refuses a v4+ version on a
> module path with no matching major suffix, and the error names the path, not
> the version, so it reads like the module is missing. Migrate imports with
> `sed -i 's#golang/v3#golang/v4#g'`.
>
> **4.0.0 is breaking — legacy removal.** Every branch that existed only for an
> older AnhurDB server is gone (the `/ingest` 404 → `/records` fallback, bare-array
> responses, sentence-matched 409s), as are the aliases `UpdateConsolidateIDs`,
> `RecentMemories`, `SearchWithSignals`, `models.StatusPending` and the
> never-sent `DeleteFileResult.RaftIndex`. `AddResult.Mode` is now `"ingest"` or
> `"records"`. See `CHANGELOG.md`.

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    anhurdb "github.com/Yoven/AnhurDB-SDK/v2/golang/v4"
)

func main() {
    ctx := context.Background()
    mem := anhurdb.NewMemory("anhur_xxx")

    // Register a write session (required before Add/Create)
    sessionID, _ := mem.CreateSession(ctx)
    result, _ := mem.Add(ctx, "I'm a data scientist at Google",
        anhurdb.WithMode("ingest"), anhurdb.WithSessionID(sessionID))

    // Reads do not need CreateSession
    hits, _ := mem.Search(ctx, "what does this user do?", anhurdb.SessionsAll())
    for _, hit := range hits {
        // A hit is {Record, Similarity}: the summary lives on the RECORD, and
        // Similarity is its SIBLING, never a field inside it.
        fmt.Printf("%s (%.2f)\n", hit.Record.Summary, hit.Similarity)
    }

    // Get user profile (SDK sends GET /profile?tag=<container_tag>)
    profile, _ := mem.Profile(ctx)
    fmt.Println(profile.Static)
    _ = result
}
```

## Constructor Options

```go
// Cloud (default)
mem := anhurdb.NewMemory("anhur_xxx")

// Self-hosted
mem := anhurdb.NewMemory("key", anhurdb.WithURL("http://localhost:8000"))

// With explicit user ID and tenant
mem := anhurdb.NewMemory("key",
    anhurdb.WithUserID("user-123"),
    anhurdb.WithTenantID("tenant-a"),
    anhurdb.WithTimeout(60 * time.Second),
)
```

| Option | Description |
|--------|-------------|
| `WithURL(url)` | Server URL (default: `https://anhurdb.yoven.ai`) |
| `WithUserID(id)` | Explicit container tag (default: derived from API key hash) |
| `WithTenantID(id)` | Multi-tenant `X-Tenant-ID` header. Must be `[A-Za-z0-9_-]`, ≤ 64 chars; otherwise every call returns an invalid_request `*APIError` and nothing is sent |
| `WithTimeout(d)` | HTTP client timeout (default: 30s) |
| `client.WithHTTPClient(c)` | Bring your own `*http.Client` (transport, proxy, TLS). Not re-exported on the facade |

## API Reference

### Core Methods

```go
sessionID, err := mem.CreateSession(ctx)               // Required before writes
result, err := mem.Add(ctx, "text",
    anhurdb.WithMode("ingest"), anhurdb.WithSessionID(sessionID))
hits, err := mem.Search(ctx, "query", anhurdb.SessionsAll())          // Hybrid plane search (lexical + server-side embedding) — the conceptual entry point
hits, err := mem.Search(ctx, "query", []string{sessionID}, anhurdb.WithLimit(20)) // One chat, with options
profile, err := mem.Profile(ctx)                       // User profile (?tag=)
```

### Typed create (POST /records, no extraction)

`Add` without pins goes to `/ingest` (episodic + platform extraction). When you
already know the typed atom, `Create` writes exactly one record and runs no
extraction LLM. The session is the FIRST argument and is required.

```go
created, err := mem.Create(ctx, sessionID, "Ship the parity release",
    anhurdb.WithCreateType("decision"),
    anhurdb.WithCreateScore(9),
    client.WithCreateStatus("saved"),
    client.WithCreateValidFrom("2026-09-14T00:00:00Z"),
    client.WithCreateMetadata(map[string]interface{}{"origin": "release-notes"}),
)
```

### Search planes (`scope`) and the session filter (ADR-0014)

`sessions` is a **required** argument on every search: `anhurdb.SessionsAll()`
(`[]string{"*"}`) is every session in the plane, or pass up to 1000 explicit
session uuids. A nil/empty slice, or `"*"` mixed with uuids, is refused locally
with an `INVALID_PARAM` `*APIError` before any request.

```go
// sessions (default) | tenant_shared | client_shared | shared_all
hits, err := mem.Search(ctx, "refund policy", anhurdb.SessionsAll(), client.WithScope("tenant_shared"))
hits, err = mem.SearchTenantShared(ctx, "refund policy", anhurdb.SessionsAll()) // tenant Shared Data library
hits, err = mem.SearchClientShared(ctx, "refund policy", anhurdb.SessionsAll()) // client-wide library
hits, err = mem.SearchShared(ctx, "refund policy", anhurdb.SessionsAll())       // both shared planes (shared_all)
hits, err = mem.Search(ctx, "refund policy", []string{sessionID})               // one chat
```

### Search controls (ADR-0031)

```go
// Retrieval budget for ONE query. Not the same as WithMode, which picks the
// WRITE path for Add.
hits, err := mem.Search(ctx, "query", anhurdb.SessionsAll(),
    anhurdb.WithSearchMode(anhurdb.SearchModeSemantic))  // fast | balanced | semantic

// Cap the Embed+HNSW wait for this query (0 = the server's own 700ms budget).
hits, err = mem.Search(ctx, "query", anhurdb.SessionsAll(),
    anhurdb.WithSemanticTimeoutMs(250))

// Ask for per-hit signals + per-leg score distributions, and read all three
// parts of the outcome in one struct.
outcome, err := mem.SearchWithRetrieval(ctx, "query", anhurdb.SessionsAll(),
    anhurdb.WithDebugSignals())
_ = outcome.Results       // []client.SearchResult
_ = outcome.Retrieval     // *client.RetrievalMeta — which arms ran, degraded?
_ = outcome.LegScores     // []client.LegScoreSummary — pre-fusion, per leg
```

`mode=semantic` is a **promise**: the server answers 503/504 rather than quietly
returning lexical results. A server older than ADR-0031 ignores the field
entirely and answers 200 with balanced results, so the SDK verifies the promise
against the response and returns a `SERVER_TOO_OLD: ...` error when it was not
kept. The other two controls only log a warning when ignored. On
`scope=shared_all` the server cannot echo a single honest mode (two legs), so
that one case warns instead of failing.

An unknown mode is refused before the request leaves:
`INVALID_PARAM: 'mode' "x" is not supported; use "fast", "balanced" or "semantic"`.

### Search & Discovery

```go
hits, err := mem.SearchByType(ctx, "fact", anhurdb.SessionsAll(), 50) // Tenant type filter only — not a Shared Data plane switch
raw, err := mem.SmartSearch(ctx, "query", anhurdb.SessionsAll(), 10)  // Lexical only (weight-boosted FTS, no embedding) — keywords/identifiers
hits, err := mem.Recall(ctx, "query", anhurdb.SessionsAll(), 20)      // Same engine as Search, MCP naming
records, err := mem.Recent(ctx, 5)                     // Most recent
```

### Graph Traversal

```go
walk, err := mem.Walk(ctx, 42, 3)                      // BFS traversal
walk, err := mem.WalkSemantic(ctx, 42, 3)              // Vector-weighted
topo, err := mem.GetContext(ctx, 42)                    // 1-hop neighbors
content, err := mem.ReadContent(ctx, 42)               // Full payload
```

### Entity Knowledge Graph

> **Entity ≠ record type.** `record.type` (`episodic`, `fact`, …) classifies the memory node.
> Entities (`person`, `organization`, …) are Layer 2 for cross-cutting search; `LinkRecordEntity` is the tag.

```go
// Search and create entities
entities, err := mem.SearchEntities(ctx, "Google", "organization", 20)
entity, err := mem.UpsertEntity(ctx, "Google", "organization", "Tech company", nil)

// Entity graph and timeline
graph, err := mem.EntityGraph(ctx, entity.ID, 2)
timeline, err := mem.EntityTimeline(ctx, entity.ID)

// Create typed relationships
err = mem.UpsertEntityEdge(ctx, 1, 2, "works_at",
    client.WithConfidence(0.95),
    client.WithEventTime("2024-01-15T00:00:00Z"),
)

// Cross-layer links
err = mem.LinkRecordEntity(ctx, 42, entity.ID, "mentions")
entities, err := mem.GetRecordEntities(ctx, 42)
```

### Batch Operations

```go
// Fetch content for multiple records (max 100 ids per call).
// The SDK does not cap the slice; the SERVER answers 400 "too many IDs (max 100)"
// above 100 and 400 for an empty slice. Chunk larger lists yourself.
record, err := mem.Get(ctx, 42)                    // one record by id
contents, err := mem.BatchReadContent(ctx, []int64{1, 2, 3})

// Bulk status update
err = mem.BatchUpdateStatus(ctx, []int64{10, 11}, "archived")
```

### File Upload

```go
// Upload document for async ingestion
// Chat: session + linked episodic (has_file=true on the episodic)
upload, err := mem.UploadFile(ctx, "report.pdf", rawBytes, sessionID, episodicID)
// Shared Data (tenant):
upload, err = mem.UploadFile(ctx, "handbook.pdf", rawBytes, "", 0, anhurdb.WithUploadMode("tenant_shared"))

// Poll processing status IN the plane the file landed in (upload.Scope is filled
// by UploadFile: tenant_shared by default, client_shared, or sessions for chat).
status, err := mem.UploadStatus(ctx, upload.RecordID, client.WithRecordScope(upload.Scope))
fmt.Println(status.Status) // "processing", "completed", "failed"
final, err := mem.WaitForUpload(ctx, upload.RecordID, client.WaitUploadOptions{Scope: upload.Scope})
```

Without a scope the server looks for the file in every plane you can upload to
and answers `*client.UploadStatusAmbiguousError` (HTTP 409) when the id is a
file in more than one — record ids are per store.

### Temporal Versioning

```go
// Mark old record as superseded (keeps history, search prefers new)
err = mem.Supersede(ctx, 42, 99)
```

### Session Management

```go
sessionID, err := mem.CreateSession(ctx)                   // Required before Add/Create
sessions, err := mem.ListSessions(ctx)                     // All sessions with stats
history, err := mem.GetSessionHistory(ctx, "uuid", 50, 0)  // Paginated history
clusters, err := mem.GetSessionClusters(ctx, "uuid", 0, 0) // Thematic clusters (0, 0 = server defaults eps 0.45, min 3)
localID := mem.NewSession()                                // Local rotate only (not registered)
_, err = mem.OpenSession(ctx)                              // Local generate + register
fmt.Println(mem.SessionID(), sessionID, localID)
fmt.Println(mem.ContainerTag())                            // Recall/profile tag (not a session)
```

### Record CRUD

```go
err = mem.Update(ctx, 42, map[string]interface{}{"summary": "Updated"})
// metadata is a merge patch: only the named keys change, nil deletes a key.
err = mem.Update(ctx, 42, map[string]interface{}{
	"metadata": map[string]interface{}{"reviewed": true, "draft_of": nil},
})
err = mem.SetScore(ctx, 42, 8) // score is NOT accepted by Update — see the note at the end
err = mem.Delete(ctx, 42)       // soft delete: archived, hidden from reads, not erased

// Delete every record of one uploaded file (by its <checksum16> ingest-key prefix).
// WithDeleteFileDryRun(true) reports MatchedCount without deleting.
result, err := mem.DeleteFile(ctx, sessionID, "3f2a9c0d1e4b5a67")
fmt.Println(result.MatchedCount, result.DeletedCount, result.Purge)
```

`metadata` on `Update` is an RFC 7396 JSON merge patch (AnhurDB ADR-0035): keys
present overwrite, a `nil`/null value deletes the key, keys you do not name are
left untouched, nested objects merge, arrays are replaced whole. Pass a JSON
string or a `map[string]interface{}` (encoded with `nil` kept as null). A patch
that is not a JSON object — or stored metadata that is not one — is refused
with 400. There is no need to read, merge and write back the whole map.

### By-id scope (records from a shared plane, ADR-0029)

A record id is a rowid **per store**. An id that came from a shared-plane search
or upload must be read and changed in THAT store, or the call addresses an
unrelated record with the same number in your chat store. Every by-id method
takes `client.WithRecordScope(...)`: `Get`, `ReadContent`, `GetContext`,
`GetGrounding`, `BatchReadContent`, `Update`, `Delete`, `Supersede`,
`UploadStatus`. No option = your own store (unchanged behaviour).

| Scope | Store |
|---|---|
| `RecordScopeSessions` | your chat store, shared-* rows excluded |
| `RecordScopeTenantShared` | tenant library `{c}_{t}_lib` (uploads after the L2 switch) |
| `RecordScopeTenantSharedLegacy` | pre-migration library rows in `{c}_{t}` |
| `RecordScopeClientShared` | client library `{c}_shared` |

```go
hits, _ := mem.SearchTenantShared(ctx, "refund policy", client.SessionsAll())
hit := hits[0].Record
// one tenant_shared search reads BOTH library stores: pick the store per hit
body, _ := mem.ReadContent(ctx, int64(hit.ID), client.WithRecordScope(client.TenantSharedScopeForHit(hit.UUID)))
```

`shared_all` (or any other value) is refused locally: an id names one store. A
row outside the named plane answers `ErrNotFound`, exactly like a missing id;
`BatchReadContent` omits it from the map.

`DeleteFile` takes `client.WithDeleteFileScope(client.RecordScopeTenantShared)`
(session may then be empty; the server deletes in both library stores and fills
`DeleteFileResult.Legs`) or `RecordScopeClientShared`. If one library store fails
the call returns `*client.LibraryDeletePartialError` with every leg's outcome;
retrying the same call is safe (idempotent).

When nothing matched (`MatchedCount == 0`), `DeleteFileResult.AlternativeSessions`
lists the sessions of the same store that DO hold that prefix
(`FileSessionCandidate{SessionUUID, MatchedCount, RootChecksum, RootFilename}`) —
check `RootChecksum` and re-issue `DeleteFile` with the right session. `Purge` tells
whether the delete is final (true, shared plane) or a restorable tombstone. There is
no `RaftIndex` (removed in 4.0.0 — the server never sends it).

### AST Query (Query Builder)

Structured filtering via `POST /api/v1/query`. Build a `QueryRequest` directly or with the fluent `NewQuery()` helper:

```go
import (
    "context"
    anhurdb "github.com/Yoven/AnhurDB-SDK/v2/golang/v4"
    "github.com/Yoven/AnhurDB-SDK/v2/golang/v4/client"
)

mem := anhurdb.NewMemory("anhur_xxx")
req := client.NewQuery().
    Where("type", client.QueryOp{Eq: "risk"}).
    Where("score", client.QueryOp{Gte: 7}).
    OrderBy("weight", "desc").
    Limit(20)
records, err := mem.Query(context.Background(), req)
```

Supported operators: `$eq`, `$gt`, `$gte`, `$lt`, `$lte`, `$in`. Filterable columns match the server whitelist (`type`, `score`, `weight`, `status`, `created_at`, etc.).

### Client key API (child clients, agent profile, impersonation — ADR-0034)

`client.NewClientAPI` takes a **client key** (kind=client), never a tenant key.
It talks only to `/api/v1/client/*` and **never sends `X-Tenant-ID`** (the
tenant travels in the body; `WithTenantID` is ignored here).

```go
// URL defaults to https://anhurdb.yoven.ai, like NewMemory; WithURL for self-hosted.
api := client.NewClientAPI(clientKey)

child, err := api.CreateChild(ctx, accountRef)        // 201 created / 200 same id (idempotent per ref)
_, err = api.CreateChildTenant(ctx, child.ChildClientID, client.CreateTenantRequest{
    TenantID: "contabilidade",
    Profile:  &client.AgentProfile{Config: map[string]string{"search_top_k": "8"}, MaxRPM: &maxRPM},
})
token, err := api.ImpersonateChild(ctx, child.ChildClientID, client.ImpersonateRequest{
    TenantID: "contabilidade", Scope: client.KeyScopeWrite, L1Only: true, TTL: time.Hour,
})
// token.APIKey, token.KeyID, token.Scope, token.L1Only, token.ExpiresAt
_, err = api.RevokeImpersonation(ctx, token.KeyID)
```

| Method | Route |
|---|---|
| `CreateChild(ctx, externalRef)` | `POST /api/v1/client/children` |
| `ListChildren(ctx)` | `GET /api/v1/client/children` |
| `SuspendChild(ctx, childID)` / `ResumeChild(ctx, childID)` | `POST /api/v1/client/children/{child_id}/suspend` / `resume` |
| `ListTenants(ctx)` / `ListChildTenants(ctx, childID)` | `GET /api/v1/client/tenants` / `GET /api/v1/client/children/{child_id}/tenants` |
| `CreateTenant(ctx, req)` / `CreateChildTenant(ctx, childID, req)` | `POST /api/v1/client/tenants` / `POST /api/v1/client/children/{child_id}/tenants` |
| `Impersonate(ctx, req)` / `ImpersonateChild(ctx, childID, req)` | `POST /api/v1/client/impersonate` / `POST /api/v1/client/children/{child_id}/impersonate` |
| `RevokeImpersonation(ctx, keyID)` | `POST /api/v1/client/impersonate/revoke` |

**Trust the echo, not the request.** The server lowers a scope above the
client key's ceiling without an error, so always read `token.Scope` and
`token.L1Only`. The SDK fails closed when the echo is WEAKER than requested:
l1_only requested but not echoed true → `ErrL1OnlyNotHonoured`; echoed scope
unknown or higher than requested → `ErrScopeNotHonoured` (both
`*client.ClientAPIResponseError`, whose `KeyID` names the minted token so you
can revoke it). A lower scope than requested is accepted.

**Errors.** A refusal is `*client.ClientAPIError` with `Status`, `Code`
(= the server's `body.error`: `child_suspended`, `child_not_found`,
`child_limit`, `tenant_limit`, `invalid_scope`, `invalid_tenant_id`,
`invalid_profile`, `invalid_child_id`, `key_not_found`, `key_not_owned`, …),
`Detail` and `ErrorType` (`IsNotLeader()` for the provably-uncommitted 503).
`errors.Is` still matches `ErrUnauthorized` (401/403), `ErrNotFound` (404),
`ErrInvalidQuery` (400/422) and `ErrServerError` (5xx). `child_limit` and
`tenant_limit` are hard caps: `Retryable()` is false. The SDK never retries.
Inputs that can never succeed (empty ref/tenant/key id, tenant `*`,
non-canonical child id, unknown scope, child impersonate without scope,
fractional TTL, `max_rpm < 1`) are refused locally with a 400 `*APIError`
before any request.

## Error Handling

```go
import "errors"

sessionID, err := mem.CreateSession(ctx)
if err != nil {
    log.Fatal(err)
}
result, err := mem.Add(ctx, "text",
    anhurdb.WithMode("ingest"), anhurdb.WithSessionID(sessionID))
if err != nil {
    switch {
    case errors.Is(err, client.ErrUnauthorized):
        log.Fatal("Bad API key")
    case errors.Is(err, client.ErrConnectionFail): // *TransportError, kind transport (since 4.1.0)
        log.Fatal("Server unreachable")
    case errors.Is(err, client.ErrEmptyInput):
        log.Fatal("Empty text")
    default:
        var transportErr *client.TransportError
        if errors.As(err, &transportErr) { // no HTTP response: transport or timeout
            log.Printf("%s (kind=%s, retryable=%v)", transportErr, transportErr.Kind(), transportErr.Retryable())
            return
        }
        var apiErr *client.APIError
        if errors.As(err, &apiErr) {
            log.Printf("HTTP %d: %s", apiErr.StatusCode, apiErr.Body)
        }
    }
}
```

A request that never produced an HTTP response is a `*client.TransportError`
(`StatusCode() == 0`). Kind `transport` (server unreachable) also matches
`errors.Is(err, client.ErrConnectionFail)`; kind `timeout` (deadline/cancel —
the server may still have processed it) does not, while
`errors.Is(err, context.DeadlineExceeded)` keeps working. Before 4.1.0 the
`ErrConnectionFail` branch never fired.

### Refused credential (HTTP 401/403): the server's reason

Every 401/403 is a `*client.AuthRefusedError` with `Status`, `Code` (the
server's `body.error`, verbatim, read whatever the Content-Type), `Detail` and
`ErrorType`. `errors.Is(err, client.ErrUnauthorized)` stays true for all of
them, so existing branches keep working. A scope refusal no longer reads as an
invalid key:

```go
var refused *client.AuthRefusedError
if errors.As(err, &refused) {
    switch {
    case refused.IsL1Only():                // 403 l1_only: token restricted to L1 sessions
    case refused.IsChildSuspended():        // 403 child_suspended
    case refused.IsInsufficientScope():     // 403 "insufficient scope — ..."
    case refused.IsImpersonationRequired(): // 403 client_key_requires_impersonation
    case refused.IsInvalidKey():            // 401 "invalid or inactive API key"
    }
}
```

Message: `Authentication failed (HTTP <s>): <code>[: <detail>]` (identical in
Python and TypeScript). A body that is not a JSON object with a string `error`
keeps the historical `authentication failed: invalid API key`, with `Code == ""`.
Not retryable. This SDK has no gRPC transport.

### Full session (HTTP 409, `session_record_limit`)

A write into a session that already holds its maximum number of records is
refused with a typed, **non-retryable** `*client.SessionRecordLimitError`,
recognised by `"code": "session_record_limit"` only (a 409 without the code is an
ordinary `*APIError`). Retrying the same
session gets the same answer; keep the data and retry later — never write it
into another session to dodge the cap.

```go
var limitErr *client.SessionRecordLimitError
if errors.As(err, &limitErr) { // or errors.Is(err, client.ErrSessionRecordLimit)
    // Limit and Count are plain ints: the server always sends both.
    log.Printf("session %s is full (limit=%d count=%d)", limitErr.SessionUUID, limitErr.Limit, limitErr.Count)
}
```

`errors.As(err, &apiErr)` still finds the underlying `*APIError` with
`StatusCode == 409`, so existing code keeps working.

### Tenant library collision (`*client.TenantLibraryCollisionError`)

When your tenant library store id collides with another registered tenant's
store (an existing tenant named `<t>_lib`), the server refuses every tenant_shared
search, by-id call, upload and delete: HTTP 409 with `"code":
"tenant_library_collision"`, which is the only thing the SDK matches (no sentence
matching, no 400 since 4.0.0). The SDK returns
a typed, **non-retryable** `*client.TenantLibraryCollisionError`
(`errors.Is(err, client.ErrTenantLibraryCollision)`); it heals only when an
operator renames one of the tenants. It is never confused with the session
record limit 409. An unreadable tenant registry is HTTP 503 (upload included) —
the ordinary retryable `*APIError` with `KindUnavailable`.

## DSPy/LangChain Integration

The SDK includes a retriever adapter for Go agentic frameworks:

```go
import "github.com/Yoven/AnhurDB-SDK/v2/golang/v4/integrations/dspy"

// sessions is required (ADR-0014): SessionsAll() opts into the whole scope.
retriever := dspy.NewRetriever(mem, 10, client.SessionsAll())
docs, err := retriever.GetRelevantDocuments(ctx, "user's role?")
```

## Thread Safety

`Memory` is safe for concurrent use. The underlying `http.Client` handles connection pooling.

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
