# Python SDK Changelog

## 2.1.0 — ADR-0031 search controls, one version number, PEP 561 (2026-09-05)

All three SDKs (Go, TypeScript, Python) converge on **2.1.0** in this change, per
`feedback_sdk_sync_invariant` (three SDKs in parity, same PR).

### The version is now ONE number

Before this release the package made four inconsistent claims: `pyproject.toml`
said `2.0.0`, the `User-Agent` said `2.1`, the README pinned a `2.0.12` wheel and
the newest changelog heading said `2.0.2`. `2.1.0` is above every shipped tag
(py 2.0.20 / go 2.0.18 / ts 2.0.17) and makes the number the wire already claimed
true.

- New `anhurdb/version.py` is the single source of truth; `USER_AGENT` is derived
  from it, never retyped. The header is now `AnhurSDK-Python/2.1.0`.
- `anhurdb.__version__` is exported (PEP 396) — previously the installed version
  could not be introspected at all.
- `tests/test_version.py` locks `pyproject.toml` to `anhurdb/version.py` and
  asserts the header on the SERVER side of a mock request. The release workflow
  rewrites the manifest with `sed`, so without that lock a release could ship a
  wheel whose metadata and whose `User-Agent` disagreed, silently.
- The published install pin in the README is deliberately still `2.0.12`: the
  2.1.0 wheel does not exist yet, and a doc pinning an unpublished version is
  worse than a stale one.

### ADR-0031 search controls (parity with the server shipped 2026-09-05)

`search()` and `search_with_retrieval()` gained three opt-in, keyword-only knobs.
Each is omitted from the request when unset, so an existing caller sends the exact
payload it sent before.

- `mode`: `"fast"` | `"balanced"` | `"semantic"`. Validated client-side — an
  unknown value raises `AnhurError: INVALID_PARAM: 'mode' must be one of: fast,
  balanced, semantic` **before** the round trip. The server normalises unknown
  modes to `balanced` on purpose (so gRPC and REST can never disagree about a
  typo), which means the server can never report the typo back.
- `semantic_timeout_ms`: caps the Embed+HNSW wait. `None`/`0` = server default
  (700 ms). Negative is refused.
- `debug_signals`: attaches the per-hit signals block and `leg_scores`.

**Cross-version guard (the reason this is not just three new fields).** An
additive proto3/JSON field is compatible on the wire, not in the semantics: a
server that predates ADR-0031 ignores `mode`, runs balanced, and answers HTTP 200
with lexical results while the caller believes it asked for strict semantic
retrieval. The SDK now detects that from the RESPONSE — a current server always
fills `retrieval.mode` — and **raises** `AnhurError: SERVER_TOO_OLD: ...` for
`mode="semantic"`. `semantic_timeout_ms` and `debug_signals` degrade without
misrepresenting the result set, so those emit a `RuntimeWarning` instead. No new
environment variable and no new configuration knob: the detection is derived from
data the server already sends.

Known blind spot, stated rather than hidden: with `scope="shared_all"` a CURRENT
server also returns an empty `retrieval.mode` (a two-plane fan-out has no single
honest mode to report), so the check cannot run there and warns instead of
raising. Raising would reject a healthy server.

### Richer search response

- `SearchHitSignals` went from 6 to the full **13** fields: `hnsw_rank`,
  `bsq_rank`, `parquet_rank`, `fts5_rank`, `astar_rank`, `entity_jaccard_rank`,
  `active_leg_weight_sum`. Because every model is `extra="ignore"`, these were
  arriving and being dropped SILENTLY before.
- New `LegScoreSummary` model and `SearchResponse.leg_scores`. `None` (key absent)
  and `[]` (key present, no legs) are kept apart.

### Other

- `Memory(..., timeout=30.0)`: the request budget is finally reachable from the
  constructor and forwarded to `HTTPConnection`. It was hardcoded at 30 s with no
  way to change it. A constructor parameter, never an environment variable.
- Exported from the package root what was already part of the return/raise
  contract but unreachable: `AnhurUploadWaitTimeout`, `SearchResponse`,
  `SearchHitSignals`, `RelatedNode`, `RetrievalMeta`, plus the new
  `LegScoreSummary` and the `SEARCH_MODE_*` constants. A type you can receive but
  cannot name is not a public API.
- `py.typed` marker added and declared in `pyproject.toml`. Without PEP 561 every
  annotation across the package was invisible to the consumer's type checker.
- `pyproject.toml` fix: `readme` and `packages` were sitting under
  `[tool.pytest.ini_options]`, so Poetry never saw them. TOML tables are
  positional; both are back under `[tool.poetry]`.

### House law: files split by domain

`anhurdb/client/__init__.py` was 2483 lines (8x the ~300-line cut) and
`anhurdb/models/record.py` was 322. Both were split BEFORE anything was added:

- `anhurdb/client/search.py` — the `POST /api/v1/search` port (`HybridSearchMixin`).
- `anhurdb/client/search_scopes.py` — plane shortcuts and `/search/type`,
  `/search/smart` (`SearchScopeMixin`).
- `anhurdb/client/search_parse.py` — response envelope → typed objects.
- `anhurdb/client/search_mode.py` — the mode vocabulary and the cross-version check.
- `anhurdb/models/search.py` — every search wire model.

`Memory` keeps its complete surface (the mixins are mixed into it) and
`from anhurdb.client import _parse_search_results` still resolves, so the split is
invisible to every existing importer.

### Docs corrected against the code

- `batch_read_content` was documented as "up to 100 records". No SDK enforces
  that, and the number is wrong: the SERVER caps a batch at **1000**
  (`server/handler/record_batch.go`, `maxBatchSize = 1000`) and answers HTTP 400
  `batch size exceeds maximum`.
- The `mode="mcp"` warning blamed the retired `execute_ast` tool. False: the SDK
  maps `/api/v1/query` to the live `query` tool. The real blocker is that
  `/api/v1/mcp/direct` is served only on the MCP server's metrics listener
  (default port 9092), not on the data plane the SDK's `url` points at.
- `smart_search` also takes `memory_type` (sent as `?type=`) and returns the raw
  response dict, not `list[SearchResult]`.

## Server-side behaviour change on `POST /api/v1/query` (2026-07-29, no SDK code changed)

**No SDK code changed. No REST route, request shape or response shape changed.**
What changed is on the server, and it can turn code that worked yesterday into an
HTTP 400 today. Only `Memory.query` / `QueryBuilder.execute()`
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
- **Undecodable body**, and in particular `filters` sent as a *list* of
  `{field, op, value}` instead of a dict keyed by column, still answers 400 and
  still keeps the stable `invalid json ast` message prefix — the message now shows
  the expected shape.

### Where Python is exposed

`QueryBuilder.where()` validates the column and the operator suffix client-side and
always writes at least one operator, so the fluent path cannot emit an empty
operator object. Two gaps remain:

```python
from anhurdb.query import QueryBuilder, Filter

# 1. An empty $in is NOT guarded client-side.
QueryBuilder().where(type__in=[])            # -> {"type": {"$in": []}}
# BEFORE: HTTP 200 + an UNFILTERED page of the tenant (the predicate vanished).
# NOW:    HTTP 400 — filter "type": $in requires a non-empty array of values

# 2. Filter() copies raw dicts with NO validation — every shape in the table
#    above can reach the server through it.
Filter({"type": "fact"})                     # bare value  -> 400
Filter({"type": {}})                         # empty object -> 400
Filter({"weight": {"$neq": 3}})              # unknown op   -> 400
```

Note for cross-language teams: the **Go** SDK is materially more exposed. Its
`client.QueryOp` tags every operator field `omitempty`, so `QueryOp{}`,
`QueryOp{In: []interface{}{}}` and `QueryOp{Eq: nil}` all marshal to `{}` and now
400. Audit Go call sites first.

### Truncated pages are now errors, not silent short reads

A row-scan or mid-iteration storage failure used to be logged and skipped, so the
endpoint answered **HTTP 200 with a shorter page** that no client could distinguish
from "there are no more records". It now answers **HTTP 500 with no records at
all**. Treat a 500 from `query` as retryable, and never cache a `query` result you
did not receive with a 200.

### How to tell whether you are affected

1. Find every `memory.query(...)`, `QueryBuilder` and `Filter` call site.
2. Look for a `$in` fed from a list that can be empty, and for any `Filter(...)` or
   hand-built AST dict carrying a bare value, an empty operator dict, or a
   `$neq`/`$nin`/`$like` operator.
3. Any of those raises `AnhurQueryError` with the server message inline:

```python
from anhurdb import AnhurQueryError

try:
    records = await memory.query(ast)
except AnhurQueryError as query_error:
    # "Invalid request (HTTP 400): {"error": "filter \"type\" has no operator: ..."}"
    ...
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
- Zero hits still come back on the wire as `"records": null`, which the SDK
  coalesces to an empty list.
- Archived records stay hidden unless you filter on `archived` explicitly.
- `QueryBuilder.semantic_search()` writes a `semantic_search` block into `filters`
  that the server still accepts and skips. It has never contributed to the result
  and still does not — it is **not** one of the new 400s.

## 2.0.2

_Generated at 2026-07-15T01:21:22Z from `v2/python/v2.0.1` → `HEAD`_

- fix(sdk): searchEntities sends q=; docs use organization not org (be79a07)
- docs: document Query Builder in Python, Go, and TypeScript SDKs (1ae72c8)
- feat(sdk): add session_id to ingest across ALL THREE SDKs + plugin (parity) (e603b94)
- refactor(sdk): make all 3 SDKs transparent HTTP transports (Go/Py/TS parity) (af90706)
- feat(sdk): AppendRelatedIDs across all 3 SDKs, mirroring AppendMainIDs (parity #13) (97c19c4)
- fix(sdk): search_by_type reads the correct 'records' envelope key (all 3 SDKs) (eb3324f)
- fix(sdk,py): read-model enum tolerance + null-records coalesce (crash fixes) (bf5fb87)
- fix(sdk): recent() returns the FULL typed record across Go/Python/TS (60a676c)
- fix(sdk): unify SearchResult to nested {record, similarity} across Go/Python/TS (8cde735)
- fix(sdk): align Go/Python/TS parity — recent route, session_uuid, typed search (ea4be89)
- feat(sdk): WalkSemantic goal-directed target across Go/Py/TS (parity) (9d393c2)
- fix(plugin,test): log flush errors + de-hardcode API key from env (4ac41a7)
- feat(plugins): dogfood AnhurDB as Claude Code LTM + SDK hardening/parity (c28f109)
- test(python): cover score/type/metadata persistence, retry, plain-text content (3711d24)
- fix(python): retry transient cluster 500s and stop wrapping plain-text content (c54685c)
- fix(python): add() must persist score/type/metadata, not drop them on ingest (4bf0aa5)
- feat(sdk): Go/Python/TS parity — new methods + metadata corruption fix (db580ec)
- feat: SDK fixes — Python AnhurClient, Go randomHex/timeout, TS CI, PyPI publish (907de0b)
- feat: so many fixes (0e74900)
- feat: mcp integration (ba6a991)

## 2.0.1

- Initial v2 release: unified `Memory` API parity across Python, TypeScript, and Go.
- Open Beta default endpoint: `https://anhurdb.yoven.ai`.
- Full MCP-aligned surface: search, query AST, manifests, entities, uploads, temporal versioning.

