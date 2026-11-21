/**
 * Every 401/403 on a data route must arrive as `AnhurAuthRefusedError` through
 * the REAL transport (a genuine `Memory` call against a stubbed
 * `globalThis.fetch`), carrying the server's code, and stay
 * `instanceof AnhurAuthError`. Bodies are the ones AnhurDB sends
 * (`server/middleware/auth*.go`, `handler/service_bridge.go`), including the
 * `text/plain` JSON body of `http.Error`. JSON and multipart paths are both
 * covered. gRPC is not: this SDK has no gRPC transport.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurAuthError, AnhurAuthRefusedError, AnhurClientAPIError } from "../index.js";
import { decodeErrorEnvelope } from "../authErrors.js";

type Predicate = "isL1Only" | "isChildSuspended" | "isInsufficientScope" | "isImpersonationRequired" | "isInvalidKey";
const PREDICATES: readonly Predicate[] = [
  "isL1Only",
  "isChildSuspended",
  "isInsufficientScope",
  "isImpersonationRequired",
  "isInvalidKey",
];

interface AuthCase {
  name: string;
  status: number;
  contentType: string;
  body: string;
  code: string;
  detail: string;
  message: string;
  predicate: Predicate | null;
}

const CASES: readonly AuthCase[] = [
  { name: "l1_only (writeErrorCode, application/json)", status: 403, contentType: "application/json",
    body: '{"error":"l1_only","detail":"token restricted to L1 sessions"}',
    code: "l1_only", detail: "token restricted to L1 sessions",
    message: "Authentication failed (HTTP 403): l1_only: token restricted to L1 sessions", predicate: "isL1Only" },
  { name: "child_suspended (http.Error, text/plain JSON body)", status: 403, contentType: "text/plain; charset=utf-8",
    body: '{"error":"child_suspended","detail":"client is suspended"}\n',
    code: "child_suspended", detail: "client is suspended",
    message: "Authentication failed (HTTP 403): child_suspended: client is suspended", predicate: "isChildSuspended" },
  { name: "insufficient scope sentence", status: 403, contentType: "text/plain; charset=utf-8",
    body: '{"error":"insufficient scope — key does not have permission for this operation"}\n',
    code: "insufficient scope — key does not have permission for this operation", detail: "",
    message: "Authentication failed (HTTP 403): insufficient scope — key does not have permission for this operation",
    predicate: "isInsufficientScope" },
  { name: "client key on a data route", status: 403, contentType: "text/plain; charset=utf-8",
    body: '{"error":"client_key_requires_impersonation"}\n',
    code: "client_key_requires_impersonation", detail: "",
    message: "Authentication failed (HTTP 403): client_key_requires_impersonation", predicate: "isImpersonationRequired" },
  { name: "invalid key 401", status: 401, contentType: "text/plain; charset=utf-8",
    body: '{"error":"invalid or inactive API key"}\n',
    code: "invalid or inactive API key", detail: "",
    message: "Authentication failed (HTTP 401): invalid or inactive API key", predicate: "isInvalidKey" },
  { name: "non-JSON body keeps today's message", status: 403, contentType: "text/plain",
    body: "Forbidden", code: "", detail: "", message: "Authentication failed (HTTP 403)", predicate: null },
  { name: "JSON without a string error keeps today's message", status: 401, contentType: "application/json",
    body: '{"error":5}', code: "", detail: "", message: "Authentication failed (HTTP 401)", predicate: null },
];

/** Run `exercise` with every fetch answering `authCase`; return what it threw. */
async function thrownBy(authCase: AuthCase, exercise: (memory: Memory) => Promise<unknown>): Promise<unknown> {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async () =>
    new Response(authCase.body, {
      status: authCase.status,
      headers: { "Content-Type": authCase.contentType },
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

function assertCase(thrown: unknown, authCase: AuthCase): void {
  assert.ok(thrown instanceof AnhurAuthRefusedError, `expected AnhurAuthRefusedError, got ${String(thrown)}`);
  assert.ok(thrown instanceof AnhurAuthError, "must stay an AnhurAuthError for existing catch blocks");
  assert.equal(thrown.statusCode, authCase.status);
  assert.equal(thrown.code, authCase.code);
  assert.equal(thrown.detail, authCase.detail);
  assert.equal(thrown.message, authCase.message);
  assert.equal(thrown.kind, "auth");
  assert.equal(thrown.retryable, false);
  const truePredicates = PREDICATES.filter((predicate) => thrown[predicate]);
  assert.deepEqual(truePredicates, authCase.predicate ? [authCase.predicate] : []);
}

describe("401/403 on data routes keep the server's reason", () => {
  for (const authCase of CASES) {
    it(`JSON path: ${authCase.name}`, async () => {
      assertCase(await thrownBy(authCase, (memory) => memory.create("s1", "turn text")), authCase);
    });
    it(`multipart path: ${authCase.name}`, async () => {
      assertCase(
        await thrownBy(authCase, (memory) => memory.uploadFile("a.txt", "hello", { sessionId: "s1", linkedEpisodicId: 7 })),
        authCase,
      );
    });
  }

  it("a mistyped optional field never costs the code (shared with the client-key surface)", () => {
    const envelopeCases: ReadonlyArray<[string, [string, string] | null]> = [
      ['{"error":"l1_only","detail":5}', ["l1_only", ""]],
      ['{"error":"x","detail":"d","error_type":{}}', ["x", "d"]],
      ['{"error":""}', null],
      ['["l1_only"]', null],
      ["null", null],
      ["", null],
    ];
    for (const [body, expected] of envelopeCases) {
      const envelope = decodeErrorEnvelope(body);
      assert.deepEqual(envelope === null ? null : [envelope.code, envelope.detail], expected, body);
      const clientError = AnhurClientAPIError.fromBody(403, "POST", "/api/v1/client/impersonate", body);
      assert.equal(clientError.code, expected ? expected[0] : "", body);
    }
  });
});
