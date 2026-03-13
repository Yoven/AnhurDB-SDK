# AnhurDB-SDK — The V2 Cognitive SDK

> **Role:** Unified Software Development Kit for AnhurDB V2.

This repository focuses on unifying and serving the Go and Python interfaces, schema models, and AST logic for the **AnhurDB V2** engine.

---

## 🏗️ Repository Structure

```
AnhurDB-SDK/
├── v2/
│   ├── golang/                    Official Go SDK V2
│   │   ├── client/                HTTP connection & pooling
│   │   ├── models/                Data structures (Record, Session)
│   │   ├── query/                 AST Generator & Executor
│   │   └── go.mod                 Go module definition
│   │
│   └── python/                    Official Python SDK V2
│       ├── anhurdb/client/        Async/Sync HTTP connection
│       ├── anhurdb/models/        Pydantic/Dataclass structures
│       ├── anhurdb/query/         AST Generator & Executor
│       ├── pyproject.toml         Poetry definition
│       └── README.md              Python-specific guide
├── .github/workflows/             CI/CD Pipelines
└── docs/                          Documentation
```

---

## 🚀 Quick Start (V2)

### Python

```bash
cd v2/python
poetry install
# or
pip install -e .
```

```python
import asyncio
from anhurdb.client import Client
from anhurdb.models import CreateRequest
from anhurdb.query import Filter, Eq, Gt

async def main():
    async with Client(url="http://localhost:8080") as client:
        # Create a record
        req = CreateRequest(
            uuid="session-v2-001",
            type="episodic",
            dimension=1024,
            weight=0.75,
            score=8,
            summary="User asked about Redis",
            vector=[...]
        )
        await client.create(req)

        # Search with AST
        results = await client.search_with_ast("session-v2-001", Filter(
            condition=Eq("type", "episodic")
        ))
        
        for r in results.records:
            print(f"ID={r.id}")

asyncio.run(main())
```

### Go

```go
import (
    "context"
    "fmt"
    "github.com/Yoven/AnhurDB-SDK/v2/golang/client"
    "github.com/Yoven/AnhurDB-SDK/v2/golang/models"
    "github.com/Yoven/AnhurDB-SDK/v2/golang/query"
)

func main() {
    c := client.NewClient("http://localhost:8080", "api-key", "tenant-id")
    c.Connect(context.Background())

    // Create
    rec := models.CreateRequest{
        UUID:      "session-001",
        Type:      "episodic",
        Dimension: 1024,
        Weight:    0.75,
        Score:     8,
        Summary:   "User asked about Redis",
    }
    c.Create(context.Background(), rec)

    // Search via AST Builder
    f := query.NewFilter().Where("type", query.Eq, "episodic")
    res, _ := c.SearchWithAST(context.Background(), "session-001", f)

    for _, doc := range res.Records {
        fmt.Println("ID:", doc.ID)
    }
}
```

---

## 📄 Documentation Index

| Document | Description |
|----------|-------------|
| [Architecture](./docs/general/ARCHITECTURE.md) | V1 Bridge pattern design (Legacy Concept) |
| [Mathematics](./docs/general/MATHEMATICS.md) | Canonical formulas with LaTeX |
| [REST API](./docs/api/REST_API.md) | Legacy REST interfaces |
