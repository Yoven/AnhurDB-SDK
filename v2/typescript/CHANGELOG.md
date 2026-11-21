# TypeScript SDK Changelog

## Unreleased

### Changed — 401/403 keep the server's reason (identical in Go, Python and TypeScript)

- Every 401/403 now throws `AnhurAuthRefusedError` (extends `AnhurAuthError`,
  so `instanceof AnhurAuthError` is still true) with `statusCode`, `code`
  (`body.error` verbatim, parsed whatever the Content-Type — the auth
  middleware answers `text/plain` with a JSON body), `detail`, `errorType`, and
  the getters `isL1Only`, `isChildSuspended`, `isInsufficientScope` (prefix
  `insufficient scope`), `isImpersonationRequired`, `isInvalidKey`; constants
  `AUTH_CODE_*`. Module `src/authErrors.ts`; exported from the package root.
- `err.name` is now `"AnhurAuthRefusedError"` (was `"AnhurAuthError"`); branch
  on `instanceof`, not on `name`.
- Message with a code: `Authentication failed (HTTP <s>): <code>[: <detail>]`.
  Non-JSON body: unchanged (`Authentication failed (HTTP <s>)`).
- `AnhurClientAPIError.fromBody` uses the same envelope decoder
  (`decodeErrorEnvelope`); behaviour unchanged.
- No gRPC transport exists in this SDK, so there is no gRPC mapping.

### Added — client-key API (ADR-0034), identical in Go, Python and TypeScript

- `new ClientAPI({ clientKey, url?, timeoutMs? })` for `/api/v1/client/*`:
  `createChild`, `listChildren`, `suspendChild`, `resumeChild`, `listTenants`,
  `listChildTenants`, `createTenant`, `createChildTenant` (optional
  `profile: { config, maxRpm }`), `impersonate`, `impersonateChild`
  (`{ tenantId, scope, l1Only, description, ttlSeconds }` → `ImpersonatedKey`),
  `revokeImpersonation`. Never sends `X-Tenant-ID`. Modules
  `src/clientKey*.ts`; shapes taken from the AnhurDB handlers
  `server/handler/client_*.go` (branch `feat/phase1-client-children`).
  Exported from the package root with its types and `KEY_SCOPE_*` constants.
- `AnhurClientAPIError` exposes the server code (`body.error`), `detail`,
  `errorType` and `isNotLeader`; `child_limit`/`tenant_limit` are not
  retryable.
- Fail-closed impersonation echo: `AnhurClientAPIResponseError` with `code`
  `l1_only_not_honoured`, `scope_not_honoured` or `unexpected_response`
  (carries the minted `keyId`).
- `DEFAULT_CLOUD_URL` in `src/memory.ts` is now exported internally so the new
  transport reuses it (not re-exported from the package root).

## 4.0.0 — legacy removal (2026-11-21)

**Breaking.** AnhurDB (`AnhurDB/server`, main) is the only server and it always
speaks the current contract, so every branch that existed only for an older
server, and every deprecated alias, is removed. Go, Python and TypeScript remove
the same things in the same release.

### Aligned across the three SDKs (identical behaviour, 2026-11-21)

- **Envelope rule on the eight listing routes.** `listSessions`, `recent`, `manifestGlobal`, `manifestSession`, `listChat`, `searchEntities`, `listEntities`, `getRecordEntities` (and `countByType`, which walks the manifest). `manifestGlobal`/`manifestSession` now always return `records` as an array (`null` → `[]`) require the JSON
  object envelope with its list key (`sessions` / `records` / `entities`); the
  value may be a list or `null` (`null` = zero rows). A bare array, an empty
  body, a missing key or a non-list value now raises `AnhurUnexpectedResponseError` (extends `AnhurError`, `kind === "server"`, exported from the package root), new module `src/envelope.ts`; `EntitiesListPage` type exported, message
  `unexpected response from GET <route template>: expected a JSON object with "<key>"`
  — byte-identical in all three. 3.x read several of these shapes as "no rows".
- **`GET /api/v1/profile` 404 fallback removed.** `profile()` no longer returns an
  empty "OSS mode" profile on 404; the route is mounted unconditionally
  (`server/router.go:136`), so a 404 surfaces as `AnhurError` with `statusCode === 404`.
- **Upload mode refusal.** An unknown upload mode (after trim + lower-case) is the typed invalid_request error, raised before any request, with one message in all three: `INVALID_PARAM: upload: mode '<value as given>' is not an upload mode (want chat|tenant_shared|client_shared)`.
- **Delete-by-file empty arguments.** A missing session (outside `tenant_shared`) or ingest-key prefix is the typed invalid_request error (status 400, not retryable, no request sent), message `INVALID_PARAM: <method>: <argument> is required`.
- **404 message.** Every HTTP 404 reads `Resource not found (HTTP 404): <path>` (API path, no base URL, no query), kind `not_found`, in all three.
- **L2 409 messages.** The tenant-library collision and the ambiguous upload status quote at most 500 characters of the server sentence in the message; the full sentence stays on the error object.
- **Parity nits (audit rows 18-21).** TypeScript changes: `uploadFile` trims and lower-cases `options.mode` and throws on an unknown value (from plain JS, `"Chat"` used to send no mode and land in tenant_shared while `scope` reported another plane); `deleteFile` empty `sessionUuid` / `ingestKeyPrefix` throws `AnhurQueryError` (400, `invalid_request`) instead of a plain `Error`. Messages `INVALID_PARAM: deleteFile: sessionUuid is required`, `INVALID_PARAM: deleteFile: ingestKeyPrefix is required` (upload message below). 404 and L2 409 messages were already in the shared form.
- **Session-record-limit message.** already used the shared layout below; a body whose `limit`/`count`/`session_uuid` is missing or mistyped stays the generic 409. Shared layout:
  `Conflict (HTTP 409): <server error, first 500 characters> (session record limit: session=<uuid>, limit=N, count=M)`.

### Removed

- `DeleteFileResult.raft_index` — the server keeps the consensus log index
  internal (`RaftIndex int64 json:"-"`, `server/service/record_delete_file.go:42`);
  the field was always `undefined`.
- Session-record-limit **sentence fallback**: the `"session has reached the
  maximum of"` prefix match, the regex that read `N` back from the sentence,
  and the regex parse of a non-JSON body. The server always sends the structured
  409 `{error, code: "session_record_limit", limit, count, session_uuid}`
  (`server/handler/service_bridge.go:81-82,109-125`; sole producer
  `server/service/record_session_cap.go:178` via `service/record_create.go:363`).
  A 409 without that code — or with the code but a missing field — is now a
  plain conflict `AnhurError`. Exported constant `SESSION_RECORD_LIMIT_TEXT_PREFIX`
  (module-internal, not re-exported from `index.ts`) is gone.
- Tenant-library-collision **sentence fallback and the 400 branch**: recognised
  only as HTTP 409 with `code: "tenant_library_collision"`
  (`server/handler/search_scope_request.go:35-43,109-118`; upload door
  `server/handler/upload.go:133-135`). A 400 is always `AnhurQueryError`.
  `AnhurTenantLibraryCollisionError.statusCode` is always 409.
- `add()` **ingest-404 → `/records` "OSS fallback"** and its sticky
  `ingestAvailable` state. `POST /api/v1/ingest` is mounted unconditionally
  (`server/router.go:135`); a 404 there is now thrown (`AnhurQueryError`,
  kind `not_found`) and nothing is written via `/records`. The caller-pinned
  records path (score/type/non-empty metadata, or `mode: "regular"`) stays.
- **Bare-array response fallbacks**: `listSessions()` (`GET /api/v1/sessions/stats`
  always answers `{sessions, count, limit, offset, has_more, next_offset}`,
  `server/handler/record_session.go:135-142`) and `recent()` (`GET /api/v1/recent`
  always answers `{records, count}`, `server/handler/record_session.go` ListRecent).
  A non-envelope body now throws `AnhurError` kind `server` instead of reading as
  data. `records: null` → `[]` coalescing is kept (the server emits null for zero rows).
- `Memory.updateConsolidateIds` — `@deprecated` alias of `linkConsolidated`
  since 2026-06-18.
- `SearchSessionPayload` (type export) — `@deprecated` since 2.1.0, constructed
  by nothing.
- `"pending"` from `MemoryStatus` — a pre-v2 record status the server never
  writes (`"pending"` survives only as the ingest ExtractionStatus,
  `server/service/ingest.go:233`, a different field).

### Changed

- `AddResult.mode`: `"cloud" | "oss"` → `"ingest" | "records"`. The value now
  names the door that wrote (`/api/v1/ingest` or `/api/v1/records`), produced by
  `add()`, `create()` and `createInSession()`.
- `AnhurSessionRecordLimitError.sessionUuid: string`, `.limit: number`,
  `.count: number` — no longer nullable (the server always sends them). Message:
  `Conflict (HTTP 409): <server sentence, ≤500 chars> (session record limit:
  session=<uuid>, limit=<N>, count=<M>)` — byte-identical to Python.
- `DeleteFileResult.purge: boolean` is required (server `json:"purge"`, no
  omitempty; Go `bool`, Python `bool`).
- Internal layout only (no export changed): `memory.ts` (1532 lines) split by
  domain into `lookupApi.ts`, `manifestApi.ts`, `graphApi.ts`, `entitiesApi.ts`,
  `sessionsApi.ts`, `writeApi.ts`, `writePath.ts`, `memoryIdentity.ts`;
  `types.ts` (681) into `coreTypes.ts`, `writeTypes.ts`, `entityTypes.ts`,
  `queryTypes.ts`, `groundingTypes.ts` (+ walk options into `walkTypes.ts`);
  `client.ts` (330) shares one attempt/timeout path in `clientFetch.ts`.
  `Memory` is still one class; `types.ts` re-exports every type.

### Migration

- `mem.updateConsolidateIds(ids, star)` → `mem.linkConsolidated(ids, star)`.
- `result.mode === "cloud"` → `=== "ingest"`; `=== "oss"` → `=== "records"`.
- Drop `null` checks on `err.sessionUuid` / `err.limit` / `err.count`.
- Stop reading `DeleteFileResult.raft_index`; replace `SearchSessionPayload`
  with `SearchPayload` (`scope: "sessions"`, `sessions: [uuid]`).

## Unreleased — delete-by-file `alternative_sessions`, deprecated `raft_index`, tenant id validation (2026-11-20)

Go, Python and TypeScript ship it together (branch `fix/sdk-audit-gaps`).

- **New:** `DeleteFileResult.purge?: boolean` — server's always-sent `purge`
  flag (true = gone for good on the shared plane, false = restorable tombstone).
- **New:** `DeleteFileResult.alternative_sessions?: FileSessionCandidate[]` and
  exported type `FileSessionCandidate` — the server's zero-match diagnostic.
- **Deprecated:** `DeleteFileResult.raft_index` (`@deprecated`). The server never
  serialises it (`json:"-"`), so it was always `undefined`. Kept so existing code
  compiles; removed in the next major. Same decision in all three SDKs.
  **Done in 4.0.0** (see above).
- **Behaviour change:** `tenantId` must follow the server's identifier rule
  (`[A-Za-z0-9_-]`, at most 64; `""` = not set). The constructor now throws
  `AnhurQueryError` (invalid_request) — previously a plain `Error`, and only for
  control characters.

## Unreleased — by-id scope for the L2 tenant library (2026-11-20, ADR-0029)

Additive (optional trailing `options`; existing calls unchanged). Go, Python and
TypeScript ship it together. Server: AnhurDB `fix/l2-lib-scope`.

- **New:** `RecordScope` (`"sessions" | "tenant_shared" | "tenant_shared_legacy" |
  "client_shared"`) and `ByIdOptions { scope }` on `get`, `readContent`,
  `getContext`, `getGrounding`, `batchReadContent`, `update`, `delete`,
  `supersede`, `uploadStatus` (sent as `?scope=`), plus `waitForUpload`
  (`UploadWaitOptions.scope`). Omitted = unchanged request; `shared_all`/unknown
  values throw `AnhurQueryError` (invalid_request) before any request.
- **New helpers:** `tenantSharedScopeForHit(uuid)`, `uploadStatusScope(mode, sessionId)`,
  `RECORD_SCOPES`.
- **Upload plane travels to the status poll:** `UploadResult.scope` (SDK-filled).
  New `AnhurUploadStatusAmbiguousError` for the server's 409.
- **Delete-by-file:** `DeleteFileOptions.scope` (`tenant_shared` | `client_shared`;
  session may be empty with tenant_shared), `DeleteFileResult.legs`
  (`DeleteFileLeg`), and `AnhurLibraryDeletePartialError` (`.legs`, HTTP 500
  `partial: true`, retryable — the server delete is idempotent).
- **New typed refusal:** `AnhurTenantLibraryCollisionError`: HTTP 409
  `"code": "tenant_library_collision"` on every door, upload included (server
  4574c4d). Classified by the code first; the sentence is a fallback only for a
  body without a code (older servers, incl. their 400 on upload). **Fallback removed in 4.0.0.**
  Non-retryable; the session-record-limit 409 is checked first and stays
  distinct. An unreadable tenant registry (503, upload too) stays the plain
  retryable `unavailable` error.
- **Transport:** failed bodies are now READ up to 64 KiB for classification
  (messages still quote 500 characters) — the 500-char read cut the partial
  delete JSON short and lost its legs.
- **Moved (house 300-line cut):** by-id methods to `src/recordsApi.ts`
  (`MemoryRecordsApi`), upload + `deleteFile` to `src/filesApi.ts`
  (`MemoryFilesApi`); `Memory` extends them, surface unchanged. Delete-file
  types to `src/deleteFileTypes.ts` (re-exported from `types.ts`). Tests:
  `src/test/record_scope.test.ts`.

## Unreleased — typed session-record-limit refusal (2026-11-20, ADR-0033)

Additive; no version number assigned yet (not published). Go, Python and
TypeScript ship it together.

- **New:** `AnhurSessionRecordLimitError` (extends `AnhurError`, exported from the package root). Raised when a write hits a session that already holds its
  maximum number of records — `sessionUuid`, `limit`, `count` (`number | null`), `serverMessage`, `code`; `kind` conflict, `retryable` false, `statusCode` 409. JSON and multipart paths both throw it.
- **Both server generations recognised:** new servers send HTTP 409 with
  `code: "session_record_limit"` plus `limit`/`count`/`session_uuid`; old
  servers send only `{"error": "session has reached the maximum of N records"}`.
  Missing fields are null (never a fake zero); `limit` is read back from the
  server's own sentence when the field is absent. Any other 409 keeps the
  generic error.
- **Non-retryable**, and that does not mean "drop it": keep the write and
  retry later; never write it into another session to dodge the cap.
- **No gRPC path:** the SDK has no gRPC transport; the gRPC form of the
  contract (ResourceExhausted / legacy AlreadyExists) is documented in
  `v2/SDK_ERROR_CONTRACT.md` for any future transport.
- Code: `src/sessionLimit.ts`; tests in `src/test/session_record_limit.test.ts`.

## 3.0.0 — SDK parity: the types stop lying (2026-09-14)

Released together with Go and Python at the SAME number. This round closes the
last divergences between the three arms against the REAL API: routes in
`AnhurDB/server/router.go`, handlers beside them, every field set re-proved by
a live call to `https://anhurdb.yoven.ai` on 2026-09-14. A behaviour all three
SDKs agreed on that the server does not support was still wrong, and several
of the fixes below are exactly that case.

It is a MAJOR because four public shapes and one method signature change.
Every break is compile-time: nothing here fails at runtime in a way a
TypeScript caller can miss.

### Breaking — `create()` takes the session as its FIRST argument

```ts
// 2.1.0
await mem.create(text, { sessionUuid: session, type: "fact" });
await mem.create(text);                        // ambient session, invisibly

// 3.0.0
await mem.create(session, text, { type: "fact" });
await mem.create(await mem.createSession(), text);
```

`sessionUuid` and `sessionId` are REMOVED from `CreateOptions`. The old
signature had three ways to name the session plus an invisible fourth: when no
option was given, the record went to whatever session the `Memory` instance
happened to be holding. `POST /api/v1/records` cannot succeed without a
session, so that fallback never prevented an error — it only decided,
silently, WHERE the record landed. A forgotten option misfiled a record
instead of failing. The session is now positional, matching Go's
`Create(ctx, sessionUUID, content, opts...)`, and a blank one throws locally
before any HTTP.

`CreateOptions` also gains `status`, the one field of the shared optional set
(`type, score, status, related_ids, valid_from, valid_until, metadata`) that
TypeScript could not express — `createRecord` hardcoded `"saved"`.

### Breaking — `SessionStats.last_active` is really `last_activity`

`database/list_sessions.go:37-43` and the live row both say `last_activity`.
The wrong spelling never threw: `row.last_active` on a row that does not carry
it is `undefined`, so every "sort by last activity" silently sorted by nothing
and every "last seen" rendered blank. Go had it right since it was written.

Also **added**, both previously unreachable from TypeScript: `types`
(`Record<string, number>`, the per-type histogram) and `summary?`.

Note the profile's `stats.last_active` is NOT a typo and was not touched: the
server genuinely uses two spellings for two different objects
(`handler/profile.go:50` vs `database/list_sessions.go:41`), both confirmed
live on the same day.

### Breaking — `BatchUpdateResult` described a response that does not exist

`handler/record_batch.go:212` is the only success path of
`PATCH /api/v1/records/mark-consolidated` and it writes one literal:
`{"message":"marked consolidated"}`. `updated_count` has never existed on any
code path, so `result.updated_count === ids.length` compiled and was always
false. The interface now declares `{ message: string }`. The exported NAME is
kept deliberately — changing the body is the fix, removing the name would be a
second break for no gain. Live-confirmed: the ack came back with exactly one
key, `message`.

### Breaking — `UploadResult` had a phantom `id` and was missing five fields

`handler/upload.go:109-119` sends a fixed nine-key map, and `id` is not one of
them; the polling key is `record_id`. A phantom optional is the worst lie a
type can tell — `upload.id ?? upload.record_id` reads like a careful fallback
whose first branch is dead code no test can reach. **Removed** `id`;
**added** `message`, `mime`, `mime_detected`, `extension`, `size_bytes`.
Live-confirmed against a real upload: the 202 body carried exactly
`[extension, filename, message, mime, mime_detected, record_id, size_bytes,
status, uuid]`, and no `id`.

### Breaking — `WalkResult` nodes are FULL records; edges have two keys

`POST /api/v1/walk` accumulates `map[int64]*model.Record` and marshals the
records whole — live, every node carried all 14 record keys. The old
`{id, type, summary, weight}` projection was not wrong on the wire, it was
wrong in the type: ten real fields hidden from every caller. `nodes` is now
`MemoryRecord[]`, so a walk node composes with the rest of the SDK.

**Removed** `type` from the edge shape (both handlers build a two-field
`{source, target}` struct and one of them carries a comment calling that shape
frozen). **Added** `truncated?: boolean`.

`truncated` is OPTIONAL, deliberately, against the letter of the parity spec:
`/walk` sends it (`record_search_graph.go:222`) and `/walk/semantic` does not
(:337-340) — confirmed live the same day, `[edges, nodes, truncated]` versus
`[edges, nodes]`. Both routes return this type, so declaring it required would
have replaced one phantom with another. Read it as `result.truncated === true`;
`undefined` means "the semantic route did not say", which is not the same claim
as "nothing was cut".

### Breaking — `ProfileResult` models three CLOSED blocks

`handler/profile.go:27-51` declares `static`, `dynamic` and `stats` as closed
Go structs, not maps. They were typed `Record<string, unknown>` behind a
`[key: string]: unknown` index signature, and that signature was not a
forward-compatibility hatch — it was a hiding place. It let the SDK's OWN 404
fallback invent `tag` and `status: "not_available"`, two keys the handler has
never emitted, and the compiler accepted them as if the server had sent them.
Callers branching on `profile.status` were branching on an SDK fiction.

`static` / `dynamic` / `stats` are now concrete, the index signature is gone,
and the 404 fallback returns the same all-zero profile the hosted server
returns for a tag it does not know.

### Added — `profile(containerTag?)`

`tag` is an IN-TENANT filter, not a tenant selector: `handler/profile.go:63-66`
takes the tenant from the auth middleware and reads `tag` only from the query
string. Measured live with the owner key: another container's tag answered 200
with ITS 6 records; an invented tag answered **200 with an empty profile**, not
404; `*` is a literal tag, not a wildcard; and an EMPTY tag is a guaranteed
**400** (`tag: tag is required`).

So there is no authorisation hole to close — the gap was that TypeScript could
not express a tag at all. `profile(containerTag?)` defaults to the client's own
derived tag and NEVER puts a blank tag on the wire. An unknown tag stays an
empty profile: turning it into an exception would hide a typo behind a fake
server failure.

### Unchanged, and asserted so it stays that way

- **`entityGraph` depth**: the server's default is **1**
  (`handler/entity.go:280`), and this SDK already omitted the param when the
  caller did not ask. Live: omitted → `depth:1`, `?depth=2` → `depth:2` — the
  value changes the graph. Python defaulted to 2 and is the arm that changed.
  A test now locks "no `depth` param unless asked".
- **`smartSearch`**: `SmartSearchResponse` / `SmartSearchHit` were already the
  reference implementation for the other two arms, nullable `results`
  included. No change.

### Housekeeping

`types.ts` was past the ~300-line house cut, so the shapes this round touched
moved to domain files instead of growing it: `profileTypes.ts`, `uploadTypes.ts`,
`walkTypes.ts`, and `SessionStats` into `sessionStats.ts` beside the paging loop
that consumes it. `memory.ts` was over the cut too, so `profile()`'s body moved
to `profile.ts` and `truncateSummary` to `summary.ts`; the file ends this round
SMALLER than it started. Every moved type is re-exported from `types.ts`, which
stays the import path for callers — no import in `index.ts` changed.

## 2.1.0 — ADR-0031 search controls (2026-09-05)

Released together with Go and Python at the SAME number. Before this release
the three SDKs carried four mutually inconsistent versions (Python 2.0.20, Go
2.0.18, TypeScript 2.0.17 on the tarball, `2.0.0` in every manifest) while all
three already claimed `2.1` in their `User-Agent`. 2.1.0 is above every shipped
tag and makes the wire-visible claim true.

### Added — the three ADR-0031 search controls

Available on `search`, `searchWithRetrieval`, the four scope wrappers,
`searchSession` and `recall`:

| Option | Wire field | Meaning |
|---|---|---|
| `mode` | `mode` | `"fast"` \| `"balanced"` \| `"semantic"`; unset = the server's `balanced` |
| `semanticTimeoutMs` | `semantic_timeout_ms` | Per-query budget for embed+HNSW; unset/0 = the server default (700 ms) |
| `debugSignals` | `debug_signals` | Ask for per-hit signals and the pre-fusion `leg_scores` |

All three keep the SDK's falsy-omit / nil-vs-zero discipline: a caller who does
not opt in posts a byte-identical body to 2.0.x.

`mode` is validated CLIENT-SIDE. `handler.normalizeSearchMode` folds any
unrecognised string into `balanced`, so a typo would otherwise buy silent
lexical results; `mode: "semanitc"` now throws
`AnhurError("INVALID_PARAM: 'mode' must be one of \"fast\", \"balanced\", \"semantic\", got …")`
before any request leaves the process.

### Added — the richer response

- `SearchHitSignals` now carries all **13** server fields: the seven added by
  ADR-0031 Stage 2 are `hnsw_rank`, `bsq_rank`, `parquet_rank`, `fts5_rank`,
  `astar_rank`, `entity_jaccard_rank` and `active_leg_weight_sum`.
- New `LegScoreSummary` (`leg` / `candidates` / `top_scores` / `mean` /
  `stddev`), surfaced as `legScores` on `searchWithRetrieval`'s result. It is
  read from the response's **top level**, beside `retrieval`, because that is
  where the server puts it (`handler/bundle.go:attachLegScores`) — not from
  inside `RetrievalMeta`, where it would have been `undefined` forever.
- `searchWithRetrieval` now returns the named `SearchWithRetrievalResult`
  (`{ results, retrieval?, legScores? }`); the previous inline shape is a
  subset of it, so no caller breaks.

### Added — the cross-version guard (fail loud on an old server)

An additive proto3/JSON field is compatible for the PARSER, not for the
MEANING. A server that predates ADR-0031 drops `mode` into unknown fields,
runs `balanced`, and answers **HTTP 200 with lexical results** — while the
caller believes it asked for strict semantics.

The response is the honest witness: a current server ALWAYS fills
`retrieval.mode`. So:

- asked `mode: "semantic"` and `retrieval.mode` came back different (or
  absent) → **throws** `AnhurError` naming the server as too old;
- `semanticTimeoutMs` / `debugSignals` ignored → **one warning per process**
  (they degrade without lying about which records came back).

One measured exception, shared with the Go SDK: `scope: "shared_all"` warns
instead of throwing. A CURRENT server leaves `retrieval.mode` empty for that
scope on purpose — `handler/record_search_shared_all.go` builds the merged meta
by hand and two legs have no single honest mode — so throwing there would
reject healthy servers on every `searchShared`. The mode IS honoured
server-side; it is simply not echoed.

The validation and guard strings match the Go SDK verbatim
(`INVALID_PARAM: 'mode' …`, `SERVER_TOO_OLD: requested mode="semantic" …`,
`anhurdb-sdk: warning: …`).

No environment variable and no constructor option gates this: the detection is
derived from a response the server already sends.

### Fixed — knobs that were silently dropped

- `searchSession` built its own payload and forwarded only `limit` and
  `typeFilter`, so `skipQueryEmbed`, `skipCognitiveRerank`, the weight
  overrides and `expandRelated` never reached the server. It now uses the same
  single payload builder as `search`. `SearchSessionPayload` is deprecated;
  nothing constructs it any more.
- `recall` re-listed the three fields it forwarded, so anything newer was lost.
  It now spreads the caller's options.

### Added — `containerTag` on the public surface

`memory.containerTag` (sync) and `memory.getContainerTag()` (awaits the async
tag derivation, like `getSessionId()`). Parity with Go `Memory.ContainerTag()`
and Python `Memory.container_tag`; TypeScript never exposed it, so a caller
could not tell which container their writes landed in.

### Added — exports that existed but were unreachable

`AnhurUploadWaitTimeout`, `AnhurErrorKind`, `WalkTarget`,
`WalkSemanticOptions`, `QueryParams` — plus the new `SearchMode`,
`LegScoreSummary`, `SearchWithRetrievalResult`, and `SDK_VERSION` /
`USER_AGENT`.

### Changed — one version constant

`src/version.ts` now holds `SDK_VERSION`, and the `User-Agent` is built from
it (`AnhurSDK-TypeScript/2.1.0`, full semver instead of the old hand-typed
`2.1`). `version.test.ts` fails the build if it ever drifts from
`package.json` again.

### Internal — house 300-line split, done BEFORE the feature

`src/memory.ts` (2150 lines) and `src/types.ts` (981) were both far past the
~300-line cut, and house law forbids growing such a file: split the domain you
are about to touch first. The hybrid-search domain now lives in
`src/search.ts` (public methods, as the `MemorySearchApi` base class `Memory`
extends), `src/searchRequest.ts` (payload assembly, validation, the guard) and
`src/searchTypes.ts` (wire types, re-exported by `types.ts`). No public name
moved: every import and every `mem.search(...)` call site is unchanged.

## Server-side behaviour change on `POST /api/v1/query` (2026-07-29, no SDK code changed)

**No SDK code changed. No REST route, request shape or response shape changed.**
What changed is on the server, and it can turn code that worked yesterday into an
HTTP 400 today. Only `Memory.query()` / `QueryBuilder.execute()`
(`POST /api/v1/query`, the AST query surface) is affected. Every other method is
untouched.

### Filter shapes that used to be silently dropped and now return 400

The old server **discarded** these and ran the query without the predicate. That
returned HTTP 200 with a **wider result set than you asked for** — wrong data
wearing a success status, which is far worse than an error. Each one is now a named
400 that says which filter and which operator is at fault:

| Payload | New 400 message |
|---|---|
| `{"type": "fact"}` (bare value, not an operator object) | `filter "type" must be an object of operators (e.g. {"$eq": value}), got a bare value` |
| `{"type": {}}` (empty operator object) | `filter "type" has no operator: use one of $eq, $gt, $gte, $lt, $lte, $in` |
| `{"type": {"$in": []}}` (empty `$in`) | `filter "type": $in requires a non-empty array of values` |
| `{"type": {"$like": "fac%"}}` (unknown operator — also `$neq`, `$nin`) | `filter "type": unsupported operator "$like" — supported operators are $eq, $gt, $gte, $lt, $lte, $in` |

Two related sharpenings on the same endpoint:

- **Non-scalar operator value** (`{"$eq": {"nested": 1}}`, `{"$eq": [1, 2]}`) was a
  generic HTTP 500; it is now a named 400 telling you which filter is malformed.
- **Undecodable body**, and in particular `filters` sent as an *array* of
  `{field, op, value}` instead of an object keyed by column, still answers 400 and
  still keeps the stable `invalid json ast` message prefix — the message now shows
  the expected shape.

### Where TypeScript is exposed

`QueryBuilder.where()` validates the column and the operator client-side and always
writes at least one operator, so the fluent path cannot emit an empty operator
object. Two gaps remain:

```ts
// 1. An empty $in is NOT guarded client-side.
new QueryBuilder().where("type", "$in", []);   // -> {"type": {"$in": []}}
// BEFORE: HTTP 200 + an UNFILTERED page of the tenant (the predicate vanished).
// NOW:    HTTP 400 — filter "type": $in requires a non-empty array of values

// 2. A hand-built AstQuery bypasses the builder entirely — every shape in the
//    table above can reach the server through it.
await mem.query({ filters: { type: "fact" } } as AstQuery);   // bare value -> 400
```

Note for cross-language teams: the **Go** SDK is materially more exposed. Its
`client.QueryOp` tags every operator field `omitempty`, so `QueryOp{}`,
`QueryOp{In: []interface{}{}}` and `QueryOp{Eq: nil}` all marshal to `{}` and now
400. Audit Go call sites first.

### Truncated pages are now errors, not silent short reads

A row-scan or mid-iteration storage failure used to be logged and skipped, so the
endpoint answered **HTTP 200 with a shorter page** that no client could distinguish
from "there are no more records". It now answers **HTTP 500 with no records at
all**. Treat a 500 from `query()` as retryable, and never cache a `query()` result
you did not receive with a 200.

### How to tell whether you are affected

1. Find every `memory.query(...)` and `QueryBuilder` call site.
2. Look for a `$in` fed from an array that can be empty, and for any hand-built
   `AstQuery` object carrying a bare value, an empty operator object, or a
   `$neq`/`$nin`/`$like` operator (a cast such as `as AstQuery` or `any` is how
   these get past the compiler).
3. Any of those throws `AnhurQueryError` with the server message inline:

```ts
import { AnhurQueryError } from "anhurdb";

try {
  const { records } = await mem.query(ast);
} catch (queryError) {
  if (queryError instanceof AnhurQueryError) {
    // 'Invalid request (HTTP 400): {"error":"filter \"type\" has no operator: ..."}'
  }
}
```

**What to do:** if the 400 fires, the query was never doing what the code claimed —
decide what the predicate should have been and write it explicitly. Omit the key
entirely when you genuinely want no predicate; do not send an empty operator object
to mean "match everything".

### Deliberately unchanged — do not expect these to fail either

These remain accept-and-ignore, and this release does **not** turn them into errors:

- `select()` is parsed but never projected; the full record always comes back.
- An unrecognised sort `order` silently falls back to `DESC` server-side.
- Server-side, a `limit` of `0` or less falls back to the default of 50, a `limit`
  above 1000 is capped at 1000, and a negative `offset` falls back to 0 — none of it
  echoed in the response. (`QueryBuilder` rejects those values client-side first.)
- Zero hits still come back on the wire as `"records": null`, which `query()`
  coalesces to an empty array (and `count` falls back to `records.length`).
- Archived records stay hidden unless you filter on `archived` explicitly.
- A `semantic_search` block inside `filters` is still accepted and skipped
  server-side. It has never contributed to the result and still does not.

## 2.0.1 — 2026-07-15

_Was headed "Unreleased" until 2.1.0; it never was. Everything below shipped
in the 2.0.x tarball series (the last of them being 2.0.17)._

_Generated at 2026-07-15T01:21:22Z from `770a36dca15f3c1129b7e2a7618f27acb61eb386` → `HEAD`_

- fix(sdk): searchEntities sends q=; docs use organization not org (be79a07)
- docs: document Query Builder in Python, Go, and TypeScript SDKs (1ae72c8)
- feat(sdk): add session_id to ingest across ALL THREE SDKs + plugin (parity) (e603b94)
- refactor(sdk): make all 3 SDKs transparent HTTP transports (Go/Py/TS parity) (af90706)
- feat(sdk): AppendRelatedIDs across all 3 SDKs, mirroring AppendMainIDs (parity #13) (97c19c4)
- fix(sdk): search_by_type reads the correct 'records' envelope key (all 3 SDKs) (eb3324f)
- fix(sdk,ts): await tagReady before createInSession metadata (container_tag mis-routing) (db83bd6)
- fix(sdk): unify SearchResult to nested {record, similarity} across Go/Python/TS (8cde735)
- fix(sdk): align Go/Python/TS parity — recent route, session_uuid, typed search (ea4be89)
- feat(sdk): WalkSemantic goal-directed target across Go/Py/TS (parity) (9d393c2)
- feat(plugins): dogfood AnhurDB as Claude Code LTM + SDK hardening/parity (c28f109)
- fix(sdk-ts): readContent devolve conteúdo cru (paridade com Go/Python) (9e8bb9b)
- fix(ts-sdk): retry idempotent writes on transient cluster errors (Bug 3) (ef22656)
- fix(ts-sdk): stop dropping score/type/metadata on add() (Bug 2, parity) (26f81fc)
- fix(ts-sdk): emit real ESM and repair tsc toolchain (Bug 1) (2b4c8c8)
- feat(sdk): Go/Python/TS parity — new methods + metadata corruption fix (db580ec)
- feat: SDK fixes — Python AnhurClient, Go randomHex/timeout, TS CI, PyPI publish (907de0b)

## 2.0.0

- Initial v2 release: unified `Memory` API parity across Python, TypeScript, and Go.
- Open Beta default endpoint: `https://anhurdb.yoven.ai`.
- Full MCP-aligned surface: search, query AST, manifests, entities, uploads, temporal versioning.

