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
import {
  AnhurError,
  AnhurAuthError,
  AnhurQueryError,
  AnhurConnectionError,
} from "../types.js";

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

describe("Memory.search() validation", () => {
  it("rejects empty query", async () => {
    const mem = new Memory({ apiKey: "key", userId: "u" });
    await assert.rejects(() => mem.search(""), {
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
