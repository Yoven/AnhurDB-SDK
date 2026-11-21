# Cross-SDK live parity probes

Exercises Go / Python / TypeScript `Memory` clients against one AnhurDB with the
same operations (health, CRUD, search, entities, graph, grounding, and the
L2/ADR-0033 tenant-library section), then prints one PASS/FAIL matrix.

## Run

```bash
export ANHUR_API_KEY=...
export ANHUR_URL=https://anhurdb.yoven.ai   # optional
export ANHUR_TENANT_ID=...                  # optional; default = one throwaway tenant per probe
bash AnhurDB-SDK/v2/scripts/parity/run_all.sh
```

Or individually:

```bash
cd AnhurDB-SDK/v2/golang && go run ./scripts/parity_probe/
python3 AnhurDB-SDK/v2/scripts/parity/probe_python.py
cd AnhurDB-SDK/v2/typescript && npx tsx ../scripts/parity/probe_typescript.ts
```

## Output and exit status

Every step prints one line per SDK:

```text
RESULT sdk=<go|python|typescript> op="<step>" status=<PASS|FAIL|SKIP> ... detail=...
```

`run_all.sh` runs all three probes (one failing does not stop the others),
parses those lines and prints a `STEP × SDK` matrix. A row is `MISMATCH` when
any cell is `FAIL`, when an SDK never printed the step (`MISSING`), or when the
three cells differ. The script exits **non-zero** on any mismatch or any probe
exiting non-zero. A step that is `SKIP` on all three SDKs is not a mismatch.

## What must match

- `ListEntities` / `GetRecordEntities` expose non-empty `entity_type`
- Upsert case variants (`Foo` / `FOO` / ` foo `) collapse to **one** entity id
  (server `NormalizeEntityName`: trim + lowercase)
- Write→read→search→entity link→graph paths succeed on all three SDKs

## L2/ADR-0033 section (tenant library, by-id scope)

Files: `golang/scripts/parity_probe/l2_scope.go`, `probe_python_l2.py`,
`probe_typescript_l2.ts` — same steps, same op names. Each probe uploads its
own small `.txt` (content carries the SDK name and a token, so the three files
differ); the delete-by-file prefix is the first 16 hex of the SHA-256 of those
bytes, the same `<checksum16>` the file_ingestor writes into `ingest_key`.

| Step | Asserts |
|---|---|
| `L2.UploadTenantShared` | upload with `mode=tenant_shared` returns `record_id > 0` |
| `L2.UploadScope` | `result.scope == tenant_shared` (SDK-filled from the mode) |
| `L2.UploadStatusScoped` | wait-for-upload polled **with that scope** reaches a terminal status that is not `failed` (budget 180 s) |
| `L2.SearchTenantShared` | `search_tenant_shared(sessions_all)` returns a hit whose `metadata` carries the file prefix (polled, budget 120 s) |
| `L2.HitScope` | `TenantSharedScopeForHit(hit.uuid) == tenant_shared` (fresh upload lands in the `_lib` store) |
| `L2.GetScoped` | get by id with that scope returns the same `id` and `uuid` |
| `L2.UpdateSummaryScoped` | update `summary` with that scope succeeds |
| `L2.ReadBackSummary` | scoped get returns the new summary (polled, budget 20 s) |
| `L2.NoLeakSessions` | get the **same id** with `scope=sessions` is 404, or a different record (ids are per store) — never the library file (same uuid or our summary). Run **before** the delete, so it can actually fail |
| `L2.DeleteByFile` | delete-by-file, empty session, `scope=tenant_shared`: `legs` non-empty, `deleted_count >= 1`, no leg `error` |
| `L2.SessionRecordLimit` | always `SKIP session_record_limit: needs admin` — the per-tenant `max_session_records` is set only through `POST /internal/admin/config` (MasterOnly) and no SDK exposes it |

A step whose prerequisite failed prints `FAIL err=blocked` instead of being
omitted, so the matrix keeps the same rows on every SDK.

The L2 steps depend on the live file_ingestor (root/chapters must exist for
the search hit and for `deleted_count >= 1`): a stalled ingestor shows up as
`L2.UploadStatusScoped` / `L2.SearchTenantShared` FAIL on all three columns.
