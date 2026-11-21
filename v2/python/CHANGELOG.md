# Python SDK Changelog

## Unreleased

### Changed — 401/403 keep the server's reason (identical in Go, Python and TypeScript)

- Every 401/403 now raises `AnhurAuthRefusedError` (subclass of
  `AnhurAuthError`, so existing `except AnhurAuthError` still catches it) with
  `status_code`, `code` (`body.error` verbatim, parsed whatever the
  Content-Type — the auth middleware answers `text/plain` with a JSON body),
  `detail`, `error_type`, and the properties `is_l1_only`,
  `is_child_suspended`, `is_insufficient_scope` (prefix `insufficient scope`),
  `is_impersonation_required`, `is_invalid_key`; constants `AUTH_CODE_*`.
  Module `anhurdb/client/auth_errors.py`; exported from `anhurdb`.
- Message with a code: `Authentication failed (HTTP <s>): <code>[: <detail>]`.
  Non-JSON body: unchanged (`Authentication failed (HTTP <s>)`).
- Fixed: the multipart (`upload_file`) path raised its auth error WITHOUT
  `status_code`, so `kind` read `transport` and `retryable` True; it now
  carries the status (kind `auth`, not retryable) like the JSON path.
- `AnhurClientAPIError.from_body` uses the same envelope decoder
  (`decode_error_envelope`); behaviour unchanged.
- No gRPC transport exists in this SDK, so there is no gRPC mapping.

### Added — client-key API (ADR-0034), identical in Go, Python and TypeScript

- `ClientAPI(client_key, url=…, timeout=…)` (async context manager) for
  `/api/v1/client/*`: `create_child`, `list_children`, `suspend_child`,
  `resume_child`, `list_tenants`, `list_child_tenants`, `create_tenant`,
  `create_child_tenant` (optional `AgentProfile(config, max_rpm)`),
  `impersonate`, `impersonate_child` (`scope`, `l1_only`, `description`,
  `ttl_seconds` → `ImpersonatedKey`), `revoke_impersonation`. Never sends
  `X-Tenant-ID`. Modules `anhurdb/client/client_key*.py`; shapes taken from the
  AnhurDB handlers `server/handler/client_*.go` (branch
  `feat/phase1-client-children`). Exported from `anhurdb`.
- `AnhurClientAPIError` exposes the server code (`body.error`), `detail`,
  `error_type` and `is_not_leader`; `child_limit`/`tenant_limit` are not
  retryable.
- Fail-closed impersonation echo: `AnhurClientAPIResponseError` with `code`
  `l1_only_not_honoured`, `scope_not_honoured` or `unexpected_response` (carries
  the minted `key_id`). `ImpersonatedKey.__repr__` never prints the secret.

Server side: AnhurDB feat/metadata-merge-patch (ADR-0035). Same change in Go,
Python and TypeScript.

- **`update` metadata semantics documented as RFC 7396 merge patch.** Keys
  present overwrite, a null value deletes the key, keys absent from the patch
  are untouched, nested objects merge, arrays are replaced whole. A non-object
  patch, or an update over stored metadata that is not a JSON object, is
  refused by the server with 400. Callers no longer need read-merge-write.
- **`metadata` may be a dict** besides the JSON string. It is encoded to
  the JSON string the REST handler decodes (`Metadata string`), keeping
  `None` as `null` so a key can be deleted. NaN/Infinity are refused (JSON has neither). An unencodable value
  raises `AnhurQueryError(status_code=400)` (`invalid_request``) before any request, message
  `INVALID_PARAM: update: metadata cannot be encoded as JSON: <cause>`.
  Before this change a dict reached the wire as a nested object and the
  server answered 400. String metadata is sent byte-for-byte as before.
  New `anhurdb/client/record_metadata_patch.py`, tests `tests/test_record_metadata_patch.py`.

## 4.0.0 — legacy removal (2026-11-21)

Breaking. Go, Python and TypeScript remove the same set together (house parity
rule). AnhurDB main is the only server and every fallback below targeted a
server generation that no longer exists; each removal is checked against the
server source (`AnhurDB/server`, paths below).

### Aligned across the three SDKs (identical behaviour, 2026-11-21)

- **Envelope rule on the eight listing routes.** `list_sessions`, `recent`, `manifest_global`, `manifest_session`, `list_chat`, `search_entities`, `list_entities`, `get_record_entities` (and `count_by_type`, which walks the manifest) require the JSON
  object envelope with its list key (`sessions` / `records` / `entities`); the
  value may be a list or `null` (`null` = zero rows). A bare array, an empty
  body, a missing key or a non-list value now raises `AnhurUnexpectedResponseError` (subclass of `AnhurError`, `kind == "server"`, exported from `anhurdb`), new module `anhurdb/client/envelope.py`, message
  `unexpected response from GET <route template>: expected a JSON object with "<key>"`
  — byte-identical in all three. 3.x read several of these shapes as "no rows".
- **`GET /api/v1/profile` 404 fallback removed.** `profile()` no longer returns an
  empty "OSS mode" profile on 404; the route is mounted unconditionally
  (`server/router.go:136`), so a 404 surfaces as `AnhurQueryError(status_code=404)`.
- **Upload mode refusal.** An unknown upload mode (after trim + lower-case) is the typed invalid_request error, raised before any request, with one message in all three: `INVALID_PARAM: upload: mode '<value as given>' is not an upload mode (want chat|tenant_shared|client_shared)`.
- **Delete-by-file empty arguments.** A missing session (outside `tenant_shared`) or ingest-key prefix is the typed invalid_request error (status 400, not retryable, no request sent), message `INVALID_PARAM: <method>: <argument> is required`.
- **404 message.** Every HTTP 404 reads `Resource not found (HTTP 404): <path>` (API path, no base URL, no query), kind `not_found`, in all three.
- **L2 409 messages.** The tenant-library collision and the ambiguous upload status quote at most 500 characters of the server sentence in the message; the full sentence stays on the error object.
- **Parity nits (audit rows 18-21).** Python changes: `delete_file` empty `session_uuid` / `ingest_key_prefix` and `upload_file` unknown `mode` raise `AnhurQueryError(status_code=400)` (`kind == "invalid_request"`) instead of `ValueError` — catch `AnhurQueryError`/`AnhurError`. Messages `INVALID_PARAM: delete_file: session_uuid is required`, `INVALID_PARAM: delete_file: ingest_key_prefix is required` (upload message below). 404 and L2 409 messages were already in the shared form.
- **Session-record-limit message.** already used the shared layout below; a body whose `limit`/`count`/`session_uuid` is missing or mistyped stays the generic 409. Shared layout:
  `Conflict (HTTP 409): <server error, first 500 characters> (session record limit: session=<uuid>, limit=N, count=M)`.

### Removed

- **`mode="mcp"` transport** (`Memory(mode=...)`, `Memory.from_api_key(mode=...)`,
  `Memory.from_client_key(mode=...)`, `HTTPConnection(mode=...)`,
  `HTTPConnection.mode`, module `client/connection_mcp_tunnel.py`). Python-only
  (no Go/TypeScript twin) and self-described "legacy/alternative": it posted to
  `/api/v1/mcp/direct`, which AnhurDB serves only on the MCP metrics listener,
  never on the data-plane port. REST is the only transport. Passing `mode=` is
  now a `TypeError`.

| Symbol | Why | Server evidence |
|---|---|---|
| `AnhurClient` (class) and `_LEGACY_LOCAL_URL` | Deprecated alias of `Memory` with a `http://localhost:8080` default; `Memory` carries the full surface | — (SDK-only alias) |
| `Memory.search_with_ast()` | Forwarding alias of `query()` | `POST /api/v1/query` (`server/router.go`) |
| `Memory.mark_consolidated()` | Alias of `batch_update_status(ids, "consolidated")` | `PATCH /api/v1/records/mark-consolidated` |
| `Memory.link_to_consolidated()`, `Memory.update_consolidate_ids()` | Aliases of `link_consolidated()` | `PATCH /api/v1/records/consolidate-ids` |
| `CreateRequest.uuid` ("legacy alias for session_id") | Two names for one value; now **raises** `ValidationError` if passed | `server/model/record.go:137-140`, `server/handler/record_crud.go:49-52` copy `session_id` → `uuid` |
| `uuid` key in every `/records` payload (`add()` records door, `create_in_session()`) | Replaced by `session_id` | same as above |
| `DeleteFileResult.raft_index` | Never serialised, always 0 | `server/service/record_delete_file.go:42` (`json:"-"`) |
| `MemoryStatus.PENDING` ("pending" record status) | Server never writes record status `pending`; `pending` is an `extraction_status` value (different field) | `server/service/ingest.go:233` |
| `add()` ingest-404 → `/records` "OSS fallback" and its sticky `_ingest_available` state (`_try_ingest`) | `/ingest` is always mounted; a 404 now raises `AnhurQueryError` (404) and nothing is written via `/records` | `server/router.go:135` |
| Session-record-limit text-prefix match + "read N from the sentence" regex (`SESSION_RECORD_LIMIT_TEXT_PREFIX`) | Code-only: `409` + `code == "session_record_limit"` | `server/handler/service_bridge.go:81-82,109-125`; producer `server/service/record_session_cap.go:178` |
| Tenant-library-collision sentence match (`TENANT_LIBRARY_COLLISION_TEXT`) and the upload-400 branch | Code-only: `409` + `code == "tenant_library_collision"` | `server/handler/search_scope_request.go:35-43,109-118`; upload `server/handler/upload.go:133-135` |
| Bare-array fallbacks: `list_sessions()` (`/sessions/stats`), `search_entities()` / `get_record_entities()`, `recent()`, `list_chat()` | Server always sends the envelope; a non-envelope answer now raises `AnhurError` (kind `server`) instead of being read as data | `server/handler/record_session.go:135-142` (stats), `:184` (chats), `:455` (recent); `server/handler/entity.go:254-257,561-565` |
| `EntityModel.entity_type` read alias `type` | Server emits only `entity_type` | `server/handler/entity.go:89`, `:438-441` |
| Fabricated ingest receipt row (`{"id": <id or 0>, "type": "episodic"}` when `records` was absent) | Server always sends `records` (`[]` when empty); a receipt never invents a row | `server/handler/ingest.go:35-38,76-87` |
| `Memory._flatten_search_results` (private, unused) | Dead code found during the split | — |

### Changed

- **`AddResult.mode` values:** `"cloud"` → `"ingest"`, `"oss"` → `"records"`. The
  value names the door that ran; `"oss"` used to come back for every pinned
  `add()` against the cloud.
- **`AnhurSessionRecordLimitError`:** `session_uuid: str`, `limit: int`,
  `count: int` — required, never `None`. A body with the code but a missing or
  mistyped field is the generic 409 (never an invented 0). Message layout
  `Conflict (HTTP 409): <server sentence> (session record limit: session=…, limit=N, count=M)`,
  byte-identical to TypeScript.
- **`list_chat()`** returns `[]` for `{"records": null}` (it returned `None`).
- **Internal split (house 300-line cut):** `anhurdb/client/__init__.py` (1375
  lines) → `__init__.py` (construction/lifecycle) + `write_add.py`,
  `record_create.py`, `record_links.py`, `record_listing.py`, `sessions.py`,
  `impersonation.py`, `container_tag.py`, `write_receipt.py`. Public names and
  signatures unchanged apart from the removals; `from anhurdb import Memory`
  is unchanged.
- **Kept on purpose (current contract, not legacy):** `tenant_shared_legacy`
  scope and the `"legacy"` delete leg; the ADR-0031 `SERVER_TOO_OLD` /
  "server ignored mode" guard; the ambiguous-upload 409 sentence match (server
  sends no code); `records: null` → `[]`; `storage` disk → REST fallback;
  `profile()` 404 → empty profile (also in TypeScript; not in the 4.0.0 set).

### Migration

| Before | After |
|---|---|
| `AnhurClient(url=..., api_key=...)` | `Memory(url=..., api_key=...)` — note the default URL is the cloud endpoint, pass `url="http://localhost:8080"` for a local server |
| `await client.search_with_ast(f, session_uuid=s)` | `await mem.query(f, session_uuid=s)` |
| `await client.mark_consolidated(ids)` | `await mem.batch_update_status(ids, "consolidated")` |
| `await client.link_to_consolidated(ids, star)` / `update_consolidate_ids(ids, star)` | `await mem.link_consolidated(ids, star)` |
| `CreateRequest(uuid=s, ...)` | `CreateRequest(session_id=s, ...)` |
| `result.mode == "cloud"` / `== "oss"` | `result.mode == "ingest"` / `== "records"` |
| `result.raft_index` | (removed; it was always 0) |
| `MemoryStatus.PENDING` | (removed; filter on `extraction_status` if you meant the ingest pipeline) |
| `except AnhurQueryError` around an `/ingest` 404 expecting a silent `/records` write | handle the 404 — fix the URL / proxy |
| `exc.count is None` checks on `AnhurSessionRecordLimitError` | always an `int` |

## Shipped in 4.0.0 — delete-by-file `alternative_sessions`, deprecated `raft_index`, tenant id validation (2026-11-20)

Go, Python and TypeScript ship it together (branch `fix/sdk-audit-gaps`).

- **New:** `DeleteFileResult.purge: bool` — server's always-sent `purge` flag
  (True = gone for good on the shared plane, False = restorable tombstone); it
  was silently ignored on parse.
- **New:** `DeleteFileResult.alternative_sessions: List[FileSessionCandidate]`
  and model `FileSessionCandidate` (exported from `anhurdb`) — the server's
  zero-match diagnostic; it was silently ignored on parse.
- **Deprecated:** `DeleteFileResult.raft_index`. The server never serialises it
  (`json:"-"`), so it was always 0. Kept so existing code works; removed in the
  next major. Same decision in all three SDKs. **Done: removed in 4.0.0.**
- **Behaviour change:** `tenant_id` must follow the server's identifier rule
  (`[A-Za-z0-9_-]`, at most 64; `""` = not set). `HTTPConnection`/`Memory`
  construction now raises `AnhurQueryError` (invalid_request) — previously only
  control characters were refused, with `ValueError`; `except ValueError` around
  a bad `tenant_id` no longer catches it.

## Shipped in 4.0.0 — by-id scope for the L2 tenant library (2026-11-20, ADR-0029)

Additive (keyword-only arguments; existing calls unchanged). Go, Python and
TypeScript ship it together. Server: AnhurDB `fix/l2-lib-scope`.

- **New:** keyword-only `scope=` (`"sessions"`, `"tenant_shared"`,
  `"tenant_shared_legacy"`, `"client_shared"`) on `get`, `read_content`,
  `get_context`, `get_grounding`, `batch_read_content`, `update`, `delete`,
  `supersede`, `upload_status`, `wait_for_upload` (sent as `?scope=`). `None` =
  unchanged request; `shared_all`/unknown values raise `AnhurQueryError`
  (invalid_request) before any request.
- **New helpers:** `tenant_shared_scope_for_hit(uuid)` (`tenant_shared` when the
  hit's uuid ends in `_lib`, else `tenant_shared_legacy`),
  `upload_status_scope(mode, session_id)`, and the `SCOPE_*` / `RECORD_SCOPES`
  constants, all exported from `anhurdb`.
- **Upload plane travels to the status poll:** `UploadResult.scope` (SDK-filled,
  excluded from `model_dump`). New `AnhurUploadStatusAmbiguousError` for the
  server's 409 "file upload in more than one plane".
- **Delete-by-file:** `delete_file(..., scope="tenant_shared"|"client_shared")`
  (session may be empty with tenant_shared), `DeleteFileResult.legs`
  (`DeleteFileLeg`), and `AnhurLibraryDeletePartialError` (`.legs`, HTTP 500
  `partial: true`, retryable — the server delete is idempotent).
- **New typed refusal:** `AnhurTenantLibraryCollisionError`: HTTP 409
  `"code": "tenant_library_collision"` on every door, upload included (server
  4574c4d). Classified by the code first; the sentence is a fallback only for a
  body without a code (older servers, incl. their 400 on upload). **Superseded in
  4.0.0: code-only on 409; the sentence and the 400 branch were removed.**
  Non-retryable; the session-record-limit 409 is checked first and stays
  distinct. An unreadable tenant registry (503, upload too) stays the plain
  retryable `unavailable` error.
- **Fix:** the multipart transport now sets `status_code` on 400/422/404
  (it was `None`, so `kind` read `transport` and `retryable` True).
- `HTTPConnection.post` / `patch` accept `params=`.
- **Moved (house 300-line cut, bodies unchanged apart from `scope`):**
  `get`/`read_content`/`batch_read_content`/`update`/`set_score`/`delete`/
  `supersede` to `client/record_by_id.py`, `delete_file` to
  `client/record_delete_file.py`. Tests: `tests/test_record_scope.py`.

## Shipped in 4.0.0 — typed session-record-limit refusal (2026-11-20, ADR-0033)

Additive; no version number assigned yet (not published). Go, Python and
TypeScript ship it together.

- **New:** `AnhurSessionRecordLimitError` (subclass of `AnhurQueryError`, exported from `anhurdb`). Raised when a write hits a session that already holds its
  maximum number of records — `session_uuid`, `limit`, `count` (`Optional[int]`), `server_message`, `code`; `kind` conflict, `retryable` False, `status_code` 409. JSON and multipart paths both raise it.
- **Both server generations recognised:** new servers send HTTP 409 with
  `code: "session_record_limit"` plus `limit`/`count`/`session_uuid`; old
  servers send only `{"error": "session has reached the maximum of N records"}`.
  Missing fields are null (never a fake zero); `limit` is read back from the
  server's own sentence when the field is absent. Any other 409 keeps the
  generic error. **Superseded in 4.0.0: code-only; `limit`/`count` are `int`,
  `session_uuid` is `str`; the old-server text path was removed.**
- **Non-retryable**, and that does not mean "drop it": keep the write and
  retry later; never write it into another session to dodge the cap.
- **No gRPC path:** the SDK has no gRPC transport; the gRPC form of the
  contract (ResourceExhausted / legacy AlreadyExists) is documented in
  `v2/SDK_ERROR_CONTRACT.md` for any future transport.
- Code: `anhurdb/client/session_limit.py`; tests in `tests/test_session_record_limit.py`.

## 3.0.0 — SDK parity: twelve untyped responses, one `create()`, the server's own depth (2026-09-14)

The last round of divergence between the three arms. Every verdict below is
anchored to a handler line AND to a read-only live call against
`https://anhurdb.yoven.ai`; the write paths were proved in a disposable
`paridade-` session that was deleted and whose deletion was proved
(`GET /api/v1/chats/{uuid}` → `count: 0`).

Breaking, hence 3.0.0 and not 2.2.0. Go and TypeScript ship the same number.

### BREAKING — twelve responses are models, not `Dict[str, Any]`

Subscript access stops working; attribute access replaces it. The field sets
below are the LIVE responses, not inferences from a handler.

| Method | Before | After | Migration |
|---|---|---|---|
| `add`, `create`, `create_in_session` | `dict` | `AddResult` | `r["records"][0]["id"]` → `r.records[0].id`; `r["mode"]` → `r.mode` |
| `profile` | `dict` | `ProfileResult` | `p["static"]` → `p.static`; `p["stats"]["total_records"]` → `p.stats.total_records` |
| `walk`, `walk_semantic` | `dict` | `WalkResult` | `w["nodes"]` → `w.nodes` (full `Record`s), `w["edges"]` → `w.edges` |
| `get_context` | `dict` | `ContextResult` | `c["target"]` → `c.target`, `c["neighbors"]` → `c.neighbors` |
| `get_grounding` | `dict` | `GroundingResult` | `g["anchors"]` → `g.anchors`; `g["depth_used"]` → `g.depth_used` |
| `list_sessions` | `list[dict]` | `list[SessionStats]` | `s["uuid"]` → `s.uuid`; the key is `last_activity`, not `last_active` |
| `manifest_global`, `manifest_session` | `dict` | `ManifestResult` | `m["records"]` → `m.records`; `m["has_more"]` → `m.has_more` |
| `upload_file` | `dict` | `UploadResult` | `u["record_id"]` → `u.record_id` |
| `upload_status`, `wait_for_upload` | `dict` | `UploadStatusResult` | `s["status"]` → `s.status`; `s["completed"]` → `s.completed` |
| `search_entities`, `get_record_entities` | `list[dict]` | `list[EntityModel]` | `e["name"]` → `e.name`; the wire key is `entity_type`, never `type` |
| `upsert_entity` | `dict` | `EntityModel` | `e["id"]` → `e.id` |
| `list_entities` | `dict` | `EntitiesPage` | `p["entities"]` → `p.entities`; follow `p.next_offset` |
| `get_entity_graph`, `entity_graph` | `dict` | `EntityGraphResult` | `g["nodes"]` → `g.nodes`; `g["node_count"]` → `g.node_count` |
| `entity_timeline` | `dict` | `EntityTimelineResult` | `t["timeline"]` → `t.timeline` |
| `smart_search` | `dict` | `SmartSearchResponse` | `r["results"]` → `r.results` — and see the null below |

Three shapes carry a trap the dicts hid:

- **`SmartSearchResponse.results` is genuinely `None`**, not `[]`, on the
  ordinary "no matches" answer. The handler marshals a nil Go slice and a nil
  slice serialises as JSON `null` (`handler/search_smart.go:216-224`). Iterate
  with `for hit in (response.results or [])`. Same `None != []` discipline
  `SearchResponse.leg_scores` already keeps.
- **`WalkResult.truncated` is `None` after `walk_semantic`.** Only
  `POST /api/v1/walk` emits the flag; `/walk/semantic` answers `{nodes, edges}`
  and makes no completeness claim. Reporting `False` would invent a guarantee
  the endpoint never gave.
- **`SmartSearchHit.relevance` is a LEXICAL score** (BM25 × cognitive decay),
  not the cosine `SearchResult.similarity`. Different unit, hence a different
  type — the two must never share a threshold.

Everything still untyped, deliberately and in all three arms: `health()`,
`get(record_id)`, `batch_read_content()`, `get_session_history()`,
`get_session_clusters()`, and the `{"message": ...}` write acks. Typing them is
separate, additive work.

Every model is `ConfigDict(populate_by_name=True, extra="ignore")` — never
`extra="forbid"` — so a server that adds a field does not break every caller.

### BREAKING — `create(session_id, content, *, ...)` replaces `create(req)`

```python
# before
await mem.create(CreateRequest(session_id=s, content=c, type=MemoryType.FACT))
# after
await mem.create(s, c, type=MemoryType.FACT)
```

Session and content are the two things `POST /api/v1/records` cannot succeed
without (a missing anchor is HTTP 422), so they became required positionals —
the same convention Go has always had and TypeScript now adopts. The old
`session_id`-or-legacy-`uuid` guess is gone: a client that picks between two
fields for you is a client that can pick wrong and never say so.

`CreateRequest` **stays exported** as a typed carrier, and gains
`optional_fields()` for the migration:

```python
await mem.create(req.session_id or req.uuid, req.content, **req.optional_fields())
```

`optional_fields()` returns only what the caller actually SET (`model_fields_set`),
so an unset field stays unset all the way to the wire instead of pinning today's
server default forever.

Accepting either a `str` or a `CreateRequest` as argument 1 was considered and
rejected — that is exactly the guessing this release deletes.

Two further changes inside `create()`:

- **The legacy `uuid` wire alias is gone.** The server field is `session_id`;
  the duplicate was belt-and-braces for a server generation that no longer
  exists. Confirmed with one live create in the `paridade-` session before
  removal, and the outgoing payload is now asserted key-by-key in
  `tests/test_parity_wire_offline.py`.
- **`valid_from` / `valid_until` now travel inside `metadata`.** They were sent
  as top-level payload fields, and `service/record_create.go` reads the
  bi-temporal window ONLY from the metadata JSON on this route — so the call
  returned HTTP 200 and a record with no window at all. The pin evaporated with
  nothing to catch. Go has folded them into the envelope since
  `parity.go:83-98`; Python now matches, so the same call produces the same
  record in both arms.

`create()` still never fabricates an episodic anchor client-side. 422 stays 422.

### BREAKING — `get_entity_graph()` / `entity_graph()` default to depth 1

The handler's own default is 1 (`entity.go:280`); this SDK defaulted to 2 and
SENT it, while Go and TypeScript both omitted the parameter. Live 2026-09-14:
omitted → `depth:1, node_count:1`; `?depth=2` → `depth:2, node_count:2`.

So the identical call returned a strictly larger graph in Python than in the
other two arms, and anyone comparing SDKs read "Python finds more entities"
when the truth was "Python silently asked a different question".

**A caller who never passed `depth` now gets a depth-1 graph where they used to
get depth-2.** Pass `depth=2` to keep the old result. The parameter is now
omitted entirely when unset rather than restated as `depth=1` — when the server
changes its default, the SDK follows instead of pinning yesterday's value.

### `profile()` — the tag is an in-tenant filter, documented and guarded

`profile(container_tag=None)` keeps its signature (Python was the arm that had
this right; Go and TypeScript gained it). What changed:

- An **empty** tag now raises `ValueError` locally. `GET /api/v1/profile`
  without a tag is a guaranteed HTTP 400 (`tag: tag is required`); spending a
  round trip to learn a string we already have is the waste the AST builder
  already refuses for `$in []`.
- An **unknown** tag stays what the server says it is: an empty profile with
  HTTP 200, never an exception. Live, `?tag=totally-not-a-real-tag-xyz`
  answered 200 with every counter at zero. Raising would hide a typo'd tag
  behind a fake outage.
- The tag filters WITHIN the caller's own tenant and cannot reach another one —
  `profile.go:63-66` resolves the tenant from the auth middleware context and
  reads `tag` only from the query string. There is no wildcard either: `?tag=*`
  is a literal tag.
- On HTTP 404 (an OSS build with no profile engine) the fallback is an
  all-default `ProfileResult`. The old dict carried `tag` and
  `status: "not_available"` keys the server has never sent on this route.

### Phantom fields removed

A declared field the server never sends is worse than a missing one: it makes
unreachable branches read like safety nets.

- `wait_for_upload()` no longer treats a truthy `payload["error"]` as terminal.
  `GET /upload/{id}/status` is a fixed seven-key map (`upload.go:220-236`) and
  has never sent an `error` key on any code path. A failed ingest is reported
  through `status`, and only through `status`.
- `UploadStatusResult` declares exactly those seven keys — no `id`, no
  `filename`, no `error`.
- `UploadResult` has `record_id` and no `id`. Polling with a phantom `id`
  returns 404 for a file that uploaded perfectly.
- `ManifestResult` has no `next_offset`. That cursor exists on
  `/sessions/stats` and `/entities/list`, and on neither manifest route
  (`search_aux.go:225-229`, `record_session.go:307-311`); modelling one would
  give every caller a permanently-zero field that reads like page one.
- `ProfileResult` is exactly `{static, dynamic, stats}` — no `tag`, no `status`.

`tests/test_parity_models_offline.py` now audits this structurally, in both
directions: every model must declare a SUPERSET of the keys the route can emit
(a missing field is a silent drop under `extra="ignore"`), and every declared
field must map to a wire key or be justified in writing in
`tests/live_field_sets.py`.

### Internals

- `client/__init__.py` was 2058 lines — past the house 300-line cut — so the
  domains this release touched were SPLIT OUT before they grew:
  `client/record_graph.py` (walk / topology / grounding),
  `client/manifests.py`, `client/uploads.py`, `client/entities.py` and
  `client/profile.py`, each a mixin on `Memory`. The public surface is
  unchanged: every method is still `Memory.<name>`, and no import path moved.
- New models live under `anhurdb/models/` by domain — `add.py`, `profile.py`,
  `graph.py`, `manifest.py`, `upload.py`, `entity.py`, `smart_search.py` —
  never inside `record.py` or `search.py`, both of which are near the cut.
- `SessionStats` fields all gained defaults. It is a READ model: one partial
  row must not raise a `ValidationError` that destroys the entire page of
  sessions, the same all-or-nothing failure `Record` already guards against.
- `EntityGraphEdge` is a new READ type distinct from `EntityEdge`. `EntityEdge`
  is what a caller BUILDS for `upsert_entity_edge` (no `id`, no `ingested_at`,
  no `weight` — the server assigns all three); `EntityGraphEdge` is what the
  graph and timeline routes ANSWER with, every field present and defaulted.
- `create(type=...)` accepts a `MemoryType` **or** its plain string value.
  Reading `.value` off the argument raised `AttributeError` on the string path
  only — invisible to every enum-using test, and caught by the live run.
- New `tests/live_field_sets.py` holds the live field sets ONCE, consumed by
  both the offline decode suite and the new live contract suite, so the offline
  one cannot drift into "passing" against a shape the server abandoned.
- New `tests/test_parity_live_contract.py` (9 cases, gated by
  `ANHUR_LIVE_AST=1`) re-proves every field set against the running server.

## 2.1.0 — AST query parity: the four silent divergences (2026-09-14)

Held the 2.1.0 release for these. All four failed with no symptom: the caller
got a 200, or an exception nobody was catching.

### BREAKING — `QueryBuilder.semantic_search()` now raises

The server accepts a `semantic_search` block and **skips** it
(`service/record_ast_query.go:169-172` logs the block and continues). Confirmed
live: sent alongside `type=risk`, it returned exactly the plain `type=risk`
result set. The method therefore did nothing at all, while its name promised
semantic ranking — a user could ship believing they had it.

Kept as a raising method rather than deleted: deleting turns an existing call
into an `AttributeError` that names nothing, while raising names
`Memory.search()` / `POST /api/v1/search`, the path that actually runs the
vector/hybrid retrieval. Nothing inside `anhurdb/` ever called it, so the blast
radius is user code — which is the code that needs to read the message. The
pseudo-key is still reachable by hand through `Filter` for anyone probing the
server. (Go refuses the key client-side; TypeScript never had the method.)

### BREAKING — client-side query rejections are `AnhurQueryError`

A bad column used to raise `ValueError` locally while the identical mistake
written as a raw dict came back as `AnhurQueryError(kind="invalid_request")`
from the server. One bug class, two `except` blocks, and anyone who wrote the
obvious one silently missed half the cases. Now unified: `where()`,
`order_by()`, `limit()`, `offset()`, `execute()` without an executor, and
`query()` with a non-AST argument all raise `AnhurQueryError` with
`kind == "invalid_request"`. `status_code` still separates the halves — `None`
when nothing was sent, `400` when the server answered. Message texts are
unchanged, so string-matching callers are unaffected.

### `None` as an operand is refused

`where(status=None)` was accepted and produced `col = NULL`, which is NULL and
never true: HTTP 200 with **zero rows on every input, forever**. The grammar has
no `$exists`, no `$ne` and no `IS NULL`, so the intent cannot be expressed
through this endpoint at all — the error says so instead of just forbidding.
Applies to `$eq`, `$gt`, `$gte`, `$lt`, `$lte` and to a `None` element inside
`$in`. Falsy values (`False`, `0`, `""`) are untouched and still filter.

### `$in []` is refused client-side

The server answers a fixed `400 filter "X": $in requires a non-empty array of
values`. Go already refused it locally; Python spent a round trip to learn a
string it already had.

### Internals

- New `anhurdb/query/grammar.py` — the server grammar mirrored in one place
  (whitelist, operator set, every rejection), with the rule for when a
  client-side duplicate of a server check earns its place.
- New `anhurdb/query/shorthand.py` (`Filter`, `Eq`) and
  `anhurdb/client/query_argument.py`, splitting `builder.py` (308 -> 274 lines)
  and shrinking `client/__init__.py` under the 300-line house rule's spirit.
  `Filter` still validates nothing — that is the documented escape hatch.
- New `tests/test_ast_client_rejections.py` (24 cases) drives the real builder;
  each guard was proven to bite by reverting it and watching the suite fail.
- `tests/test_builder.py::test_execute_without_executor_raises` used
  `asyncio.get_event_loop()` and passed only when an earlier test left a loop
  installed. Now `asyncio.run`.

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
- The published install pin in the README is deliberately still the newest PUBLISHED
  wheel (`v2/python/v2.0.20`, corrected 2026-09-13 — this line said `2.0.12`, a pin
  the README had already moved off): the
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

