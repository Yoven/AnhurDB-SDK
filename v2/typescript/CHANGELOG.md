# TypeScript SDK Changelog

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

## Unreleased — server behaviour change on `POST /api/v1/query` (2026-07-29)

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

