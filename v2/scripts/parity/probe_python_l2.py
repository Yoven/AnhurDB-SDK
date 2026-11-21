"""The "L2/ADR-0033" section of the Python live parity probe.

One domain in one sentence: upload one throwaway file into the tenant library
and walk it through every by-id door with an explicit scope, then prove the
chat plane cannot see it and delete it by file.

The Go (``golang/scripts/parity_probe/l2_scope.go``) and TypeScript
(``probe_typescript_l2.ts``) probes run the SAME steps with the SAME op names;
``run_all.sh`` diffs the RESULT lines of the three into one PASS/FAIL matrix.
Renaming an op here without renaming it there is a parity break.
"""

from __future__ import annotations

import asyncio
import hashlib
import time
from typing import Any, Callable, Optional, Tuple

from anhurdb import (
    SCOPE_SESSIONS,
    SCOPE_TENANT_SHARED,
    Memory,
    sessions_all,
    tenant_shared_scope_for_hit,
)

# Junior Tip [why poll at all]: the library hit appears only after the
# file_ingestor wrote its root/chapters AND the search index caught up; a PATCH
# is applied through Raft and a load-balanced read may land on a node one index
# behind. One read would turn replication lag into a false FAIL.
UPLOAD_WAIT_BUDGET_SECONDS = 180.0
SEARCH_BUDGET_SECONDS = 120.0
SEARCH_INTERVAL_SECONDS = 3.0
READ_BACK_BUDGET_SECONDS = 20.0
READ_BACK_INTERVAL_SECONDS = 2.0

# Ops after L2.UploadTenantShared, in print order; a failed upload reports
# each as blocked so every SDK prints the same op set.
DEPENDENT_OPS = [
    "L2.UploadScope",
    "L2.UploadStatusScoped",
    "L2.SearchTenantShared",
    "L2.HitScope",
    "L2.GetScoped",
    "L2.UpdateSummaryScoped",
    "L2.ReadBackSummary",
    "L2.NoLeakSessions",
    "L2.DeleteByFile",
]

Emitter = Callable[[str, str, str, Any], None]


async def run_l2_section(mem: Memory, token: int, emit_status: Emitter) -> int:
    """Run the L2 steps; return how many FAILed. ``emit_status(op, status,
    detail, err)`` prints one RESULT line (status PASS | FAIL | SKIP)."""
    fail_count = 0

    def passed(operation: str, detail: str) -> None:
        emit_status(operation, "PASS", detail, None)

    def failed(operation: str, detail: str, err: Any = None) -> None:
        nonlocal fail_count
        fail_count += 1
        emit_status(operation, "FAIL", detail, err)

    def blocked(operation: str) -> None:
        failed(operation, "prior L2 step failed", "blocked")

    # Junior Tip [the file identity is computed, not looked up]: the server
    # hashes the uploaded bytes with SHA-256 and the file_ingestor keys every
    # root/chapter as "<first 16 hex>:...". Hashing the same bytes here gives
    # the delete-by-file prefix without trusting any response field.
    file_bytes = (
        f"parity-l2-python: AnhurDB L2 tenant library probe token={token}\n"
        "This throwaway document proves by-id scope on the tenant library plane.\n"
    ).encode("utf-8")
    ingest_key_prefix = hashlib.sha256(file_bytes).hexdigest()[:16]

    try:
        upload = await mem.upload_file(
            f"parity-l2-python-{token}.txt", file_bytes, mode="tenant_shared"
        )
        if upload.record_id <= 0:
            raise RuntimeError(f"no record_id in {upload!r}")
        passed("L2.UploadTenantShared", f"record_id={upload.record_id} prefix={ingest_key_prefix}")
    except Exception as exc:  # noqa: BLE001
        failed("L2.UploadTenantShared", "", exc)
        for operation in DEPENDENT_OPS:
            blocked(operation)
        _report_session_cap_skip(emit_status)
        return fail_count

    if upload.scope == SCOPE_TENANT_SHARED:
        passed("L2.UploadScope", upload.scope)
    else:
        failed("L2.UploadScope", "want tenant_shared", f"scope={upload.scope!r}")

    try:
        final = await mem.wait_for_upload(
            upload.record_id, timeout=UPLOAD_WAIT_BUDGET_SECONDS, scope=upload.scope
        )
        if (final.status or "").lower() == "failed":
            failed("L2.UploadStatusScoped", f"status={final.status}", "ingest failed")
        else:
            passed("L2.UploadStatusScoped", f"status={final.status}")
    except Exception as exc:  # noqa: BLE001
        failed("L2.UploadStatusScoped", "", exc)

    hit = await _find_library_hit(mem, ingest_key_prefix)
    if hit is None:
        failed("L2.SearchTenantShared", "no hit whose metadata carries the file prefix", "not found")
        for operation in DEPENDENT_OPS[3:8]:
            blocked(operation)
        fail_count += await _run_delete_by_file(mem, ingest_key_prefix, emit_status)
        _report_session_cap_skip(emit_status)
        return fail_count
    hit_id, hit_uuid = hit
    passed("L2.SearchTenantShared", f"id={hit_id} uuid={hit_uuid}")

    hit_scope = tenant_shared_scope_for_hit(hit_uuid)
    if hit_scope == SCOPE_TENANT_SHARED:
        passed("L2.HitScope", hit_scope)
    else:
        failed("L2.HitScope", "fresh upload must land in the _lib store", f"scope={hit_scope!r}")

    try:
        scoped = await mem.get(hit_id, scope=hit_scope)
        if int(scoped.get("id") or 0) != hit_id or str(scoped.get("uuid")) != hit_uuid:
            failed("L2.GetScoped", f"id={scoped.get('id')} uuid={scoped.get('uuid')}", "mismatch")
        else:
            passed("L2.GetScoped", f"id={hit_id}")
    except Exception as exc:  # noqa: BLE001
        failed("L2.GetScoped", "", exc)

    new_summary = f"parity-l2 summary updated token={token}"
    try:
        await mem.update(hit_id, scope=hit_scope, summary=new_summary)
        passed("L2.UpdateSummaryScoped", "ok")
        last_summary, read_error = await _wait_for_summary(mem, hit_id, hit_scope, new_summary)
        if read_error is None:
            passed("L2.ReadBackSummary", "match")
        else:
            failed("L2.ReadBackSummary", f"last={last_summary}", read_error)
    except Exception as exc:  # noqa: BLE001
        failed("L2.UpdateSummaryScoped", "", exc)
        blocked("L2.ReadBackSummary")

    # Junior Tip [leak check BEFORE the delete]: after delete-by-file the
    # library row is tombstoned, so a sessions read could "pass" only because
    # nothing is left to leak. Checking while the file is alive can fail.
    try:
        sessions_record = await mem.get(hit_id, scope=SCOPE_SESSIONS)
        if str(sessions_record.get("uuid")) == hit_uuid or sessions_record.get("summary") == new_summary:
            failed("L2.NoLeakSessions", f"uuid={sessions_record.get('uuid')}", "cross-plane leak")
        else:
            passed("L2.NoLeakSessions", f"other_record uuid={sessions_record.get('uuid')}")
    except Exception as exc:  # noqa: BLE001
        if getattr(exc, "status_code", None) == 404:
            passed("L2.NoLeakSessions", "not_found")
        else:
            failed("L2.NoLeakSessions", "want 404 or another record", exc)

    fail_count += await _run_delete_by_file(mem, ingest_key_prefix, emit_status)
    _report_session_cap_skip(emit_status)
    return fail_count


async def _find_library_hit(mem: Memory, ingest_key_prefix: str) -> Optional[Tuple[int, str]]:
    """Poll search_tenant_shared until a hit's metadata carries the prefix."""
    deadline = time.monotonic() + SEARCH_BUDGET_SECONDS
    while True:
        try:
            hits = await mem.search_tenant_shared("AnhurDB L2 tenant library probe", sessions_all())
        except Exception:  # noqa: BLE001 — a transient error is retried until the budget
            hits = []
        for search_hit in hits:
            record = search_hit.record
            if (record.id or 0) > 0 and ingest_key_prefix in (record.metadata or ""):
                return int(record.id or 0), record.uuid
        if time.monotonic() + SEARCH_INTERVAL_SECONDS > deadline:
            return None
        await asyncio.sleep(SEARCH_INTERVAL_SECONDS)


async def _wait_for_summary(
    mem: Memory, record_id: int, scope: str, want: str
) -> Tuple[str, Optional[str]]:
    """Re-read (scoped) until the summary equals ``want``; (last, error)."""
    deadline = time.monotonic() + READ_BACK_BUDGET_SECONDS
    last_summary = ""
    while True:
        try:
            read_back = await mem.get(record_id, scope=scope)
            last_summary = str(read_back.get("summary"))
            if last_summary == want:
                return last_summary, None
        except Exception as exc:  # noqa: BLE001
            if time.monotonic() + READ_BACK_INTERVAL_SECONDS > deadline:
                return last_summary, str(exc)
        if time.monotonic() + READ_BACK_INTERVAL_SECONDS > deadline:
            return last_summary, f"summary not updated within {READ_BACK_BUDGET_SECONDS}s"
        await asyncio.sleep(READ_BACK_INTERVAL_SECONDS)


async def _run_delete_by_file(mem: Memory, ingest_key_prefix: str, emit_status: Emitter) -> int:
    """Delete the file from the tenant library; 1 if the step FAILed."""
    try:
        result = await mem.delete_file("", ingest_key_prefix, scope=SCOPE_TENANT_SHARED)
    except Exception as exc:  # noqa: BLE001
        emit_status("L2.DeleteByFile", "FAIL", "", exc)
        return 1
    leg_errors = sum(1 for leg in result.legs if leg.error)
    detail = f"legs={len(result.legs)} deleted={result.deleted_count} leg_errors={leg_errors}"
    if not result.legs or result.deleted_count < 1 or leg_errors:
        emit_status("L2.DeleteByFile", "FAIL", detail, "want legs>=1, deleted>=1, no leg error")
        return 1
    emit_status("L2.DeleteByFile", "PASS", detail, None)
    return 0


def _report_session_cap_skip(emit_status: Emitter) -> None:
    """Junior Tip [do not invent the door]: the per-tenant cap is set only via
    POST /internal/admin/config (MasterOnly) and no SDK exposes it, so a probe
    running with a tenant key cannot lower it."""
    emit_status("L2.SessionRecordLimit", "SKIP", "SKIP session_record_limit: needs admin", None)
