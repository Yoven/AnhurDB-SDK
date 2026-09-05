/**
 * The cross-SDK divergences closed on 2026-09-05.
 *
 * Domain: proof that this SDK behaves like the Go and Python SDKs on the
 * ADR-0031 knobs. Each block below corresponds to one divergence an independent
 * probe measured by capturing the ACTUAL request body from every entry point in
 * every SDK.
 *
 * Junior Tip [why the strings are asserted with EXACT equality]: the previous
 * round pinned the INVALID_PARAM strings and asserted SERVER_TOO_OLD by
 * substring. `match(/^SERVER_TOO_OLD/)` is true for any wording at all, which is
 * exactly how the three SDKs drifted into three different sentences for the same
 * fact. A cross-SDK contract has to be compared the way a contract is compared.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurError } from "../types.js";
import { resetSearchKnobWarnings } from "../searchMode.js";
import type { SearchMode } from "../searchTypes.js";

interface CapturedSearch {
  url: URL;
  body: Record<string, unknown>;
}

/**
 * Run `invokeSdk` with `fetch` intercepted, answering with `responseBody`.
 * Returns the captured request plus whatever the SDK returned or threw.
 */
async function captureSearch<ReturnValue>(
  responseBody: unknown,
  invokeSdk: (memory: Memory) => Promise<ReturnValue>,
): Promise<{ captured: CapturedSearch | null; value?: ReturnValue; error?: unknown }> {
  const originalFetch = globalThis.fetch;
  let captured: CapturedSearch | null = null;
  globalThis.fetch = (async (requestUrl: unknown, init: RequestInit) => {
    captured = {
      url: new URL(String(requestUrl)),
      body: JSON.parse(String(init.body ?? "{}")) as Record<string, unknown>,
    };
    return new Response(JSON.stringify(responseBody), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }) as unknown as typeof fetch;

  try {
    const memory = new Memory({ apiKey: "key", userId: "u", url: "http://localhost:8000" });
    const value = await invokeSdk(memory);
    return { captured, value };
  } catch (error: unknown) {
    return { captured, error };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

/** Collect every console.warn line written while `run` executes. */
async function captureWarnings(run: () => Promise<void>): Promise<string[]> {
  resetSearchKnobWarnings();
  const lines: string[] = [];
  const originalWarn = console.warn;
  console.warn = (...args: unknown[]) => {
    lines.push(args.join(" "));
  };
  try {
    await run();
  } finally {
    console.warn = originalWarn;
  }
  return lines;
}

// ── L4: mode case and whitespace ────────────────────────────────────────────

describe("mode is trimmed and lowercased before it is validated", () => {
  const cases: Array<{ name: string; requested: string; wire: SearchMode }> = [
    { name: "uppercase", requested: "SEMANTIC", wire: "semantic" },
    { name: "surrounding whitespace", requested: " semantic ", wire: "semantic" },
    { name: "title case", requested: "Balanced", wire: "balanced" },
  ];

  for (const testCase of cases) {
    it(`accepts ${testCase.name} and puts the normalised form on the wire`, async () => {
      // The server echoes the NORMALISED mode, which is what a real server does.
      // That also proves the read-back comparison uses the normalised form and
      // does not throw SERVER_TOO_OLD at a perfectly healthy server.
      const { captured, error } = await captureSearch(
        { results: [], retrieval: { mode: testCase.wire } },
        (memory) =>
          memory.search("q", ["chat-1"], { mode: testCase.requested as SearchMode }),
      );
      assert.equal(error, undefined, `mode "${testCase.requested}" must be accepted`);
      assert.ok(captured);
      assert.equal(captured!.body.mode, testCase.wire);
    });
  }

  it("still echoes what the caller typed when the mode is a typo", async () => {
    const { captured, error } = await captureSearch({ results: [] }, (memory) =>
      memory.search("q", ["chat-1"], { mode: " SEMANITC " as SearchMode }),
    );
    assert.equal(captured, null, "a typo must never reach the server");
    assert.ok(error instanceof AnhurError);
    assert.equal(
      (error as AnhurError).message,
      'INVALID_PARAM: \'mode\' " SEMANITC " is not supported; use "fast", "balanced" or "semantic"',
    );
  });
});

// ── L2: the shared_all warning is a pinned cross-SDK string ─────────────────

describe("the shared_all warning is byte-identical across the three SDKs", () => {
  it("warns, does not throw, and says exactly what Go and Python say", async () => {
    let sdkError: unknown;
    const warnings = await captureWarnings(async () => {
      const { error } = await captureSearch({ results: [] }, (memory) =>
        memory.search("q", ["chat-1"], { mode: "semantic", scope: "shared_all" }),
      );
      sdkError = error;
    });
    assert.equal(sdkError, undefined, "shared_all must warn, never fail");
    assert.deepEqual(warnings, [
      'anhurdb-sdk: warning: mode="semantic" cannot be CONFIRMED on scope="shared_all" — the ' +
        "server never echoes retrieval.mode for a two-leg merge, so a server too old for ADR-0031 " +
        "looks identical to a current one here. Use a single scope to get the strict-semantic " +
        "guarantee verified.",
    ]);
  });

  it("says exactly what Go and Python say when an old server ignored fast/balanced", async () => {
    const warnings = await captureWarnings(async () => {
      await captureSearch({ results: [] }, (memory) =>
        memory.search("q", ["chat-1"], { mode: "fast" }),
      );
    });
    assert.deepEqual(warnings, [
      'anhurdb-sdk: warning: this AnhurDB server ignored mode="fast" (it predates ADR-0031) and ' +
        "ran its own balanced pipeline; the results are balanced results.",
    ]);
  });
});

// ── L1 (parity direction): searchSession is on the same path as search ──────

describe("searchSession is the same request as search", () => {
  it("forwards all three ADR-0031 knobs", async () => {
    const { captured, error } = await captureSearch(
      { results: [], retrieval: { mode: "semantic" } },
      (memory) =>
        memory.searchSession("q", "sess-1", {
          mode: "semantic",
          semanticTimeoutMs: 1500,
          debugSignals: true,
        }),
    );
    assert.equal(error, undefined);
    assert.ok(captured);
    assert.equal(captured!.body.mode, "semantic");
    assert.equal(captured!.body.semantic_timeout_ms, 1500);
    assert.equal(captured!.body.debug_signals, true);
    assert.equal(captured!.body.scope, "sessions");
  });

  it("refuses a negative budget before the round trip", async () => {
    const { captured, error } = await captureSearch({ results: [] }, (memory) =>
      memory.searchSession("q", "sess-1", { semanticTimeoutMs: -1 }),
    );
    assert.equal(captured, null, "a negative budget must never reach the server");
    assert.ok(error instanceof AnhurError);
    assert.equal(
      (error as AnhurError).message,
      "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0",
    );
  });

  it("fails loud when the server ignored mode=semantic", async () => {
    const { error } = await captureSearch(
      { results: [{ record: { id: 1 }, similarity: 0.4 }] },
      (memory) => memory.searchSession("q", "sess-1", { mode: "semantic" }),
    );
    assert.ok(error instanceof AnhurError, "degraded hits must never be handed back");
    assert.equal(
      (error as AnhurError).message,
      'SERVER_TOO_OLD: requested mode="semantic" but the server answered ' +
        'retrieval.mode="" — this server predates ADR-0031 and IGNORED the mode field, so these ' +
        "results came from the balanced budget and may be purely lexical. Upgrade the server, or " +
        'drop to mode="balanced" and read retrieval.degraded yourself.',
    );
  });

  it("sends a byte-identical body for a caller that passes no knob", async () => {
    const { captured, error } = await captureSearch({ results: [] }, (memory) =>
      memory.searchSession("q", "sess-1"),
    );
    assert.equal(error, undefined);
    assert.ok(captured);
    assert.equal(
      JSON.stringify(captured!.body, Object.keys(captured!.body).sort()),
      JSON.stringify(
        { limit: 10, scope: "sessions", sessions: ["sess-1"], text: "q" },
        ["limit", "scope", "sessions", "text"],
      ),
    );
  });
});
