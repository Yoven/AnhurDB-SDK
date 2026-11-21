/**
 * SDK 4.0.0 legacy removal: each test proves one removed fallback or alias is
 * really gone — through the REAL transport (a genuine `Memory` against a
 * stubbed `globalThis.fetch`) where the removal is behaviour, and through the
 * compiler where it is a type.
 *
 * Server truth these removals rely on (AnhurDB main):
 *  - `POST /api/v1/ingest` mounted unconditionally: `server/router.go:135`.
 *  - `/api/v1/recent` always `{"records", "count"}`: `handler/record_session.go` ListRecent.
 *  - DeleteFileResult's log index is `json:"-"`: `service/record_delete_file.go:42`.
 * The session-limit, collision and sessions/stats removals are proven in
 * `session_record_limit.test.ts`, `record_scope.test.ts` and
 * `session_stats_paging.test.ts`, beside the cases they replaced.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurError, AnhurQueryError } from "../index.js";
import type { DeleteFileResult, MemoryStatus } from "../index.js";

/** One canned answer: HTTP status + raw body. */
interface StubAnswer {
  status: number;
  body: string;
}

/** Run `exercise` against a fetch that answers `answerFor(path)`; record paths. */
async function withStub<T>(
  answerFor: (pathname: string) => StubAnswer,
  exercise: (memory: Memory) => Promise<T>,
): Promise<{ value?: T; thrown?: unknown; paths: string[] }> {
  const originalFetch = globalThis.fetch;
  const paths: string[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const pathname = new URL(String(input)).pathname;
    paths.push(pathname);
    const answer = answerFor(pathname);
    return new Response(answer.body, {
      status: answer.status,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;
  try {
    const memory = new Memory({ apiKey: "test-key-000", userId: "u", url: "http://localhost:8000" });
    return { value: await exercise(memory), paths };
  } catch (thrown: unknown) {
    return { thrown, paths };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

describe("4.0.0: add() has no ingest-404 → /records fallback", () => {
  it("a 404 from /ingest is thrown as not_found and never writes via /records", async () => {
    const { thrown, paths } = await withStub(
      () => ({ status: 404, body: JSON.stringify({ error: "not found" }) }),
      async (memory) => {
        for (const attempt of [1, 2]) {
          await assert.rejects(
            memory.add(`hello ${attempt}`, { sessionId: "s1" }),
            (error: unknown) =>
              error instanceof AnhurQueryError &&
              error.statusCode === 404 &&
              error.kind === "not_found",
          );
        }
      },
    );
    assert.equal(thrown, undefined);
    // Both adds went to /ingest: no reroute, and no sticky "ingest unavailable".
    assert.deepEqual(paths, ["/api/v1/ingest", "/api/v1/ingest"]);
  });

  it("AddResult.mode names the door: ingest and records", async () => {
    const { value } = await withStub(
      (pathname) => ({
        status: 200,
        body: JSON.stringify(
          pathname === "/api/v1/ingest"
            ? { id: 1, records: [{ id: 1, type: "episodic", summary: "x" }] }
            : { id: 2 },
        ),
      }),
      async (memory) => [
        (await memory.add("plain", { sessionId: "s1" })).mode,
        (await memory.add("pinned", { sessionId: "s1", score: 9 })).mode,
        (await memory.create("s1", "direct")).mode,
        (await memory.createInSession("direct", "s1")).mode,
      ],
    );
    assert.deepEqual(value, ["ingest", "records", "records", "records"]);
  });
});

describe("4.0.0: recent() reads only the envelope", () => {
  it("unwraps records and coalesces records:null to []", async () => {
    const { value } = await withStub(
      () => ({ status: 200, body: JSON.stringify({ records: null, count: 0 }) }),
      (memory) => memory.recent(5),
    );
    assert.deepEqual(value, []);
  });

  it("a bare array is a contract violation, not records", async () => {
    const { value, thrown } = await withStub(
      () => ({ status: 200, body: JSON.stringify([{ id: 1 }]) }),
      (memory) => memory.recent(5),
    );
    assert.equal(value, undefined);
    assert.ok(thrown instanceof AnhurError, String(thrown));
    assert.equal((thrown as AnhurError).kind, "server");
  });
});

describe("4.0.0: removed aliases and fields", () => {
  it("Memory has no updateConsolidateIds alias (linkConsolidated is the one name)", () => {
    const memory = new Memory({ apiKey: "test-key-000", userId: "u" });
    assert.equal("updateConsolidateIds" in memory, false);
    assert.equal(typeof memory.linkConsolidated, "function");
  });

  it("DeleteFileResult has no raft_index and a required purge (compile-time)", () => {
    // Each constant only compiles while its claim is true; reintroducing the
    // field (or making purge optional again) breaks `npm run build:test`.
    const raftIndexDeclared: "raft_index" extends keyof DeleteFileResult ? true : false = false;
    const purgeOptional: undefined extends DeleteFileResult["purge"] ? true : false = false;
    assert.equal(raftIndexDeclared, false);
    assert.equal(purgeOptional, false);
  });

  it("MemoryStatus has no pre-v2 \"pending\" (compile-time)", () => {
    const pendingIsStatus: "pending" extends MemoryStatus ? true : false = false;
    assert.equal(pendingIsStatus, false);
    const judgeStatus: MemoryStatus = "pending_judge";
    assert.equal(judgeStatus, "pending_judge");
  });
});
