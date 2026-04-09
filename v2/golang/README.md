# AnhurDB Go SDK

Official Go client for [AnhurDB](https://anhurdb.com) — cognitive memory for AI agents.

**Zero external dependencies.** Uses only `net/http`, `crypto/sha256`, `encoding/json`, and other stdlib packages.

## Install

```bash
go get github.com/anhurdb/sdk-go/v2
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    anhurdb "github.com/anhurdb/sdk-go/v2"
)

func main() {
    ctx := context.Background()
    mem := anhurdb.NewMemory("anhur_xxx")

    // Store a memory
    result, _ := mem.Add(ctx, "I'm a data scientist at Google")

    // Search across all sessions
    hits, _ := mem.Search(ctx, "what does this user do?")
    for _, h := range hits {
        fmt.Printf("%s (%.2f)\n", h.Summary, h.Similarity)
    }

    // Get user profile
    profile, _ := mem.Profile(ctx)
    fmt.Println(profile.Static)
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
| `WithURL(url)` | Server URL (default: `https://api.anhurdb.com`) |
| `WithUserID(id)` | Explicit container tag (default: derived from API key hash) |
| `WithTenantID(id)` | Multi-tenant `X-Tenant-ID` header |
| `WithTimeout(d)` | HTTP client timeout (default: 30s) |

## API Reference

### Core Methods

```go
result, err := mem.Add(ctx, "text")                    // Store memory
hits, err := mem.Search(ctx, "query")                  // Hybrid search
hits, err := mem.Search(ctx, "query", WithLimit(20))   // With options
profile, err := mem.Profile(ctx)                       // User profile
```

### Search & Discovery

```go
hits, err := mem.SearchByType(ctx, "fact", 50)         // Filter by type
raw, err := mem.SmartSearch(ctx, "query", 10)          // Cognitive-weighted
hits, err := mem.Recall(ctx, "query", 20)              // Global alias
records, err := mem.RecentMemories(ctx, 5)             // Most recent
```

### Graph Traversal

```go
walk, err := mem.Walk(ctx, 42, 3)                      // BFS traversal
walk, err := mem.WalkSemantic(ctx, 42, 3)              // Vector-weighted
topo, err := mem.GetContext(ctx, 42)                    // 1-hop neighbors
content, err := mem.ReadContent(ctx, 42)               // Full payload
```

### Entity Knowledge Graph

```go
// Search and create entities
entities, err := mem.SearchEntities(ctx, "Google", "org", 20)
entity, err := mem.UpsertEntity(ctx, "Google", "org", "Tech company", nil)

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
sessions, err := mem.ListSessions(ctx)                     // All sessions with stats
history, err := mem.GetSessionHistory(ctx, "uuid", 50, 0)  // Paginated history
clusters, err := mem.GetSessionClusters(ctx, "uuid")       // Thematic clusters
mem.NewSession()                                           // Rotate session UUID
fmt.Println(mem.SessionID())                               // Current session
fmt.Println(mem.ContainerTag())                            // User identifier
```

### Record CRUD

```go
err = mem.Update(ctx, 42, map[string]interface{}{"summary": "Updated"})
err = mem.Delete(ctx, 42)
```

## Error Handling

```go
import "errors"

result, err := mem.Add(ctx, "text")
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
import "github.com/anhurdb/sdk-go/v2/integrations/dspy"

retriever := dspy.NewRetriever(mem, 10)
docs, err := retriever.GetRelevantDocuments(ctx, "user's role?")
```

## Thread Safety

`Memory` is safe for concurrent use. The underlying `http.Client` handles connection pooling.

## License

MIT
