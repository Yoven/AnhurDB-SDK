/**
 * The four AST-grammar rejections the TypeScript builder gained on 2026-09-14,
 * driven through the REAL `QueryBuilder` — never a hand-built AST object.
 *
 * Junior Tip [why every case here goes through the builder]: a test that asserts
 * on a literal `{ filters: { type: { $eq: null } } }` proves only that the
 * literal was typed correctly. It would keep passing if `where()` were deleted.
 * The whole point of these guards is that they sit on the path a caller uses,
 * so the test has to take that path too — `new QueryBuilder().where(...)`.
 *
 * Each rejection closes a SILENT failure confirmed against the live router on
 * 2026-09-13: the server answered 200 to every one of them and returned rows
 * that did not mean what the caller asked for.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { QueryBuilder } from "../query.js";
import { AnhurQueryError } from "../types.js";

/**
 * Run `rejectingCall`, assert it threw the SDK's typed client-side rejection,
 * and hand the error back for a message assertion.
 *
 * Junior Tip [the shape is asserted on EVERY rejection, not once]: a caller
 * writes ONE catch block. If a single guard throws a bare `Error`, that block
 * misses it, and the miss is invisible until it happens in production. Checking
 * class + kind + retryable on every path is what makes "one catch block" true.
 */
function assertClientRejection(rejectingCall: () => unknown): AnhurQueryError {
  let thrown: unknown;
  try {
    rejectingCall();
  } catch (rejection) {
    thrown = rejection;
  }
  assert.ok(
    thrown instanceof AnhurQueryError,
    "expected a client-side AnhurQueryError, got: " + String(thrown),
  );
  const rejection = thrown as AnhurQueryError;
  assert.equal(rejection.name, "AnhurQueryError");
  assert.equal(rejection.kind, "invalid_request");
  assert.equal(rejection.retryable, false);
  // No HTTP happened, so there is no status to carry. Anything else would be a
  // fabricated status a caller could branch on.
  assert.equal(rejection.statusCode, undefined);
  return rejection;
}

describe("P1-2 — the sort DIRECTION is checked at runtime, not only by tsc", () => {
  it("rejects a direction the server would silently turn into DESC", () => {
    const rejection = assertClientRejection(() =>
      new QueryBuilder().orderBy("id", "sideways" as never),
    );
    assert.match(rejection.message, /QueryBuilder\.orderBy: order "sideways"/);
    assert.match(rejection.message, /Allowed: asc, desc/);
    // The message must name the CONSEQUENCE, because the server's own answer
    // (200, rows in DESC order) tells the caller nothing.
    assert.match(rejection.message, /SILENTLY sorts DESC/);
  });

  it("rejects every shape a plain-JavaScript caller can actually reach", () => {
    for (const badDirection of [
      "sideways",
      "ascending",
      "DESCENDING",
      "",
      " asc",
      "1",
    ]) {
      const rejection = assertClientRejection(() =>
        new QueryBuilder().orderBy("created_at", badDirection as never),
      );
      assert.match(
        rejection.message,
        /is not allowed/,
        `direction ${JSON.stringify(badDirection)} must be rejected`,
      );
    }
  });

  it("rejects non-string directions arriving from untyped JSON", () => {
    // `undefined` is deliberately absent: it triggers the default parameter,
    // which is the documented "no direction given" spelling, not a mistake.
    for (const badDirection of [null, 1, {}, ["asc"]]) {
      assertClientRejection(() =>
        new QueryBuilder().orderBy("weight", badDirection as never),
      );
    }
  });

  it("still accepts asc/desc, and folds the case the server also accepts", () => {
    assert.deepEqual(new QueryBuilder().orderBy("weight", "asc").build().sort, [
      { field: "weight", order: "asc" },
    ]);
    assert.deepEqual(
      new QueryBuilder().orderBy("weight", "DESC" as never).build().sort,
      [{ field: "weight", order: "desc" }],
    );
    // Default direction is unchanged.
    assert.deepEqual(new QueryBuilder().orderBy("id").build().sort, [
      { field: "id", order: "desc" },
    ]);
  });

  it("leaves the builder untouched when it refuses a direction", () => {
    // Junior Tip [a rejected call must not half-apply]: if the clause were
    // pushed and THEN validated, a caller catching the error and building
    // anyway would ship the clause it was just told was illegal.
    const builder = new QueryBuilder().orderBy("id", "asc");
    assert.throws(() => builder.orderBy("weight", "sideways" as never));
    assert.deepEqual(builder.build().sort, [{ field: "id", order: "asc" }]);
  });
});

describe("P1-3 — a null filter value is refused, with the reason", () => {
  it("rejects $eq null: it can never match, and cannot be rewritten", () => {
    const rejection = assertClientRejection(() =>
      new QueryBuilder().whereEquals("superseded_by", null),
    );
    assert.match(
      rejection.message,
      /QueryBuilder\.where: filter "superseded_by": \$eq cannot take null/,
    );
    // WHY, in the message: never-true SQL, and no operator to express it.
    assert.match(rejection.message, /col = NULL/);
    assert.match(rejection.message, /\$exists, \$ne or IS NULL/);
  });

  it("rejects null on EVERY operator, not just the obvious $eq", () => {
    for (const operator of ["$eq", "$gt", "$gte", "$lt", "$lte", "$in"] as const) {
      const rejection = assertClientRejection(() =>
        new QueryBuilder().where("weight", operator, null),
      );
      assert.match(
        rejection.message,
        new RegExp(`\\${operator} cannot take null`),
        `operator ${operator} must refuse null`,
      );
    }
  });

  it("rejects null through BOTH public spellings of a value", () => {
    // `whereEquals` is sugar over `where`; both must bite, or the guard is a
    // guard on one door of a room with two.
    assertClientRejection(() => new QueryBuilder().where("type", "$eq", null));
    assertClientRejection(() => new QueryBuilder().whereEquals("type", null));
  });

  it("rejects a null ELEMENT hiding inside a $in list", () => {
    // `col IN ('fact', NULL)` never matches on the NULL: that element is dead
    // weight the caller believes is doing work.
    const rejection = assertClientRejection(() =>
      new QueryBuilder().where("type", "$in", ["fact", null]),
    );
    assert.match(rejection.message, /\$in cannot take null/);
  });

  it("does not confuse null with the falsy values the grammar allows", () => {
    // 0, "" and false are all legal scalars; only null is impossible.
    assert.deepEqual(new QueryBuilder().whereEquals("weight", 0).build().filters, {
      weight: { $eq: 0 },
    });
    assert.deepEqual(new QueryBuilder().whereEquals("prefix", "").build().filters, {
      prefix: { $eq: "" },
    });
    assert.deepEqual(
      new QueryBuilder().whereEquals("archived", false).build().filters,
      { archived: { $eq: false } },
    );
  });

  it("leaves the builder untouched when it refuses a value", () => {
    const builder = new QueryBuilder().whereEquals("type", "fact");
    assert.throws(() => builder.whereEquals("status", null));
    assert.deepEqual(builder.build().filters, { type: { $eq: "fact" } });
  });
});

describe("$in with an empty array is refused client-side, as in Go", () => {
  it("rejects it instead of spending a round trip to be told 400", () => {
    const rejection = assertClientRejection(() =>
      new QueryBuilder().where("type", "$in", []),
    );
    assert.match(
      rejection.message,
      /filter "type": \$in requires a non-empty array of values/,
    );
  });

  it("still accepts a $in list that carries values", () => {
    assert.deepEqual(
      new QueryBuilder().where("type", "$in", ["fact", "risk"]).build().filters,
      { type: { $in: ["fact", "risk"] } },
    );
    // A single-element list is legal; only ZERO elements are not.
    assert.deepEqual(
      new QueryBuilder().where("type", "$in", ["fact"]).build().filters,
      { type: { $in: ["fact"] } },
    );
  });
});

describe("the widened guard does not reject anything the server accepts", () => {
  it("builds a full, legal query end to end", () => {
    const ast = new QueryBuilder()
      .where("type", "$in", ["fact", "decision"])
      .where("weight", "$gt", 0.8)
      .where("weight", "$lte", 1)
      .whereEquals("archived", false)
      .orderBy("created_at", "asc")
      .orderBy("id", "desc")
      .limit(200)
      .offset(50)
      .build();

    assert.deepEqual(ast.filters, {
      type: { $in: ["fact", "decision"] },
      weight: { $gt: 0.8, $lte: 1 },
      archived: { $eq: false },
    });
    assert.deepEqual(ast.sort, [
      { field: "created_at", order: "asc" },
      { field: "id", order: "desc" },
    ]);
    assert.deepEqual(ast.pagination, { limit: 200, offset: 50 });
  });

  it("keeps the pagination rejections on the same typed contract", () => {
    // These guards predate the change; they must not be the one path left
    // throwing a bare Error.
    assertClientRejection(() => new QueryBuilder().limit(0));
    assertClientRejection(() => new QueryBuilder().limit(1001));
    assertClientRejection(() => new QueryBuilder().offset(-1));
  });
});
