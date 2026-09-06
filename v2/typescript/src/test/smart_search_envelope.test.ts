/**
 * `smartSearch` answers a TYPED envelope, not `unknown`.
 *
 * Two failures are being fenced here at once:
 *
 *  1. The return type was `Promise<unknown>`, so `response.count` did not even
 *     compile and every caller had to cast — a cast is a guess, and the guess
 *     nobody made was that this endpoint does NOT answer the `{record,
 *     similarity}` shape the other search verbs answer. This file reads the
 *     fields directly; under the old signature it fails to build, which is the
 *     proof the type is doing work.
 *  2. `results` is a Go slice, so "no matches" arrives as JSON `null`, not
 *     `[]`. A type promising an array would make `.length` compile and throw at
 *     runtime on the most ordinary answer there is.
 *
 * Wire shape copied from `AnhurDB/server/handler/search_smart.go` (envelope)
 * and `AnhurDB/server/duckdb/engine_lifecycle.go` (row).
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { sessionsAll } from "../sessionFilter.js";

async function withSmartSearchReply(body: unknown): Promise<{
  requestUrl: string;
  response: Awaited<ReturnType<Memory["smartSearch"]>>;
}> {
  const originalFetch = globalThis.fetch;
  let requestUrl = "";
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    requestUrl = String(input);
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;
  try {
    const memory = new Memory({
      apiKey: "test-key-000",
      userId: "u",
      url: "http://localhost:8000",
    });
    const response = await memory.smartSearch("engineering", sessionsAll(), 5);
    return { requestUrl, response };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

describe("smartSearch envelope", () => {
  it("reads results/count/scope/bundle_hash off the real reply", async () => {
    const { requestUrl, response } = await withSmartSearchReply({
      results: [
        {
          id: 42,
          uuid: "u-42",
          type: "fact",
          summary: "the engineer ships",
          metadata: "{}",
          score: 0.8,
          weight: 1.2,
          status: "completed",
          relevance: 3.5,
          bm25: 2.1,
          created_at: "2026-09-01T00:00:00Z",
          updated_at: "2026-09-01T00:00:00Z",
        },
      ],
      count: 1,
      scope: "sessions",
      bundle_hash: "deadbeef",
      bundle_ordering: "smart_relevance",
    });

    assert.match(requestUrl, /\/api\/v1\/search\/smart\?/);
    assert.equal(response.count, 1);
    assert.equal(response.scope, "sessions");
    assert.equal(response.bundle_ordering, "smart_relevance");
    // The score key is `relevance` (lexical), NOT `similarity` (cosine).
    assert.equal(response.results?.[0].relevance, 3.5);
    assert.equal(response.results?.[0].summary, "the engineer ships");
  });

  it("survives the nil-slice answer the server really sends when empty", async () => {
    const { response } = await withSmartSearchReply({
      results: null,
      count: 0,
      scope: "sessions",
      bundle_hash: "",
      bundle_ordering: "smart_relevance",
    });
    assert.equal(response.count, 0);
    assert.deepEqual(response.results ?? [], []);
  });
});
