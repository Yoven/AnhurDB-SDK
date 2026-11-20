/**
 * The session-record-limit refusal (ADR-0033) must arrive TYPED through the
 * real transport: a genuine `Memory` call against a stubbed `globalThis.fetch`,
 * for the new structured body, the old text-only body, and the multipart path.
 * A generic 409 must stay a plain `AnhurError`. gRPC is not covered because
 * this SDK has no gRPC transport.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurError, AnhurSessionRecordLimitError } from "../index.js";
import { sessionRecordLimitFromResponse } from "../sessionLimit.js";

const NEW_BODY = JSON.stringify({
  error: "session has reached the maximum of 500 records (refused)",
  code: "session_record_limit",
  limit: 500,
  count: 503,
  session_uuid: "s-full-1",
});
const OLD_BODY = JSON.stringify({ error: "session has reached the maximum of 500 records" });

/** Run `exercise` with every fetch answering 409 + `body`; return what it threw. */
async function thrownBy(
  body: string,
  exercise: (memory: Memory) => Promise<unknown>,
): Promise<unknown> {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async () =>
    new Response(body, {
      status: 409,
      headers: { "Content-Type": "application/json" },
    })) as typeof fetch;
  try {
    const memory = new Memory({ apiKey: "test-key-000", userId: "u", url: "http://localhost:8000" });
    await exercise(memory);
    return undefined;
  } catch (thrown: unknown) {
    return thrown;
  } finally {
    globalThis.fetch = originalFetch;
  }
}

function assertNonRetryableConflict(thrown: unknown): AnhurSessionRecordLimitError {
  assert.ok(thrown instanceof AnhurSessionRecordLimitError, `expected typed refusal, got ${String(thrown)}`);
  assert.ok(thrown instanceof AnhurError, "must stay an AnhurError for existing catch blocks");
  assert.equal(thrown.statusCode, 409);
  assert.equal(thrown.kind, "conflict");
  assert.equal(thrown.retryable, false);
  assert.equal(thrown.code, "session_record_limit");
  return thrown;
}

describe("session record limit (ADR-0033) is a typed, non-retryable refusal", () => {
  it("new structured body carries session, limit and count", async () => {
    const refusal = assertNonRetryableConflict(
      await thrownBy(NEW_BODY, (memory) => memory.create("s-full-1", "turn text")),
    );
    assert.equal(refusal.sessionUuid, "s-full-1");
    assert.equal(refusal.limit, 500);
    assert.equal(refusal.count, 503);
    assert.match(refusal.message, /session=s-full-1, limit=500, count=503/);
  });

  it("old text-only body is still recognised; missing fields are null", async () => {
    const refusal = assertNonRetryableConflict(
      await thrownBy(OLD_BODY, (memory) => memory.create("s1", "turn text")),
    );
    assert.equal(refusal.sessionUuid, null);
    assert.equal(refusal.limit, 500); // read back from the server's own sentence
    assert.equal(refusal.count, null);
    assert.match(refusal.message, /session=unknown, limit=500, count=unknown/);
  });

  it("multipart upload path is typed too", async () => {
    const refusal = assertNonRetryableConflict(
      await thrownBy(NEW_BODY, (memory) => memory.uploadFile("a.txt", "hello", { sessionId: "s-full-1", linkedEpisodicId: 7 })),
    );
    assert.equal(refusal.count, 503);
  });

  it("a generic 409 stays a plain AnhurError", async () => {
    const thrown = await thrownBy(JSON.stringify({ error: "record already superseded" }), (memory) =>
      memory.create("s1", "turn text"),
    );
    assert.ok(thrown instanceof AnhurError);
    assert.ok(!(thrown instanceof AnhurSessionRecordLimitError));
    assert.equal((thrown as AnhurError).retryable, false);
  });

  it("JSON cut short by the excerpt cap is still recognised from the raw text", () => {
    const truncated = NEW_BODY.slice(0, NEW_BODY.indexOf('"session_uuid"') + 20);
    const refusal = sessionRecordLimitFromResponse(409, truncated);
    assert.ok(refusal instanceof AnhurSessionRecordLimitError);
    assert.equal(refusal.limit, 500);
    assert.equal(refusal.count, 503);
  });

  it("the text on a non-409 status is not a session limit", () => {
    assert.equal(sessionRecordLimitFromResponse(500, OLD_BODY), null);
  });
});
