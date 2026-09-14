# TypeScript SDK Changelog

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

