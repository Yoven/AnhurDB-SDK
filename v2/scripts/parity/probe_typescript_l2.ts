/**
 * The "L2/ADR-0033" section of the TypeScript live parity probe.
 *
 * One domain in one sentence: upload one throwaway file into the tenant library
 * and walk it through every by-id door with an explicit scope, then prove the
 * chat plane cannot see it and delete it by file.
 *
 * The Go (`golang/scripts/parity_probe/l2_scope.go`) and Python
 * (`probe_python_l2.py`) probes run the SAME steps with the SAME op names;
 * `run_all.sh` diffs the RESULT lines of the three into one PASS/FAIL matrix.
 * Renaming an op here without renaming it there is a parity break.
 */
import { createHash } from "node:crypto";

import {
  AnhurError,
  Memory,
  sessionsAll,
  tenantSharedScopeForHit,
  type RecordScope,
} from "../../typescript/src/index.js";

/** Prints one RESULT line; status is PASS | FAIL | SKIP. */
export type L2Emitter = (operation: string, status: "PASS" | "FAIL" | "SKIP", detail: string, err?: unknown) => void;

// Junior Tip [why poll at all]: the library hit appears only after the
// file_ingestor wrote its root/chapters AND the search index caught up; a PATCH
// is applied through Raft and a load-balanced read may land on a node one index
// behind. One read would turn replication lag into a false FAIL.
const UPLOAD_WAIT_BUDGET_MS = 180_000;
const SEARCH_BUDGET_MS = 120_000;
const SEARCH_INTERVAL_MS = 3_000;
const READ_BACK_BUDGET_MS = 20_000;
const READ_BACK_INTERVAL_MS = 2_000;

/** Ops after L2.UploadTenantShared, in print order; a failed upload reports
 * each as blocked so every SDK prints the same op set. */
const DEPENDENT_OPS = [
  "L2.UploadScope",
  "L2.UploadStatusScoped",
  "L2.SearchTenantShared",
  "L2.HitScope",
  "L2.GetScoped",
  "L2.UpdateSummaryScoped",
  "L2.ReadBackSummary",
  "L2.NoLeakSessions",
  "L2.DeleteByFile",
];

const sleep = (milliseconds: number) => new Promise((resolve) => setTimeout(resolve, milliseconds));

/** Run the L2 steps; resolve to how many FAILed. */
export async function runL2Section(mem: Memory, token: number, emitStatus: L2Emitter): Promise<number> {
  let failCount = 0;
  const passed = (operation: string, detail: string) => emitStatus(operation, "PASS", detail);
  const failed = (operation: string, detail: string, err: unknown) => {
    failCount++;
    emitStatus(operation, "FAIL", detail, err);
  };
  const blocked = (operation: string) => failed(operation, "prior L2 step failed", "blocked");

  // Junior Tip [the file identity is computed, not looked up]: the server
  // hashes the uploaded bytes with SHA-256 and the file_ingestor keys every
  // root/chapter as "<first 16 hex>:...". Hashing the same bytes here gives the
  // delete-by-file prefix without trusting any response field.
  const fileText =
    `parity-l2-typescript: AnhurDB L2 tenant library probe token=${token}\n` +
    "This throwaway document proves by-id scope on the tenant library plane.\n";
  const fileBytes = new TextEncoder().encode(fileText);
  const ingestKeyPrefix = createHash("sha256").update(fileBytes).digest("hex").slice(0, 16);

  let recordId = 0;
  let uploadScope: RecordScope | undefined;
  try {
    const upload = await mem.uploadFile(`parity-l2-typescript-${token}.txt`, fileBytes, { mode: "tenant_shared" });
    recordId = upload.record_id ?? 0;
    uploadScope = upload.scope;
    if (recordId <= 0) throw new Error(`no record_id in ${JSON.stringify(upload)}`);
    passed("L2.UploadTenantShared", `record_id=${recordId} prefix=${ingestKeyPrefix}`);
  } catch (err) {
    failed("L2.UploadTenantShared", "", err);
    DEPENDENT_OPS.forEach(blocked);
    reportSessionCapSkip(emitStatus);
    return failCount;
  }

  if (uploadScope === "tenant_shared") {
    passed("L2.UploadScope", uploadScope);
  } else {
    failed("L2.UploadScope", "want tenant_shared", `scope=${String(uploadScope)}`);
  }

  try {
    const finalStatus = await mem.waitForUpload(recordId, { timeoutMs: UPLOAD_WAIT_BUDGET_MS, scope: uploadScope });
    if ((finalStatus.status ?? "").toLowerCase() === "failed") {
      failed("L2.UploadStatusScoped", `status=${finalStatus.status}`, "ingest failed");
    } else {
      passed("L2.UploadStatusScoped", `status=${finalStatus.status}`);
    }
  } catch (err) {
    failed("L2.UploadStatusScoped", "", err);
  }

  const hit = await findLibraryHit(mem, ingestKeyPrefix);
  if (hit === undefined) {
    failed("L2.SearchTenantShared", "no hit whose metadata carries the file prefix", "not found");
    DEPENDENT_OPS.slice(3, 8).forEach(blocked);
    failCount += await runDeleteByFile(mem, ingestKeyPrefix, emitStatus);
    reportSessionCapSkip(emitStatus);
    return failCount;
  }
  passed("L2.SearchTenantShared", `id=${hit.id} uuid=${hit.uuid}`);

  const hitScope = tenantSharedScopeForHit(hit.uuid);
  if (hitScope === "tenant_shared") {
    passed("L2.HitScope", hitScope);
  } else {
    failed("L2.HitScope", "fresh upload must land in the _lib store", `scope=${hitScope}`);
  }

  try {
    const scoped = await mem.get(hit.id, { scope: hitScope });
    if (Number(scoped.id ?? 0) !== hit.id || String(scoped.uuid) !== hit.uuid) {
      failed("L2.GetScoped", `id=${String(scoped.id)} uuid=${String(scoped.uuid)}`, "mismatch");
    } else {
      passed("L2.GetScoped", `id=${hit.id}`);
    }
  } catch (err) {
    failed("L2.GetScoped", "", err);
  }

  const newSummary = `parity-l2 summary updated token=${token}`;
  try {
    await mem.update(hit.id, { summary: newSummary }, { scope: hitScope });
    passed("L2.UpdateSummaryScoped", "ok");
    const [lastSummary, readError] = await waitForSummary(mem, hit.id, hitScope, newSummary);
    if (readError === undefined) {
      passed("L2.ReadBackSummary", "match");
    } else {
      failed("L2.ReadBackSummary", `last=${lastSummary}`, readError);
    }
  } catch (err) {
    failed("L2.UpdateSummaryScoped", "", err);
    blocked("L2.ReadBackSummary");
  }

  // Junior Tip [leak check BEFORE the delete]: after delete-by-file the library
  // row is tombstoned, so a sessions read could "pass" only because nothing is
  // left to leak. Checking while the file is alive is the check that can fail.
  try {
    const sessionsRecord = await mem.get(hit.id, { scope: "sessions" });
    if (String(sessionsRecord.uuid) === hit.uuid || sessionsRecord.summary === newSummary) {
      failed("L2.NoLeakSessions", `uuid=${String(sessionsRecord.uuid)}`, "cross-plane leak");
    } else {
      passed("L2.NoLeakSessions", `other_record uuid=${String(sessionsRecord.uuid)}`);
    }
  } catch (err) {
    if (err instanceof AnhurError && err.statusCode === 404) {
      passed("L2.NoLeakSessions", "not_found");
    } else {
      failed("L2.NoLeakSessions", "want 404 or another record", err);
    }
  }

  failCount += await runDeleteByFile(mem, ingestKeyPrefix, emitStatus);
  reportSessionCapSkip(emitStatus);
  return failCount;
}

/** Poll searchTenantShared until a hit's metadata carries the file prefix. */
async function findLibraryHit(mem: Memory, ingestKeyPrefix: string): Promise<{ id: number; uuid: string } | undefined> {
  const deadline = Date.now() + SEARCH_BUDGET_MS;
  for (;;) {
    let hits: Awaited<ReturnType<Memory["searchTenantShared"]>> = [];
    try {
      hits = await mem.searchTenantShared("AnhurDB L2 tenant library probe", sessionsAll());
    } catch {
      // A transient error is retried until the budget runs out.
    }
    for (const searchHit of hits) {
      if (searchHit.record.id > 0 && (searchHit.record.metadata ?? "").includes(ingestKeyPrefix)) {
        return { id: searchHit.record.id, uuid: searchHit.record.uuid };
      }
    }
    if (Date.now() + SEARCH_INTERVAL_MS > deadline) return undefined;
    await sleep(SEARCH_INTERVAL_MS);
  }
}

/** Re-read (scoped) until the summary equals `want`; [last, error]. */
async function waitForSummary(
  mem: Memory,
  recordId: number,
  scope: RecordScope,
  want: string,
): Promise<[string, string | undefined]> {
  const deadline = Date.now() + READ_BACK_BUDGET_MS;
  let lastSummary = "";
  for (;;) {
    try {
      const readBack = await mem.get(recordId, { scope });
      lastSummary = String(readBack.summary);
      if (lastSummary === want) return [lastSummary, undefined];
    } catch (err) {
      if (Date.now() + READ_BACK_INTERVAL_MS > deadline) return [lastSummary, String(err)];
    }
    if (Date.now() + READ_BACK_INTERVAL_MS > deadline) {
      return [lastSummary, `summary not updated within ${READ_BACK_BUDGET_MS}ms`];
    }
    await sleep(READ_BACK_INTERVAL_MS);
  }
}

/** Delete the file from the tenant library; 1 if the step FAILed. */
async function runDeleteByFile(mem: Memory, ingestKeyPrefix: string, emitStatus: L2Emitter): Promise<number> {
  try {
    const result = await mem.deleteFile("", ingestKeyPrefix, { scope: "tenant_shared" });
    const legs = result.legs ?? [];
    const legErrors = legs.filter((leg) => leg.error).length;
    const detail = `legs=${legs.length} deleted=${result.deleted_count} leg_errors=${legErrors}`;
    if (legs.length === 0 || result.deleted_count < 1 || legErrors > 0) {
      emitStatus("L2.DeleteByFile", "FAIL", detail, "want legs>=1, deleted>=1, no leg error");
      return 1;
    }
    emitStatus("L2.DeleteByFile", "PASS", detail);
    return 0;
  } catch (err) {
    emitStatus("L2.DeleteByFile", "FAIL", "", err);
    return 1;
  }
}

/**
 * Junior Tip [do not invent the door]: the per-tenant cap is set only via
 * POST /internal/admin/config (MasterOnly) and no SDK exposes it, so a probe
 * running with a tenant key cannot lower it.
 */
function reportSessionCapSkip(emitStatus: L2Emitter): void {
  emitStatus("L2.SessionRecordLimit", "SKIP", "SKIP session_record_limit: needs admin");
}
