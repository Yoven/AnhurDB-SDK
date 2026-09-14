# Go SDK Changelog

## 2.1.0 — ADR-0031 search controls, one version constant, three parity fixes (2026-09-05)

First release where the Go, TypeScript and Python SDKs carry the SAME version.
All three previously claimed `2.1` in their User-Agent while their manifests said
`2.0.0` and their published tags were `2.0.17` / `2.0.18` / `2.0.20` — four
mutually inconsistent truths. `2.1.0` sits above every shipped tag and makes the
wire-visible claim true.

> **Install pins are NOT bumped in this release.** `README.md` still points at a
> published tag, because `2.1.0` is not published yet and a doc that pins an
> unpublished version is worse than a stale one.

### Added — the three ADR-0031 search controls

| Option | Wire field | Meaning |
|---|---|---|
| `WithSearchMode(mode)` | `mode` | `fast` / `balanced` / `semantic`; unset = server default (balanced) |
| `WithSemanticTimeoutMs(ms)` | `semantic_timeout_ms` | caps the Embed+HNSW wait; `0` = server default (700ms) |
| `WithDebugSignals()` | `debug_signals` | attaches per-hit signals and per-leg score distributions |

`WithSearchMode` is a distinct name from the pre-existing `WithMode`, which picks
the WRITE path for `Add` (`ingest` / `regular`). Two unrelated concepts.

An unrecognised mode is refused **client-side** with
`INVALID_PARAM: 'mode' "x" is not supported; use "fast", "balanced" or "semantic"`.
The server would silently normalise it to `balanced` and answer 200 — right for a
server (two ports, one behaviour), wrong for an SDK (a caller's typo must be
audible).

### Added — the richer response

- `SearchHitSignals` now carries all **13** server fields. The seven new ones —
  `hnsw_rank`, `bsq_rank`, `parquet_rank`, `fts5_rank`, `astar_rank`,
  `entity_jaccard_rank`, `active_leg_weight_sum` — are the un-folded view of the
  two already-fused ranks, plus the RRF denominator.
- `LegScoreSummary` and `SearchOutcome` are new. `leg_scores` is read from the
  **top level** of the response, where the server puts it on both ports — it is
  deliberately NOT a field of `RetrievalMeta`.
- `Memory.SearchWithSignals(ctx, query, sessions, opts...) (*SearchOutcome, error)`
  returns results + retrieval + leg scores in one struct. `Search` and
  `SearchWithRetrieval` keep their signatures and are now projections of it.

### Added — cross-VERSION safety (ADR-0031 amendment, 2026-09-05)

An additive proto field is compatible for the **parser**, not for the **promise**.
A server predating ADR-0031 drops `mode` into `unknownFields` and answers 200 with
balanced, possibly purely lexical, results — while the caller believes it asked
for strict semantics and that a 503 would have come otherwise.

The SDK now checks the **response**, which is the honest detector: a current
server always resolves `retrieval.mode`.

- `mode=semantic` + a server that did not echo `semantic` ⇒ the call **fails**
  with `SERVER_TOO_OLD: ...`, naming the server as the cause.
- `semantic_timeout_ms` / `debug_signals` / `mode=fast` ignored ⇒ a warning on the
  standard logger. They degrade without misrepresenting which records matched.
- **Exception, measured in the server:** for `scope=shared_all` the REST handler
  builds its `RetrievalMeta` by hand and leaves `mode` empty on purpose (two legs,
  no single honest mode). A current server is therefore indistinguishable from an
  old one there, so that one case warns instead of failing — a blanket fail-loud
  would have rejected every `shared_all` query against a healthy server.

No new environment variable and no new configuration knob: the detection is
derived from the response the server already sends.

### Added — `client.Version`

The SDK had no version symbol at all; the only runtime-observable version was a
string literal inside `setAuthHeaders`. `client.Version` (`2.1.0`) is now the
single source, and `client.UserAgent` is derived from it. A release bumps one line.

### Fixed

- **`Walk` / `WalkSemantic` sent `depth: 0`** when the caller passed a
  non-positive depth, while TypeScript sent `depth ?? 3` and Python defaulted to
  `3`. The same call in three languages produced three different requests, and the
  Go one came back looking like an empty graph. Now falls back to `3`.
- **Session-filter rejections are typed.** `normalizeSessionFilter` returned bare
  `fmt.Errorf` values, so callers had to match on text to tell "you sent something
  invalid" from "the network died". They are now `*APIError` with
  `StatusCode: 400`, `Kind() == KindInvalidRequest`, `Retryable() == false`. The
  message strings are **byte-identical** — they are pinned against the Python and
  TypeScript SDKs, so `APIError.Error()` renders a client-side rejection verbatim
  instead of wrapping it in `AnhurDB API error (HTTP 400): ...`.
- **Query builder parity.** `SelectFields`, `WhereEquals`, `Build` and `Execute`
  were missing next to the TypeScript and Python builders; `types.go` carried a
  doc comment naming the gap. (`SelectFields`, not `Select`: Go forbids a method
  and a field sharing a name, and renaming the exported `Select` field would break
  every literal.)
- **`go.mod` and `.tool-versions` no longer look contradictory.** `go 1.24` is the
  language floor imposed on consumers; `toolchain go1.26.0` is what the
  maintainers build with, matching `.tool-versions`. Both are now written down.

### Fixed — AST parity round, 2026-09-14 (2.1.0 held for these)

Two divergences found by the exhaustive live AST matrix (398 wire exchanges,
102/102 column x operator pairs asserted against the production router). Both
failed **silently**, which is why the release was held.

**BREAKING (compile-time) — `Memory.Query` no longer takes `...ReadOption`.**

```go
// BEFORE: compiled, and the option was thrown away at runtime
records, _ := mem.Query(ctx, req, client.WithAsOf("2026-03-15T12:00:00Z"))
// NOW:    compile error — the endpoint has no as-of surface to honour
records, _ := mem.Query(ctx, req)
```

The old signature ended in `opts ...ReadOption` and the body contained `_ = opts`.
`WithAsOf`, `WithSince`, `WithUntil` and `WithKeyword` compiled, were accepted,
and were discarded before the request was built — a caller who scoped a query to
a point in time silently received the **unscoped** query.

Why deletion and not honouring them (probed live, read-only, production router,
2026-09-14):

- `?as_of=`, `?since=`, `?until=`, `?q=` on the POST URL: identical count with
  and without, including a `since` in 2030 that would have emptied the page.
  `server/handler/record_query.go` reads the body and nothing else.
- `as_of` as a top-level body key: silently ignored (the grammar keeps four keys
  — `select`, `filters`, `sort`, `pagination` — and drops the rest).
- As-of is **not expressible as filters**. The server's real predicate
  (`server/database/list_record.go`, `GetRecordAsOf`) is `created_at <= T AND
  (valid_from IS NULL OR valid_from <= T) AND (valid_until IS NULL OR
  valid_until > T)` plus a supersede-chain unwind. The AST grammar has no `OR`,
  no `IS NULL`, no subquery. Measured on a tenant whose unfiltered page returns
  1000 rows: `valid_until $gt "2020-01-01T00:00:00Z"` → **0 rows**;
  `superseded_by $eq null` → **0 rows**, although every row the server returns
  already satisfies `superseded_by IS NULL`. A "best effort" translation would
  not under-scope the answer, it would annihilate it with HTTP 200.
- `since`/`until` alone *would* translate (`created_at $gte` / `$lte`, confirmed
  working live) and are still deliberately not wired: that is already a
  first-class filter, and a hidden second writer of the same column would
  silently `AND` against the caller's explicit `created_at` and return an empty
  page for a query that looks right.

Migration: drop the options, or express the window as what it is —
`NewQuery().Where("created_at", QueryOp{Gte: since, Lte: until})`. For a real
point-in-time read use `ManifestGlobal` / `ManifestSession`, which carry
`as_of` / `since` / `until` as query parameters and enforce the
as_of-XOR-since/until rule server-side. No caller in this repository passed an
option to `Query`, so nothing in-tree breaks.

**The empty-operator error now names the cause.** It used to read
`filter "type" has no operator: set one of Eq, Gt, ...` — and the commonest way
to reach it is `QueryOp{Eq: nil}`, a caller who *did* set `Eq` and is then told
to set `Eq`. The operator was not missing, it was erased by `omitempty`. New
text:

```text
query: filter "type": no operator survived encoding — every QueryOp field is
`omitempty`, so QueryOp{Eq: nil} marshals to {} exactly like QueryOp{}; set a
NON-NIL Eq, Gt, Gte, Lt, Lte or In. A literal $eq:null is unreachable from Go on
purpose: the server compiles it to `col = NULL`, which is never true, so it would
return 200 with zero rows for every input
```

Tests: `client/query_execute_test.go`. Each assertion was proven to bite by
reverting the fix (re-adding the variadic, restoring the old message, dropping
`omitempty` from `QueryOp.Eq`) and confirming the matching test fails.

### House-law splits (~300 lines, by DOMAIN)

`client.go` (1679 lines) and `types.go` (1042) were far past the cut and could not
be grown, so the touched domains moved out first:

- `client/search.go` — the search endpoints
- `client/search_types.go` — the search response types
- `client/search_options.go` — `ReadOption`/`SearchOption` and every `With*`
- `client/search_mode.go` — the mode enum, its validation, the cross-version guard
- `client/graph_walk.go` — `Walk` / `WalkSemantic`
- `client/query_builder.go` — the fluent AST builder
- `client/version.go` — `Version` / `UserAgent`
- `client/query_execute.go` — `Memory.Query`, the AST execute path (split out of `parity.go` on 2026-09-14, which was 532 lines and could not grow)

`client.go` is down to 1359 lines and `types.go` to 696; both remain scheduled
refactors, and neither grew in this change. `parity.go` went 532 → 474 on
2026-09-14 when the AST execute path moved out.

## Server-side behaviour change on `POST /api/v1/query` (2026-07-29, no SDK code changed)

**No SDK code changed. No REST route, request shape or response shape changed.**
What changed is on the server, and it can turn code that worked yesterday into an
HTTP 400 today. Only `Memory.Query` (`POST /api/v1/query`, the AST query surface)
is affected. Every other method is untouched.

### ⚠️ BREAKING FOR GO CALLERS — `client.QueryOp` can marshal to `{}`

`QueryOp` tags all six operator fields `omitempty`
(`v2/golang/client/types.go`), so an operator object with nothing set — or with
only a `nil`/empty value set — serialises to an **empty object**, which the server
now rejects:

```go
// All three marshal to {"filters":{"type":{}}} on the wire:
client.QueryOp{}                          // zero value
client.QueryOp{In: []interface{}{}}       // empty $in
client.QueryOp{Eq: nil}                   // explicit nil $eq

records, err := mem.Query(ctx, client.NewQuery().Where("type", client.QueryOp{}))
// BEFORE: HTTP 200 + an UNFILTERED page of the tenant (the predicate vanished).
// NOW:    HTTP 400 — filter "type" has no operator: use one of
//                    $eq, $gt, $gte, $lt, $lte, $in
```

Consequences to be aware of:

> **Correction (2026-07-29, same day, `v2/golang/v2.0.13`):** the first and
> third bullets below described the Go SDK as it stood when this entry was
> written (01:52 UTC+1) — about 9.5 hours before `client/query_validation.go`
> shipped at 11:31 UTC+1 (commit `68ebcc8`, tag `v2/golang/v2.0.13`), same
> calendar day. That commit added
> `QueryRequest.Validate()`, called by `Query()` before any request leaves the
> process, bringing Go to parity with Python's and TypeScript's client-side
> validation. The bullets are corrected in place below (marked **"as of
> v2.0.13"**) rather than deleted, since they remain an accurate record of the
> narrow window between the server change and the client-side fix.

- `QueryOp{In: []interface{}{}}` used to report the generic *"has no operator"*
  message, not the `$in`-specific one, because the empty slice was dropped
  before it reached the server. **As of v2.0.13:** `Validate()` checks for an
  empty `$in` list before the generic empty-operator check, so the Go SDK now
  returns the dedicated local error `query: filter "<field>": $in requires a
  non-empty list of values` — no round trip, no generic message. Guarding
  empty slices in caller code is no longer required to get the right message.
- `$eq: null` is currently **not expressible from Go**. The server accepts a null
  scalar, but `omitempty` erases it. Use Python/TypeScript or a hand-built JSON body
  if you need it. **Still true as of v2.0.13** — `Validate()` changes what gets
  rejected before sending, not `QueryOp`'s wire encoding, so this gap is
  unaffected by that fix.
  - **Amendment 2026-09-14 — this was mis-classified as a gap; Go is the SDK
    that is right.** The mechanical fact above is unchanged: `omitempty` erases
    a nil interface, so `QueryOp{Eq: nil}` is byte-identical to `QueryOp{}` and
    `$eq: null` cannot leave this SDK. What was wrong is calling that a
    deficiency. The server compiles `$eq: null` to `col = ?` bound to NULL, and
    `col = NULL` is never true in SQL. Measured live against the production
    router on 2026-09-14 (read-only): `{"superseded_by":{"$eq":null}}` answered
    **HTTP 200, count=0** on a tenant whose unfiltered page returns 1000 rows —
    every one of which satisfies `superseded_by IS NULL`. Python and TypeScript
    can put that predicate on the wire; what they have is not a capability, it
    is a way to write a query that can never match and never complains. Nothing
    to close here, and the earlier advice to "use Python/TypeScript or a
    hand-built JSON body if you need it" is **withdrawn** — needing it is the
    bug. Pinned by `TestQueryOpNilFieldsVanishOnTheWire`.
- Unlike the Python and TypeScript builders, `QueryRequest.Where` used to apply
  **no client-side column whitelist** and `Limit`/`Offset` used to apply **no
  range check** — a bad column name only failed at the server (400 `invalid
  filter field`), and an out-of-range limit or offset was silently adjusted
  server-side (see the last section) with nothing echoed back in the response.
  **As of v2.0.13:** `Validate()` checks `Filters` and `Sort` field names
  against the same column whitelist the server uses, and rejects a `limit`
  outside `[1, 1000]` or a negative `offset` with a named local error — both
  now fail before the request is built, matching Python/TypeScript. The
  server-side fallback behaviour described above is unchanged and still
  applies to any request that reaches it by another path (hand-built JSON, a
  `QueryRequest` built without going through `Validate()`, another SDK).

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

- **Non-scalar operator value** (`{"$eq": {"nested": 1}}`, `{"$eq": [1,2]}`) was a
  generic HTTP 500; it is now a named 400 telling you which filter is malformed.
- **Undecodable body**, and in particular `filters` sent as an *array* of
  `{field, op, value}` instead of an object keyed by column, still answers 400 and
  still keeps the stable `invalid json ast` message prefix — the message now shows
  the expected shape.

### Truncated pages are now errors, not silent short reads

A row-scan or mid-iteration storage failure used to be logged and skipped, so the
endpoint answered **HTTP 200 with a shorter page** that no client could distinguish
from "there are no more records". It now answers **HTTP 500 with no records at
all**. Treat a 500 from `Query` as retryable, and never cache a `Query` result you
did not receive with a 200.

### How to tell whether you are affected

1. Find every `Memory.Query` / `NewQuery()` call site.
2. Look for an operator object that can end up empty — `QueryOp{}` built
   conditionally, a `$in` slice that can be empty, an `Eq` fed from a `nil`
   interface — and for any hand-built `QueryRequest`/JSON with a bare value or a
   `$neq`/`$nin`/`$like` operator.
3. Any of those returns `*client.APIError` with `StatusCode == 400` and a `Body` of
   `{"error": "<message above>"}` — **except** an empty `$in`, a disallowed
   filter/sort column, or an out-of-range `limit`/`offset`, which **as of
   v2.0.13** are caught by `QueryRequest.Validate()` before any request is
   sent: those return a plain `error` (not `*client.APIError`), so the
   `errors.As` check below falls through to the generic `err != nil` branch
   instead — check for that first if you need to distinguish "never left the
   process" from "the server said no":

```go
records, queryError := mem.Query(ctx, request)
var apiError *client.APIError
if errors.As(queryError, &apiError) && apiError.StatusCode == 400 {
    // Malformed filter. The body names the offending field and operator.
}
```

**What to do:** if the 400 fires, the query was never doing what the code claimed —
decide what the predicate should have been and write it explicitly. Skip the filter
entirely (omit the key) when you genuinely want no predicate; do not send an empty
operator object to mean "match everything".

### Deliberately unchanged — do not expect these to fail either

These remain accept-and-ignore, and this release does **not** turn them into errors:

- `Select` is parsed but never projected; the full record always comes back.
- An unrecognised sort `order` silently falls back to `DESC`.
- A `limit` of `0` or less falls back to the default of 50, a `limit` above 1000 is
  capped at 1000, and a negative `offset` falls back to 0 — none of it echoed in the
  response, so the adjustment is undetectable from the client.
- Zero hits still come back on the wire as `"records": null` (`Query` normalises
  that to an empty slice).
- Archived records stay hidden unless you filter on `archived` explicitly.
- A `semantic_search` block inside `filters` is still accepted and skipped
  server-side. It has never contributed to the result and still does not.

## 2.0.2

_Generated at 2026-07-15T01:21:22Z from `v2/golang/v2.0.1` → `HEAD`_
_(heading corrected 2026-09-05: this shipped long ago — 2.0.13+ is tagged — and
"Unreleased" was reading as a live section eleven patch releases later.)_

- fix(sdk): searchEntities sends q=; docs use organization not org (be79a07)
- docs: document Query Builder in Python, Go, and TypeScript SDKs (1ae72c8)
- feat(sdk): add session_id to ingest across ALL THREE SDKs + plugin (parity) (e603b94)
- refactor(sdk): make all 3 SDKs transparent HTTP transports (Go/Py/TS parity) (af90706)
- feat(sdk): AppendRelatedIDs across all 3 SDKs, mirroring AppendMainIDs (parity #13) (97c19c4)
- fix(sdk): search_by_type reads the correct 'records' envelope key (all 3 SDKs) (eb3324f)
- fix(sdk,go): add weight+score to v2/golang models.Record (parity) (4eb343e)
- fix(sdk): recent() returns the FULL typed record across Go/Python/TS (60a676c)
- fix(sdk): unify SearchResult to nested {record, similarity} across Go/Python/TS (8cde735)
- fix(sdk): align Go/Python/TS parity — recent route, session_uuid, typed search (ea4be89)
- feat(sdk): WalkSemantic goal-directed target across Go/Py/TS (parity) (9d393c2)
- feat(plugins): dogfood AnhurDB as Claude Code LTM + SDK hardening/parity (c28f109)
- fix(sdk-go): ListSessions não falha mais em tenant vazia (empty-sessions crash) (1a4a47b)
- test(sdk-go): live e2e proving Add score/type persistence + robust readback (c6e39ef)
- fix(sdk-go): idempotent retry for transient cluster errors on writes (9644349)
- fix(sdk-go): Memory.Add functional options (WithScore/WithType/WithMetadata) (28bf732)
- fix(go): ReadContent must not unwrap a JSON {"content":...} envelope (a82d860)
- feat(sdk): Go/Python/TS parity — new methods + metadata corruption fix (db580ec)
- feat: SDK fixes — Python AnhurClient, Go randomHex/timeout, TS CI, PyPI publish (907de0b)
- feat: so many fixes (0e74900)
- feat: mcp integration (ba6a991)

## 2.0.1

- Initial v2 release: unified `Memory` API parity across Python, TypeScript, and Go.
- Open Beta default endpoint: `https://anhurdb.yoven.ai`.
- Full MCP-aligned surface: search, query AST, manifests, entities, uploads, temporal versioning.

