/**
 * QueryBuilder → wire bytes for POST /api/v1/query.
 *
 * WHY THIS FILE EXISTS — and why it is not a mock that agrees with itself.
 *
 * Every expected body below was captured from the REAL production router
 * (https://anhurdb.yoven.ai) on 2026-09-13 while executing a 110-case matrix
 * against a disposable `ast-teste-*` session, and each one was confirmed to
 * return the exact record set the grammar predicts. The literals here are
 * therefore transcriptions of verified production behaviour, NOT restatements
 * of what `query.ts` happens to do today. If the builder drifts, these fail.
 *
 * Junior Tip [why pin bytes instead of asserting "it works"]: the AST surface
 * has no schema negotiation. The server decodes the body into a fixed Go
 * struct and whitelists every column name by hand; a renamed key, a nested
 * wrapper, or a float where an int belongs is not a type error anywhere in
 * TypeScript — it is an HTTP 400 (or, worse, a silently different result set)
 * that only a real request reveals. Pinning the bytes is the cheapest way to
 * make an invisible wire contract fail loudly at build time.
 *
 * The live half of this matrix lives in `query_ast_live.test.ts`.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { QueryBuilder } from "../query.js";
import type { AstQuery } from "../types.js";

/** Serialise exactly as `Memory.query` does, so we compare real wire bytes. */
function wireBytes(ast: AstQuery): string {
  return JSON.stringify(ast);
}

describe("QueryBuilder wire shape — top level", () => {
  it("sends filters/pagination FLAT, never wrapped in {query:...}", () => {
    assert.equal(
      wireBytes(new QueryBuilder().whereEquals("type", "fact").build()),
      '{"filters":{"type":{"$eq":"fact"}},"pagination":{"limit":50,"offset":0}}',
    );
  });

  it("always emits pagination, even when the caller never touched it", () => {
    // Junior Tip [why this matters]: the server defaults limit to 50 itself,
    // so an absent pagination block would still work — but the three SDKs must
    // put the SAME bytes on the wire for a byte-for-byte parity diff to mean
    // anything. Emitting it unconditionally is the parity contract.
    assert.equal(
      wireBytes(new QueryBuilder().build()),
      '{"filters":{},"pagination":{"limit":50,"offset":0}}',
    );
  });

  it("omits `sort` and `select` entirely when unused", () => {
    const ast = new QueryBuilder().whereEquals("id", 1).build();
    assert.equal("sort" in ast, false);
    assert.equal("select" in ast, false);
  });

  it("emits `select` when asked, deduped, first-seen order preserved", () => {
    // The server PARSES `select` and then ignores it — the SQL projection is a
    // fixed column list. Confirmed live: a select:["id"] query still returns
    // full records. We send it for forward-compat and Python parity only.
    assert.equal(
      wireBytes(new QueryBuilder().select("summary", "id", "summary").build()),
      '{"filters":{},"pagination":{"limit":50,"offset":0},"select":["summary","id"]}',
    );
  });
});

describe("QueryBuilder wire shape — all six operators", () => {
  // The server implements EXACTLY these six (astQuerySupportedOperators).
  // $ne / $nin / $like / $exists / $regex do not exist and are HTTP 400.
  const OPERATOR_CASES: ReadonlyArray<[string, AstQuery, string]> = [
    [
      "$eq",
      new QueryBuilder().where("score", "$eq", 5).build(),
      '{"filters":{"score":{"$eq":5}},"pagination":{"limit":50,"offset":0}}',
    ],
    [
      "$gt",
      new QueryBuilder().where("score", "$gt", 5).build(),
      '{"filters":{"score":{"$gt":5}},"pagination":{"limit":50,"offset":0}}',
    ],
    [
      "$gte",
      new QueryBuilder().where("score", "$gte", 5).build(),
      '{"filters":{"score":{"$gte":5}},"pagination":{"limit":50,"offset":0}}',
    ],
    [
      "$lt",
      new QueryBuilder().where("score", "$lt", 5).build(),
      '{"filters":{"score":{"$lt":5}},"pagination":{"limit":50,"offset":0}}',
    ],
    [
      "$lte",
      new QueryBuilder().where("score", "$lte", 5).build(),
      '{"filters":{"score":{"$lte":5}},"pagination":{"limit":50,"offset":0}}',
    ],
    [
      "$in",
      new QueryBuilder().where("type", "$in", ["fact", "risk"]).build(),
      '{"filters":{"type":{"$in":["fact","risk"]}},"pagination":{"limit":50,"offset":0}}',
    ],
  ];

  for (const [operatorName, ast, expectedBody] of OPERATOR_CASES) {
    it(`serialises ${operatorName} exactly as the server parses it`, () => {
      assert.equal(wireBytes(ast), expectedBody);
    });
  }

  it("carries every scalar JSON type the server accepts", () => {
    // assertASTScalar admits exactly nil | bool | float64 | string.
    assert.equal(
      wireBytes(
        new QueryBuilder()
          .whereEquals("consolidated", true)
          .whereEquals("score", 7)
          .whereEquals("type", "fact")
          .build(),
      ),
      '{"filters":{"consolidated":{"$eq":true},"score":{"$eq":7},"type":{"$eq":"fact"}},' +
        '"pagination":{"limit":50,"offset":0}}',
    );
  });
});

describe("QueryBuilder wire shape — the only nesting the grammar has", () => {
  it("ANDs two operators on ONE field into one condition object", () => {
    // Junior Tip [there is no $and/$or/$not, and that is the whole story]:
    // `filters` is flat at a FIXED depth of two — column → {operator → value}
    // — and the server unconditionally ANDs every predicate into one WHERE.
    // Precedence therefore never arises. Any SDK that grows an `or()` would be
    // inventing a capability the server cannot execute, which is a silent-loss
    // bug, not a feature.
    assert.equal(
      wireBytes(
        new QueryBuilder().where("score", "$gte", 3).where("score", "$lte", 6).build(),
      ),
      '{"filters":{"score":{"$gte":3,"$lte":6}},"pagination":{"limit":50,"offset":0}}',
    );
  });

  it("ANDs predicates across several fields", () => {
    assert.equal(
      wireBytes(
        new QueryBuilder()
          .whereEquals("uuid", "sess-1")
          .whereEquals("type", "fact")
          .where("score", "$gt", 5)
          .build(),
      ),
      '{"filters":{"uuid":{"$eq":"sess-1"},"type":{"$eq":"fact"},"score":{"$gt":5}},' +
        '"pagination":{"limit":50,"offset":0}}',
    );
  });

  it("lets the LAST value win when the same operator repeats on a field", () => {
    assert.equal(
      wireBytes(
        new QueryBuilder().whereEquals("type", "fact").whereEquals("type", "risk").build(),
      ),
      '{"filters":{"type":{"$eq":"risk"}},"pagination":{"limit":50,"offset":0}}',
    );
  });
});

describe("QueryBuilder wire shape — sort", () => {
  it("emits {field, order} in the caller's order, not sorted", () => {
    assert.equal(
      wireBytes(
        new QueryBuilder().orderBy("type", "asc").orderBy("id", "asc").build(),
      ),
      '{"filters":{},"pagination":{"limit":50,"offset":0},' +
        '"sort":[{"field":"type","order":"asc"},{"field":"id","order":"asc"}]}',
    );
  });

  it("defaults the order argument to desc", () => {
    // Matches the server's own fallback: an absent or unrecognised direction
    // silently becomes DESC (astQueryAllowedSortOrders), and the default page
    // with no sort at all is ORDER BY id DESC.
    assert.equal(
      wireBytes(new QueryBuilder().orderBy("created_at").build()),
      '{"filters":{},"pagination":{"limit":50,"offset":0},' +
        '"sort":[{"field":"created_at","order":"desc"}]}',
    );
  });

  it("accepts every one of the 17 whitelisted columns as a sort key", () => {
    const SERVER_WHITELIST = [
      "id", "uuid", "type", "dimension", "weight", "score", "status",
      "consolidated", "archived", "created_at", "updated_at", "prefix",
      "metadata", "summary", "superseded_by", "valid_from", "valid_until",
    ];
    for (const columnName of SERVER_WHITELIST) {
      assert.doesNotThrow(
        () => new QueryBuilder().orderBy(columnName, "asc").build(),
        `orderBy("${columnName}") must be accepted — it is in the server whitelist`,
      );
    }
    assert.equal(SERVER_WHITELIST.length, 17);
  });
});

describe("QueryBuilder wire shape — pagination", () => {
  it("puts limit/offset INSIDE pagination — top level is an HTTP 400", () => {
    // Junior Tip [why the server rejects instead of ignoring]: top-level
    // limit/offset used to be silently dropped, so a caller asking for 500 rows
    // quietly got 50 and never knew. rejectStrayTopLevelPagination now answers
    // 400 so the mistake is visible. The builder can never produce that shape;
    // this test is the proof it cannot.
    const body = JSON.parse(
      wireBytes(new QueryBuilder().limit(500).offset(100).build()),
    ) as Record<string, unknown>;
    assert.equal("limit" in body, false);
    assert.equal("offset" in body, false);
    assert.deepEqual(body.pagination, { limit: 500, offset: 100 });
  });

  it("accepts the boundaries the server enforces: 1 and 1000", () => {
    assert.deepEqual(new QueryBuilder().limit(1).build().pagination, { limit: 1, offset: 0 });
    assert.deepEqual(
      new QueryBuilder().limit(1000).build().pagination,
      { limit: 1000, offset: 0 },
    );
  });
});
