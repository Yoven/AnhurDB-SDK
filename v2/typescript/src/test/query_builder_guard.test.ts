/**
 * QueryBuilder client-side guard: what it catches, and what it provably does not.
 *
 * `query.ts` advertises that the grammar is "validated CLIENT-side here as an
 * early, actionable error, AND again server-side (HTTP 400) as the source of
 * truth". Until 2026-09-14 that claim covered exactly two things — the FIELD
 * NAME and the OPERATOR NAME — and everything else reached the server. The
 * guard now also covers the sort DIRECTION, a `null` VALUE and an empty `$in`
 * (see query_builder_rejections.test.ts for those); the cases at the bottom of
 * this file are what is STILL unchecked.
 *
 * Junior Tip [why a partial guard needs a test that names its holes]: a guard
 * the caller believes is total is worse than no guard. "The SDK validates my
 * query" invites callers to skip their own checks; the cases below are the
 * exhaustive list of what still gets through, each verified against the real
 * router on 2026-09-13. When someone widens the guard, the characterisation
 * tests at the bottom fail — deliberately — so the widening is a decision, not
 * an accident. Any such widening MUST land in Go, Python and TypeScript in the
 * same change; a guard that exists in one SDK only is a parity break.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { QueryBuilder } from "../query.js";
import { AnhurQueryError } from "../types.js";

describe("client-side guard — what it DOES reject", () => {
  it("rejects a column outside the 17-name server whitelist", () => {
    assert.throws(
      () => new QueryBuilder().where("bogus_col", "$eq", 1),
      /QueryBuilder\.where: field "bogus_col" is not allowed/,
    );
  });

  it("rejects a whitelisted column in the wrong case — the server is case-sensitive", () => {
    assert.throws(
      () => new QueryBuilder().where("TYPE", "$eq", "fact"),
      /field "TYPE" is not allowed/,
    );
  });

  it("rejects operators the server does not implement", () => {
    // Live: the server answers 400 `unsupported operator "$ne"`. Exposing $ne
    // would be a silent-loss bug, so it is absent from the union type AND from
    // the runtime set — the cast below is what a JavaScript caller can do.
    for (const absentOperator of ["$ne", "$nin", "$like", "$exists", "$regex", "$EQ"]) {
      assert.throws(
        () => new QueryBuilder().where("type", absentOperator as never, "x"),
        /is not supported/,
        `operator ${absentOperator} must be rejected client-side`,
      );
    }
  });

  it("rejects a non-whitelisted sort column", () => {
    assert.throws(
      () => new QueryBuilder().orderBy("bogus", "asc"),
      /QueryBuilder\.orderBy: field "bogus" is not allowed/,
    );
  });

  it("rejects a page size outside the server's 1..1000 window", () => {
    assert.throws(() => new QueryBuilder().limit(0), /must be between 1 and 1000/);
    assert.throws(() => new QueryBuilder().limit(-5), /must be between 1 and 1000/);
    assert.throws(() => new QueryBuilder().limit(1001), /must be between 1 and 1000/);
    assert.throws(() => new QueryBuilder().offset(-1), /offset cannot be negative/);
  });

  it("names every allowed column in the error, so the fix is in the message", () => {
    try {
      new QueryBuilder().where("consolidate_id", "$eq", 1);
      assert.fail("expected a client-side rejection");
    } catch (guardError) {
      // `consolidate_id` is the classic near-miss: it is a real record field and
      // an MCP tool whitelists it locally, but the AST endpoint does not.
      assert.match((guardError as Error).message, /Allowed: archived, consolidated, created_at/);
      assert.match((guardError as Error).message, /valid_until, weight$/);
    }
  });
});

describe("client-side guard — the ERROR TYPE it throws is the SDK's own", () => {
  it("throws AnhurQueryError with kind invalid_request and retryable false", () => {
    // Junior Tip [one mistake, one contract — fixed 2026-09-14]: filtering on
    // `bogus_col` used to produce a plain `Error` when the builder caught it
    // and an `AnhurQueryError` (kind "invalid_request", retryable false,
    // statusCode 400) when the same AST went straight to `memory.query()`. A
    // caller writing `catch (e) { if (e instanceof AnhurQueryError) show(e) }`
    // dropped every builder error on the floor, and one branching on `e.kind`
    // read `undefined` — which, worse, meant `retryable` was undefined on an
    // error that can NEVER succeed on a retry. Both paths are now the same
    // class and the same kind; only `statusCode` differs, and it is absent
    // client-side for the honest reason that no HTTP request happened.
    let thrown: unknown;
    try {
      new QueryBuilder().where("bogus_col", "$eq", 1);
    } catch (guardError) {
      thrown = guardError;
    }
    assert.ok(thrown instanceof AnhurQueryError);
    assert.equal((thrown as Error).name, "AnhurQueryError");
    assert.equal((thrown as AnhurQueryError).kind, "invalid_request");
    assert.equal((thrown as AnhurQueryError).retryable, false);
    assert.equal((thrown as AnhurQueryError).statusCode, undefined);
  });
});

describe("client-side guard — what it does NOT reject (verified live)", () => {
  // Each case below builds successfully and is answered by the SERVER. The
  // quoted message is the literal body production returned on 2026-09-13.
  it("lets a NON-ARRAY $in value through — server: same non-empty-array 400", () => {
    assert.deepEqual(
      new QueryBuilder().where("type", "$in", "fact").build().filters,
      { type: { $in: "fact" } },
    );
  });

  it("lets a nested array inside $in through — server: 'got []interface {}'", () => {
    assert.deepEqual(
      new QueryBuilder().where("type", "$in", [["fact"]]).build().filters,
      { type: { $in: [["fact"]] } },
    );
  });

  it("lets an OBJECT value through for a scalar operator — server: 'got map[string]interface {}'", () => {
    assert.deepEqual(
      new QueryBuilder().whereEquals("type", { a: 1 }).build().filters,
      { type: { $eq: { a: 1 } } },
    );
  });

  it("does not validate `select` against the whitelist at all", () => {
    // Harmless today only because the server parses `select` and ignores it.
    assert.deepEqual(new QueryBuilder().select("bogus_col").build().select, ["bogus_col"]);
  });

  it("does not validate a NON-ARRAY $in element type — server decides", () => {
    // Only the EMPTY array is refused client-side (all three SDKs). What each
    // element contains is left to the server, which names the offending type.
    assert.deepEqual(
      new QueryBuilder().where("type", "$in", [{ a: 1 }]).build().filters,
      { type: { $in: [{ a: 1 }] } },
    );
  });
});

describe("characterised DEFECTS — current behaviour pinned so a fix is deliberate", () => {
  it("BUG: build() shallow-copies filters, so later mutation rewrites a returned AST", () => {
    // Junior Tip [one level too shallow]: `build()` does
    // `filters: { ...this.filters }`, which copies the MAP but shares every
    // per-field condition OBJECT by reference. `sort` and `select` are copied
    // properly; `filters` is not. The consequence is real and silent:
    //
    //   const factQuery = builder.whereEquals("type","fact").build();
    //   builder.whereEquals("type","risk");        // meant as a second query
    //   await memory.query(factQuery);             // sends type=risk
    //
    // No error, no 400 — just the wrong rows under the right variable name.
    // The fix is a per-field copy in build(); it must land in all three SDKs.
    const builder = new QueryBuilder().whereEquals("type", "fact");
    const firstAst = builder.build();
    assert.deepEqual(firstAst.filters, { type: { $eq: "fact" } });

    builder.whereEquals("type", "risk");

    // CURRENT (defective) behaviour — the already-built AST mutated underneath.
    assert.deepEqual(
      firstAst.filters,
      { type: { $eq: "risk" } },
      "if this now says 'fact', the aliasing bug was fixed — update this test",
    );
  });

  it("BUG: limit(NaN) passes the range guard and serialises to null on the wire", () => {
    // `NaN < 1` and `NaN > 1000` are both false, so the 1..1000 guard admits it,
    // and JSON.stringify turns NaN into null. The server decodes
    // `{"limit":null}` into a Go int as a no-op (zero), so limit <= 0 falls back
    // to the default 50 and answers HTTP 200. Live-confirmed: a caller writing
    // `.limit(Number(userInput))` with junk input gets 50 rows and is never told.
    const nanLimit = new QueryBuilder().limit(Number.NaN).build();
    assert.ok(Number.isNaN(nanLimit.pagination?.limit as number));
    assert.equal(JSON.stringify(nanLimit.pagination), '{"limit":null,"offset":0}');

    const nanOffset = new QueryBuilder().offset(Number.NaN).build();
    assert.equal(JSON.stringify(nanOffset.pagination), '{"limit":50,"offset":null}');
  });

  it("BUG: a FRACTIONAL limit/offset is admitted and the server's 400 blames `filters`", () => {
    // The server decodes pagination as map[string]int, so 1.5 fails the decode
    // and produces the GENERIC parse error — whose text is
    // 'invalid json ast — filters must be an OBJECT keyed by column...'. The
    // fault is in pagination; the message points at filters. Rounding in the
    // builder would make the whole class unreachable.
    assert.equal(new QueryBuilder().limit(1.5).build().pagination?.limit, 1.5);
    assert.equal(new QueryBuilder().offset(2.7).build().pagination?.offset, 2.7);
  });

  it("BUG: an `undefined` value builds an operator-less filter the server must reject", () => {
    // `whereEquals("type", options.type)` with an absent option produces
    // `{"type":{}}` on the wire, because JSON.stringify drops undefined values.
    // The server answers 400 'filter "type" has no operator'. This is the most
    // likely real-world TypeScript mistake in the whole surface and it is the
    // one the guard does not catch.
    const ast = new QueryBuilder().whereEquals("type", undefined).build();
    assert.deepEqual(ast.filters, { type: { $eq: undefined } });
    assert.equal(JSON.stringify(ast.filters), '{"type":{}}');
  });

});
