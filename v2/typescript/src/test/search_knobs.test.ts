/**
 * ADR-0031 search controls: `mode`, `semantic_timeout_ms`, `debug_signals` —
 * plus the richer response (13 per-hit signals, `leg_scores`) and the
 * cross-version guard.
 *
 * The contract under test is the WIRE, so every assertion reads the body the
 * SDK actually posted or the object it actually returned — never the intent of
 * the code. The guard tests are the important ones: they encode the defect
 * ADR-0031 exists to kill, where an old server answers 200 with lexical
 * results for a request that asked for strict semantics.
 *
 * Same design as the Go (httptest) and Python (aiohttp) suites.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { sessionsAll } from "../sessionFilter.js";
import { AnhurError } from "../types.js";
import { resetSearchKnobWarnings } from "../searchMode.js";
import type { SearchHitSignals, SearchWithRetrievalResult } from "../searchResults.js";

/** A retrieval block from a server that DOES understand ADR-0031. */
function retrievalBlock(mode: string): Record<string, unknown> {
  return {
    mode,
    signals_used: ["fts5"],
    semantic_attempted: mode !== "fast",
    semantic_used: mode === "semantic",
    degraded: false,
    elapsed_ms: 7,
    content_simhash_enabled: true,
    content_simhash_weight: 0.2,
    astar_enabled: false,
    astar_weight: 0,
    entity_jaccard_enabled: false,
    entity_jaccard_weight: 0,
  };
}

/** What the SDK put on the wire, decoded. */
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

describe("ADR-0031 knobs on the wire", () => {
  it("omits all three knobs when the caller does not ask", async () => {
    const { captured } = await captureSearch(
      { results: [], retrieval: retrievalBlock("balanced") },
      (memory) => memory.search("q", sessionsAll()),
    );
    assert.ok(captured);
    assert.equal(captured!.url.pathname, "/api/v1/search");
    assert.equal("mode" in captured!.body, false);
    assert.equal("semantic_timeout_ms" in captured!.body, false);
    assert.equal("debug_signals" in captured!.body, false);
  });

  it("sends mode, semantic_timeout_ms and debug_signals when asked", async () => {
    const { captured } = await captureSearch(
      { results: [], retrieval: retrievalBlock("semantic") },
      (memory) =>
        memory.search("q", sessionsAll(), {
          mode: "semantic",
          semanticTimeoutMs: 1500,
          debugSignals: true,
        }),
    );
    assert.ok(captured);
    assert.equal(captured!.body.mode, "semantic");
    assert.equal(captured!.body.semantic_timeout_ms, 1500);
    assert.equal(captured!.body.debug_signals, true);
  });

  it("omits semantic_timeout_ms=0 — zero already MEANS the server default", async () => {
    const { captured } = await captureSearch(
      { results: [], retrieval: retrievalBlock("balanced") },
      (memory) => memory.search("q", sessionsAll(), { semanticTimeoutMs: 0 }),
    );
    assert.ok(captured);
    assert.equal("semantic_timeout_ms" in captured!.body, false);
  });

  it("rejects an unknown mode client-side, before any request", async () => {
    const { captured, error } = await captureSearch(
      { results: [] },
      (memory) =>
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        memory.search("q", sessionsAll(), { mode: "semanitc" as any }),
    );
    assert.equal(captured, null, "a typo must never reach the server");
    assert.ok(error instanceof AnhurError);
    assert.match((error as AnhurError).message, /INVALID_PARAM: 'mode' "semanitc" is not supported/);
    assert.equal((error as AnhurError).kind, "invalid_request");
    assert.equal((error as AnhurError).retryable, false);
  });

  // Junior Tip [what this test would have caught, 2026-09-05]: before this
  // fix, `semanticTimeoutMs: -1` fell through the `> 0` omit-gate in
  // buildSearchPayload and DISAPPEARED — the request left without the field,
  // the server applied its own 700ms default, and the caller believed they had
  // capped the embedder. Python already raised INVALID_PARAM for the same
  // input, so one call behaved two ways across the SDKs. The message is
  // asserted in full, not by substring, so the three wordings cannot drift.
  it("rejects a negative semantic_timeout_ms client-side, before any request", async () => {
    const { captured, error } = await captureSearch(
      { results: [] },
      (memory) => memory.search("q", sessionsAll(), { semanticTimeoutMs: -1 }),
    );
    assert.equal(captured, null, "a negative budget must never reach the server");
    assert.ok(error instanceof AnhurError);
    assert.equal(
      (error as AnhurError).message,
      "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0",
    );
    assert.equal((error as AnhurError).kind, "invalid_request");
    assert.equal((error as AnhurError).retryable, false);
  });

  it("rejects a non-integer semantic_timeout_ms — the wire field is an int32", async () => {
    const { captured, error } = await captureSearch(
      { results: [] },
      (memory) => memory.search("q", sessionsAll(), { semanticTimeoutMs: 1.5 }),
    );
    assert.equal(captured, null);
    assert.ok(error instanceof AnhurError);
    assert.equal(
      (error as AnhurError).message,
      "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0",
    );
  });

  it("forwards the knobs through searchSession (it used to drop them)", async () => {
    const { captured } = await captureSearch(
      { results: [], retrieval: retrievalBlock("fast") },
      (memory) => memory.searchSession("q", "chat-1", { mode: "fast", limit: 3 }),
    );
    assert.ok(captured);
    assert.equal(captured!.body.mode, "fast");
    assert.equal(captured!.body.limit, 3);
    assert.deepEqual(captured!.body.sessions, ["chat-1"]);
    assert.equal(captured!.body.scope, "sessions");
  });

  it("forwards the knobs through recall (it used to drop them)", async () => {
    const { captured } = await captureSearch(
      { results: [], retrieval: retrievalBlock("fast") },
      (memory) => memory.recall("q", sessionsAll(), 4, { mode: "fast" }),
    );
    assert.ok(captured);
    assert.equal(captured!.body.mode, "fast");
    assert.equal(captured!.body.limit, 4);
  });
});

describe("ADR-0031 cross-version guard (mode=semantic)", () => {
  it("throws when the server answers a different mode", async () => {
    const { error } = await captureSearch(
      { results: [{ record: { id: 1 }, similarity: 0.4 }], retrieval: retrievalBlock("balanced") },
      (memory) => memory.search("q", sessionsAll(), { mode: "semantic" }),
    );
    assert.ok(error instanceof AnhurError, "an ignored mode=semantic must not return results");
    // EXACT equality: this message is a pinned cross-SDK contract, and the Go
    // and Python suites compare the identical string the same way. A substring
    // assertion is what let the three wordings drift apart in the first place.
    assert.equal(
      (error as AnhurError).message,
      'SERVER_TOO_OLD: requested mode="semantic" but the server answered ' +
        'retrieval.mode="balanced" — this server predates ADR-0031 and IGNORED the mode field, so these ' +
        "results came from the balanced budget and may be purely lexical. Upgrade the server, or " +
        'drop to mode="balanced" and read retrieval.degraded yourself.',
    );
  });

  it("throws when the server sends no retrieval block at all", async () => {
    const { error } = await captureSearch(
      { results: [{ record: { id: 1 }, similarity: 0.4 }] },
      (memory) => memory.search("q", sessionsAll(), { mode: "semantic" }),
    );
    assert.ok(error instanceof AnhurError);
    // A server that echoes NO mode reads as retrieval.mode="" — the same
    // sentence Go and Python produce, not a second wording for the same fact.
    assert.equal(
      (error as AnhurError).message,
      'SERVER_TOO_OLD: requested mode="semantic" but the server answered ' +
        'retrieval.mode="" — this server predates ADR-0031 and IGNORED the mode field, so these ' +
        "results came from the balanced budget and may be purely lexical. Upgrade the server, or " +
        'drop to mode="balanced" and read retrieval.degraded yourself.',
    );
  });

  it("passes when the server confirms semantic", async () => {
    const { value, error } = await captureSearch(
      { results: [{ record: { id: 1 }, similarity: 0.9 }], retrieval: retrievalBlock("semantic") },
      (memory) => memory.search("q", sessionsAll(), { mode: "semantic" }),
    );
    assert.equal(error, undefined);
    assert.equal(value?.length, 1);
  });

  it("does not fire for balanced — a normal request keeps working", async () => {
    const { value, error } = await captureSearch(
      { results: [{ record: { id: 1 }, similarity: 0.9 }], retrieval: retrievalBlock("balanced") },
      (memory) => memory.search("q", sessionsAll(), { mode: "balanced" }),
    );
    assert.equal(error, undefined);
    assert.equal(value?.length, 1);
  });
});

describe("ADR-0031 cross-version guard (warn-only knobs)", () => {
  it("warns once, and does not throw, when an old server drops debugSignals", async () => {
    resetSearchKnobWarnings();
    const originalWarn = console.warn;
    const warnings: string[] = [];
    console.warn = (...args: unknown[]) => {
      warnings.push(args.map(String).join(" "));
    };
    try {
      const { error } = await captureSearch(
        { results: [{ record: { id: 1 }, similarity: 0.5 }] },
        (memory) =>
          memory.search("q", sessionsAll(), { debugSignals: true, semanticTimeoutMs: 900 }),
      );
      assert.equal(error, undefined, "a dropped debug knob must not break the call");
    } finally {
      console.warn = originalWarn;
    }
    // The warning text uses the WIRE field names, because that is what the Go
    // and Python SDKs print and what an operator greps for in a mixed log.
    assert.deepEqual(warnings.sort(), [
      "anhurdb-sdk: warning: this AnhurDB server ignored debug_signals (it predates ADR-0031); " +
        "per-hit signals and leg_scores are absent, not empty.",
      "anhurdb-sdk: warning: this AnhurDB server ignored semantic_timeout_ms=900 (it predates " +
        "ADR-0031); the server's own semantic budget (700ms) was used instead.",
    ]);
  });

  it("stays silent when the server proves it understands the knobs", async () => {
    resetSearchKnobWarnings();
    const originalWarn = console.warn;
    const warnings: string[] = [];
    console.warn = (...args: unknown[]) => {
      warnings.push(args.map(String).join(" "));
    };
    try {
      await captureSearch(
        { results: [], retrieval: retrievalBlock("balanced") },
        (memory) => memory.search("q", sessionsAll(), { debugSignals: true }),
      );
    } finally {
      console.warn = originalWarn;
    }
    assert.deepEqual(warnings, []);
  });
});

describe("ADR-0031 richer response", () => {
  it("reads all 13 per-hit signals", async () => {
    const allThirteen: SearchHitSignals = {
      fts_rank: 1,
      semantic_rank: 2,
      simhash_rank: 3,
      simhash_hamming: 4,
      rrf_score: 0.5,
      semantic_cosine: 0.6,
      hnsw_rank: 7,
      bsq_rank: 8,
      parquet_rank: 9,
      fts5_rank: 10,
      astar_rank: 11,
      entity_jaccard_rank: 12,
      active_leg_weight_sum: 1.75,
    };
    const { value } = await captureSearch(
      {
        results: [{ record: { id: 1 }, similarity: 0.5, signals: allThirteen }],
        retrieval: retrievalBlock("balanced"),
      },
      (memory) => memory.search("q", sessionsAll(), { debugSignals: true }),
    );
    // Every field survives the nesting — a dropped one reads back undefined.
    assert.deepEqual(value?.[0].signals, allThirteen);
  });

  it("surfaces leg_scores from the response's TOP LEVEL, beside retrieval", async () => {
    const { value } = await captureSearch(
      {
        results: [{ record: { id: 1 }, similarity: 0.5 }],
        retrieval: retrievalBlock("balanced"),
        leg_scores: [
          { leg: "fts5", candidates: 12, top_scores: [0.9, 0.8], mean: 0.4, stddev: 0.2 },
          { leg: "hnsw", candidates: 0, mean: 0, stddev: 0 },
        ],
      },
      (memory) =>
        memory.searchWithRetrieval("q", sessionsAll(), { debugSignals: true }),
    );
    const searchOutcome = value as SearchWithRetrievalResult;
    assert.equal(searchOutcome.legScores?.length, 2);
    assert.equal(searchOutcome.legScores?.[0].leg, "fts5");
    assert.equal(searchOutcome.legScores?.[0].candidates, 12);
    assert.deepEqual(searchOutcome.legScores?.[1].top_scores, undefined);
    assert.equal(searchOutcome.retrieval?.mode, "balanced");
  });

  it("leaves legScores undefined when the server sends none", async () => {
    const { value } = await captureSearch(
      { results: [], retrieval: retrievalBlock("balanced") },
      (memory) => memory.searchWithRetrieval("q", sessionsAll()),
    );
    assert.equal((value as SearchWithRetrievalResult).legScores, undefined);
  });
});

describe("ADR-0031 guard: scope=shared_all is exempt from the throw", () => {
  it("warns instead of throwing when a CURRENT server echoes no mode for shared_all", async () => {
    resetSearchKnobWarnings();
    const originalWarn = console.warn;
    const warnings: string[] = [];
    console.warn = (...args: unknown[]) => {
      warnings.push(args.map(String).join(" "));
    };
    let outcome: { value?: unknown; error?: unknown };
    try {
      // What a HEALTHY server really sends for shared_all: weights and the
      // degrade verdict, no `mode` — handler/record_search_shared_all.go.
      outcome = await captureSearch(
        {
          results: [{ record: { id: 1 }, similarity: 0.7 }],
          retrieval: {
            degraded: false,
            astar_enabled: false,
            astar_weight: 0,
            entity_jaccard_enabled: false,
            entity_jaccard_weight: 0,
          },
        },
        (memory) => memory.searchShared("q", sessionsAll(), { mode: "semantic" }),
      );
    } finally {
      console.warn = originalWarn;
    }
    assert.equal(outcome.error, undefined, "a healthy shared_all server must not be rejected");
    assert.equal((outcome.value as unknown[]).length, 1);
    assert.ok(warnings.some((line) => line.includes('scope="shared_all"')));
  });

  it("still throws for a single scope, so the exemption is not a blanket hole", async () => {
    resetSearchKnobWarnings();
    const { error } = await captureSearch(
      { results: [{ record: { id: 1 }, similarity: 0.7 }] },
      (memory) => memory.searchTenantShared("q", sessionsAll(), { mode: "semantic" }),
    );
    assert.ok(error instanceof AnhurError);
    assert.match((error as AnhurError).message, /^SERVER_TOO_OLD/);
  });
});
