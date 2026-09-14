/**
 * LIVE: the null trap, pinned from BOTH ends — the client refuses it, the server allows it.
 *
 * WHY THIS FILE EXISTS
 * --------------------
 * Two facts about `null` in an AST filter are true at the same time, and losing
 * either one causes a different accident:
 *
 *   1. THE SERVER accepts `null` as a scalar and answers HTTP 200 with ZERO
 *      rows, on every column, because it compiles the predicate to `col = ?`
 *      bound to NULL — never true in SQL. No error, no warning. Confirmed live
 *      against production on 2026-09-14, including on `superseded_by`, a column
 *      that is NULL on every row the endpoint can return: the honest answer
 *      there is "all of them" and the caller gets none.
 *   2. THE SDK (2.1.0+) refuses to build that filter at all. Commit 63e3dae
 *      added the guard precisely because fact 1 is a silent wrong answer, and
 *      the AST grammar has no `$exists`, no `$ne` and no IS NULL, so "this
 *      column is null" is not expressible through POST /api/v1/query under any
 *      spelling.
 *
 * Those two facts landed in the SAME commit and contradicted each other in the
 * suite: `query_ast_live_errors.test.ts` still asserted a 200 for a call the new
 * guard now stops inside the process. Gated behind `ANHUR_AST_LIVE`, it never
 * ran in CI, and the contradiction shipped in 2.1.0.
 *
 * Junior Tip [why fact 1 is not simply deleted, 2026-09-14]: a suite that only
 * records "the SDK refuses null" leaves nothing explaining WHY the refusal is
 * worth its breaking change. The next person to meet the guard, with no record
 * of the server's behaviour, removes it as over-zealous validation and restores
 * the silent empty page. So fact 1 is still proven here — through a RAW AST
 * object, which `Memory.query()` still forwards unvalidated by design.
 *
 *     ANHUR_AST_LIVE=1 ANHUR_API_KEY=... npm test
 */
import { after, before, describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { AnhurQueryError } from "../types.js";
import {
  ascending, LIVE_ENABLED, LIVE_SKIP_REASON, seedAstFixture,
  teardownAstFixture, type AstFixture,
} from "./astLiveFixture.js";

let fixture: AstFixture;

/**
 * The columns the trap is walked on. `superseded_by` is the sharp one: the
 * server pins `superseded_by IS NULL` on every returnable row, so `= null` is
 * the caller's most natural way to ask for exactly the rows they already have.
 */
const NULL_TRAP_COLUMNS = ["superseded_by", "prefix", "valid_from"];

/**
 * Run `build` with `globalThis.fetch` counted, and report what it threw.
 *
 * `build` is synchronous on purpose: the 2.1.0 guards fire while the builder is
 * still assembling the AST, so the refusal happens before any request object
 * exists.
 *
 * Junior Tip [why the fetch counter and not just `assert.throws`]: an exception
 * alone does not say WHERE the refusal happened. A guard that somehow ran after
 * the POST would still throw and the test would still pass, while the server had
 * already done the work and the caller had already paid the round trip.
 * `httpCalls === 0` is the difference between "the SDK complained" and "the SDK
 * refused", and only the second one is the contract.
 */
function countHttpCalls(build: () => unknown): { thrown?: unknown; httpCalls: number } {
  const originalFetch = globalThis.fetch;
  let httpCalls = 0;
  globalThis.fetch = ((...fetchArguments: Parameters<typeof fetch>) => {
    httpCalls += 1;
    return originalFetch(...fetchArguments);
  }) as typeof fetch;
  try {
    build();
    return { httpCalls };
  } catch (thrown: unknown) {
    return { thrown, httpCalls };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

/** The full typed-error contract for a refusal that never left the process. */
function assertClientSideRejection(thrown: unknown, label: string): void {
  assert.ok(thrown instanceof AnhurQueryError, `${label}: must be AnhurQueryError, got ${thrown}`);
  const rejection = thrown as AnhurQueryError;
  assert.equal(rejection.kind, "invalid_request", `${label}: kind must be invalid_request`);
  assert.equal(rejection.retryable, false, `${label}: a malformed query must never be retried`);
  assert.equal(
    rejection.statusCode,
    undefined,
    `${label}: statusCode must stay unset — nothing was ever sent`,
  );
  assert.match(rejection.message, /null/i, `${label}: the message must name the null operand`);
}

describe("live AST — the null contract", { skip: LIVE_ENABLED ? false : LIVE_SKIP_REASON }, () => {
  before(async () => { fixture = await seedAstFixture("null-contract"); });
  after(async () => { await teardownAstFixture(fixture); });

  describe("what the SERVER does (raw AST, bypassing the builder)", () => {
    it("answers 200 with an empty page for $eq null, on every column", async () => {
      for (const column of NULL_TRAP_COLUMNS) {
        const result = await fixture.memory.query({
          filters: { uuid: { $eq: fixture.mainSession }, [column]: { $eq: null } },
          pagination: { limit: 200 },
        } as never);
        assert.equal(
          result.count,
          0,
          `${column}: $eq null matched rows — the server no longer compiles it to \`col = NULL\`. ` +
            `That is a CONTRACT CHANGE; re-read the guard in queryGuards.ts before touching it.`,
        );
        assert.deepEqual(result.records, []);
      }
      assert.ok(
        fixture.visibleIds.length > 0,
        "every visible row satisfies `superseded_by IS NULL`, and the query above " +
          "returned none of them — that gap IS the trap",
      );
    });

    it("treats a null ELEMENT inside $in as inert, not as an error", async () => {
      // `score IN (7, '9', NULL)` is not an SQL error: the NULL element simply
      // never matches, so the rows the OTHER elements matched still come back.
      // A caller who wrote the null on purpose ("7, 9, or unset") gets a
      // silently incomplete answer — which is what the builder guard prevents.
      const result = await fixture.memory.query({
        filters: { uuid: { $eq: fixture.mainSession }, score: { $in: [7, "9", null] } },
        pagination: { limit: 200 },
      } as never);
      const expectedIds = ascending(
        fixture.visibleIds.filter((id) => fixture.scoreOf[id] === 7 || fixture.scoreOf[id] === 9),
      );
      assert.deepEqual(
        ascending(result.records.map((record) => record.id)),
        expectedIds,
        "the null element must be inert: SQLite affinity still converts the TEXT '9'",
      );
    });
  });

  describe("what the BUILDER does (2.1.0+): refuse, before anything ships", () => {
    it("refuses $eq null on every column, with nothing on the wire", () => {
      for (const column of NULL_TRAP_COLUMNS) {
        const { thrown, httpCalls } = countHttpCalls(() =>
          fixture.scoped().whereEquals(column, null).build(),
        );
        assert.equal(httpCalls, 0, `${column}: the refused filter still hit the wire`);
        assertClientSideRejection(thrown, column);
      }
    });

    it("refuses a null element inside $in, with nothing on the wire", () => {
      const { thrown, httpCalls } = countHttpCalls(() =>
        fixture.scoped().where("score", "$in", [7, "9", null]).build(),
      );
      assert.equal(httpCalls, 0, "the refused $in still hit the wire");
      assertClientSideRejection(thrown, "score $in");
    });
  });
});
