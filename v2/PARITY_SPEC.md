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
| execute_ast | `Query` / `query` / `query` | `POST /api/v1/query` — **advanced / not day-to-day RAG**; prefer `smart_search` / `recall` for chat recall |
| manifest_global | `ManifestGlobal` / `manifest_global` / `manifestGlobal` | `GET /api/v1/manifest` |
| manifest_session | `ManifestSession` / `manifest_session` / `manifestSession` | `GET /api/v1/chats/{uuid}/manifest` |
| list_chat | `ListChat` / `list_chat` / `listChat` | `GET /api/v1/chats/{uuid}` |
| count_by_type | `CountByType` / `count_by_type` / `countByType` | Client pages `GET /api/v1/manifest` |
| list_types | `ListTypes` / `list_types` / `listTypes` | Local static taxonomy |
| list_sessions_stats | `ListSessions` / `list_sessions` / `listSessions` | `GET /api/v1/sessions/stats` |
| get_chat_history | `GetSessionHistory` / `get_session_history` / `getSessionHistory` | `GET /api/v1/sessions/{uuid}/history` |
| get_session_clusters | `GetSessionClusters` / `get_session_clusters` / `getSessionClusters` | `GET /api/v1/sessions/{uuid}/clusters` |
| walk_graph | `Walk` / `walk` / `walk` | `POST /api/v1/walk` |
| walk_semantic | `WalkSemantic` / `walk_semantic` / `walkSemantic` | `POST /api/v1/walk/semantic` — **advanced / not day-to-day RAG**; need `seed_id` + goal, not free-text search |
| get_grounding | `GetGrounding` / `get_grounding` / `getGrounding` | `GET /api/v1/records/{id}/grounding` |
| batch_read_content | `BatchReadContent` / `batch_read_content` / `batchReadContent` | `POST /api/v1/records/batch-content` |
| batch_update_status | `BatchUpdateStatus` / `batch_update_status` / `batchUpdateStatus` | `PATCH /api/v1/records/mark-consolidated` |
| link_consolidated | `LinkConsolidated` / `link_consolidated` / `linkConsolidated` | `PATCH /api/v1/records/consolidate-ids` |
| append_main_ids | `AppendMainIDs` / `append_main_ids` / `appendMainIds` | `PATCH /api/v1/records/append-main-ids` |
| append_main_links | `AppendMainLinks` / `append_main_links` / `appendMainLinks` | `PATCH /api/v1/records/append-main-ids` (batch) |
| append_related_ids | `AppendRelatedIDs` / `append_related_ids` / `appendRelatedIds` | `PATCH /api/v1/records/append-related-ids` |
| create_session | `CreateSession` / `create_session` / `createSession` | `POST /api/v1/sessions` (required before writes). Omit id → server generates |
| open_session | `OpenSession` / `open_session` / `openSession` | Local generate + register (`new_session` then `create_session`) |
| new_session | `NewSession` / `new_session` / `newSession` | Client-side id rotation only — does **not** register; writes fail until create/open_session |
| upload_file | `UploadFile` / `upload_file` / `uploadFile` | `POST /api/v1/upload` |
| upload_status | `UploadStatus` / `upload_status` / `uploadStatus` | `GET /api/v1/upload/{id}/status` |
| wait_for_upload | `WaitForUpload` / `wait_for_upload` / `waitForUpload` | polling client-side sobre upload_status — sem rota própria |
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
| `search` / `recall` | Both hit `POST /api/v1/search`. Default `scope=sessions` (chat plane; never `shared-*`). Shared Data requires explicit scope or a `search_*` helper. **Agent UX:** the server auto-embeds the query `text` server-side (`AnhurDB/server/handler/search_query_vector.go::resolveSearchQueryVectorB64`, since 2026-07-15) and blends it with FTS5 for hybrid ranking — callers do not need to submit a vector. `smart_search` remains the weight-boosted lexical door (MCP: `search` with `strategy=lexical`; MCP `recall` is the RRF fan-out) for callers who want lexical-weighted ranking instead. |
| `smart_search` | Same scope enum via `?scope=` (default `sessions`). Prefer for conceptual text queries (weight-boosted FTS). |
| `search_by_type` | Tenant-store type index only — **no `scope` / not a Shared Data plane switch**. Use `search_*` helpers or `scope=` for specialty docs. |
| `/search/global` | Server deprecated alias only — SDKs must not call it. |
| `count_by_type` | Implemented by paging the manifest. |
| `add` vs `create` | **Session-first:** `create_session` (or `open_session`) before any write. **Write paths:** `add(text, mode="ingest")` → `POST /ingest` (episodic + async extraction, LLM billed). `create(...)` → `POST /records` (one typed record, no extraction). **Trap:** `add` with pinned `type`/`score`/`metadata` skips ingest and hits `/records`. Never both for the same turn. Unregistered client session → fail loud before HTTP. |
| `create` | Python uses `CreateRequest`; Go uses options; TypeScript uses `CreateOptions`. |
| `query` | Python/Go return record lists; TypeScript returns `{ records, count }`. |
| `query` AST validation | Since 2026-07-28 the server rejects a malformed filter with a **named HTTP 400** instead of dropping the predicate: bare value instead of an operator object, empty operator object `{}`, empty `$in`, unknown operator, and a non-scalar operator value. The dropped predicate used to widen the result set and answer 200 — a silent wrong answer. Client-side pre-validation reached parity in Go SDK v2.0.13 (2026-07-30); one narrow, structural residual difference remains — see "Known parity gaps". |
| Anchor policy | SDKs send one request. Server returns HTTP 422 if no episodic anchor exists. |

## Known parity gaps

Deviations from this contract are bugs (see Principles). These are the open ones,
recorded here so they are tracked rather than rediscovered.

### Query builder pre-validation (`query` / `POST /api/v1/query`) — recorded 2026-07-29, closed 2026-07-30 (Go SDK v2.0.13)

The server-side AST contract is identical for all three SDKs. Client-side
pre-validation reached parity in Go SDK v2.0.13 (2026-07-30) —
`v2/golang/client/query_validation.go` adds `QueryRequest.Validate()`, and
`Query()` calls it before the request leaves the process (commit `68ebcc8`,
2026-07-29). Go now raises the same class of local error Python and
TypeScript always have; Principle 4 ("fail loud — no silent drops") is
enforced at the same layer in all three, not just server-side.

| Guard | Python `QueryBuilder` | TypeScript `QueryBuilder` | Go `QueryRequest` |
|---|---|---|---|
| Filter/sort column whitelist (17 columns, mirrors the server) | yes, raises `ValueError` | yes, throws `Error` | yes, since v2.0.13 — `Validate()` errors locally, `query: field "<f>" is not allowed in filters` |
| Operator whitelist (`$eq $gt $gte $lt $lte $in`) | yes, raises `ValueError` | yes, throws `Error` | n/a — `QueryOp` is a closed struct, no unknown operator is expressible |
| `limit` range 1–1000 | yes, raises `ValueError` | yes, throws `Error` | yes, since v2.0.13 — `Validate()` errors locally instead of relying on the server's silent fallback |
| `offset >= 0` | yes, raises `ValueError` | yes, throws `Error` | yes, since v2.0.13 — `Validate()` errors locally instead of relying on the server's silent fallback |
| Cannot emit an empty operator object | yes (fluent path) | yes (fluent path) | yes, since v2.0.13 — `QueryOp.isEmpty()` inside `Validate()` |
| Empty `$in` guarded before the request | no | no | yes, since v2.0.13 — `QueryOp.hasEmptyInList()` gives it a **dedicated** message (`$in requires a non-empty list of values`), distinct from the generic "no operator" case; Python and TypeScript still reach the server for this one |

**Residual gap — `$eq: null` is still not expressible from Go.** `QueryOp.Eq`
is `interface{}` tagged `omitempty` (`v2/golang/client/types.go`); a Go `nil`
interface value is indistinguishable from an unset field, so neither
`Validate()` nor the wire encoding can tell "caller wants `$eq: null`" apart
from "caller set nothing" — `Validate()` reports the latter:
`filter "<field>" has no operator`. The server accepts an explicit null
scalar, and Python/TypeScript can send it; Go cannot without bypassing the
SDK and posting raw JSON. This is a type-system constraint of the closed
`QueryOp` struct (would need an `Option[T]`-style wrapper to close), not a
missed validation check, and is not currently planned.

Escape hatches that bypass pre-validation and reach the server raw: Python
`Filter({...})` (copies the dict with no validation) and TypeScript a hand-built
`AstQuery` (typically via `as AstQuery`) — both can carry any shape in the table,
including an unknown operator. In Go a hand-built `QueryRequest` bypasses nothing
extra, because `Filters` is a `map[string]QueryOp` and `QueryOp` is a closed
struct; sending an unknown operator from Go requires bypassing the SDK and posting
raw JSON.

### Accept-and-ignore behaviours (identical in all three, by design)

Not gaps to close silently — changing any of them is a declared contract change:
`select` is never projected; an unrecognised sort `order` falls back to `DESC`;
`limit`/`offset` adjustments are not echoed; zero hits arrive on the wire as
`"records": null`, which all three SDKs coalesce to an empty list; archived rows
are hidden unless `archived` is filtered explicitly; a
`semantic_search` block inside `filters` (Python `QueryBuilder.semantic_search()`)
is accepted and skipped server-side and has never contributed to the result.

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

## wait_for_upload (2026-08-07)

Polling de upload com semântica IDÊNTICA nos três SDKs. Motivação medida em
produção: leituras são load-balanced, então logo após o `POST /upload` um
follower que ainda não aplicou a entrada devolve **404 legítimo e transiente**
(read-your-writes). Antes deste helper cada cliente tratava isso de um jeito —
o runner Go tolerava, o Python morria, o TS passava por sorte de timing.

Contrato (defaults iguais nos três: timeout 120s, interval 5s, grace 30s):

- 404 **dentro** da janela de graça → estado "pendente", continua o polling.
- 404 **além** da graça → o erro real re-emerge (`ErrNotFound` /
  `AnhurQueryError(status_code=404)` / `AnhurQueryError.statusCode===404`) —
  id inválido não pode virar espera infinita (falhar alto).
- Terminal = `completed=true` OU `error` presente OU status em
  `completed|saved|done|failed`. **`failed` retorna o payload** — ingest
  falhado é dado terminal que o chamador inspeciona, não erro de transporte.
- Timeout → erro tipado com o último status visto: Go `ErrUploadWaitTimeout`,
  Python `AnhurUploadWaitTimeout`, TS `AnhurUploadWaitTimeout`.
- Erros de transporte/5xx durante o polling são transientes por default — o
  loop É o retry.

Suporte: os erros HTTP dos três SDKs carregam o status estruturado
(Go `ErrNotFound` tipado; Python `AnhurError.status_code`; TS
`AnhurError.statusCode`) — nunca parsear a mensagem.
