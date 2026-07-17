# AnhurDB SDK v2 — Parity Specification

**Status:** Implemented (2026-07). Python, TypeScript, and Go expose the same `Memory`
API. This document is the public contract; deviations are bugs.

**Open Beta endpoint:** `https://anhurdb.yoven.ai` (default in all three SDKs).

## Principles

1. **One client class** — `Memory` in every language. Legacy aliases exist only for
   backward compatibility.
2. **Consistent method names** — idiomatic casing per language (`PascalCase`,
   `snake_case`, `camelCase`).
3. **Same semantics** — equivalent parameters and behavior across SDKs.
4. **Fail loud** — server errors are surfaced; no silent drops.

## Canonical API surface

| Tool | Go / Python / TypeScript | HTTP route |
|---|---|---|
| health | `Health` / `health` / `health` | `GET /api/v1/health` |
| ingest_memory | `Add` / `add` / `add` | `POST /api/v1/ingest` (default) — episodic + platform extraction; LLM+embed billed. Falls back to `/records` if ingest 404 or caller pins type/score/metadata |
| create_memory | `Create` / `create` / `create` | `POST /api/v1/records` — one typed record, **no** extraction LLM (embed only) |
| create_in_session | `CreateInSession` / `create_in_session` / `createInSession` | `POST /api/v1/records` |
| get_memory | `Get` / `get` / `get` | `GET /api/v1/records/{id}` |
| update_memory | `Update` / `update` / `update` | `PATCH /api/v1/records/{id}` |
| delete | `Delete` / `delete` / `delete` | `DELETE /api/v1/records/{id}` |
| forget | `Forget` / `forget` / `forget` | Stub (not yet available) |
| supersede_record | `Supersede` / `supersede` / `supersede` | `POST /api/v1/records/supersede` |
| read_content | `ReadContent` / `read_content` / `readContent` | `GET /api/v1/records/{id}/content` |
| get_memory_context | `GetContext` / `get_context` / `getContext` | `GET /api/v1/records/{id}/topology` |
| recall | `Recall` / `recall` / `recall` | `POST /api/v1/search` (`scope=sessions` default; same `scope` as search) |
| semantic_search | `Search` / `search` / `search` | `POST /api/v1/search` (`scope=sessions` default) |
| search_sessions | `SearchSessions` / `search_sessions` / `searchSessions` | `POST /api/v1/search` (`scope=sessions`) |
| search_tenant_shared | `SearchTenantShared` / `search_tenant_shared` / `searchTenantShared` | `POST /api/v1/search` (`scope=tenant_shared`) |
| search_client_shared | `SearchClientShared` / `search_client_shared` / `searchClientShared` | `POST /api/v1/search` (`scope=client_shared`) |
| search_shared | `SearchShared` / `search_shared` / `searchShared` | `POST /api/v1/search` (`scope=shared_all`) |
| semantic_search (one chat) | `SearchSession` / `search_session` / `searchSession` | `POST /api/v1/search` (`scope=sessions` + `uuid`) |
| search_by_type | `SearchByType` / `search_by_type` / `searchByType` | `GET /api/v1/search/type` |
| smart_search | `SmartSearch` / `smart_search` / `smartSearch` | `GET /api/v1/search/smart` (`scope` query, default `sessions`) |
| recent_memories | `Recent` / `recent` / `recent` | `GET /api/v1/manifest` or `/recent` |
| execute_ast | `Query` / `query` / `query` | `POST /api/v1/query` |
| manifest_global | `ManifestGlobal` / `manifest_global` / `manifestGlobal` | `GET /api/v1/manifest` |
| manifest_session | `ManifestSession` / `manifest_session` / `manifestSession` | `GET /api/v1/chats/{uuid}/manifest` |
| list_chat | `ListChat` / `list_chat` / `listChat` | `GET /api/v1/chats/{uuid}` |
| count_by_type | `CountByType` / `count_by_type` / `countByType` | Client pages `GET /api/v1/manifest` |
| list_types | `ListTypes` / `list_types` / `listTypes` | Local static taxonomy |
| list_sessions_stats | `ListSessions` / `list_sessions` / `listSessions` | `GET /api/v1/sessions/stats` |
| get_chat_history | `GetSessionHistory` / `get_session_history` / `getSessionHistory` | `GET /api/v1/sessions/{uuid}/history` |
| get_session_clusters | `GetSessionClusters` / `get_session_clusters` / `getSessionClusters` | `GET /api/v1/sessions/{uuid}/clusters` |
| walk_graph | `Walk` / `walk` / `walk` | `POST /api/v1/walk` |
| walk_semantic | `WalkSemantic` / `walk_semantic` / `walkSemantic` | `POST /api/v1/walk/semantic` |
| get_grounding | `GetGrounding` / `get_grounding` / `getGrounding` | `GET /api/v1/records/{id}/grounding` |
| batch_read_content | `BatchReadContent` / `batch_read_content` / `batchReadContent` | `POST /api/v1/records/batch-content` |
| batch_update_status | `BatchUpdateStatus` / `batch_update_status` / `batchUpdateStatus` | `PATCH /api/v1/records/mark-consolidated` |
| link_consolidated | `LinkConsolidated` / `link_consolidated` / `linkConsolidated` | `PATCH /api/v1/records/consolidate-ids` |
| append_main_ids | `AppendMainIDs` / `append_main_ids` / `appendMainIds` | `PATCH /api/v1/records/append-main-ids` |
| append_main_links | `AppendMainLinks` / `append_main_links` / `appendMainLinks` | `PATCH /api/v1/records/append-main-ids` (batch) |
| append_related_ids | `AppendRelatedIDs` / `append_related_ids` / `appendRelatedIds` | `PATCH /api/v1/records/append-related-ids` |
| new_session | `NewSession` / `new_session` / `newSession` | Client-side session rotation |
| upload_file | `UploadFile` / `upload_file` / `uploadFile` | `POST /api/v1/upload` |
| upload_status | `UploadStatus` / `upload_status` / `uploadStatus` | `GET /api/v1/upload/{id}/status` |
| list_entities | `ListEntities` / `list_entities` / `listEntities` | `GET /api/v1/entities/list` |
| search_entities | `SearchEntities` / `search_entities` / `searchEntities` | `GET /api/v1/entities` |
| upsert_entity | `UpsertEntity` / `upsert_entity` / `upsertEntity` | `POST /api/v1/entities` |
| upsert_entity_edge | `UpsertEntityEdge` / `upsert_entity_edge` / `upsertEntityEdge` | `POST /api/v1/entities/edges` |
| link_record_entity | `LinkRecordEntity` / `link_record_entity` / `linkRecordEntity` | `POST /api/v1/entities/link` |
| get_entity_graph | `EntityGraph` / `entity_graph` / `entityGraph` | `GET /api/v1/entities/{id}/graph` |
| entity_timeline | `EntityTimeline` / `entity_timeline` / `entityTimeline` | `GET /api/v1/entities/{id}/timeline` |
| get_record_entities | `GetRecordEntities` / `get_record_entities` / `getRecordEntities` | `GET /api/v1/records/{id}/entities` |
| get_profile | `Profile` / `profile` / `profile` | `GET /api/v1/profile` |

## Behavioral notes

| Topic | Behavior |
|---|---|
| `search` / `recall` | Both hit `POST /api/v1/search`. Default `scope=sessions` (chat plane; never `shared-*`). Shared Data requires explicit scope or a `search_*` helper. **Agent UX:** query string is sent as `text` (FTS5) — not embedding similarity; prefer `smart_search` / MCP `recall` for conceptual RAG without a vector. |
| `smart_search` | Same scope enum via `?scope=` (default `sessions`). Prefer for conceptual text queries (weight-boosted FTS). |
| `search_by_type` | Tenant-store type index only — **no `scope` / not a Shared Data plane switch**. Use `search_*` helpers or `scope=` for specialty docs. |
| `/search/global` | Server deprecated alias only — SDKs must not call it. |
| `count_by_type` | Implemented by paging the manifest. |
| `create` | Python uses `CreateRequest`; Go uses options; TypeScript uses `CreateOptions`. |
| `query` | Python/Go return record lists; TypeScript returns `{ records, count }`. |
| Anchor policy | SDKs send one request. Server returns HTTP 422 if no episodic anchor exists. |

## Deprecated aliases

| Language | Deprecated | Canonical |
|---|---|---|
| Python | `AnhurClient`, `search_with_ast`, `mark_consolidated`, `link_to_consolidated`, `update_consolidate_ids` | `Memory`, `query`, `batch_update_status`, `link_consolidated` |
| Go | `RecentMemories`, `UpdateConsolidateIDs` | `Recent`, `LinkConsolidated` |
| TypeScript | `updateConsolidateIds` | `linkConsolidated` |

## Acceptance criteria

- All three SDKs build and pass unit tests.
- Every row in the table above is implemented in all three languages.
- Default cloud URL is `https://anhurdb.yoven.ai`.
- Entity type whitelist uses `organization`, not `org`.
