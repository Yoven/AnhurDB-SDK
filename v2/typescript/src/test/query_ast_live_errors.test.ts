/**
 * LIVE: the AST error contract, and the mistakes the server ACCEPTS in silence.
 *
 * Two different contracts live here and a caller must not confuse them:
 *
 *   1. REJECTED — HTTP 400, surfaced as `AnhurQueryError` with
 *      kind "invalid_request", retryable false, statusCode 400. The server's
 *      own sentence is embedded (escaped) in `.message`.
 *   2. ACCEPTED AND IGNORED — HTTP 200 with a result set that is not the one
 *      the caller thinks they asked for. These are the dangerous ones: a
 *      failure wearing a success status. Every case in the second block was
 *      confirmed against production; each is a place where a reasonable person
 *      would have predicted a 400 and does not get one.
 *
 *     ANHUR_AST_LIVE=1 ANHUR_API_KEY=... npm test
 */
import { after, before, describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { AnhurQueryError } from "../types.js";
import {
  ascending, LIVE_ENABLED, LIVE_SKIP_REASON, seedAstFixture, serverReason,
  teardownAstFixture, type AstFixture,
} from "./astLiveFixture.js";

let fixture: AstFixture;

/** Assert the server rejects `ast` with the full typed-error contract. */
async function serverRejects(ast: Record<string, unknown>, reason: RegExp): Promise<void> {
  await assert.rejects(
    () => fixture.memory.query(ast as never),
    (rejection: AnhurQueryError) => {
      assert.equal(rejection instanceof AnhurQueryError, true, "must be AnhurQueryError");
      assert.equal(rejection.statusCode, 400);
      assert.equal(rejection.kind, "invalid_request");
      assert.equal(rejection.retryable, false, "a malformed query must never be retried");
      assert.match(serverReason(rejection), reason);
      return true;
    },
  );
}

describe("live AST — error contract", { skip: LIVE_ENABLED ? false : LIVE_SKIP_REASON }, () => {
  before(async () => { fixture = await seedAstFixture("errors"); });
  after(async () => { await teardownAstFixture(fixture); });

  describe("rejections the SERVER owns (the builder cannot pre-empt them)", () => {
    it("rejects a column outside the whitelist", async () => {
      await serverRejects({ filters: { bogus_col: { $eq: 1 } } }, /invalid filter field: "bogus_col"/);
    });

    it("rejects an operator it does not implement", async () => {
      await serverRejects({ filters: { type: { $ne: "fact" } } }, /unsupported operator "\$ne"/);
    });

    it("rejects a bare value where an operator object belongs", async () => {
      await serverRejects({ filters: { type: "fact" } }, /must be an object of operators/);
    });

    it("rejects an operator-less filter — the shape an `undefined` value produces", async () => {
      // `whereEquals("type", options.type)` with an absent option builds
      // `{"type":{}}`, because JSON.stringify drops undefined. The builder does
      // not catch it; this 400 is the only feedback the caller gets.
      await serverRejects({ filters: { type: {} } }, /has no operator/);
    });

    it("rejects a non-scalar value, naming the Go type it received", async () => {
      await serverRejects({ filters: { type: { $eq: { a: 1 } } } }, /got map\[string\]interface \{\}/);
      await serverRejects({ filters: { type: { $eq: ["fact"] } } }, /got \[\]interface \{\}/);
    });

    it("rejects an empty $in list — a boundary the builder lets through", async () => {
      await serverRejects({ filters: { type: { $in: [] } } }, /\$in requires a non-empty array of values/);
    });

    it("rejects a non-array $in value", async () => {
      await serverRejects({ filters: { type: { $in: "fact" } } }, /\$in requires a non-empty array of values/);
    });

    it("rejects a nested array inside $in", async () => {
      await serverRejects({ filters: { type: { $in: [["fact"]] } } }, /got \[\]interface \{\}/);
    });

    it("rejects top-level limit/offset instead of silently ignoring them", async () => {
      // This used to be an accept-and-ignore: a caller asking for 500 rows
      // quietly got 50. It is now a 400 precisely so the mistake is visible.
      await serverRejects({ limit: 5, filters: {} }, /must live inside the "pagination" object/);
      await serverRejects({ offset: 5, filters: {} }, /must live inside the "pagination" object/);
    });

    it("rejects a bad sort field, including the empty one a missing key produces", async () => {
      await serverRejects({ sort: [{ field: "bogus", order: "asc" }] }, /invalid sort field: "bogus"/);
      await serverRejects({ sort: [{ order: "asc" }] }, /invalid sort field: ""/);
    });

    it("rejects an oversized body with a DIFFERENT, shorter message", async () => {
      // Junior Tip [do not string-match the long message]: a body over 1 MiB
      // fails at MaxBytesReader and answers the bare 'invalid json ast',
      // WITHOUT the grammar hint the undecodable-body case carries. A client
      // matching the long text will not recognise this one.
      const oversize = {
        filters: {
          uuid: { $eq: fixture.mainSession },
          summary: { $in: Array.from({ length: 12000 }, (_, index) => "x".repeat(100) + index) },
        },
      };
      await assert.rejects(
        () => fixture.memory.query(oversize as never),
        (rejection: AnhurQueryError) => {
          assert.equal(rejection.statusCode, 400);
          assert.equal(serverReason(rejection), "invalid json ast");
          return true;
        },
      );
    });
  });

  describe("ACCEPTED AND IGNORED — HTTP 200 where a 400 would be kinder", () => {
    it("parses `select` and then ignores it, returning whole records", async () => {
      const result = await fixture.memory.query(
        fixture.scoped().select("id").orderBy("id", "asc").build(),
      );
      assert.deepEqual(result.records.map((record) => record.id), fixture.visibleIds);
      assert.ok(result.records[0].summary, "select:['id'] must NOT strip the other columns");
    });

    it("ignores an unknown top-level key entirely", async () => {
      const result = await fixture.memory.query({
        unknown_top: "x",
        filters: { uuid: { $eq: fixture.mainSession } },
        pagination: { limit: 1000 },
      } as never);
      assert.deepEqual(ascending(result.records.map((record) => record.id)), fixture.visibleIds);
    });

    it("silently sorts DESC on an unrecognised sort direction", async () => {
      // The direction whitelist is ASC/DESC/asc/desc; anything else falls back
      // to DESC without a word. TypeScript's union type is the only thing
      // stopping this, and a cast or a JavaScript caller walks straight past it.
      const result = await fixture.memory.query({
        filters: { uuid: { $eq: fixture.mainSession } },
        sort: [{ field: "id", order: "sideways" }],
        pagination: { limit: 1000 },
      } as never);
      assert.deepEqual(
        result.records.map((record) => record.id),
        [...fixture.visibleIds].reverse(),
      );
    });

    it("silently CLAMPS an oversized limit rather than rejecting it", async () => {
      const result = await fixture.memory.query({
        filters: { uuid: { $eq: fixture.mainSession } },
        pagination: { limit: 5000 },
      } as never);
      assert.equal(result.count, fixture.visibleIds.length);
    });

    it("silently replaces a non-positive limit with the default 50", async () => {
      const result = await fixture.memory.query({
        filters: { uuid: { $eq: fixture.mainSession } },
        pagination: { limit: 0 },
      } as never);
      assert.equal(result.count, fixture.visibleIds.length);
    });

    it("accepts and SKIPS the `semantic_search` pseudo-key, contributing nothing", async () => {
      // Not a column: the server logs it and moves on. A caller who believes
      // they added a semantic leg gets a purely structural query and no warning.
      const result = await fixture.memory.query({
        filters: { semantic_search: { query: "anything at all" }, uuid: { $eq: fixture.mainSession } },
        pagination: { limit: 1000 },
      } as never);
      assert.deepEqual(ascending(result.records.map((record) => record.id)), fixture.visibleIds);
    });

    it("TRAP: $eq null answers 200 with an empty page, on every column", async () => {
      // `null` is in the server's scalar set, so it compiles to `col = ?` bound
      // to NULL — never true in SQL. `superseded_by IS NULL` is pinned on EVERY
      // returnable row, so the honest answer is "all of them"; the caller gets
      // zero, with no error. Go cannot express this; TypeScript and Python can,
      // which makes the trap theirs alone.
      const result = await fixture.memory.query(
        fixture.scoped().whereEquals("superseded_by", null).build(),
      );
      assert.equal(result.count, 0);
      assert.deepEqual(result.records, []);
    });

    it("normalises the wire's `records: null` into an empty array", async () => {
      // The server returns a nil Go slice on zero hits, so the wire literally
      // carries "records": null — never []. Callers index this; the SDK must
      // absorb it or every empty result is a TypeError.
      const result = await fixture.memory.query(
        fixture.scoped().whereEquals("type", "no-such-type").build(),
      );
      assert.deepEqual(result.records, []);
      assert.equal(result.count, 0);
    });
  });
});
