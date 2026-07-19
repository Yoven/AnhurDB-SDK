# AnhurDB Go SDK

Official Go client for [AnhurDB](https://anhur.yoven.ai) — cognitive memory for AI agents.

> **Open Beta:** get an API key in [ControlPlane](https://anhur.yoven.ai/app), then point the SDK at `https://anhurdb.yoven.ai` (default).

**Zero external dependencies.** Uses only `net/http`, `crypto/sha256`, `encoding/json`, and other stdlib packages.

## Install

Module tags ship on [GitHub Releases](https://github.com/Yoven/AnhurDB-SDK/releases) (`v2/golang/vX.Y.Z`).

```bash
go get github.com/Yoven/AnhurDB-SDK/v2/golang/v2@v2.0.11
```

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
    hits, _ := mem.Search(ctx, "what does this user do?")
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
hits, err := mem.Search(ctx, "query")                  // Plane search (query=FTS text; prefer SmartSearch for conceptual RAG)
hits, err := mem.Search(ctx, "query", WithLimit(20))   // With options
profile, err := mem.Profile(ctx)                       // User profile (?tag=)
```

### Search & Discovery

```go
hits, err := mem.SearchByType(ctx, "fact", 50)         // Tenant type filter only — not a Shared Data plane switch
raw, err := mem.SmartSearch(ctx, "query", 10)          // Prefer for conceptual text (weight-boosted FTS)
hits, err := mem.Recall(ctx, "query", 20)              // Global alias
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
// Fetch content for multiple records (max 100)
contents, err := mem.BatchReadContent(ctx, []int64{1, 2, 3})

// Bulk status update
err = mem.BatchUpdateStatus(ctx, []int64{10, 11}, "archived")
```

### File Upload

```go
// Upload document for async ingestion
upload, err := mem.UploadFile(ctx, "report.pdf", base64Content, "session-1")

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
