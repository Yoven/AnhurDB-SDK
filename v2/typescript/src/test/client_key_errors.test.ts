/**
 * How ClientAPI reports refusals, construction errors and concurrent use.
 * Error bodies are the server's real bytes: writeErrorCode
 * (server/handler/response.go), ClientKeyOnly's http.Error
 * (server/middleware/auth.go) and handleDispatchError's not_leader envelope.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { ClientAPI } from "../clientKeyApi.js";
import { AnhurClientAPIError } from "../clientKeyErrors.js";
import { AnhurError } from "../errors.js";
import { withFake } from "./clientKeyFake.js";

const CHILD_ID = "9b1d2c3e-4f5a-4b6c-8d7e-0f1a2b3c4d5e";
const IMPERSONATE = `POST /api/v1/client/children/${CHILD_ID}/impersonate`;

describe("ClientAPI errors", () => {
  const cases: Array<{ name: string; status: number; contentType: string; body: string; code: string;
    retryable: boolean; notLeader: boolean; message?: string }> = [
    { name: "child suspended", status: 403, contentType: "application/json",
      body: '{"error":"child_suspended","detail":"child client is suspended"}', code: "child_suspended",
      retryable: false, notLeader: false,
      message: `Client API error (HTTP 403) on ${IMPERSONATE}: child_suspended: child client is suspended` },
    { name: "child not found", status: 404, contentType: "application/json",
      body: '{"error":"child_not_found","detail":"x"}', code: "child_not_found", retryable: false, notLeader: false },
    { name: "child limit is a hard ceiling", status: 429, contentType: "application/json",
      body: '{"error":"child_limit","detail":"x"}', code: "child_limit", retryable: false, notLeader: false },
    { name: "frozen client key sentence", status: 403, contentType: "text/plain",
      body: '{"error":"client API key required"}\n', code: "client API key required", retryable: false, notLeader: false },
    { name: "not leader", status: 503, contentType: "application/json",
      body: '{"error":"not the leader (node-2)","error_type":"not_leader","detail":"retry"}',
      code: "not the leader (node-2)", retryable: true, notLeader: true },
    { name: "non json body", status: 502, contentType: "text/html", body: "<html>bad gateway</html>", code: "",
      retryable: true, notLeader: false, message: `Client API error (HTTP 502) on ${IMPERSONATE}: <html>bad gateway</html>` },
  ];
  for (const testCase of cases) {
    it(`exposes the server code: ${testCase.name}`, () =>
      withFake(async (fake) => {
        fake.staticAnswer = { status: testCase.status, contentType: testCase.contentType, body: testCase.body };
        const api = new ClientAPI({ clientKey: "client_key_secret", url: fake.baseUrl });
        await assert.rejects(api.impersonateChild(CHILD_ID, { tenantId: "geral", scope: "write" }), (error: unknown) => {
          assert.ok(error instanceof AnhurClientAPIError);
          assert.ok(error instanceof AnhurError);
          assert.equal(error.code, testCase.code);
          assert.equal(error.statusCode, testCase.status);
          assert.equal(error.retryable, testCase.retryable);
          assert.equal(error.isNotLeader, testCase.notLeader);
          if (testCase.message) assert.equal(error.message, testCase.message);
          return true;
        });
      }));
  }

  it("refuses an empty or header-unsafe client key at construction", () => {
    assert.throws(() => new ClientAPI({ clientKey: "" }));
    assert.throws(() => new ClientAPI({ clientKey: "bad\r\nkey" }));
  });

  it("is safe for concurrent use", () =>
    withFake(async (fake) => {
      const api = new ClientAPI({ clientKey: "client_key_secret", url: fake.baseUrl });
      const tokens = await Promise.all(Array.from({ length: 16 }, (_, index) =>
        api.impersonateChild(CHILD_ID, { tenantId: `tenant-${index}`, scope: "read_only" })));
      assert.equal(tokens.length, 16);
      assert.equal(fake.requests.length, 16);
    }));
});
