# AnhurDB SDK v2 — Canonical Parity Spec

> **Status:** ✅ IMPLEMENTED (2026-06-18) — all three build green, parity verified, adversarial-review
> defects fixed. See "Implementation result" at the end. The three SDKs (`golang`, `python`, `typescript`)
> MUST converge to this single surface. Per the parity invariant, every method below ships in
> all three in the same change. This doc is the source of truth; deviations are bugs.

## Principles

1. **One client class.** A single `Memory` class/type per SDK. Python's `Memory`+`AnhurClient`
   split collapses into `Memory` (`AnhurClient` kept only as a thin deprecated alias for one release).
2. **Method name = MCP tool name**, in idiomatic casing (Go `PascalCase`, Python `snake_case`,
   TS `camelCase`). This is what makes the SDKs mirror the MCP surface and each other.
3. **Same params, same order, same semantics** across languages (modulo language idiom:
   Go `opts ...Option`, Python kwargs, TS options object).
4. **No silent loss / fail loud** — every method surfaces server errors; no swallowed failures.

## Canonical surface (target — all three SDKs)

Legend per cell: `✓` already present & correct · `+` must ADD · `~name` RENAME from existing.

| MCP tool | Canonical (Go / Py / TS) | Endpoint | Go | Py | TS |
|---|---|---|---|:--:|:--:|:--:|
| ingest_memory | `Add` / `add` / `add` | POST /ingest→/records | ✓ | ✓ | ✓ |
| create_memory | `Create` / `create` / `create` | POST /records | +full (was text-only `CreateInSession`) | ✓ `create(req)` | +full (was `createInSession`) |
| update_memory | `Update` / `update` / `update` | PATCH /records/{id} | ✓ | ✓ | ✓ |
| (delete) | `Delete` / `delete` / `delete` | DELETE /records/{id} | ✓ | ✓ | ✓ |
| supersede_record | `Supersede` / `supersede` / `supersede` | POST /records/supersede | ✓ | ✓ | ✓ |
| read_content | `ReadContent` / `read_content` / `readContent` | GET /records/{id}/content | ✓ | ✓ | ✓ |
| get_memory_context | `GetContext` / `get_context` / `getContext` | GET /records/{id}/topology | ✓ | ✓ | ✓ |
| recall | `Recall` / `recall` / `recall` | (see fidelity note) | ~fidelity | ~fidelity | ~fidelity |
| semantic_search | `Search` / `search` / `search` (global) | POST /search/global | ✓ | ✓ | ✓ |
| semantic_search (session) | `SearchSession` / `search_session` / `searchSession` | POST /search | + | ✓ | + |
| search_by_type | `SearchByType` / `search_by_type` / `searchByType` | GET /search/type | ✓ | ✓ | ✓ |
| smart_search | `SmartSearch` / `smart_search` / `smartSearch` | GET /search/smart | ✓ | ✓ | ✓ |
| recent_memories | `Recent` / `recent` / `recent` | GET /manifest|/recent | ~`RecentMemories` | ✓ | ✓ |
| execute_ast | `Query` (fluent) / `query`+QueryBuilder / `query` | POST /query | + | ✓ | +finish (partial) |
| manifest_global | `ManifestGlobal` / `manifest_global` / `manifestGlobal` | GET /manifest | + | ✓ | + |
| manifest_session | `ManifestSession` / `manifest_session` / `manifestSession` | GET /chats/{uuid}/manifest | + | ✓ | + |
| list_chat | `ListChat` / `list_chat` / `listChat` | GET /chats/{uuid} | + | ✓ | + |
| count_by_type | `CountByType` / `count_by_type` / `countByType` | GET /manifest (limit=0) | + | ✓ | + |
| list_types | `ListTypes` / `list_types` / `listTypes` | local (no REST route; static taxonomy) | + | + | + |
| list_sessions_stats | `ListSessions` / `list_sessions` / `listSessions` | GET /sessions/stats | ✓ | ✓ | ✓ |
| get_chat_history_paginated | `GetSessionHistory` / `get_session_history` / `getSessionHistory` | GET /sessions/{uuid}/history | ✓ | ✓ | ✓ |
| get_session_clusters | `GetSessionClusters` / `get_session_clusters` / `getSessionClusters` | GET /sessions/{uuid}/clusters | ✓ | ✓ | ✓ |
| session_health | — OUT OF SCOPE | no REST route (gRPC SessionService only) | ✗ | ✗ | ✗ |
| walk_graph | `Walk` / `walk` / `walk` | POST /walk | ✓ | ✓ | ✓ |
| (walk_semantic) | `WalkSemantic` / `walk_semantic` / `walkSemantic` | POST /walk/semantic | ✓ | ✓ | ✓ |
| get_grounding | `GetGrounding` / `get_grounding` / `getGrounding` | GET /records/{id}/grounding | + | + | + |
| analyze_relationship | — OUT OF SCOPE | no REST route (LLM analyst is MCP-internal) | ✗ | ✗ | ✗ |
| batch_read_content | `BatchReadContent` / `batch_read_content` / `batchReadContent` | POST /records/batch-content | ✓ | ✓ | ✓ |
| batch_update_status | `BatchUpdateStatus` / `batch_update_status` / `batchUpdateStatus` | PATCH /records/mark-consolidated | ✓ | ~`mark_consolidated` | ✓ |
| link_consolidated | `LinkConsolidated` / `link_consolidated` / `linkConsolidated` | PATCH /records/consolidate-ids | ~`UpdateConsolidateIDs` | ~`update_consolidate_ids`/`link_to_consolidated` | ~`updateConsolidateIds` |
| (append_main_ids) | `AppendMainIDs` / `append_main_ids` / `appendMainIds` | PATCH /records/append-main-ids | ✓ | ✓ | ✓ |
| upload_file | `UploadFile` / `upload_file` / `uploadFile` | POST /upload | ✓ | ✓ | ✓ |
| upload_status | `UploadStatus` / `upload_status` / `uploadStatus` | GET /upload/{id}/status | ✓ | ✓ | ✓ |
| list_entities | `ListEntities` / `list_entities` / `listEntities` | GET /entities/list | ✓ | ✓ | ✓ |
| search_entities | `SearchEntities` / `search_entities` / `searchEntities` | GET /entities | ✓ | ✓ | ✓ |
| upsert_entity | `UpsertEntity` / `upsert_entity` / `upsertEntity` | POST /entities | ✓ | ✓ | ✓ |
| upsert_entity_edge | `UpsertEntityEdge` / `upsert_entity_edge` / `upsertEntityEdge` | POST /entities/edges | ✓ | ✓ | ✓ |
| link_record_entity | `LinkRecordEntity` / `link_record_entity` / `linkRecordEntity` | POST /entities/link | ✓ | ✓ | ✓ |
| get_entity_graph | `EntityGraph` / `entity_graph` / `entityGraph` | GET /entities/{id}/graph | ✓ | ✓ | ✓ |
| entity_timeline | `EntityTimeline` / `entity_timeline` / `entityTimeline` | GET /entities/{id}/timeline | ✓ | ✓ | ✓ |
| (get_record_entities) | `GetRecordEntities` / `get_record_entities` / `getRecordEntities` | GET /records/{id}/entities | ✓ | ✓ | ✓ |
| get_profile | `Profile` / `profile` / `profile` | GET /profile | ✓ | ✓ | ✓ |
| get_session_clusters | (above) | | | | |

Options/getters (`WithURL/WithUserID/WithTenantID/WithTimeout/WithMinIndex`, `NewSession`,
`SessionID`, `ContainerTag`) stay; align names but already near-parity.

## Per-SDK work

### Go (`golang`) — ADD: SearchSession, Query (AST/fluent), ManifestGlobal, ManifestSession,
ListChat, CountByType, ListTypes (local), GetGrounding; make `Create` full-fidelity
(type/score/related_ids/valid_from via opts). RENAME: `RecentMemories`→`Recent`,
`UpdateConsolidateIDs`→`LinkConsolidated`. (Go is the buildable reference + the plugin dogfoods it.)

### TypeScript (`typescript`) — ADD: searchSession, finish `query` (AST is partial),
manifestGlobal, manifestSession, listChat, countByType, listTypes (local), getGrounding;
make `create` full-fidelity. RENAME: `updateConsolidateIds`→`linkConsolidated`.

### Python (`python`) — Already the most complete. WORK: collapse `AnhurClient` into `Memory`
(single class) keeping `AnhurClient = Memory` deprecated alias; RENAME `mark_consolidated`→
`batch_update_status`, `update_consolidate_ids`/`link_to_consolidated`→`link_consolidated`;
ADD: list_types (local), get_grounding; FIX `append_related_links`
(calls an unregistered route — either register server route or remove). Keep QueryBuilder.

> **Route-verified (2026-06-18, from server/router.go):** feasible REST endpoints confirmed for
> search_session (`POST /search`), query (`POST /query`), manifest_global (`GET /manifest`),
> manifest_session (`GET /chats/{uuid}/manifest`), list_chat (`GET /chats/{uuid}`), count_by_type
> (`GET /manifest?limit=0`), get_grounding (`GET /records/{id}/grounding`). **No REST route** for
> session_health or analyze_relationship (gRPC/MCP-only) → out of scope. **No `/recall`** endpoint
> → `recall` stays a documented alias of `/search/global`, identical in all three (the 4-way
> fan-out+RRF lives in the MCP server, not the data plane). Non-MCP endpoints that exist and Python
> already wraps (`/graph`, `/records/{id}/explain`, `/stats/access`, `/tenant/engine-config`,
> `/records/decay`) are a secondary parity item for Go/TS.

## Correctness items (fold into the same change)

- **`recall` fidelity:** all three currently alias `recall`→`/search/global`. The MCP `recall`
  does a 4-way fan-out (smart + fact-type + consolidated-type + entity) with RRF. Decide:
  either (a) point SDK `recall` at a server fan-out endpoint if one exists, or (b) rename the
  alias to `searchGlobal` and implement a true `recall`. Must be identical across the three.
- **Python `append_related_links`** → unregistered server route; fix or drop (no dead methods).
- **`analyze_relationship`** is LLM-backed (analyst dispatch). Confirm a public REST surface
  exists before adding to the SDKs; if it's MCP-internal only, mark it explicitly out of scope.

## Acceptance

- All three build/compile + lint clean.
- Each canonical method present in all three with aligned name + equivalent signature.
- Parity test: a generated checklist (this table) passes for all three.
- Go validated live (plugin path); Python/TS validated against the same local tenant.

## Implementation result (2026-06-18)

Implemented via a coordinated workflow (contract → 3 parallel implementers → parity + adversarial
review), then hardened by hand. **All three SDKs build green** (Go `go build`+`go vet`, Python
`compileall`/ast, TS `tsc --noEmit`); **AnhurAgents** (live Go consumer) builds clean against the
changed SDK. Every rename kept a deprecated forwarding alias.

**Adversarial-review defects FIXED:**
- 🔴 Python `create()` did not inject `container_tag` → records fell out of every container-scoped
  search/profile (the 2026-05-22 corruption class). Now injects via `_build_metadata_json`.
- 🔴 Python `batch_update_status` dropped the `status` arg (could only ever mark "consolidated"). Now
  `batch_update_status(ids, status)` sends status; `mark_consolidated` alias passes "consolidated".
- 🟡 metadata-only `add()` silently dropped the metadata on Go & TS (ingest body is only
  `{content, container_tag}` — verified in `handler/ingest.go`). Routing now forces the records path
  on metadata in all three (Python already did — the reviewer's direction was inverted).
- 🟡 Go `models.Record` used `related_json`/`main_json` tags while reads emit `related_ids`/`main_ids`
  → graph edges silently nil on the new Query/ListChat/Manifest reads. Tags corrected (SDK never
  marshals Record for writes — verified safe).
- 🟢 Python canonical `query()` added (`search_with_ast` → deprecated alias) — closes the last gap.
- 🟢 TS removed the phantom `query` payload key the global-search handler ignores.

**Anchor-seed — RESOLVED (2026-06-18):** a derived-type write into a session with no episodic anchor
now seeds an episodic anchor and retries the write ONCE in all three SDKs — Go via
`postRecordSeedingAnchor` (wired into both `Create` and the `add()` path `createRecord`), Python via a
`create()` catch-seed-retry, TS as the reference (`createRecord`). Detected on the server's
"…without an episodic anchor…" 422; episodic writes never trigger it. Live-validated on the local
tenant: `Create(type="fact")` into a fresh session succeeded and the session ended with exactly one
episodic anchor + the fact.

**Follow-ups — ALL RESOLVED (2026-06-18):**
- **weight seed** aligned to `score/10` across the three create paths: Go `Create` + `createRecord`
  now derive it; Python `create()` seeds it from score when the caller didn't pin `weight`; TS already
  did via `createRecord`. Episodic anchor seeds keep weight 0.5. (The regression agent still recomputes
  weight server-side — this only keeps the SDK seed identical across the three.)
- **summary truncation** made codepoint-safe everywhere: Go `truncateSummary` (`[]rune`), TS
  `truncateSummary` (`Array.from`), Python `str[:200]` (already codepoint) — same 200-code-point cut,
  no split UTF-8 runes / UTF-16 surrogates.
- **write-side edge keys** — RECONCILED in the AnhurDB server (2026-06-18). **Correction:** my earlier
  "not a bug" call was WRONG — I assumed without reading the Update handler (a never-assume miss). The
  PATCH `/records/{id}` handler read ONLY `related_ids` (json tag) and had NO `main_ids` field at all,
  so the validator's `related_json`/`main_json` updates were **silently DROPPED** (its relationship
  repairs never applied — the likely cause of the validator re-flag loop). Server fix: (1) the Update
  handler now accepts `related_ids`+`main_ids` (canonical, matching Create) AND `related_json`+
  `main_json` (legacy aliases), coalesced; (2) new `CommandReplaceMainIDs` + `db.ReplaceMainIDs` so
  main_ids replacement actually works (`AppendMainIDs` could only add); (3) `AnhurAgents/cmd/validator`
  migrated to the canonical keys. Create + Update are now consistent; server + AnhurAgents build green.
  Follow-ups: the gRPC `UpdateRecordRequest` proto has `related_ids` but no `main_ids` (REST has it —
  needs a proto field + regen); the new Raft command requires all nodes on the new binary before it is
  issued (rolling deploy).
