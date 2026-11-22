/**
 * X-Tenant-ID is refused at construction with the SDK's validation error.
 *
 * Rule = the server's identifier rule (AnhurDB `database.validateIdentifierCharset`
 * + `MaxIdentifierLength`): letters, digits, `-` and `_`, at most 133. Messages are
 * pinned byte-for-byte with Go (`client/tenant_header_test.go`) and Python
 * (`tests/test_tenant_id_validation.py`).
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { buildRequestHeaders } from "../clientHeaders.js";
import { AnhurQueryError } from "../types.js";

const CHARSET_MESSAGE =
  "INVALID_PARAM: tenant_id contains an invalid character — allowed: letters, digits, '-' and '_'";

/** Assert `run` throws an invalid_request AnhurQueryError with `message`. */
function assertRefused(run: () => unknown, message: string): void {
  assert.throws(run, (error: unknown) => {
    assert.ok(error instanceof AnhurQueryError, `expected AnhurQueryError, got ${String(error)}`);
    assert.equal(error.message, message);
    assert.equal(error.kind, "invalid_request");
    assert.equal(error.retryable, false);
    return true;
  });
}

describe("tenantId validation", () => {
  for (const tenantId of ["1a99ce02-e9b6-442a-8c10-d67b9f3446b8_prod", "a".repeat(133), "1a99ce02-e9b6-442a-8c10-d67b9f3446b8_kaveo-veterinaria-vacinacao-4d7d5ed7_lib"]) {
    it(`accepts ${tenantId.slice(0, 20)}…`, () => {
      assert.equal(buildRequestHeaders("key", tenantId)["X-Tenant-ID"], tenantId);
    });
  }

  it("treats empty as not configured", () => {
    assert.equal(buildRequestHeaders("key", "")["X-Tenant-ID"], undefined);
  });

  for (const tenantId of ["acme/prod", "acme prod", "bad\r\nInjected: true", "bad\x00id", "açme"]) {
    it(`refuses ${JSON.stringify(tenantId)}`, () => {
      assertRefused(() => buildRequestHeaders("key", tenantId), CHARSET_MESSAGE);
    });
  }

  it("refuses an over-long tenant", () => {
    assertRefused(
      () => buildRequestHeaders("key", "a".repeat(134)),
      "INVALID_PARAM: tenant_id exceeds 133 characters (got 134)",
    );
  });

  it("refuses at Memory construction", () => {
    assertRefused(
      () => new Memory({ apiKey: "key", url: "http://localhost:8000", tenantId: "acme/prod" }),
      CHARSET_MESSAGE,
    );
  });
});
