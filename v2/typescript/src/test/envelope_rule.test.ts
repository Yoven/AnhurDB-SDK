/**
 * The 4.0.0 envelope rule (`src/envelope.ts`) on all eight listing routes, and
 * the cross-SDK session-limit message.
 *
 * Twins: Go `client/envelope_only_test.go` + `session_limit_test.go`, Python
 * `tests/test_envelope_rule.py`. A bare array, an empty body, a missing key or
 * a non-array value throws `AnhurUnexpectedResponseError` (kind "server") with
 * the byte-identical message; a present key whose value is null is empty.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurUnexpectedResponseError } from "../index.js";
import { sessionRecordLimitFromResponse } from "../sessionLimit.js";

const BAD_BODIES: Record<string, string> = {
  "bare array": '[{"id":7,"name":"n"}]',
  "empty body": "",
  "json null": "null",
  "missing key": '{"count":0}',
  "non-list key": '{"records":{},"sessions":{},"entities":{}}',
};
const NULL_BODY = '{"records":null,"sessions":null,"entities":null,"count":0,"has_more":false}';

/** [route template, key, call] — the same eight routes Go and Python cover. */
const ROUTES: Array<[string, string, (memory: Memory) => Promise<unknown>]> = [
  ["/api/v1/sessions/stats", "sessions", (memory) => memory.listSessions()],
  ["/api/v1/recent", "records", (memory) => memory.recent(10)],
  ["/api/v1/manifest", "records", (memory) => memory.manifestGlobal()],
  ["/api/v1/chats/{uuid}/manifest", "records", (memory) => memory.manifestSession("s1")],
  ["/api/v1/chats/{uuid}", "records", (memory) => memory.listChat("s1")],
  ["/api/v1/entities", "entities", (memory) => memory.searchEntities("q")],
  ["/api/v1/entities/list", "entities", (memory) => memory.listEntities()],
  ["/api/v1/records/{id}/entities", "entities", (memory) => memory.getRecordEntities(7)],
];

/** Run `exercise` with every fetch answered 200 + `body`. */
async function withFixedBody<T>(body: string, exercise: (memory: Memory) => Promise<T>): Promise<T> {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async () =>
    new Response(body, { status: 200, headers: { "Content-Type": "application/json" } })) as typeof fetch;
  try {
    return await exercise(new Memory({ apiKey: "test-key-000", userId: "u", url: "http://localhost:8000" }));
  } finally {
    globalThis.fetch = originalFetch;
  }
}

describe("envelope rule (4.0.0) on every listing route", () => {
  for (const [route, key, call] of ROUTES) {
    for (const [bodyName, body] of Object.entries(BAD_BODIES)) {
      it(`${route}: ${bodyName} throws AnhurUnexpectedResponseError`, async () => {
        await assert.rejects(withFixedBody(body, call), (thrown: unknown) => {
          assert.ok(thrown instanceof AnhurUnexpectedResponseError, String(thrown));
          assert.equal(
            thrown.message,
            `unexpected response from GET ${route}: expected a JSON object with "${key}"`,
          );
          assert.equal(thrown.kind, "server");
          assert.equal(thrown.statusCode, undefined);
          return true;
        });
      });
    }
    it(`${route}: a null list is empty, not an error`, async () => {
      await withFixedBody(NULL_BODY, call);
    });
  }
});

describe("session-limit message is byte-identical across SDKs", () => {
  it("layout with the 500-character server excerpt", () => {
    const sentence = "é".repeat(600);
    const refusal = sessionRecordLimitFromResponse(409, JSON.stringify({
      error: sentence, code: "session_record_limit", limit: 3, count: 4, session_uuid: "s9",
    }));
    assert.ok(refusal);
    assert.equal(
      refusal.message,
      `Conflict (HTTP 409): ${"é".repeat(500)} (session record limit: session=s9, limit=3, count=4)`,
    );
    assert.equal(refusal.serverMessage, sentence);
  });

  it("mistyped fields are not typed", () => {
    const base = { error: "x", code: "session_record_limit", limit: 5, count: 5, session_uuid: "s" };
    for (const variant of [
      { ...base, limit: null },
      { ...base, limit: "5" },
      { ...base, count: 5.5 },
      { ...base, session_uuid: 7 },
    ]) {
      assert.equal(sessionRecordLimitFromResponse(409, JSON.stringify(variant)), null, JSON.stringify(variant));
    }
  });
});
