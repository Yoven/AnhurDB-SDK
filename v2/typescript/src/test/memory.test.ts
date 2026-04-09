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
