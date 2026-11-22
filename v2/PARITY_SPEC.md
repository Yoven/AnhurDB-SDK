# AnhurDB SDK v2 — Parity Specification

**Status:** Implemented (2026-07); **4.0.0 (2026-11-21) in all three SDKs** — legacy
removal, see "4.0.0 — legacy removal" below. Python, TypeScript, and Go expose the same
`Memory` API. This document is the public contract; deviations are bugs.

**Open Beta endpoint:** `https://anhurdb.yoven.ai` (default in all three SDKs).

## Principles

1. **One client class** — `Memory` in every language. No aliases: every deprecated
   alias was removed in 4.0.0. AnhurDB `main` is the only server, so no SDK carries a
   branch for an older server.
2. **Consistent method names** — idiomatic casing per language (`PascalCase`,
   `snake_case`, `camelCase`).
3. **Same semantics** — equivalent parameters and behavior across SDKs.
4. **Fail loud** — server errors are surfaced; no silent drops.

## Canonical API surface

| Tool | Go / Python / TypeScript | HTTP route |
|---|---|---|
| health | `Health` / `health` / `health` | `GET /api/v1/health` |
| ingest_memory | `Add` / `add` / `add` | `POST /api/v1/ingest` (default) — episodic + platform extraction; LLM+embed billed. Goes to `/records` only when the caller pins type/score/metadata; a 404 from `/ingest` is an error (no fallback since 4.0.0). `AddResult.mode` = `"ingest"` \| `"records"` |
| create_memory | `Create` / `create` / `create` | `POST /api/v1/records` — one typed record, **no** extraction LLM (embed only) |
| create_in_session | `CreateInSession` / `create_in_session` / `createInSession` | `POST /api/v1/records` |
| get_memory | `Get` / `get` / `get` | `GET /api/v1/records/{id}` |
| update_memory | `Update` / `update` / `update` | `PATCH /api/v1/records/{id}`. `metadata` = RFC 7396 merge patch (AnhurDB ADR-0035; null apaga a chave, chave ausente fica). Aceita string JSON ou mapa/dict/objeto, codificado para string JSON mantendo null; valor não codificável (NaN/Infinity) = invalid_request local, mensagem `INVALID_PARAM: <method>: metadata cannot be encoded as JSON: <cause>` (2026-11-21) |
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
| semantic_search (one chat) | `SearchSession` / `search_session` / `searchSession` | `POST /api/v1/search` (`scope=sessions` + `sessions: [<uuid>]`; the singular `uuid` field is gone, ADR-0014) |
| search_by_type | `SearchByType` / `search_by_type` / `searchByType` | `GET /api/v1/search/type` |
| smart_search | `SmartSearch` / `smart_search` / `smartSearch` | `GET /api/v1/search/smart` (`scope` query, default `sessions`) |
| recent_memories | `Recent` / `recent` / `recent` | `GET /api/v1/recent` |
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
| batch_read_content | `BatchReadContent` / `batch_read_content` / `batchReadContent` | `POST /api/v1/records/batch-content` — max **100** ids per call (server `service.MaxBatchContentIDs`, 400 `too many IDs (max 100)`; empty list = 400). No SDK caps it locally. The 1000 `maxBatchSize` is for the batch WRITES only |
| batch_update_status | `BatchUpdateStatus` / `batch_update_status` / `batchUpdateStatus` | `PATCH /api/v1/records/mark-consolidated` |
| link_consolidated | `LinkConsolidated` / `link_consolidated` / `linkConsolidated` | `PATCH /api/v1/records/consolidate-ids` |
| append_main_ids | `AppendMainIDs` / `append_main_ids` / `appendMainIds` | `PATCH /api/v1/records/append-main-ids` |
| append_main_links | `AppendMainLinks` / `append_main_links` / `appendMainLinks` | `PATCH /api/v1/records/append-main-ids` (batch) |
| append_related_ids | `AppendRelatedIDs` / `append_related_ids` / `appendRelatedIds` | `PATCH /api/v1/records/append-related-ids` |
| set_score | `SetScore` / `set_score` / `setScore` | `POST /api/v1/records/set-score` (comando replicado). **Não** existe pelo `update`: o PATCH não tem campo `score` e o descarta respondendo 200 — medido 2026-08-15 |
| create_session | `CreateSession` / `create_session` / `createSession` | `POST /api/v1/sessions` (required before writes). Omit id → server generates |
| open_session | `OpenSession` / `open_session` / `openSession` | Local generate + register (`new_session` then `create_session`) |
| new_session | `NewSession` / `new_session` / `newSession` | Client-side id rotation only — does **not** register; writes fail until create/open_session |
| upload_file | `UploadFile` / `upload_file` / `uploadFile` | `POST /api/v1/upload` — `mode` is trimmed and lower-cased in all three; an unknown value is the typed invalid_request error, no request sent (see `SDK_ERROR_CONTRACT.md`, 2026-11-21) |
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
| get_profile | `Profile` / `profile` / `profile` | `GET /api/v1/profile` — every HTTP error is raised, a 404 included (the "OSS: empty profile on 404" fallback was removed in 4.0.0; the route is always mounted, `server/router.go:136`). An unknown tag is still an all-zero profile with HTTP 200 |

## Client key API (ADR-0034, released in 4.1.0 — 2026-11-22)

A second handle, separate from `Memory`, for a **client key** (kind=client):
Go `NewClientAPI(key, opts...)` / Python `ClientAPI(key, url=, timeout=)`
(async context manager) / TypeScript `new ClientAPI({ clientKey, url, timeoutMs })`.
Server truth: AnhurDB `server/router_client.go` and `server/handler/client_*.go`
(branch `feat/phase1-client-children`). It **never sends `X-Tenant-ID`**.

| Operation | Go / Python / TypeScript | HTTP route |
|---|---|---|
| create_child | `CreateChild` / `create_child` / `createChild` | `POST /api/v1/client/children` `{"external_ref"}` → 201 `created:true` / 200 `created:false`, same `child_client_id` |
| list_children | `ListChildren` / `list_children` / `listChildren` | `GET /api/v1/client/children` → `{"children":[…]}` |
| suspend_child | `SuspendChild` / `suspend_child` / `suspendChild` | `POST /api/v1/client/children/{child_id}/suspend` → `{"child_client_id","status"}` |
| resume_child | `ResumeChild` / `resume_child` / `resumeChild` | `POST /api/v1/client/children/{child_id}/resume` |
| list_tenants | `ListTenants` / `list_tenants` / `listTenants` | `GET /api/v1/client/tenants` → `{"tenants":[{"tenant_id"}]}`, returned as a list of ids |
| list_child_tenants | `ListChildTenants` / `list_child_tenants` / `listChildTenants` | `GET /api/v1/client/children/{child_id}/tenants` |
| create_tenant | `CreateTenant` / `create_tenant` / `createTenant` | `POST /api/v1/client/tenants` `{"tenant_id","description","profile":{"config","max_rpm"}}` |
| create_child_tenant | `CreateChildTenant` / `create_child_tenant` / `createChildTenant` | `POST /api/v1/client/children/{child_id}/tenants` |
| impersonate | `Impersonate` / `impersonate` / `impersonate` | `POST /api/v1/client/impersonate` |
| impersonate_child | `ImpersonateChild` / `impersonate_child` / `impersonateChild` | `POST /api/v1/client/children/{child_id}/impersonate` |
| revoke_impersonation | `RevokeImpersonation` / `revoke_impersonation` / `revokeImpersonation` | `POST /api/v1/client/impersonate/revoke` `{"key_id"}` |

Identical behaviour in the three:

- **Wire.** Impersonate sends `tenant_id`; `scope` only when set; `l1_only` only
  when true; `description` only when set; `expires_in` AND `ttl_seconds` (equal)
  only when a TTL is given. A nil/absent profile omits the key; an empty profile
  sends `"profile":{}`; empty `config` is omitted; `max_rpm` only when set.
- **Local refusals** (typed invalid_request, status 400, not retryable, no
  request): empty (after trim) `external_ref`/`tenant_id`/`key_id`; `tenant_id`
  `*`; child id that is not 36-char hyphenated hex; scope outside
  `admin|write|read_only`; child impersonate without scope; TTL negative or not
  whole seconds; `max_rpm < 1`. Message prefix `INVALID_PARAM: client API: `.
- **Refusals from the server** → Go `*ClientAPIError` / `AnhurClientAPIError`
  with status, `code` (= `body.error`, verbatim — a code on the ADR-0034
  envelope, or a frozen sentence such as `client API key required`), `detail`,
  `error_type` and is-not-leader. Kind follows the status; `child_limit` and
  `tenant_limit` are NOT retryable. No SDK retries (`not_leader` included).
  Message: `Client API error (HTTP <s>) on <METHOD> <path>: <code>[: <detail>]`
  (non-JSON body: the first 500 characters; empty: `(empty body)`).
- **Fail-closed echo** → Go `*ClientAPIResponseError` (`errors.Is`
  `ErrL1OnlyNotHonoured` / `ErrScopeNotHonoured` / `ErrUnexpectedResponse`) /
  `AnhurClientAPIResponseError` with `code` `l1_only_not_honoured`,
  `scope_not_honoured` or `unexpected_response`, kind `server`, not retryable,
  carrying the minted key id. l1_only requested and not echoed `true` → refused;
  scope requested and echo unknown or higher (read_only < write < admin) →
  refused; lower → accepted. Missing `api_key`/`key_id`/parseable `expires_at`,
  a non-canonical `child_client_id` on create, a missing list key, or a non-object
  2xx body → `unexpected_response`.
- Not covered: `GET /api/v1/client/impersonations` (exists on the server, not in
  this surface yet).

## 401/403 on data routes keep the server's reason (unreleased — 2026-11-21)

Before: every 401/403 collapsed into a bare auth error (Go `ErrUnauthorized`
"authentication failed: invalid API key", Python/TypeScript `AnhurAuthError`
"Authentication failed (HTTP <s>)") and the body was dropped, so a scope
refusal read as an invalid key. Server truth (AnhurDB `main`):
`server/middleware/auth.go`, `auth_aes_gates.go`, `auth_legacy_key.go`,
`auth_resolved_identity.go` (`http.Error`, i.e. `text/plain` with a JSON body)
and `handler/service_bridge.go` (`writeErrorCode`, `application/json`).

| Body (`body.error`) | Status | Go / Python / TypeScript predicate |
|---|---|---|
| `l1_only` (+ `detail` "token restricted to L1 sessions") | 403 | `IsL1Only()` / `is_l1_only` / `isL1Only` |
| `child_suspended` (+ `detail` "client is suspended") | 403 | `IsChildSuspended()` / `is_child_suspended` / `isChildSuspended` |
| `insufficient scope — key does not have permission for this operation` | 403 | `IsInsufficientScope()` / `is_insufficient_scope` / `isInsufficientScope` (prefix `insufficient scope`) |
| `client_key_requires_impersonation` | 403 | `IsImpersonationRequired()` / `is_impersonation_required` / `isImpersonationRequired` |
| `invalid or inactive API key` | 401 | `IsInvalidKey()` / `is_invalid_key` / `isInvalidKey` |

Identical behaviour in the three:

- **Type.** Go `*AuthRefusedError{Status, Code, Detail, ErrorType}` (Unwrap →
  `ErrUnauthorized`, so `errors.Is` is unchanged) / Python
  `AnhurAuthRefusedError(AnhurAuthError)` / TypeScript
  `AnhurAuthRefusedError extends AnhurAuthError`. Kind `auth`, not retryable,
  status 401 or 403. Constants `AuthCode*` / `AUTH_CODE_*` / `AUTH_CODE_*`.
- **Decoding.** `code` = `body.error` verbatim when the body is a JSON object
  with a non-empty string `error`, **whatever the Content-Type**; `detail` and
  `error_type` are kept when they are strings, a mistyped one is dropped alone
  (the code survives). Same decoder as the client-key surface
  (`*ClientAPIError` / `AnhurClientAPIError`).
- **Message.** With a code: `Authentication failed (HTTP <s>): <code>[: <detail>]`,
  byte-identical. Without one (non-JSON body, or `error` not a string): the
  historical text of each SDK — Go `authentication failed: invalid API key`,
  Python/TypeScript `Authentication failed (HTTP <s>)` — and `code` is empty.
- **Paths.** Every data-route verb, multipart upload included (Python's
  multipart path used to omit `status_code`; fixed).
- **gRPC.** None of the three SDKs has a gRPC transport, so there is nothing to
  map. If one is added, PermissionDenied with message `l1_only` /
  `child_suspended` / `client_key_requires_impersonation` / `insufficient
  scope — …` must become this type with status 403 and that message as `code`,
  and Unauthenticated (`invalid or inactive API key`) status 401
  (`server/grpc/interceptor*.go`).

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
| Session record limit (2026-11-20, ADR-0033) | A write into a full session returns HTTP 409. All three SDKs raise a TYPED, non-retryable refusal — Go `*SessionRecordLimitError` (+ `ErrSessionRecordLimit`), Python `AnhurSessionRecordLimitError`, TypeScript `AnhurSessionRecordLimitError` — carrying session uuid, limit and count — all three non-nullable since 4.0.0. Recognised ONLY by HTTP 409 + `code == "session_record_limit"` + an integer `limit`, an integer `count` and a string `session_uuid` (the server always sends all four, `handler/service_bridge.go:109-125`); a body missing or mistyping any of them, and any other 409 (including the bare sentence without a code), stays the generic 409 error. Message, byte-identical: `Conflict (HTTP 409): <server error, first 500 characters> (session record limit: session=<uuid>, limit=N, count=M)`. No SDK has a gRPC transport, so the gRPC form (ResourceExhausted / legacy AlreadyExists) has no SDK path today. Contract: `SDK_ERROR_CONTRACT.md`. |

## By-id scope — L2 tenant library (2026-11-20, ADR-0029)

Server: AnhurDB `fix/l2-lib-scope`. A record id is a rowid **per store**; since
tenant-library uploads land in `{c}_{t}_lib`, an id from a shared-plane hit must
be addressed to its store or the call reads/mutates an unrelated chat row with
the same number. All three SDKs, same semantics:

| Topic | Go / Python / TypeScript |
|---|---|
| Scope values | `RecordScope*` consts / `SCOPE_*` / `RecordScope` union: `sessions`, `tenant_shared`, `tenant_shared_legacy`, `client_shared`. `shared_all` and unknown values refused **locally** (invalid_request, 400-classified, no request sent) |
| Carrier | `WithRecordScope(s)` (variadic `...ByIDOption`) / keyword-only `scope=` / trailing `{ scope }` |
| Methods | `Get`, `ReadContent`, `GetContext`, `GetGrounding`, `BatchReadContent`, `Update`, `Delete`, `Supersede`, `UploadStatus`, `WaitForUpload` — sent as `?scope=` on the REST route (PATCH/POST/DELETE too). No scope = request byte-identical to before |
| Hit → scope | `TenantSharedScopeForHit` / `tenant_shared_scope_for_hit` / `tenantSharedScopeForHit`: uuid ends in `_lib` → `tenant_shared`, else `tenant_shared_legacy` |
| Upload plane | `UploadResult.Scope` / `.scope` / `.scope`, SDK-filled from the mode sent (default & `tenant_shared` → `tenant_shared`, `client_shared` → `client_shared`, chat/session → `sessions`); helper `UploadStatusScope` / `upload_status_scope` / `uploadStatusScope`. Pass it to the status poll |
| Delete-by-file | scope `tenant_shared` (session may be empty; server deletes both library stores) or `client_shared`; other values refused locally. Result gains `legs[]` `{generation, session_uuid, skipped, dispatch_skipped, matched_count, deleted_count, error}` |
| `WaitForUpload` | returns/raises at once on a non-retryable error (Go changed to match Python/TS, which already did) |
| Collision refusal | HTTP 409 `code == "tenant_library_collision"` on every door, upload included (server 4574c4d) → `*TenantLibraryCollisionError` / `AnhurTenantLibraryCollisionError` (conflict, non-retryable). Classified by `code` ONLY (4.0.0 removed the sentence match and the upload-400 branch, both for servers older than 4574c4d). Checked after the session-record-limit 409 (`code == "session_record_limit"`), so the two never mix |
| Registry unavailable | HTTP 503 (upload too, was 400) → the ordinary `unavailable` error, retryable, untyped |

Not exposed by any SDK: `GET /records/{id}/explain` (the server honours `?scope=`
there too; no SDK method exists) and gRPC (no SDK has a gRPC transport; the
server's `UpdateRecordRequest.scope=14`, `DeleteRecordRequest.scope=2`,
`SupersedeRequest.scope=3`, and the read requests' `scope=11` are for a future one).

## Delete-by-file answer + tenant id input (2026-11-20, fix/sdk-audit-gaps)

| Topic | Go / Python / TypeScript |
|---|---|
| `alternative_sessions` | `DeleteFileResult.AlternativeSessions []FileSessionCandidate` / `.alternative_sessions: List[FileSessionCandidate]` (default `[]`) / `.alternative_sessions?: FileSessionCandidate[]`. Element `{session_uuid, matched_count, root_checksum?, root_filename?}` = server `database.FileSessionCandidate` (`server/database/file_record_sessions.go:38-48`). Filled only when `matched_count == 0`: absent/empty = prefix not in this store; non-empty = wrong session (re-issue the strict delete with a candidate after checking `root_checksum`) |
| `purge` | `DeleteFileResult.Purge bool` / `.purge: bool` (default `False`) / `.purge?: boolean`. Server `json:"purge"`, always sent: `true` = gone for good (shared plane), `false` = restorable tombstone (chat plane) |
| `raft_index` | **Removed in 4.0.0** from all three — the server never serialises it (`json:"-"`, `server/service/record_delete_file.go:42`) |
| Empty arguments | Missing session (outside `tenant_shared`) or ingest-key prefix: typed validation error, invalid_request / non-retryable, no request sent — Go `*APIError` (clientSide, 400), Python `AnhurQueryError(400)`, TypeScript `AnhurQueryError(400)`; message `INVALID_PARAM: <method>: <argument> is required` (since 2026-11-21; was `fmt.Errorf` / `ValueError` / `Error`) |
| Tenant id rule | Server identifier rule (`database.validateIdentifierCharset` + `MaxIdentifierLength`): `[A-Za-z0-9_-]`, ≤ 64; empty = not set. Charset checked before length |
| Tenant id refusal | Typed validation error, invalid_request / non-retryable, no request sent: Go `*APIError` (clientSide, 400) from the FIRST call (`NewMemory` has no error return; checked per request because `HTTPConnection.TenantID` is exported); Python `AnhurQueryError` from `HTTPConnection`/`Memory` construction; TypeScript `AnhurQueryError` from `new Memory`/`HttpClient`. Messages pinned byte-identical: `INVALID_PARAM: tenant_id contains an invalid character — allowed: letters, digits, '-' and '_'` and `INVALID_PARAM: tenant_id exceeds 64 characters (got N)` |

Why reject instead of the old behaviours (Go silently dropped the header, Python
and TS only blocked control characters): the server's master-key path STRIPS
every other character and falls back to `default` when nothing is left
(`server/middleware/auth_master_tenant.go`, `sanitizeTenant`), so both a dropped
header and `acme/prod` → `acmeprod` acted on another tenant's store with no error.

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
| Filter/sort column whitelist (17 columns, mirrors the server) | yes, raises `AnhurQueryError` (invalid_request; `ValueError` before 2.1.0) | yes, throws `AnhurQueryError` (invalid_request) | yes, since v2.0.13 — `Validate()` errors locally, `query: field "<f>" is not allowed in filters` |
| Operator whitelist (`$eq $gt $gte $lt $lte $in`) | yes, raises `AnhurQueryError` | yes, throws `AnhurQueryError` | n/a — `QueryOp` is a closed struct, no unknown operator is expressible |
| `limit` range 1–1000 | yes, raises `AnhurQueryError` | yes, throws `AnhurQueryError` | yes, since v2.0.13 — `Validate()` errors locally instead of relying on the server's silent fallback |
| `offset >= 0` | yes, raises `AnhurQueryError` | yes, throws `AnhurQueryError` | yes, since v2.0.13 — `Validate()` errors locally instead of relying on the server's silent fallback |
| Cannot emit an empty operator object | yes (fluent path) | yes (fluent path) | yes, since v2.0.13 — `QueryOp.isEmpty()` inside `Validate()` |
| Empty `$in` guarded before the request | yes (`AnhurQueryError`, amended 2026-11-22: the table said "no") | yes (`AnhurQueryError`, same amendment) | yes, since v2.0.13 — `QueryOp.hasEmptyInList()` gives it a **dedicated** message (`$in requires a non-empty list of values`), distinct from the generic "no operator" case |

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

> **Amendment 2026-09-14 — the paragraph above is kept verbatim for history, and
> its VERDICT is wrong. This is not a gap, and Go is the SDK that is right.**
> The mechanics it describes are accurate and unchanged: `omitempty` erases a nil
> interface, `QueryOp{Eq: nil}` is byte-identical to `QueryOp{}` on the wire, and
> `$eq: null` cannot leave the Go SDK. What is wrong is calling that a
> deficiency to be closed with an `Option[T]` wrapper.
>
> The exhaustive live AST matrix (398 wire exchanges against the production
> router, 2026-09-14) established that the server compiles `$eq: null` to
> `col = ?` bound to NULL, and `col = NULL` is never true in SQL. Read-only
> probes on the same day, on a tenant whose unfiltered page returns 1000 rows:
>
> | request body | result |
> |---|---|
> | `{"filters":{"superseded_by":{"$eq":null}}}` | HTTP 200, `count=0` — although EVERY row the server returns satisfies `superseded_by IS NULL` |
> | `{"filters":{"valid_until":{"$eq":null}}}` | HTTP 200, `count=0` |
>
> So `$eq: null` is a predicate that returns 200 with **always zero rows on every
> input**. Python and TypeScript can put it on the wire; that is not a capability
> Go is missing, it is a way to write a dead query that never complains — exactly
> the silent-wrong-answer class this spec exists to eliminate.
>
> Corrected parity statement: `$eq: null` is **unreachable from Go by
> construction, and correctly so**. The Go SDK refuses it locally with a named
> error before spending a request. The open item, if any, belongs to the other
> two SDKs (warn or refuse), not to Go. The old advice to "bypass the SDK and post
> raw JSON" to obtain it is **withdrawn**.
>
> Go-side evidence: `v2/golang/client/query_execute_test.go`
> (`TestQueryOpNilFieldsVanishOnTheWire`), and the reworded local error now opens
> with "no operator survived encoding" and goes on to name `omitempty` and
> `QueryOp{Eq: nil}` explicitly, replacing the old `has no operator` text — which
> told a caller who had set `Eq` to set `Eq`.
>
> Also amended the same day: `Memory.Query` dropped its `...ReadOption` variadic
> (it accepted `WithAsOf`/`WithSince`/`WithUntil`/`WithKeyword` and discarded
> them; `POST /api/v1/query` has no temporal or keyword surface — confirmed live
> for both query parameters and top-level body keys). Python and TypeScript owners
> should confirm their `query()` entry points do not accept temporal arguments on
> this route either; that check has NOT been done here — this amendment covers the
> Go column only.

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

## 4.0.0 — legacy removal (2026-11-21)

AnhurDB `main` is the only server and there are no external clients, so every
branch that existed only for an older server, and every deprecated alias, is gone
— identically in the three SDKs. Per-symbol detail and server evidence: each
SDK's `CHANGELOG.md`.

| Removed | Go | Python | TypeScript |
|---|---|---|---|
| Deprecated aliases | `RecentMemories`, `UpdateConsolidateIDs`, `SearchWithSignals` | `AnhurClient`, `search_with_ast`, `mark_consolidated`, `link_to_consolidated`, `update_consolidate_ids`, `CreateRequest.uuid` | `updateConsolidateIds`, `SearchSessionPayload` |
| Record status `pending` ("pre-v2") | `models.StatusPending` | `MemoryStatus.PENDING` | `"pending"` in the `MemoryStatus` union |
| `DeleteFileResult.raft_index` | `RaftIndex` | `raft_index` | `raft_index` |
| `add()` ingest-404 → `/records` fallback (+ sticky state) | yes | yes | yes |
| Session-limit sentence match / number parse | yes | yes | yes |
| Collision sentence match + upload-400 branch | yes | yes | yes |
| Bare-array / non-envelope tolerance on listings | yes | yes | yes |
| Entity `type` alias for `entity_type` | — | `EntityModel` alias | — |
| `profile()` 404 → empty profile | yes | yes (matched `"404"` in the message text) | yes |
| `mode="mcp"` tunnel transport | — | yes (Python-only) | — |

`AddResult.mode` values are now `"ingest"` / `"records"` (were `"cloud"` / `"oss"`).
The Go module path moved to `github.com/Yoven/AnhurDB-SDK/v2/golang/v4`.

### Envelope rule (identical in the three SDKs)

These eight routes always answer a JSON object envelope. The body must be an
object that HAS the list key; the key's value must be a list or `null`
(`null` = zero rows → empty list). A bare array, an empty body, a missing key or a
non-list value raises Go `*UnexpectedResponseError` (`ErrUnexpectedResponse`) /
Python `AnhurUnexpectedResponseError` / TypeScript `AnhurUnexpectedResponseError`,
kind `server`, no status, message
`unexpected response from GET <route template>: expected a JSON object with "<key>"`.

| Route | Key | Go / Python / TypeScript |
|---|---|---|
| `GET /api/v1/sessions/stats` | `sessions` | `ListSessions` / `list_sessions` / `listSessions` |
| `GET /api/v1/recent` | `records` | `Recent` / `recent` / `recent` |
| `GET /api/v1/manifest` | `records` | `ManifestGlobal` / `manifest_global` / `manifestGlobal` (and `CountByType` / `count_by_type` / `countByType`) |
| `GET /api/v1/chats/{uuid}/manifest` | `records` | `ManifestSession` / `manifest_session` / `manifestSession` |
| `GET /api/v1/chats/{uuid}` | `records` | `ListChat` / `list_chat` / `listChat` |
| `GET /api/v1/entities` | `entities` | `SearchEntities` / `search_entities` / `searchEntities` |
| `GET /api/v1/entities/list` | `entities` | `ListEntities` / `list_entities` / `listEntities` |
| `GET /api/v1/records/{id}/entities` | `entities` | `GetRecordEntities` / `get_record_entities` / `getRecordEntities` |

Pinned by `client/envelope_only_test.go`, `tests/test_envelope_rule.py`,
`src/test/envelope_rule.test.ts` (every route × every bad shape, exact message).

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
(Go `*NotFoundError` com `Status`/`Path`, casado por `errors.Is(err, ErrNotFound)`;
Python `AnhurError.status_code`; TS `AnhurError.statusCode`) — nunca parsear a
mensagem. Desde 2026-11-21 a mensagem do 404 é a mesma nos três:
`Resource not found (HTTP 404): <caminho>`.

## Where()/where(): per-column operator accumulation (2026-09-14)

Server grammar fact (proved live by the three-SDK differential harness, one
fixture, one pass, against production): two operators on one column AND
together — `{"score":{"$gte":3,"$lte":5}}` is a legal range.

- **Go bug B3, fixed 2026-09-14**: `QueryRequest.Where` REPLACED the column's
  operator object, so `.Where("score", QueryOp{Gte: 3}).Where("score",
  QueryOp{Lte: 5})` put only `{"score":{"$lte":5}}` on the wire — HTTP 200,
  5 rows where TypeScript and Python returned 3, no error anywhere. Go now
  MERGES per column (client/query_where.go), like the other two.
- **Cross-SDK divergence, deliberate and OPEN — the SAME operator twice on one
  column.** TypeScript (`src/query.ts where()`: `condition[operator] = value`)
  and Python (`anhurdb/query/builder.py where()`: per-field dict assignment;
  the bare `where(field=value)` form even replaces the WHOLE per-column dict,
  so `where(score__gte=3).where(score=5)` silently drops the `$gte`) both
  overwrite last-wins, silently. Go REFUSES loudly with a typed error (kind
  `invalid_request`, retryable false, surfaced at Validate/Build/Query): the
  wire format has one slot per operator, so keeping either value silently
  discards a predicate — the exact defect class B3 was. The refusal is the
  INTENDED behaviour for all three SDKs; TypeScript and Python owners should
  adopt it (including the Python bare-kwarg replace). Until they do, this entry
  is the record that the difference is known and which side is correct.

## RESOLVED (3.0.0): 14 accepted-and-discarded `...ReadOption` variadics in Go client.go (2026-09-14)

> Fixed in 3.0.0 (`client/read_option_support.go`, pinned by
> `read_option_refusal_test.go`); the line numbers below point at the pre-split
> `client.go` and are kept only as history.

Same silent-discard class as the `Memory.Query` variadic deleted on 2026-09-14:
each method ends in `opts ...ReadOption` with a bare `_ = opts` in the body, so
`WithAsOf`/`WithSince`/`WithUntil`/`WithKeyword` compile, are accepted, and are
thrown away before the request is built. Recorded, NOT fixed (client.go is far
past the ~300-line cut; each fix must move its method out first and decide per
route whether the option should be honoured or made uncompilable):

`Profile` (client.go:495), `ListSessions` (:552), `GetContext` (:608),
`ReadContent` (:634), `Recent` (:653), `BatchReadContent` (:853),
`UploadStatus` (:965), `ListEntities` (:1008), `SearchEntities` (:1042),
`EntityGraph` (:1110), `EntityTimeline` (:1136), `GetRecordEntities` (:1207),
`GetSessionHistory` (:1240), `GetSessionClusters` (:1258).
