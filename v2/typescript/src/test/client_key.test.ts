/**
 * ClientAPI (client-key surface, ADR-0034) against the node:http fake in
 * `clientKeyFake.ts`. Same cases as Go `client/client_key_test.go` and Python
 * `tests/test_client_key.py`.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { ClientAPI } from "../clientKeyApi.js";
import { AnhurClientAPIError, AnhurClientAPIResponseError } from "../clientKeyErrors.js";
import { AnhurQueryError } from "../errors.js";
import type { KeyScope } from "../clientKeyTypes.js";
import { withFake, type FakeClientServer } from "./clientKeyFake.js";

const CHILD_ID = "9b1d2c3e-4f5a-4b6c-8d7e-0f1a2b3c4d5e";

function apiFor(fake: FakeClientServer): ClientAPI {
  return new ClientAPI({ clientKey: "client_key_secret", url: fake.baseUrl });
}

describe("ClientAPI wire contract", () => {
  it("impersonateChild sends the exact body, X-API-Key and no X-Tenant-ID", () =>
    withFake(async (fake) => {
      const token = await apiFor(fake).impersonateChild(CHILD_ID, {
        tenantId: "geral", scope: "write", l1Only: true, ttlSeconds: 900,
      });
      const sent = fake.requests[0];
      assert.equal(sent.method, "POST");
      assert.equal(sent.path, `/api/v1/client/children/${CHILD_ID}/impersonate`);
      assert.equal(sent.apiKey, "client_key_secret");
      assert.equal(sent.hasTenantHeader, false);
      assert.deepEqual(sent.body, { tenant_id: "geral", scope: "write", l1_only: true, expires_in: 900, ttl_seconds: 900 });
      assert.equal(token.api_key, "anhur_secret");
      assert.equal(token.key_id, "ik_42");
      assert.equal(token.scope, "write");
      assert.equal(token.l1_only, true);
      assert.equal(token.client_id, CHILD_ID);
      assert.equal(token.expires_in, 900);
      assert.equal(token.expires_at.toISOString(), "2026-11-21T13:00:00.000Z");
    }));

  it("plain impersonate omits optional fields and returns the ceiling scope", () =>
    withFake(async (fake) => {
      const token = await apiFor(fake).impersonate({ tenantId: "it-process" });
      assert.equal(fake.requests[0].path, "/api/v1/client/impersonate");
      assert.equal(fake.requests[0].rawBody, '{"tenant_id":"it-process"}');
      assert.equal(token.scope, "admin");
      assert.equal(token.l1_only, false);
    }));

  const refusals: Array<[string, (api: ClientAPI) => Promise<unknown>]> = [
    ["empty external ref", (api) => api.createChild("  ")],
    ["non canonical child id", (api) => api.suspendChild("../tenants")],
    ["empty child id", (api) => api.listChildTenants("")],
    ["empty tenant", (api) => api.createTenant({ tenantId: " " })],
    ["wildcard tenant", (api) => api.impersonate({ tenantId: "*" })],
    ["unknown scope", (api) => api.impersonate({ tenantId: "geral", scope: "root" as KeyScope })],
    ["child without scope", (api) => api.impersonateChild(CHILD_ID, { tenantId: "geral" })],
    ["fractional ttl", (api) => api.impersonate({ tenantId: "geral", ttlSeconds: 1.5 })],
    ["max_rpm below one", (api) => api.createTenant({ tenantId: "geral", profile: { maxRpm: 0 } })],
    ["empty key id", (api) => api.revokeImpersonation("")],
  ];
  for (const [name, call] of refusals) {
    it(`refuses locally and sends nothing: ${name}`, () =>
      withFake(async (fake) => {
        await assert.rejects(call(apiFor(fake)), (error: unknown) => {
          assert.ok(error instanceof AnhurQueryError);
          assert.equal(error.statusCode, 400);
          assert.equal(error.retryable, false);
          assert.ok(error.message.startsWith("INVALID_PARAM: client API: "));
          return true;
        });
        assert.equal(fake.requests.length, 0);
      }));
  }

  const baseToken = { api_key: "anhur_secret", key_id: "ik_42", tenant_id: "geral", kind: "impersonation",
    expires_in: 900, expires_at: "2026-11-21T13:00:00Z" };
  const echoCases: Array<[string, KeyScope, boolean, Record<string, unknown>, string | null]> = [
    ["l1_only absent", "write", true, { scope: "write" }, "l1_only_not_honoured"],
    ["l1_only false", "write", true, { scope: "write", l1_only: false }, "l1_only_not_honoured"],
    ["scope higher", "read_only", false, { scope: "admin", l1_only: false }, "scope_not_honoured"],
    ["scope absent", "write", false, { l1_only: false }, "scope_not_honoured"],
    ["scope lower is accepted", "admin", false, { scope: "write", l1_only: false }, null],
    ["no api_key", "write", false, { scope: "write", api_key: "" }, "unexpected_response"],
  ];
  for (const [name, scope, l1Only, override, wantCode] of echoCases) {
    it(`fails closed on a weak echo: ${name}`, () =>
      withFake(async (fake) => {
        fake.impersonateOverride = () => ({ ...baseToken, ...override });
        const call = apiFor(fake).impersonateChild(CHILD_ID, { tenantId: "geral", scope, l1Only });
        if (wantCode === null) {
          assert.equal((await call).scope, "write");
          return;
        }
        await assert.rejects(call, (error: unknown) => {
          assert.ok(error instanceof AnhurClientAPIResponseError);
          assert.equal(error.code, wantCode);
          assert.equal(error.retryable, false);
          if (wantCode !== "unexpected_response") assert.equal(error.keyId, "ik_42");
          return true;
        });
      }));
  }

  it("createChild is idempotent per ref and listChildren reads the envelope", () =>
    withFake(async (fake) => {
      const api = apiFor(fake);
      const first = await api.createChild("3f2c9a1e-ref");
      const second = await api.createChild("3f2c9a1e-ref");
      assert.equal(first.created, true);
      assert.equal(second.created, false);
      assert.equal(first.child_client_id, second.child_client_id);
      assert.deepEqual(fake.requests[0].body, { external_ref: "3f2c9a1e-ref" });
      const children = await api.listChildren();
      assert.deepEqual(children.map((child) => child.child_client_id), [first.child_client_id]);
    }));

  it("createChildTenant sends the profile; {} and absent profile differ on the wire", () =>
    withFake(async (fake) => {
      const api = apiFor(fake);
      const tenant = await api.createChildTenant(CHILD_ID, {
        tenantId: "contabilidade", description: "agent", profile: { config: { search_top_k: "8" }, maxRpm: 5000 },
      });
      assert.deepEqual(fake.requests[0].body, { tenant_id: "contabilidade", description: "agent",
        profile: { config: { search_top_k: "8" }, max_rpm: 5000 } });
      assert.equal(tenant.max_rpm, 1200);
      assert.equal(tenant.client_id, CHILD_ID);
      assert.equal(tenant.composite_tenant, `${CHILD_ID}_contabilidade`);
      const emptyProfile = await api.createTenant({ tenantId: "geral", profile: {} });
      assert.equal(fake.requests[1].rawBody, '{"tenant_id":"geral","profile":{}}');
      assert.equal(emptyProfile.max_rpm, undefined);
      await api.createTenant({ tenantId: "geral" });
      assert.equal(fake.requests[2].rawBody, '{"tenant_id":"geral"}');
    }));

  it("suspend/resume hit their paths and echo the re-read status", () =>
    withFake(async (fake) => {
      const api = apiFor(fake);
      const child = await api.createChild("ref");
      const suspended = await api.suspendChild(child.child_client_id);
      const resumed = await api.resumeChild(child.child_client_id);
      assert.equal(suspended.status, "suspended");
      assert.equal(resumed.status, "active");
      assert.equal(fake.requests[1].path, `/api/v1/client/children/${child.child_client_id}/suspend`);
      assert.equal(fake.requests[2].path, `/api/v1/client/children/${child.child_client_id}/resume`);
      assert.deepEqual(await api.listChildTenants(child.child_client_id), ["contabilidade", "geral"]);
    }));

  it("plain list and revoke wire; unknown key exposes key_not_found", () =>
    withFake(async (fake) => {
      const api = apiFor(fake);
      assert.deepEqual(await api.listTenants(), ["contabilidade", "geral"]);
      assert.equal(fake.requests[0].method, "GET");
      assert.equal(fake.requests[0].path, "/api/v1/client/tenants");
      const revoked = await api.revokeImpersonation("ik_42");
      assert.deepEqual(fake.requests[1].body, { key_id: "ik_42" });
      assert.deepEqual(revoked, { revoked: true, key_id: "ik_42" });
      await assert.rejects(api.revokeImpersonation("ik_missing"), (error: unknown) => {
        assert.ok(error instanceof AnhurClientAPIError);
        assert.equal(error.code, "key_not_found");
        assert.equal(error.statusCode, 404);
        assert.equal(error.kind, "not_found");
        return true;
      });
    }));
});
