# AnhurDB Go SDK

Official Go client for [AnhurDB](https://anhur.yoven.ai) — cognitive memory for AI agents.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point the SDK at `https://anhurdb.yoven.ai` (default).

**Zero external dependencies.** Uses only `net/http`, `crypto/sha256`, `encoding/json`, and other stdlib packages.

## Install

Module tags ship on [GitHub Releases](https://github.com/Yoven/AnhurDB-SDK/releases) (`v2/golang/vX.Y.Z`).

```bash
go get github.com/Yoven/AnhurDB-SDK/v2/golang/v2@v2.0.18
```

> The **source** in this tree is `2.1.0` (`client.Version`), converged with the
> TypeScript and Python SDKs. The pin above deliberately names the newest
> **published** tag: pinning a version that is not released yet would break
> `go get` for everyone who follows this README. It moves to `v2.1.0` when the
> tag is pushed. (It was stale at `v2.0.11` — seven releases behind — until
> 2026-09-05.)

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    anhurdb "github.com/Yoven/AnhurDB-SDK/v2/golang/v2"
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
    for _, h := range hits {
        fmt.Printf("%s (%.2f)\n", h.Summary, h.Similarity)
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
| `WithTenantID(id)` | Multi-tenant `X-Tenant-ID` header |
| `WithTimeout(d)` | HTTP client timeout (default: 30s) |

## API Reference

### Core Methods

```go
sessionID, err := mem.CreateSession(ctx)               // Required before writes
result, err := mem.Add(ctx, "text",
    anhurdb.WithMode("ingest"), anhurdb.WithSessionID(sessionID))
hits, err := mem.Search(ctx, "query", anhurdb.SessionsAll())          // Plane search (query=FTS text; prefer SmartSearch for conceptual RAG)
hits, err := mem.Search(ctx, "query", []string{sessionID}, anhurdb.WithLimit(20)) // One chat, with options
profile, err := mem.Profile(ctx)                       // User profile (?tag=)
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
outcome, err := mem.SearchWithSignals(ctx, "query", anhurdb.SessionsAll(),
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
raw, err := mem.SmartSearch(ctx, "query", anhurdb.SessionsAll(), 10)  // Prefer for conceptual text (weight-boosted FTS)
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
// Fetch content for multiple records.
// Nothing caps the slice client-side — the server decides. Batch it yourself if
// you are sending thousands; an empty slice is a 400 from the server.
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

// Poll processing status
status, err := mem.UploadStatus(ctx, upload.ID)
fmt.Println(status.Status) // "processing", "completed", "failed"
```

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
clusters, err := mem.GetSessionClusters(ctx, "uuid")       // Thematic clusters
localID := mem.NewSession()                                // Local rotate only (not registered)
_, err = mem.OpenSession(ctx)                              // Local generate + register
fmt.Println(mem.SessionID(), sessionID, localID)
fmt.Println(mem.ContainerTag())                            // Recall/profile tag (not a session)
```

### Record CRUD

```go
err = mem.Update(ctx, 42, map[string]interface{}{"summary": "Updated"})
err = mem.Delete(ctx, 42)
```

### AST Query (Query Builder)

Structured filtering via `POST /api/v1/query`. Build a `QueryRequest` directly or with the fluent `NewQuery()` helper:

```go
import (
    "context"
    anhurdb "github.com/Yoven/AnhurDB-SDK/v2/golang/v2"
    "github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
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
    case errors.Is(err, client.ErrConnectionFail):
        log.Fatal("Server unreachable")
    case errors.Is(err, client.ErrEmptyInput):
        log.Fatal("Empty text")
    default:
        var apiErr *client.APIError
        if errors.As(err, &apiErr) {
            log.Printf("HTTP %d: %s", apiErr.StatusCode, apiErr.Body)
        }
    }
}
```

## DSPy/LangChain Integration

The SDK includes a retriever adapter for Go agentic frameworks:

```go
import "github.com/Yoven/AnhurDB-SDK/v2/golang/v2/integrations/dspy"

retriever := dspy.NewRetriever(mem, 10)
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
