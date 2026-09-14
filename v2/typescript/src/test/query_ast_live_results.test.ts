/**
 * LIVE: does each AST operator return exactly the rows it should?
 *
 * This is the half of the AST suite a mock cannot replace. Asserting HTTP 200
 * proves nothing about an operator; every test here computes the expected id
 * set from the fixture's own known column values and demands the server return
 * that set, in that order. The error contract lives in
 * `query_ast_live_errors.test.ts`; the byte-level builder pins are offline in
 * `query_builder_wire.test.ts`.
 *
 *     ANHUR_AST_LIVE=1 ANHUR_API_KEY=... npm test
 */
import { after, before, describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { QueryBuilder } from "../query.js";
import {
  ascending, LIVE_ENABLED, LIVE_SKIP_REASON, seedAstFixture, teardownAstFixture,
  type AstFixture,
} from "./astLiveFixture.js";

let fixture: AstFixture;

describe("live AST — results", { skip: LIVE_ENABLED ? false : LIVE_SKIP_REASON }, () => {
  before(async () => { fixture = await seedAstFixture("results"); });
  after(async () => { await teardownAstFixture(fixture); });

  describe("$eq on the columns a caller actually controls", () => {
    it("returns exactly the rows of that type", async () => {
      const { scoped, idsFor, visibleIds, typeOf } = fixture;
      assert.deepEqual(
        ascending(await idsFor(scoped().whereEquals("type", "fact").build())),
        ascending(visibleIds.filter((id) => typeOf[id] === "fact")),
      );
    });

    it("returns exactly one row for a unique id and for a unique score", async () => {
      const { scoped, idsFor, ids } = fixture;
      assert.deepEqual(await idsFor(scoped().whereEquals("id", ids.risk7).build()), [ids.risk7]);
      assert.deepEqual(await idsFor(scoped().whereEquals("score", 5).build()), [ids.decision5]);
    });

    it("matches an exact summary string", async () => {
      const { scoped, idsFor, ids } = fixture;
      assert.deepEqual(
        await idsFor(scoped().whereEquals("summary", "ast-teste live risk7").build()),
        [ids.risk7],
      );
    });

    it("answers with an EMPTY page, not an error, when nothing carries the value", async () => {
      const { scoped, idsFor } = fixture;
      assert.deepEqual(await idsFor(scoped().whereEquals("type", "no-such-type").build()), []);
    });
  });

  describe("the two implicit predicates every query silently carries", () => {
    it("hides archived rows until the caller filters `archived` explicitly", async () => {
      // The server appends `AND archived = 0` UNLESS `archived` appears in
      // filters. That carve-out is the ONLY way to see a deleted row, and a
      // caller who does not know it will conclude the row is gone forever.
      const { scoped, idsFor, ids, visibleIds } = fixture;
      assert.equal(visibleIds.includes(ids.archived4), false, "archived row must be hidden by default");
      assert.deepEqual(await idsFor(scoped().whereEquals("archived", 1).build()), [ids.archived4]);
      assert.deepEqual(
        ascending(await idsFor(scoped().whereEquals("archived", 0).build())),
        visibleIds,
      );
    });

    it("confines the page to one session when `uuid` is filtered", async () => {
      // Junior Tip [REST has no session scope of its own]: the handler always
      // passes sessionUUID "" to RunASTQuery, so over HTTP the ONLY thing
      // confining a query to a chat is a `uuid` filter the caller wrote. Forget
      // it and you page the whole tenant. gRPC/MCP scope from outside the
      // payload; REST does not.
      const { idsFor, ids, otherSession } = fixture;
      assert.deepEqual(
        await idsFor(new QueryBuilder().whereEquals("uuid", otherSession).limit(1000).build()),
        [ids.other5],
      );
    });
  });

  describe("ordered comparisons return exactly the predicted subset", () => {
    const THRESHOLD = 5;
    const CASES: ReadonlyArray<["$gt" | "$gte" | "$lt" | "$lte", (score: number) => boolean]> = [
      ["$gt", (score) => score > THRESHOLD],
      ["$gte", (score) => score >= THRESHOLD],
      ["$lt", (score) => score < THRESHOLD],
      ["$lte", (score) => score <= THRESHOLD],
    ];
    for (const [operator, predicate] of CASES) {
      it(`score ${operator} ${THRESHOLD}`, async () => {
        const { scoped, idsFor, visibleIds, scoreOf } = fixture;
        assert.deepEqual(
          ascending(await idsFor(scoped().where("score", operator, THRESHOLD).build())),
          ascending(visibleIds.filter((id) => predicate(scoreOf[id]))),
        );
      });
    }

    it("returns an empty page past the boundary, never an error", async () => {
      const { scoped, idsFor } = fixture;
      assert.deepEqual(await idsFor(scoped().where("score", "$gt", 9).build()), []);
    });

    it("compares timestamps as strings, in both directions", async () => {
      const { scoped, idsFor, visibleIds } = fixture;
      assert.deepEqual(
        ascending(await idsFor(scoped().where("created_at", "$gt", "2000-01-01T00:00:00Z").build())),
        visibleIds,
      );
      assert.deepEqual(
        await idsFor(scoped().where("created_at", "$lt", "2000-01-01T00:00:00Z").build()),
        [],
      );
    });
  });

  describe("$in", () => {
    it("returns the union of the listed values", async () => {
      const { scoped, idsFor, visibleIds, typeOf } = fixture;
      assert.deepEqual(
        ascending(await idsFor(scoped().where("type", "$in", ["fact", "risk"]).build())),
        ascending(visibleIds.filter((id) => ["fact", "risk"].includes(typeOf[id]))),
      );
    });

    it("works with a single element", async () => {
      const { scoped, idsFor, ids } = fixture;
      assert.deepEqual(
        await idsFor(scoped().where("type", "$in", ["decision"]).build()),
        [ids.decision5],
      );
    });

    it("silently drops list values nothing carries", async () => {
      const { scoped, idsFor, ids } = fixture;
      assert.deepEqual(
        ascending(await idsFor(scoped().where("id", "$in", [ids.fact3, ids.risk7, 999999999]).build())),
        ascending([ids.fact3, ids.risk7]),
      );
    });

    it("accepts a 1000-element list", async () => {
      // The grammar puts NO cap on $in length; the only ceiling is the 1 MiB
      // body limit, exercised in the errors suite.
      const { scoped, idsFor, ids } = fixture;
      const longList = [...Array(999).keys()].map((offset) => offset + 1).concat([ids.fact9]);
      assert.deepEqual(await idsFor(scoped().where("id", "$in", longList).build()), [ids.fact9]);
    });
  });

  describe("combination — the grammar's ONLY nesting is an implicit AND", () => {
    it("ANDs two operators on one field into a closed range", async () => {
      // Junior Tip [there is no $and/$or/$not anywhere]: `filters` is flat at a
      // fixed depth of two, and the server ANDs every predicate into one WHERE
      // clause. Precedence cannot arise. An SDK that grew an `or()` would be
      // advertising a capability the server cannot execute.
      const { scoped, idsFor, visibleIds, scoreOf } = fixture;
      assert.deepEqual(
        ascending(await idsFor(scoped().where("score", "$gte", 3).where("score", "$lte", 6).build())),
        ascending(visibleIds.filter((id) => scoreOf[id] >= 3 && scoreOf[id] <= 6)),
      );
    });

    it("ANDs predicates across three different fields", async () => {
      const { scoped, idsFor, visibleIds, scoreOf, typeOf } = fixture;
      assert.deepEqual(
        ascending(await idsFor(scoped().whereEquals("type", "fact").where("score", "$gt", 5).build())),
        ascending(visibleIds.filter((id) => typeOf[id] === "fact" && scoreOf[id] > 5)),
      );
    });

    it("answers a contradiction with an empty page, never an error", async () => {
      const { scoped, idsFor } = fixture;
      assert.deepEqual(
        await idsFor(scoped().where("score", "$gt", 9).where("score", "$lt", 1).build()),
        [],
      );
    });

    it("lets the LAST value win when one operator is set twice on a field", async () => {
      const { scoped, idsFor, ids } = fixture;
      assert.deepEqual(
        await idsFor(scoped().whereEquals("type", "fact").whereEquals("type", "risk").build()),
        [ids.risk7],
      );
    });
  });

  describe("sort and pagination", () => {
    it("orders ascending and descending on demand, and defaults to id DESC", async () => {
      const { scoped, idsFor, visibleIds } = fixture;
      assert.deepEqual(await idsFor(scoped().orderBy("id", "asc").build()), visibleIds);
      assert.deepEqual(await idsFor(scoped().orderBy("id", "desc").build()), [...visibleIds].reverse());
      assert.deepEqual(await idsFor(scoped().build()), [...visibleIds].reverse());
    });

    it("applies several sort terms left to right", async () => {
      const { scoped, idsFor, visibleIds, typeOf } = fixture;
      assert.deepEqual(
        await idsFor(scoped().orderBy("type", "asc").orderBy("id", "asc").build()),
        [...visibleIds].sort((left, right) =>
          typeOf[left] < typeOf[right] ? -1 : typeOf[left] > typeOf[right] ? 1 : left - right),
      );
    });

    it("pages disjointly on limit/offset and runs off the end cleanly", async () => {
      const { idsFor, visibleIds, mainSession } = fixture;
      const pinned = () => new QueryBuilder().whereEquals("uuid", mainSession).orderBy("id", "asc");
      assert.deepEqual(await idsFor(pinned().limit(2).offset(0).build()), visibleIds.slice(0, 2));
      assert.deepEqual(await idsFor(pinned().limit(2).offset(2).build()), visibleIds.slice(2, 4));
      assert.deepEqual(await idsFor(pinned().limit(2).offset(999).build()), []);
    });
  });
});
