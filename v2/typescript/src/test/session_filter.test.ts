/**
 * Session filter (ADR-0014) tests for the AnhurDB TypeScript SDK.
 *
 * Two layers:
 *   1. `normalizeSessions` — the pure contract (wildcard, cap, rejections),
 *      table-driven, no I/O.
 *   2. The search family over a mocked `fetch` — proves the resolved filter
 *      reaches the wire (JSON body for POST, repeated query key for GET) and
 *      that a rejected filter never produces a request at all.
 *
 * Node's built-in test runner (node --test) — zero test dependencies.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurError } from "../types.js";
import {
  MAX_SESSION_FILTER_UUIDS,
  SESSION_WILDCARD,
  normalizeSessions,
  sessionsAll,
} from "../sessionFilter.js";

// ── Layer 1: the pure contract ────────────────────────────────

describe("normalizeSessions() — accepted filters", () => {
  const atTheCap = Array.from(
    { length: MAX_SESSION_FILTER_UUIDS },
    (_unused, index) => `session-${index}`,
  );

  const acceptedCases: Array<{
    name: string;
    input: string[];
    expected: string[];
  }> = [
    { name: "one session", input: ["session-a"], expected: ["session-a"] },
    {
      name: "many sessions",
      input: ["session-a", "session-b", "session-c"],
      expected: ["session-a", "session-b", "session-c"],
    },
    {
      name: "wildcard",
      input: [SESSION_WILDCARD],
      expected: [SESSION_WILDCARD],
    },
    { name: "wildcard helper", input: sessionsAll(), expected: ["*"] },
    {
      name: "surrounding whitespace is trimmed",
      input: ["  session-a  "],
      expected: ["session-a"],
    },
    {
      name: "duplicates collapse",
      input: ["session-a", "session-a", "session-b"],
      expected: ["session-a", "session-b"],
    },
    {
      name: "exactly at the cap is allowed",
      input: atTheCap,
      expected: atTheCap,
    },
  ];

  for (const testCase of acceptedCases) {
    it(testCase.name, () => {
      assert.deepEqual(normalizeSessions(testCase.input), testCase.expected);
    });
  }
});

describe("normalizeSessions() — rejected filters", () => {
  const overTheCap = Array.from(
    { length: MAX_SESSION_FILTER_UUIDS + 1 },
    (_unused, index) => `session-${index}`,
  );

  const rejectedCases: Array<{
    name: string;
    input: unknown;
    message: string;
  }> = [
    {
      name: "absent",
      input: undefined,
      message: `INVALID_PARAM: 'sessions' is required; use ["*"] for every session in scope`,
    },
    {
      name: "null",
      input: null,
      message: `INVALID_PARAM: 'sessions' is required; use ["*"] for every session in scope`,
    },
    {
      name: "empty list",
      input: [],
      message: `INVALID_PARAM: 'sessions' cannot be empty; use ["*"] for every session in scope`,
    },
    {
      name: "empty entry",
      input: ["session-a", "   "],
      message: "INVALID_PARAM: 'sessions' contains an empty entry",
    },
    {
      name: "wildcard mixed with an explicit session",
      input: [SESSION_WILDCARD, "session-a"],
      message:
        `INVALID_PARAM: 'sessions' mixes "*" with 1 explicit session(s); ` +
        "the wildcard must stand alone",
    },
    {
      name: "above the cap",
      input: overTheCap,
      message:
        `INVALID_PARAM: at most ${MAX_SESSION_FILTER_UUIDS} sessions per request ` +
        `(got ${MAX_SESSION_FILTER_UUIDS + 1}); use ["*"] for all`,
    },
    {
      // A JavaScript caller (no compiler) can hand us anything.
      name: "a bare string is not a list",
      input: "session-a",
      message: "INVALID_PARAM: 'sessions' must be a list of strings",
    },
    {
      name: "a non-string entry",
      input: [42],
      message: "INVALID_PARAM: 'sessions' must be a list of strings",
    },
  ];

  for (const testCase of rejectedCases) {
    it(testCase.name, () => {
      assert.throws(
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        () => normalizeSessions(testCase.input as any),
        (thrown: unknown) => {
          assert.ok(thrown instanceof AnhurError);
          assert.equal((thrown as AnhurError).message, testCase.message);
          return true;
        },
      );
    });
  }
});

// ── Layer 2: what actually reaches the wire ───────────────────

const emptyEnvelope = { results: [], records: [], count: 0 };

/** Installs a fetch stub that records the last request and returns an empty hit set. */
function withCapturedFetch(): {
  captured: { url: string; body: Record<string, unknown>; count: number };
  restore: () => void;
} {
  const originalFetch = globalThis.fetch;
  const captured = { url: "", body: {} as Record<string, unknown>, count: 0 };
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    captured.url = String(input);
    captured.body = JSON.parse(String(init?.body ?? "{}"));
    captured.count += 1;
    return new Response(JSON.stringify(emptyEnvelope), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }) as typeof fetch;
  return { captured, restore: () => (globalThis.fetch = originalFetch) };
}

describe("session filter on the wire", () => {
  it("search() sends `sessions` in the body and no legacy `uuid`", async () => {
    const { captured, restore } = withCapturedFetch();
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await mem.search("hello", ["session-a", "session-b"]);
      assert.deepEqual(captured.body.sessions, ["session-a", "session-b"]);
      assert.ok(!("uuid" in captured.body));
    } finally {
      restore();
    }
  });

  it("searchSession() sends the singleton filter", async () => {
    const { captured, restore } = withCapturedFetch();
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await mem.searchSession("hello", "conv-42");
      assert.deepEqual(captured.body.sessions, ["conv-42"]);
      assert.ok(!("uuid" in captured.body));
    } finally {
      restore();
    }
  });

  it("the wildcard reaches the wire verbatim", async () => {
    const { captured, restore } = withCapturedFetch();
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      await mem.search("hello", sessionsAll());
      assert.deepEqual(captured.body.sessions, ["*"]);
    } finally {
      restore();
    }
  });

  it("the GET searches repeat the `sessions` query key", async () => {
    const { captured, restore } = withCapturedFetch();
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });

      await mem.smartSearch("engineering", ["conv-a", "conv-b"]);
      assert.deepEqual(
        new URL(captured.url).searchParams.getAll("sessions"),
        ["conv-a", "conv-b"],
      );

      await mem.searchByType("fact", sessionsAll());
      assert.deepEqual(new URL(captured.url).searchParams.getAll("sessions"), [
        "*",
      ]);
    } finally {
      restore();
    }
  });

  it("a rejected filter never reaches the network", async () => {
    const { captured, restore } = withCapturedFetch();
    try {
      const mem = new Memory({ apiKey: "key", userId: "u" });
      const badFilters: Record<string, string[]> = {
        absent: undefined as unknown as string[],
        empty: [],
        mixed: [SESSION_WILDCARD, "conv-a"],
        emptyEntry: [""],
      };
      const callers: Record<string, (bad: string[]) => Promise<unknown>> = {
        search: (bad) => mem.search("q", bad),
        searchSessions: (bad) => mem.searchSessions("q", bad),
        searchTenantShared: (bad) => mem.searchTenantShared("q", bad),
        searchClientShared: (bad) => mem.searchClientShared("q", bad),
        searchShared: (bad) => mem.searchShared("q", bad),
        recall: (bad) => mem.recall("q", bad, 5),
        smartSearch: (bad) => mem.smartSearch("q", bad, 5),
        searchByType: (bad) => mem.searchByType("fact", bad, 5),
      };

      for (const [filterName, badFilter] of Object.entries(badFilters)) {
        for (const [methodName, callMethod] of Object.entries(callers)) {
          await assert.rejects(
            () => callMethod(badFilter),
            (thrown: unknown) => {
              assert.ok(
                thrown instanceof AnhurError,
                `${methodName}/${filterName} threw ${String(thrown)}`,
              );
              assert.ok(
                (thrown as AnhurError).message.startsWith("INVALID_PARAM: "),
                `${methodName}/${filterName}: ${(thrown as AnhurError).message}`,
              );
              return true;
            },
          );
        }
      }

      assert.equal(
        captured.count,
        0,
        "a rejected session filter still produced an HTTP request",
      );
    } finally {
      restore();
    }
  });
});
