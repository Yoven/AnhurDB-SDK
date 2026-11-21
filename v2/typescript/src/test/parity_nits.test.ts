/**
 * Cross-SDK alignments of 2026-11-21 (audit rows 18-21) — TypeScript side.
 *
 * Go (`client/parity_nits_test.go`) and Python (`tests/test_parity_nits.py`)
 * pin the same answers. Every test drives a real `Memory` method against a
 * stubbed `globalThis.fetch`, so it asserts what reaches the wire:
 *   - `uploadFile` trims + lower-cases `mode` and refuses an unknown value with
 *     the typed invalid_request error, before any request leaves;
 *   - a 404 reads `Resource not found (HTTP 404): <path>`, kind `not_found`.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurQueryError } from "../types.js";

/** What the stub saw: the request path and the multipart `mode` field. */
interface CapturedRequest {
  calls: number;
  path?: string;
  mode?: string | null;
}

/** Run `exercise` with `fetch` answering `status`/`body`; restore it after. */
async function withStub(
  status: number,
  body: string,
  exercise: (memory: Memory) => Promise<unknown>,
): Promise<{ captured: CapturedRequest; value?: unknown; error?: unknown }> {
  const captured: CapturedRequest = { calls: 0 };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    captured.calls += 1;
    captured.path = new URL(String(input)).pathname;
    if (init?.body instanceof FormData) captured.mode = init.body.get("mode") as string | null;
    return new Response(body, { status, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  try {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const value = await exercise(memory);
    return { captured, value };
  } catch (error) {
    return { captured, error };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

const accepted = JSON.stringify({ record_id: 7, status: "pending" });

describe("uploadFile mode normalisation (parity with Go/Python)", () => {
  it("refuses an unknown mode with the typed invalid_request error, never sending", async () => {
    const { captured, error } = await withStub(202, accepted, (memory) =>
      memory.uploadFile("a.txt", "hi", { mode: "Library" as unknown as "chat" }));
    assert.equal(captured.calls, 0);
    assert.ok(error instanceof AnhurQueryError);
    assert.equal(error.statusCode, 400);
    assert.equal(error.kind, "invalid_request");
    assert.equal(error.retryable, false);
    assert.equal(
      error.message,
      "INVALID_PARAM: upload: mode 'Library' is not an upload mode (want chat|tenant_shared|client_shared)",
    );
  });

  it("trims and lower-cases a known mode before sending it", async () => {
    const { captured, value, error } = await withStub(202, accepted, (memory) =>
      memory.uploadFile("a.txt", "hi", { mode: " CLIENT_SHARED " as unknown as "client_shared" }));
    assert.equal(error, undefined);
    assert.equal(captured.mode, "client_shared");
    assert.equal((value as { scope: string }).scope, "client_shared");
  });

  it("treats 'Chat' as chat (session and episodic still required)", async () => {
    const { captured, error } = await withStub(202, accepted, (memory) =>
      memory.uploadFile("a.txt", "hi", { mode: "Chat" as unknown as "chat" }));
    assert.equal(captured.calls, 0);
    assert.ok(error instanceof Error);
    assert.match(error.message, /session_id is required/);
  });
});

describe("404 message (parity with Go/Python)", () => {
  it("quotes the request path", async () => {
    const { captured, error } = await withStub(404, "Not Found", (memory) => memory.get(42));
    assert.ok(error instanceof AnhurQueryError);
    assert.equal(error.statusCode, 404);
    assert.equal(error.kind, "not_found");
    assert.equal(error.message, `Resource not found (HTTP 404): ${captured.path}`);
  });
});
