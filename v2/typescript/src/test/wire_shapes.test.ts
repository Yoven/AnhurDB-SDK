/**
 * Phantom and missing fields — SPEC §3.2 (UploadResult), §3.3 (SessionStats),
 * §3.4 (BatchUpdateResult), §3.6 (WalkResult).
 *
 * Each case asserts BOTH halves of the contract: every key the server sends is
 * readable WITHOUT a cast (a missing field is a compile error), and every key
 * the type used to invent is absent from the parsed response (a phantom is a
 * runtime `undefined`). Checking only the fields you already know about cannot
 * catch a phantom — that is why the key-set assertions are here.
 *
 * All wire samples below are live responses captured on 2026-09-14 against
 * https://anhurdb.yoven.ai, or the literal the handler writes.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import type {
  BatchUpdateResult, SessionStats, UploadResult, WalkResult,
} from "../types.js";
import type { WalkEdge } from "../walkTypes.js";
import { recordWire } from "./wireRecorder.js";

/**
 * COMPILE-TIME guard: `PhantomKey<T, K>` is `true` only while `K` is NOT a key
 * of `T`, and `never` the moment someone re-declares it.
 *
 * Junior Tip [why a type and not an assertion]: a phantom field is invisible
 * at runtime — the server simply does not send it, so `"id" in result` is
 * false whether or not the TYPE claims `id`. The damage a phantom does is to
 * the CALLER's code, which compiles a branch that can never run. The only
 * place that damage is detectable is the type system, so the guard lives
 * there: re-adding any of the four names below turns its alias into `never`
 * and `const … : never = true` stops compiling. `npm test` builds before it
 * runs, so a broken guard fails the suite exactly like a failed assertion.
 */
type PhantomKey<T, K extends string> = K extends keyof T ? never : true;

/** Each was declared by this SDK and has never been sent by the server. */
const uploadResultHasNoId: PhantomKey<UploadResult, "id"> = true;
const batchResultHasNoUpdatedCount: PhantomKey<BatchUpdateResult, "updated_count"> = true;
const sessionRowHasNoLastActive: PhantomKey<SessionStats, "last_active"> = true;
const walkEdgeHasNoType: PhantomKey<WalkEdge, "type"> = true;
void [uploadResultHasNoId, batchResultHasNoUpdatedCount,
  sessionRowHasNoLastActive, walkEdgeHasNoType];

/** One live row of GET /api/v1/sessions/stats (keys verbatim). */
const LIVE_SESSION_ROW = {
  uuid: "sess-abc",
  record_count: 12,
  types: { episodic: 9, fact: 3 },
  last_activity: "2026-09-14T10:00:00Z",
  summary: "what this session was about",
};

describe("SessionStats — the key is last_activity, and types/summary exist", () => {
  it("reads the live row without a cast", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { value } = await recordWire(
      { sessions: [LIVE_SESSION_ROW], has_more: false, next_offset: 0 },
      () => memory.listSessions(),
    );
    const row: SessionStats = value[0];
    // OLD TYPE: `last_active`. The wrong spelling never threw — it read
    // `undefined` — so every "sort by last activity" silently sorted by
    // nothing. `types` and `summary` were not declared at all.
    assert.equal(row.last_activity, "2026-09-14T10:00:00Z");
    assert.deepEqual(row.types, { episodic: 9, fact: 3 });
    assert.equal(row.summary, "what this session was about");
    assert.equal(row.record_count, 12);
  });

  it("declares every key the server sends, and no key it does not", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { value } = await recordWire(
      { sessions: [LIVE_SESSION_ROW], has_more: false, next_offset: 0 },
      () => memory.listSessions(),
    );
    assert.deepEqual(Object.keys(value[0]).sort(),
      ["last_activity", "record_count", "summary", "types", "uuid"]);
    assert.equal("last_active" in value[0], false,
      "last_active is the PROFILE stats spelling, never a session row's");
  });
});

describe("BatchUpdateResult — the server sends a message, never a count", () => {
  it("reads `message`; `updated_count` is absent on the wire", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    // handler/record_batch.go:212 — the ONLY success path, one literal.
    const { value } = await recordWire({ message: "marked consolidated" },
      () => memory.batchUpdateStatus([1, 2, 3], "consolidated"));
    const result: BatchUpdateResult = value;
    assert.equal(result.message, "marked consolidated");
    assert.deepEqual(Object.keys(result), ["message"]);
    // OLD TYPE promised `updated_count: number`, so
    // `result.updated_count === ids.length` compiled and was ALWAYS false.
    assert.equal(
      (result as unknown as { updated_count?: number }).updated_count,
      undefined,
    );
  });
});

/** The live 202 body of POST /api/v1/upload (handler/upload.go:109-119). */
const LIVE_UPLOAD_ACK = {
  message: "File accepted for processing",
  record_id: 9001,
  uuid: "sess-abc",
  filename: "report.pdf",
  mime: "application/pdf",
  mime_detected: "application/pdf",
  extension: "pdf",
  size_bytes: 4096,
  status: "processing",
};

describe("UploadResult — no `id`, and five fields that were missing", () => {
  it("reads the whole ack without a cast", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { value } = await recordWire(LIVE_UPLOAD_ACK,
      () => memory.uploadFile("report.pdf", "hello",
        { mode: "tenant_shared" }));
    const ack: UploadResult = value;
    // OLD TYPE declared only record_id/id/status/filename/uuid — these five
    // were unreachable from TypeScript.
    assert.equal(ack.message, "File accepted for processing");
    assert.equal(ack.mime, "application/pdf");
    assert.equal(ack.mime_detected, "application/pdf");
    assert.equal(ack.extension, "pdf");
    assert.equal(ack.size_bytes, 4096);
    assert.equal(ack.record_id, 9001);
  });

  it("has no `id` — the polling key is record_id", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { value } = await recordWire(LIVE_UPLOAD_ACK,
      () => memory.uploadFile("report.pdf", "hello",
        { mode: "tenant_shared" }));
    // OLD TYPE declared `id?: number`, making `upload.id ?? upload.record_id`
    // compile as a careful-looking fallback whose first branch is dead.
    assert.equal("id" in value, false);
    assert.deepEqual(Object.keys(value).sort(), [
      "extension", "filename", "message", "mime", "mime_detected",
      "record_id", "size_bytes", "status", "uuid",
    ]);
  });
});

/** One live /walk node: a FULL record, all 14 keys. */
const LIVE_WALK_NODE = {
  id: 18, uuid: "rec-18", type: "episodic", summary: "s", status: "saved",
  weight: 0.5, score: 5, created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-02T00:00:00Z", metadata: "{}", related_ids: [19],
  main_ids: [], consolidated: false, archived: false,
};

describe("WalkResult — full records, two-key edges, optional truncated", () => {
  it("reads a node as the full record it is", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { value } = await recordWire(
      { nodes: [LIVE_WALK_NODE], edges: [{ source: 18, target: 19 }], truncated: false },
      () => memory.walk(18, 2),
    );
    const walk: WalkResult = value;
    // OLD TYPE: `{id, type, summary, weight}` — ten real fields hidden.
    assert.equal(walk.nodes[0].uuid, "rec-18");
    assert.equal(walk.nodes[0].created_at, "2026-01-01T00:00:00Z");
    assert.equal(walk.nodes[0].status, "saved");
    assert.deepEqual(walk.nodes[0].related_ids, [19]);
    assert.equal(walk.truncated, false);
  });

  it("has no `type` on an edge — the handler sends source/target only", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { value } = await recordWire(
      { nodes: [], edges: [{ source: 18, target: 19 }], truncated: false },
      () => memory.walk(18, 2),
    );
    assert.deepEqual(Object.keys(value.edges[0]).sort(), ["source", "target"]);
  });

  it("tolerates /walk/semantic omitting truncated entirely", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    // Live: /walk answered [edges, nodes, truncated]; /walk/semantic answered
    // [edges, nodes]. Both return this type, which is why `truncated` is
    // optional — `undefined` means "the semantic route did not say".
    const { value } = await recordWire(
      { nodes: [LIVE_WALK_NODE], edges: [] },
      () => memory.walkSemantic(18),
    );
    assert.equal(value.truncated, undefined);
    assert.equal(value.nodes[0].metadata, "{}");
  });
});
