/**
 * The HTTP status must survive the trip from the wire to the typed error.
 *
 * WHY THIS FILE EXISTS, and why it stubs `fetch` instead of stubbing an SDK
 * method: `upload_wait.test.ts` "covered" the read-your-writes grace by
 * CONSTRUCTING `new AnhurQueryError("...404...", 404)` by hand. That fabricates
 * the very thing under test — the presence of the status — so the suite stayed
 * green for as long as the real `request()` path was dropping it. Every test
 * here drives a genuine `Memory` method against a stubbed `globalThis.fetch`,
 * so the assertion runs against whatever the transport actually built.
 *
 * The class of bug: an `AnhurError` built without its `statusCode` resolves
 * `kind` via `kindForStatus(undefined)` → `"transport"` → `retryable: true`.
 * A 404 then announces itself as a retryable network glitch (the opposite of
 * `not_found`), and every caller that branches on `statusCode === 404` — the
 * upload grace window, the OSS-endpoint probes — takes the wrong branch in
 * silence.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurError, AnhurQueryError } from "../types.js";

/** One canned reply for the stubbed `fetch`. */
interface StubbedReply {
  status: number;
  body: string;
  contentType?: string;
}

/**
 * Run `exercise` with `globalThis.fetch` answering each call from `replies`
 * (the last entry repeats), and always restore the real fetch.
 */
async function withStubbedFetch<T>(
  replies: StubbedReply[],
  exercise: (memory: Memory) => Promise<T>,
): Promise<{
  value?: T;
  thrown?: unknown;
  /** Every URL the SDK actually asked for, in order. */
  requestUrls: string[];
}> {
  const originalFetch = globalThis.fetch;
  const requestUrls: string[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const reply = replies[Math.min(requestUrls.length, replies.length - 1)];
    requestUrls.push(String(input));
    return new Response(reply.body, {
      status: reply.status,
      headers: { "Content-Type": reply.contentType ?? "application/json" },
    });
  }) as typeof fetch;
  try {
    const memory = new Memory({
      apiKey: "test-key-000",
      userId: "u",
      url: "http://localhost:8000",
    });
    const value = await exercise(memory);
    return { value, requestUrls };
  } catch (thrown: unknown) {
    return { thrown, requestUrls };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

describe("typed errors carry the real HTTP status (JSON path)", () => {
  it("404 from a GET becomes not_found, NOT a retryable transport error", async () => {
    const { thrown } = await withStubbedFetch(
      [{ status: 404, body: JSON.stringify({ error: "record not found" }) }],
      (memory) => memory.uploadStatus(9),
    );
    assert.ok(thrown instanceof AnhurQueryError, "expected AnhurQueryError");
    // The three assertions that the status-less constructor failed:
    assert.equal((thrown as AnhurQueryError).statusCode, 404);
    assert.equal((thrown as AnhurQueryError).kind, "not_found");
    assert.equal((thrown as AnhurQueryError).retryable, false);
  });

  it("500 from a GET carries its status and is classified as server", async () => {
    const { thrown } = await withStubbedFetch(
      [{ status: 500, body: "boom" }],
      (memory) => memory.uploadStatus(9),
    );
    assert.ok(thrown instanceof AnhurError);
    assert.equal((thrown as AnhurError).statusCode, 500);
    assert.equal((thrown as AnhurError).kind, "server");
  });
});

describe("typed errors carry the real HTTP status (multipart path)", () => {
  it("404 from an upload matches the JSON path exactly", async () => {
    const { thrown } = await withStubbedFetch(
      [{ status: 404, body: "no such route" }],
      (memory) =>
        memory.uploadFile("a.txt", "hello", { mode: "tenant_shared" }),
    );
    assert.ok(thrown instanceof AnhurQueryError);
    assert.equal((thrown as AnhurQueryError).statusCode, 404);
    assert.equal((thrown as AnhurQueryError).kind, "not_found");
  });

  it("caps an oversized upload reply instead of returning it", async () => {
    // 100 MB + 1 char. The cap was enforced on the JSON path only; the
    // multipart path read the whole body straight into memory.
    const oversized = "x".repeat(100 * 1024 * 1024 + 1);
    const { thrown } = await withStubbedFetch(
      [{ status: 200, body: oversized, contentType: "text/plain" }],
      (memory) =>
        memory.uploadFile("a.txt", "hello", { mode: "tenant_shared" }),
    );
    assert.ok(thrown instanceof AnhurError, "expected the size cap to fire");
    assert.match(String((thrown as Error).message), /exceeds maximum size/);
  });
});

describe("404 detection branches on the status, never on the message", () => {
  it("profile() falls back to 'not available' on a REAL 404", async () => {
    const { value } = await withStubbedFetch(
      [{ status: 404, body: JSON.stringify({ error: "no such endpoint" }) }],
      (memory) => memory.profile(),
    );
    assert.equal(value?.status, "not_available");
  });

  it("profile() rethrows a 500 whose body merely mentions 404", async () => {
    // The exact false positive `err.message.includes("404")` produced: the SDK
    // reported an empty profile for a server that was actually broken.
    const { thrown, value } = await withStubbedFetch(
      [{ status: 500, body: "upstream proxy returned 404 while retrying" }],
      (memory) => memory.profile(),
    );
    assert.equal(value, undefined, "a 500 must not become an empty profile");
    assert.ok(thrown instanceof AnhurError);
    assert.equal((thrown as AnhurError).statusCode, 500);
  });

  it("add() does not disable the ingest path on a 500 mentioning 404", async () => {
    // `ingestAvailable` is sticky: ONE false positive reroutes EVERY later
    // write to the OSS `/records` fallback for the lifetime of the client.
    //
    // The proof cannot be "the call threw", because the fallback request hits
    // the same 500 and throws an identical-looking error — that is exactly how
    // this would slip through review. The proof is the ROUTE: with the message
    // sniffing, the first add falls through to `/api/v1/records` and the second
    // add never even attempts `/api/v1/ingest`. So we assert that every request
    // the SDK made went to the ingest endpoint.
    const { requestUrls } = await withStubbedFetch(
      [{ status: 500, body: "handler panic: record 404 missing" }],
      async (memory) => {
        for (const attempt of [1, 2]) {
          await assert.rejects(
            memory.add(`hello ${attempt}`, { sessionId: "session-under-test" }),
            (thrown: unknown) =>
              thrown instanceof AnhurError && thrown.statusCode === 500,
          );
        }
      },
    );
    assert.deepEqual(
      requestUrls.map((url) => new URL(url).pathname),
      ["/api/v1/ingest", "/api/v1/ingest"],
    );
  });
});
