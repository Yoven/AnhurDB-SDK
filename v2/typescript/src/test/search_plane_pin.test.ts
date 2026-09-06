/**
 * The plane wrappers answer from the plane their NAME promises.
 *
 * Junior Tip [why this file exists, 2026-09-06]: on this date the three SDKs
 * disagreed about one knob. Go PREPENDED its scope, so a caller-supplied
 * `WithScope` won and `SearchTenantShared` could answer from `client_shared`.
 * Python raised a bare `TypeError` with no `.kind` and no `.retryable`, outside
 * the error contract. TypeScript already spread `...options` first and pinned
 * after it, which is the behaviour all three now share. Reading the three
 * sources did not catch the divergence — each looked right on its own. Only the
 * WIRE BODY is an honest witness, so every wrapper is driven against a mocked
 * `fetch` here and the assertion is on the JSON that actually left the client.
 *
 * This file also guards the ORDER of the spread. `{ ...options, scope }` and
 * `{ scope, ...options }` are one character apart and mean opposite things.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { sessionsAll } from "../sessionFilter.js";
import type { SearchScope } from "../searchTypes.js";

/**
 * A plane that is never the right answer for the wrappers not named for it, so
 * a leak is unambiguous instead of accidentally equal to the expected value.
 */
const INTRUDER_SCOPE: SearchScope = "client_shared";

/** Each plane wrapper and the scope its NAME promises on the wire. */
const PLANE_WRAPPERS: ReadonlyArray<{
  readonly wrapperName: string;
  readonly expectedWireScope: string;
  readonly invoke: (memory: Memory, callerScope?: SearchScope) => Promise<unknown>;
}> = [
  {
    wrapperName: "searchSessions",
    expectedWireScope: "sessions",
    invoke: (memory, callerScope) =>
      memory.searchSessions("q", sessionsAll(), callerScope ? { scope: callerScope } : undefined),
  },
  {
    wrapperName: "searchTenantShared",
    expectedWireScope: "tenant_shared",
    invoke: (memory, callerScope) =>
      memory.searchTenantShared("q", sessionsAll(), callerScope ? { scope: callerScope } : undefined),
  },
  {
    wrapperName: "searchClientShared",
    expectedWireScope: "client_shared",
    invoke: (memory, callerScope) =>
      memory.searchClientShared("q", sessionsAll(), callerScope ? { scope: callerScope } : undefined),
  },
  {
    wrapperName: "searchShared",
    expectedWireScope: "shared_all",
    invoke: (memory, callerScope) =>
      memory.searchShared("q", sessionsAll(), callerScope ? { scope: callerScope } : undefined),
  },
];

/**
 * Installs a `fetch` that answers every search with an empty result set and
 * hands back the body it captured. Restores the real `fetch` afterwards so one
 * failing case cannot poison the rest of the suite.
 */
async function captureSearchBody(
  drive: () => Promise<unknown>,
): Promise<Record<string, unknown>> {
  const originalFetch = globalThis.fetch;
  let capturedBody: Record<string, unknown> = {};
  globalThis.fetch = (async (_url: string, init?: RequestInit) => {
    capturedBody = JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
    return new Response(JSON.stringify({ results: [], scope: capturedBody.scope }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }) as typeof fetch;
  try {
    await drive();
  } finally {
    globalThis.fetch = originalFetch;
  }
  return capturedBody;
}

describe("plane wrappers pin their own scope", () => {
  for (const planeCase of PLANE_WRAPPERS) {
    it(`${planeCase.wrapperName}() sends scope=${planeCase.expectedWireScope} with no caller scope`, async () => {
      const memory = new Memory({ apiKey: "key", userId: "u" });
      const body = await captureSearchBody(() => planeCase.invoke(memory));
      console.log(
        `WIRE typescript | ${planeCase.wrapperName.padEnd(20)} | caller_scope="" | wire_scope="${String(body.scope)}"`,
      );
      assert.equal(body.scope, planeCase.expectedWireScope);
    });

    it(`${planeCase.wrapperName}() OVERRIDES a caller-supplied scope instead of honouring or refusing it`, async () => {
      const memory = new Memory({ apiKey: "key", userId: "u" });
      const body = await captureSearchBody(() => planeCase.invoke(memory, INTRUDER_SCOPE));
      console.log(
        `WIRE typescript | ${planeCase.wrapperName.padEnd(20)} | caller_scope="${INTRUDER_SCOPE}" | wire_scope="${String(body.scope)}"`,
      );
      assert.equal(
        body.scope,
        planeCase.expectedWireScope,
        `${planeCase.wrapperName} must pin ${planeCase.expectedWireScope}; a caller-supplied scope is overridden, never honoured and never thrown`,
      );
    });
  }

  it("the pin does not eat the caller's other knobs", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const body = await captureSearchBody(() =>
      memory.searchTenantShared("q", sessionsAll(), { scope: INTRUDER_SCOPE, limit: 7 }),
    );
    console.log(`WIRE typescript | searchTenantShared + limit=7 | body=${JSON.stringify(body)}`);
    assert.equal(body.scope, "tenant_shared");
    assert.equal(body.limit, 7);
  });
});
