# AnhurDB REST API Quick Reference

This document summarizes the REST API endpoints available in the AnhurDB server.

For the full, detailed API reference with curl examples, see the [Wiki API Reference](../../AnhurDB/docs/wiki/data/pages/api_reference.txt).

## Authentication

Include these headers:

```
X-API-Key: your-api-key
X-Tenant-ID: my-tenant
```

## Endpoints

### System
| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/health` | Server health check (always public) |
| GET | `/metrics` | Prometheus metrics (always public) |

### Record CRUD
| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/records` | Create a new memory record |
| GET | `/api/v1/records/{id}` | Get a single record |
| GET | `/api/v1/records/{id}/content` | Get decompressed .gz content |
| GET | `/api/v1/records/{id}/topology` | Get record + graph neighbors |
| PATCH | `/api/v1/records/{id}` | Update record fields |
| DELETE | `/api/v1/records/{id}` | Soft-delete (archived=1, weight=0) |

### Search
| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/search` | Session search (vector + text hybrid) |
| POST | `/api/v1/search/global` | Cross-session search (safe types only) |

### Sessions
| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/sessions` | List all session UUIDs |
| GET | `/api/v1/chats/{uuid}` | List records in a session |
| GET | `/api/v1/chats/{uuid}/manifest` | Lightweight session manifest |
| GET | `/api/v1/manifest` | Cross-session manifest |

### Graph
| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/walk` | BFS graph traversal |
| GET | `/api/v1/graph` | Full graph (viewer) |

### Batch Operations
| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/records/batch-content` | Read multiple .gz files |
| PATCH | `/api/v1/records/mark-consolidated` | Mark as consolidated |
| PATCH | `/api/v1/records/consolidate-ids` | Set consolidate_id |
| PATCH | `/api/v1/records/append-main-ids` | Append parent links |
| PATCH | `/api/v1/records/append-related-ids` | Append lateral links |
| PATCH | `/api/v1/records/decay` | Batch weight/dimension decay |

### Admin
| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/admin/tenants` | List all tenants |
| GET | `/api/v1/admin/keys` | List API keys |
| POST | `/api/v1/admin/keys` | Create API key |
| DELETE | `/api/v1/admin/keys/{key}` | Revoke API key |

## SDK Mapping

### Python
```python
from anhurdb import AnhurClient

client = AnhurClient("http://localhost:8080", api_key="my-key", tenant_id="my-app")

# CRUD
record_id = client.save(record)         # POST /records
record = client.get(1)                  # GET /records/1
content = client.get_content(1)         # GET /records/1/content
client.delete(1)                        # DELETE /records/1

# Search
results = client.search("uuid", text="query")     # POST /search
results = client.search_global(text="query")       # POST /search/global

# Batch
client.mark_consolidated([1, 2, 3])
client.decay([1, 2], target_dimension=64)
```

### Go
```go
client := anhurdb.NewClient("http://localhost:8080", "my-key", "my-app")

rec, _ := client.Create(req)                       // POST /records
rec, _ := client.GetRecord(1)                      // GET /records/1
client.Delete(1)                                   // DELETE /records/1
results, _ := client.Search(query)                 // POST /search
results, _ := client.SearchGlobal(query)           // POST /search/global
```
