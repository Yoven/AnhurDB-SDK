/**
 * The session-record-limit refusal (ADR-0033) must arrive TYPED through the
 * real transport: a genuine `Memory` call against a stubbed `globalThis.fetch`,
 * for the structured body and the multipart path. Since 4.0.0 the refusal is
 * recognised by its `code` ONLY: the sentence without the code, or the code
 * with a field missing, stays a plain 409 `AnhurError`. gRPC is not covered
 * because this SDK has no gRPC transport.
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
/** The pre-code server body. Since 4.0.0 it is NOT the typed refusal. */
const SENTENCE_ONLY_BODY = JSON.stringify({ error: "session has reached the maximum of 500 records" });

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
  it("structured body carries session, limit and count", async () => {
    const refusal = assertNonRetryableConflict(
      await thrownBy(NEW_BODY, (memory) => memory.create("s-full-1", "turn text")),
    );
    assert.equal(refusal.sessionUuid, "s-full-1");
    assert.equal(refusal.limit, 500);
    assert.equal(refusal.count, 503);
    assert.equal(
      refusal.message,
      "Conflict (HTTP 409): session has reached the maximum of 500 records (refused) " +
        "(session record limit: session=s-full-1, limit=500, count=503)",
    );
  });

  it("4.0.0: the sentence without the code is a plain conflict, not the typed refusal", async () => {
    const thrown = await thrownBy(SENTENCE_ONLY_BODY, (memory) => memory.create("s1", "turn text"));
    assert.ok(thrown instanceof AnhurError, String(thrown));
    assert.ok(!(thrown instanceof AnhurSessionRecordLimitError));
    assert.equal((thrown as AnhurError).statusCode, 409);
    assert.equal((thrown as AnhurError).kind, "conflict");
  });

  it("4.0.0: the code with a missing field is a plain conflict (no invented values)", () => {
    for (const missing of ["limit", "count", "session_uuid"]) {
      const fields: Record<string, unknown> = JSON.parse(NEW_BODY);
      delete fields[missing];
      assert.equal(
        sessionRecordLimitFromResponse(409, JSON.stringify(fields)),
        null,
        `body without ${missing} must not be typed`,
      );
    }
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

  it("4.0.0: every field but no code is a plain conflict (the sentence is not the signal)", () => {
    const fields: Record<string, unknown> = JSON.parse(NEW_BODY);
    delete fields.code;
    assert.equal(sessionRecordLimitFromResponse(409, JSON.stringify(fields)), null);
  });

  it("4.0.0: a body that is not JSON is never read by regex", () => {
    const truncated = NEW_BODY.slice(0, NEW_BODY.indexOf('"session_uuid"') + 20);
    assert.equal(sessionRecordLimitFromResponse(409, truncated), null);
  });

  it("the structured body on a non-409 status is not a session limit", () => {
    assert.equal(sessionRecordLimitFromResponse(500, NEW_BODY), null);
  });
});
