/**
 * Basic unit tests for the AnhurDB TypeScript SDK.
 *
 * Uses Node's built-in test runner (node --test) — zero test dependencies.
 * These tests exercise constructor validation, argument guards, and
 * the session ID format without requiring a running server.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { sessionsAll } from "../sessionFilter.js";
import {
  AnhurError,
  AnhurAuthError,
  AnhurQueryError,
  AnhurConnectionError,
} from "../types.js";
import type { SearchResult } from "../types.js";

// ── Constructor tests ─────────────────────────────────────────

describe("Memory constructor", () => {
  it("throws when apiKey is missing", () => {
    assert.throws(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      () => new Memory({ apiKey: "" } as any),
      { message: "apiKey is required" },
    );
  });

  it("creates instance with apiKey only", () => {
    const mem = new Memory({ apiKey: "test-key-123" });
    assert.ok(mem);
    assert.ok(typeof mem.sessionId === "string");
  });

  it("creates instance with explicit userId", () => {
    const mem = new Memory({ apiKey: "test-key", userId: "user-42" });
    // When userId is provided, sessionId should start with it
    assert.ok(mem.sessionId.startsWith("user-42-"));
  });

  it("uses default URL when not provided", () => {
    const mem = new Memory({ apiKey: "test-key", userId: "u1" });
    assert.ok(mem.toString().includes("u1"));
  });

  it("accepts custom URL", () => {
    const mem = new Memory({
      apiKey: "key",
      url: "http://localhost:8000",
      userId: "u1",
    });
    assert.ok(mem);
  });
});

// ── Argument validation tests ─────────────────────────────────

describe("Memory.add() validation", () => {
  it("rejects empty text", async () => {
    const mem = new Memory({ apiKey: "key", userId: "u" });
    await assert.rejects(() => mem.add(""), {
      message: "text cannot be empty",
    });
  });

  it("accepts score, type, and metadata options (parity surface)", () => {
    const mem = new Memory({ apiKey: "key", userId: "u" });
    // Type-level guarantee: the add() signature must accept all three
    // capability fields so the TS SDK stays at parity with Go/Python.
    // We only assert the call is well-typed and the method exists; no
    // network call is made here.
    const call = (): Promise<unknown> =>
      mem.add("hello", {
        score: 9,
        type: "fact",
        metadata: { source: "unit-test" },
      });
    assert.equal(typeof call, "function");
  });
});

// ── add() routing: empty metadata must NOT count as a pin ──────
// Junior Tip [three-way parity]: Go's forceRecordsPath checks
// `len(cfg.metadata) > 0`; Python's checks `bool(metadata)` — both treat an
// empty `{}` as "no pin". Before this fix the TS check was
// `metadata !== undefined`, so `add(text, {metadata: {}})` was misrouted to
// the synchronous /records path even though the caller pinned nothing.
// These tests lock the routing decision by asserting which endpoint the
// mocked fetch actually receives — mirrors
// python/tests/test_http_mock.py::TestAddForceRecordsPathOnPin.
describe("Memory.add() metadata pin routing (parity: Go len()>0, Python bool())", () => {
  const mockIngestVsRecords = (): {
    calls: { ingest: number; records: number };
    restore: () => void;
  } => {
    const originalFetch = globalThis.fetch;
    const calls = { ingest: 0, records: 0 };
    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/api/v1/ingest")) {
        calls.ingest++;
        return new Response(
          JSON.stringify({
            id: 1,
            records: [{ id: 1, type: "episodic", summary: "x" }],
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      }
      if (url.includes("/api/v1/records")) {
        calls.records++;
        return new Response(JSON.stringify({ id: 2, uuid: "sess-1" }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      throw new Error(`unexpected URL in mock: ${url}`);
    }) as typeof fetch;
    return {
      calls,
      restore: () => {
        globalThis.fetch = originalFetch;
      },
    };
  };

  it("add(text, {metadata: {}}) is NOT a pin — still hits /api/v1/ingest", async () => {
    const mock = mockIngestVsRecords();
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const result = await mem.add("empty metadata", {
        metadata: {},
        sessionId: "sess-1",
      });
      assert.equal(result.mode, "cloud");
      assert.equal(mock.calls.ingest, 1);
      assert.equal(mock.calls.records, 0);
    } finally {
      mock.restore();
    }
  });

  it("add(text, {metadata: {k: 'v'}}) IS a pin — forces /api/v1/records", async () => {
    const mock = mockIngestVsRecords();
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const result = await mem.add("pinned metadata", {
        metadata: { k: "v" },
        sessionId: "sess-1",
      });
      assert.equal(result.mode, "oss");
      assert.equal(mock.calls.ingest, 0);
      assert.equal(mock.calls.records, 1);
    } finally {
      mock.restore();
    }
  });
});

describe("Memory.search() validation", () => {
  it("rejects empty query", async () => {
    const mem = new Memory({ apiKey: "key", userId: "u" });
    await assert.rejects(() => mem.search("", sessionsAll()), {
      message: "query cannot be empty",
    });
  });
});

// ── forget() stub test ────────────────────────────────────────

describe("Memory.forget()", () => {
  it("throws NotImplementedError", async () => {
    const mem = new Memory({ apiKey: "key", userId: "u" });
    await assert.rejects(() => mem.forget(), {
      message: /not yet available/,
    });
  });

  it("throws NotImplementedError with memoryId", async () => {
    const mem = new Memory({ apiKey: "key", userId: "u" });
    await assert.rejects(() => mem.forget(42), {
      message: /not yet available/,
    });
  });
});

// ── Session management tests ──────────────────────────────────

describe("Memory.newSession()", () => {
  it("returns a new session ID", async () => {
    const mem = new Memory({ apiKey: "key", userId: "u" });
    const oldId = mem.sessionId;
    // Small delay to ensure timestamp differs
    await new Promise((r) => setTimeout(r, 1100));
    const newId = await mem.newSession();
    assert.notEqual(newId, oldId);
    assert.equal(newId, mem.sessionId);
  });
});

describe("Memory.createSession()", () => {
  it("accepts optional sessionId and mode on add (parity surface)", () => {
    const mem = new Memory({ apiKey: "key", userId: "u" });
    const createCall = (): Promise<string> =>
      mem.createSession({ sessionId: "sess-1", metadata: { agent: "test" } });
    const addCall = (): Promise<unknown> =>
      mem.add("hello", { mode: "regular", sessionId: "sess-1" });
    assert.equal(typeof createCall, "function");
    assert.equal(typeof addCall, "function");
  });
});

// ── Error hierarchy tests ─────────────────────────────────────

describe("Error types", () => {
  it("AnhurAuthError extends AnhurError", () => {
    const err = new AnhurAuthError("bad key");
    assert.ok(err instanceof AnhurError);
    assert.ok(err instanceof Error);
    assert.equal(err.name, "AnhurAuthError");
  });

  it("AnhurQueryError extends AnhurError", () => {
    const err = new AnhurQueryError("bad query");
    assert.ok(err instanceof AnhurError);
    assert.equal(err.name, "AnhurQueryError");
  });

  it("AnhurConnectionError extends AnhurError", () => {
    const err = new AnhurConnectionError("timeout");
    assert.ok(err instanceof AnhurError);
    assert.equal(err.name, "AnhurConnectionError");
  });
});

// ── toString() test ───────────────────────────────────────────

describe("Memory.toString()", () => {
  it("includes container tag and session", () => {
    const mem = new Memory({ apiKey: "key", userId: "agent-x" });
    const str = mem.toString();
    assert.ok(str.includes("agent-x"));
    assert.ok(str.includes("Memory("));
  });
});

// ── readContent rawText parity (2026-06-11) ───────────────────
// GET /records/{id}/content responde text/plain cru. Antes, readContent fazia
// get<{content}> → JSON.parse falhava → {message} → data.content undefined → ""
// (perda total do conteúdo). getText devolve o corpo verbatim, alinhando com
// Go (raw bytes) e Python (raw_text=True).
describe("Memory.readContent (rawText parity)", () => {
  it("returns raw text/plain content verbatim, not empty string", async () => {
    const originalFetch = globalThis.fetch;
    const rawContent = "O usuário validou acentuação (ção, ã) e emoji 🧠 sem corromper.";
    globalThis.fetch = (async () =>
      new Response(rawContent, {
        status: 200,
        headers: { "content-type": "text/plain; charset=utf-8" },
      })) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const content = await mem.readContent(123);
      assert.equal(content, rawContent);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("returns empty string on empty body (no throw)", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response("", { status: 200 })) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const content = await mem.readContent(404);
      assert.equal(content, "");
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});

// ── walkSemantic() goal-directed body serialization ───────────
// Junior Tip [contract]: POST /api/v1/walk/semantic decodes seed_id/depth plus
// the optional goal-directed keys (target, max_cost, target_tag, vector). We
// assert (a) backward-compat — no goal-directed keys leak when no options are
// passed — and (b) goalVector (raw bytes) is base64-encoded into wire `vector`.
describe("Memory.walkSemantic (goal-directed body serialization)", () => {
  // Capture the JSON body that walkSemantic serialises onto the wire by
  // intercepting fetch and returning a well-formed (empty) WalkResult.
  const captureBody = async (
    run: (mem: Memory) => Promise<unknown>,
  ): Promise<Record<string, unknown>> => {
    const originalFetch = globalThis.fetch;
    let captured: Record<string, unknown> = {};
    globalThis.fetch = (async (_url: unknown, init: RequestInit) => {
      captured = JSON.parse(init.body as string) as Record<string, unknown>;
      return new Response(JSON.stringify({ nodes: [], edges: [], count: 0 }), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await run(mem);
    } finally {
      globalThis.fetch = originalFetch;
    }
    return captured;
  };

  it("omits goal-directed keys with no options (pure Dijkstra, backward-compat)", async () => {
    const body = await captureBody((mem) => mem.walkSemantic(7, 4));
    assert.equal(body.seed_id, 7);
    assert.equal(body.depth, 4);
    assert.ok(!("target" in body));
    assert.ok(!("vector" in body));
    assert.ok(!("target_tag" in body));
    assert.ok(!("max_cost" in body));
  });

  it("serializes semantic target with base64-encoded goalVector + maxCost", async () => {
    const goalVector = new Uint8Array([0, 1, 2, 253, 254, 255]);
    const expectedVector = Buffer.from(goalVector).toString("base64");
    const body = await captureBody((mem) =>
      mem.walkSemantic(7, undefined, {
        target: "semantic",
        goalVector,
        maxCost: 3.5,
      }),
    );
    assert.equal(body.seed_id, 7);
    assert.equal(body.target, "semantic");
    assert.equal(body.vector, expectedVector);
    assert.equal(body.max_cost, 3.5);
    assert.ok(!("target_tag" in body));
  });

  it("serializes tag target with target_tag and no vector", async () => {
    const body = await captureBody((mem) =>
      mem.walkSemantic(9, undefined, {
        target: "tag",
        targetTag: "project-x",
      }),
    );
    assert.equal(body.target, "tag");
    assert.equal(body.target_tag, "project-x");
    assert.ok(!("vector" in body));
    assert.ok(!("max_cost" in body));
  });
});

// ── search() nested SearchResult shape (2026-07-03) ───────────
// Junior Tip [nested parity]: every search method returns the canonical
// `{ record: <full MemoryRecord>, similarity }` shape — the full record NESTED
// (no dropped fields) with the score in the SIBLING `similarity` key (NOT a flat
// `score`). Mirrors Python `SearchResult(record, similarity)` and Go
// `SearchResult{Record, Similarity}`. We mock fetch to return the exact server
// envelope `{results:[{record:{...}, similarity}]}` and assert the SDK nests it.
describe("Memory.search (nested SearchResult shape)", () => {
  const serverEnvelope = {
    results: [
      {
        record: {
          id: 42,
          uuid: "sess-abc",
          type: "fact",
          summary: "User is a data scientist at Google",
          status: "saved",
          weight: 0.9,
          score: 9,
          related_ids: [7, 8],
          main_ids: [3],
          created_at: "2026-07-03T00:00:00Z",
          updated_at: "2026-07-03T00:00:00Z",
        },
        similarity: 0.63,
      },
    ],
    count: 1,
  };

  const withMockedFetch = async (
    run: (mem: Memory) => Promise<unknown>,
  ): Promise<unknown> => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response(JSON.stringify(serverEnvelope), {
        status: 200,
        headers: { "content-type": "application/json" },
      })) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      return await run(mem);
    } finally {
      globalThis.fetch = originalFetch;
    }
  };

  it("search() nests the full record under .record with sibling .similarity", async () => {
    const results = (await withMockedFetch((mem) =>
      mem.search("what does this user do?", sessionsAll()),
    )) as SearchResult[];
    assert.equal(results.length, 1);
    // Score lives in the sibling `similarity`, NOT a flat `score`.
    assert.equal(results[0].similarity, 0.63);
    // The full record is preserved, nested under `record`.
    assert.equal(results[0].record.id, 42);
    assert.equal(results[0].record.summary, "User is a data scientist at Google");
    // Fields the old flat shape dropped must survive.
    assert.equal(results[0].record.uuid, "sess-abc");
    assert.deepEqual(results[0].record.related_ids, [7, 8]);
    // No flat leakage: the top-level hit has no `score`/`summary`/`id`.
    assert.ok(!("score" in results[0]));
    assert.ok(!("summary" in results[0]));
    assert.ok(!("id" in results[0]));
  });

  it("search() posts /api/v1/search with default scope=sessions", async () => {
    const originalFetch = globalThis.fetch;
    let capturedUrl = "";
    let capturedBody: { scope?: string } = {};
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      capturedUrl = String(input);
      capturedBody = JSON.parse(String(init?.body ?? "{}"));
      return new Response(JSON.stringify(serverEnvelope), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await mem.search("q", sessionsAll());
      assert.ok(capturedUrl.includes("/api/v1/search"));
      assert.ok(!capturedUrl.includes("/search/global"));
      assert.equal(capturedBody.scope, "sessions");
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("searchTenantShared() sends scope=tenant_shared", async () => {
    const originalFetch = globalThis.fetch;
    let capturedBody: { scope?: string } = {};
    globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
      capturedBody = JSON.parse(String(init?.body ?? "{}"));
      return new Response(JSON.stringify(serverEnvelope), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await mem.searchTenantShared("Nomad", sessionsAll());
      assert.equal(capturedBody.scope, "tenant_shared");
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("recall() returns the same nested shape", async () => {
    const results = (await withMockedFetch((mem) =>
      mem.recall("google", sessionsAll()),
    )) as SearchResult[];
    assert.equal(results.length, 1);
    assert.equal(results[0].record.id, 42);
    assert.equal(results[0].similarity, 0.63);
  });

  it("searchByType() reads the bare `records` envelope and nests each record", async () => {
    // Junior Tip [real contract, 2026-07-04]: GET /api/v1/search/type returns a BARE
    // `{records:[<Record>],count}` array — NOT the `{results:[{record,similarity}]}`
    // envelope of POST /api/v1/search. The
    // SDK must read `records` and wrap each into a SearchResult; reading `results`
    // (the old bug) returned [] for every call. A type filter carries no semantic
    // distance, so `similarity` is 0.
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({ records: [serverEnvelope.results[0].record], count: 1 }),
        { status: 200, headers: { "content-type": "application/json" } },
      )) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const results = (await mem.searchByType("fact", sessionsAll())) as SearchResult[];
      assert.equal(results.length, 1);
      assert.equal(results[0].record.type, "fact");
      assert.equal(results[0].record.id, 42);
      assert.equal(results[0].similarity, 0);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("defaults similarity to 0 when the server omits it", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({ results: [{ record: { id: 1, summary: "x" } }] }),
        { status: 200, headers: { "content-type": "application/json" } },
      )) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const results = (await mem.search("q", sessionsAll())) as SearchResult[];
      assert.equal(results[0].similarity, 0);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});

// ── search() astar/entity-jaccard weight overrides (ADR-0021) ─
// Junior Tip [nil-vs-zero on the wire]: `astarWeight`/`entityJaccardWeight`
// mirror the server's `*float64` contract — `undefined` (not passed) must
// omit the wire key entirely (server keeps its own default), while an
// explicit `0` must reach the server as JSON `0` (server rescales the leg to
// zero for this query only). A naive `if (options?.astarWeight)` truthiness
// check would silently swallow the `0` case — these tests lock the `!==
// undefined` behavior so a future refactor cannot regress it quietly.
describe("Memory.search() astar/entity-jaccard weight overrides", () => {
  const emptyEnvelope = { results: [], count: 0 };

  const captureBody = async (
    run: (mem: Memory) => Promise<unknown>,
  ): Promise<Record<string, unknown>> => {
    const originalFetch = globalThis.fetch;
    let capturedBody: Record<string, unknown> = {};
    globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
      capturedBody = JSON.parse(String(init?.body ?? "{}"));
      return new Response(JSON.stringify(emptyEnvelope), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await run(mem);
    } finally {
      globalThis.fetch = originalFetch;
    }
    return capturedBody;
  };

  it("omits astar_weight/entity_jaccard_weight from the payload when not passed", async () => {
    const body = await captureBody((mem) => mem.search("q", sessionsAll()));
    assert.ok(!("astar_weight" in body));
    assert.ok(!("entity_jaccard_weight" in body));
  });

  it("sends astar_weight when set to a positive value", async () => {
    const body = await captureBody((mem) =>
      mem.search("q", sessionsAll(), { astarWeight: 0.4 }));
    assert.equal(body.astar_weight, 0.4);
  });

  it("sends an explicit astar_weight: 0 (does not treat it as omitted)", async () => {
    const body = await captureBody((mem) =>
      mem.search("q", sessionsAll(), { astarWeight: 0 }));
    assert.ok("astar_weight" in body);
    assert.equal(body.astar_weight, 0);
  });

  it("sends entity_jaccard_weight independently of astar_weight", async () => {
    const body = await captureBody((mem) =>
      mem.search("q", sessionsAll(), { entityJaccardWeight: 0 }));
    assert.ok(!("astar_weight" in body));
    assert.ok("entity_jaccard_weight" in body);
    assert.equal(body.entity_jaccard_weight, 0);
  });

  // ── expand_related (ADR-0021) ──────────────────────────────
  // Junior Tip [bool, not nil-vs-zero]: unlike astar/entity-jaccard weight,
  // expand_related is a plain bool with no "explicit zero" case to protect —
  // same falsy-omit discipline as skip_query_embed/skip_cognitive_rerank.
  it("omits expand_related from the payload when not passed", async () => {
    const body = await captureBody((mem) => mem.search("q", sessionsAll()));
    assert.ok(!("expand_related" in body));
  });

  it("omits expand_related from the payload when explicitly false", async () => {
    const body = await captureBody((mem) =>
      mem.search("q", sessionsAll(), { expandRelated: false }));
    assert.ok(!("expand_related" in body));
  });

  it("sends expand_related: true when requested", async () => {
    const body = await captureBody((mem) =>
      mem.search("q", sessionsAll(), { expandRelated: true }));
    assert.equal(body.expand_related, true);
  });

  it("sends astar_weight and expand_related together without interfering", async () => {
    const body = await captureBody((mem) =>
      mem.search("q", sessionsAll(), { expandRelated: true, astarWeight: 0.2 }));
    assert.equal(body.expand_related, true);
    assert.equal(body.astar_weight, 0.2);
  });
});

// ── search() provenance/scope/signals/related_nodes + searchWithRetrieval() (ADR-0021) ─
// Junior Tip [the debt ADR-0021 flagged]: the server has sent
// `provenance`/`scope`/`signals` per hit, and a top-level `retrieval` block,
// since ADR-0012 — but `nestSearchResults` only ever copied `record`/
// `similarity`, so an SDK caller had no way to see which plane answered a
// `shared_all` hit. `related_nodes` (ADR-0021, expand_related) has the same
// shape of debt: real server field, dropped by the SDK until modeled. These
// tests pin the fix directly against a raw server envelope shaped exactly
// like the real response (see AnhurDB/server/handler/bundle.go
// hybridResponsePayloadWithRetrieval and record_search_expand_related.go).
describe("Memory.search() surfaces provenance/scope/signals/related_nodes + searchWithRetrieval()", () => {
  const fullEnvelope = {
    results: [
      {
        record: { id: 1, uuid: "u1", type: "fact", summary: "s", status: "saved", weight: 1, score: 5, created_at: "t", updated_at: "t" },
        similarity: 0.5,
        provenance: "tenant_shared",
        scope: "shared_all",
        signals: {
          fts_rank: 1,
          semantic_rank: 2,
          rrf_score: 0.9,
          semantic_cosine: 0.87,
        },
        related_nodes: [
          { id: 123, type: "fact", summary: "Works remotely", weight: 0.7 },
          { id: 456, type: "preference", summary: "Prefers async standups", weight: 0.4 },
        ],
      },
    ],
    count: 1,
    scope: "shared_all",
    bundle_hash: "abc",
    bundle_ordering: "hybrid",
    retrieval: {
      mode: "balanced",
      signals_used: ["fts", "semantic"],
      semantic_attempted: true,
      semantic_used: true,
      degraded: false,
      elapsed_ms: 12,
      content_simhash_enabled: true,
      content_simhash_weight: 0.1,
      astar_enabled: true,
      astar_weight: 0.3,
      entity_jaccard_enabled: false,
      entity_jaccard_weight: 0,
    },
  };

  const withMockedFetch = async (
    run: (mem: Memory) => Promise<unknown>,
  ): Promise<unknown> => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response(JSON.stringify(fullEnvelope), {
        status: 200,
        headers: { "content-type": "application/json" },
      })) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      return await run(mem);
    } finally {
      globalThis.fetch = originalFetch;
    }
  };

  it("search() carries provenance/scope/signals/related_nodes onto each SearchResult", async () => {
    const results = (await withMockedFetch((mem) =>
      mem.search("q", sessionsAll()))) as SearchResult[];
    assert.equal(results[0].provenance, "tenant_shared");
    assert.equal(results[0].scope, "shared_all");
    assert.deepEqual(results[0].signals, {
      fts_rank: 1,
      semantic_rank: 2,
      rrf_score: 0.9,
      semantic_cosine: 0.87,
    });
    assert.equal(results[0].related_nodes?.length, 2);
    assert.equal(results[0].related_nodes?.[0].id, 123);
    assert.equal(results[0].related_nodes?.[0].summary, "Works remotely");
    assert.equal(results[0].related_nodes?.[0].weight, 0.7);
  });

  it("nestSearchResults leaves related_nodes undefined (not []) when the server omits it", async () => {
    // A hit from a request that never set expandRelated (or a server that
    // predates ADR-0021) sends no `related_nodes` key at all — the SDK must
    // NOT default it to an empty array, since `undefined` vs `[]` are two
    // different facts (not-requested vs requested-but-nothing-found).
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({ results: [{ record: { id: 1 }, similarity: 0.1 }] }),
        { status: 200, headers: { "content-type": "application/json" } },
      )) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const results = (await mem.search("q", sessionsAll())) as SearchResult[];
      assert.equal(results[0].related_nodes, undefined);
      assert.ok(!("related_nodes" in results[0]));
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("searchWithRetrieval() returns the same results plus the retrieval block", async () => {
    const { results, retrieval } = (await withMockedFetch((mem) =>
      mem.searchWithRetrieval("q", sessionsAll()))) as {
      results: SearchResult[];
      retrieval?: { mode: string; astar_weight: number; degraded: boolean };
    };
    assert.equal(results[0].provenance, "tenant_shared");
    assert.ok(retrieval);
    assert.equal(retrieval?.mode, "balanced");
    assert.equal(retrieval?.astar_weight, 0.3);
    assert.equal(retrieval?.degraded, false);
  });

  it("searchWithRetrieval() leaves retrieval undefined when the server omits the block", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ results: [] }), {
        status: 200,
        headers: { "content-type": "application/json" },
      })) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const { retrieval } = await mem.searchWithRetrieval("q", sessionsAll());
      assert.equal(retrieval, undefined);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});

// ── Anchor-seed removal (single request, surface 422) ─────────
// Junior Tip [contract, 2026-07-06]: the SDK USED to catch a 422 "episodic
// anchor" on a derived (fact/decision/…) create, fabricate a SYNTHETIC episodic
// record from the same text, and retry once — polluting the graph and diverging
// from the gRPC path. That seed-and-retry was removed. A derived create into an
// anchor-less session must now make EXACTLY ONE request and surface the typed
// AnhurQueryError unchanged; the caller (agents/plugin) writes an episodic
// record first. These tests lock that: request count == 1, and the error type.
describe("Memory.create (no anchor seeding)", () => {
  it("fails loud before HTTP when createSession was never called", async () => {
    const originalFetch = globalThis.fetch;
    let callCount = 0;
    globalThis.fetch = (async () => {
      callCount++;
      return new Response("{}", { status: 200 });
    }) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await assert.rejects(
        () => mem.create("some fact", { type: "fact" }),
        (err: unknown) =>
          err instanceof Error &&
          err.message.includes("create a session first"),
      );
      assert.equal(callCount, 0);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("makes exactly one request and surfaces the typed 422, no synthetic seed", async () => {
    const originalFetch = globalThis.fetch;
    let callCount = 0;
    globalThis.fetch = (async () => {
      callCount++;
      // Mirrors the server's honest anchor error for a genuinely-empty session.
      return new Response(
        "cannot create fact without an episodic anchor in session; " +
          "create an episodic record first",
        { status: 422 },
      );
    }) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await assert.rejects(
        () =>
          mem.create("some fact", { type: "fact", sessionId: "registered-sess" }),
        // A 422 maps to AnhurQueryError (see HttpClient.request).
        (err: unknown) => err instanceof AnhurQueryError,
      );
      // The load-bearing assertion: ONE attempt only. Seed-and-retry would have
      // fired a second (episodic) POST and a third (retry) POST.
      assert.equal(callCount, 1);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("surfaces the 422 even for a non-episodic add() (records path)", async () => {
    const originalFetch = globalThis.fetch;
    let callCount = 0;
    globalThis.fetch = (async () => {
      callCount++;
      return new Response(
        "cannot create decision without an episodic anchor in session",
        { status: 422 },
      );
    }) as typeof fetch;
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      // A pinned type forces the synchronous /records path (createRecord).
      await assert.rejects(
        () =>
          mem.add("a decision", {
            type: "decision",
            sessionId: "registered-sess",
          }),
        (err: unknown) => err instanceof AnhurQueryError,
      );
      assert.equal(callCount, 1);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});
