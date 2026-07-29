# Go SDK Changelog

## Unreleased — server behaviour change on `POST /api/v1/query` (2026-07-29)

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

- `QueryOp{In: []interface{}{}}` reports the generic *"has no operator"* message,
  not the `$in`-specific one, because the empty slice is dropped before it reaches
  the server. Guard empty slices in your own code before calling `Where`.
- `$eq: null` is currently **not expressible from Go**. The server accepts a null
  scalar, but `omitempty` erases it. Use Python/TypeScript or a hand-built JSON body
  if you need it.
- Unlike the Python and TypeScript builders, `QueryRequest.Where` applies **no
  client-side column whitelist** and `Limit`/`Offset` apply **no range check**. A bad
  column name only fails at the server (400 `invalid filter field`), and an
  out-of-range limit or offset is silently adjusted server-side (see the last
  section) with nothing echoed back in the response.

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
   `{"error": "<message above>"}`:

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

## Unreleased (2.0.2)

_Generated at 2026-07-15T01:21:22Z from `v2/golang/v2.0.1` → `HEAD`_

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

