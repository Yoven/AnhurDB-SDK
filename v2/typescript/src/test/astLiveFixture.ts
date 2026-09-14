/**
 * Disposable production fixture for the live AST query suites.
 *
 * The two live suites (`query_ast_live_results.test.ts` and
 * `query_ast_live_errors.test.ts`) both need a small set of records whose
 * columns they know exactly. This module seeds them into a throwaway
 * `ast-teste-*` session, hands back a typed handle, and deletes everything
 * afterwards. It is NOT a `.test.ts` file: it holds no assertions of its own
 * beyond the one that matters most, the fixture guard.
 *
 * Junior Tip [why the guard is here and not in each suite]: if the seed
 * silently does not land — wrong key, a session-first refusal, a 503 mid-write
 * — then every "expected an empty page" assertion downstream passes against an
 * empty database and the run goes green while proving nothing. That is the
 * single most dangerous failure mode of a live test, so `seedAstFixture`
 * refuses to return until it has SEEN its own rows come back through the very
 * endpoint under test. An absence is not evidence until the measurement is
 * proven to have run.
 *
 * WRITE DISCIPLINE: every record is written under `ast-teste-<stamp>` and
 * tagged `container_tag: "ast-teste-container"`, and `teardownAstFixture`
 * deletes each one by id. No pre-existing tenant data is written or asserted on.
 */
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { QueryBuilder } from "../query.js";

/** True when the caller opted in to hitting a real server. */
export const LIVE_ENABLED =
  process.env.ANHUR_AST_LIVE === "1" && !!process.env.ANHUR_API_KEY;

/** Reason string for `describe(..., { skip })` when the opt-in is absent. */
export const LIVE_SKIP_REASON = "set ANHUR_AST_LIVE=1 and ANHUR_API_KEY to run";

/** Everything a live suite needs to write expectations without guessing. */
export interface AstFixture {
  memory: Memory;
  /** Session holding every row the suites assert on. */
  mainSession: string;
  /** A second session, to prove a `uuid` filter really isolates. */
  otherSession: string;
  /** Logical name → server id, for targeted assertions and cleanup. */
  ids: Record<string, number>;
  /** Ids the query endpoint can return, ascending. */
  visibleIds: number[];
  /** score of each visible row, so expectations are COMPUTED, never copied. */
  scoreOf: Record<number, number>;
  /** type of each visible row. */
  typeOf: Record<number, string>;
  /** A builder already pinned to `mainSession` with a full page. */
  scoped: () => QueryBuilder;
  /** Run an AST and return the ids in the server's own order. */
  idsFor: (ast: ReturnType<QueryBuilder["build"]>) => Promise<number[]>;
}

/** Sort a copy ascending — expectations are order-explicit everywhere. */
export const ascending = (ids: readonly number[]): number[] =>
  [...ids].sort((left, right) => left - right);

/**
 * Rows to seed. The FIRST must be episodic: the server refuses a derived type
 * in an empty session ("create an episodic record first") and the SDK never
 * fabricates that anchor.
 */
const SEED_PLAN: ReadonlyArray<{ name: string; type: string; score: number }> = [
  { name: "episodic1", type: "episodic", score: 1 },
  { name: "pref2", type: "preference", score: 2 },
  { name: "fact3", type: "fact", score: 3 },
  { name: "decision5", type: "decision", score: 5 },
  { name: "risk7", type: "risk", score: 7 },
  { name: "fact9", type: "fact", score: 9 },
];

/**
 * Seed a fresh disposable session and prove it is queryable.
 *
 * @param label - Short suffix so concurrent suites cannot collide on a session.
 * @throws AssertionError if the seeded rows are not visible (fixture guard).
 */
export async function seedAstFixture(label: string): Promise<AstFixture> {
  const stamp = new Date().toISOString().replace(/[-:.TZ]/g, "").slice(0, 14);
  const mainSession = `ast-teste-${stamp}-${label}`;
  const otherSession = `${mainSession}-other`;

  const memory = new Memory({
    apiKey: process.env.ANHUR_API_KEY as string,
    url: process.env.ANHUR_URL ?? "https://anhurdb.yoven.ai",
    userId: "ast-teste-container",
  });
  await memory.createSession({ sessionId: mainSession });
  await memory.createSession({ sessionId: otherSession });

  const ids: Record<string, number> = {};
  const scoreOf: Record<number, number> = {};
  const typeOf: Record<number, string> = {};

  for (const plan of SEED_PLAN) {
    const created = await memory.create(`ast-teste live ${plan.name}`, {
      type: plan.type as never,
      score: plan.score,
      sessionId: mainSession,
    });
    const newId = created.records[0].id;
    ids[plan.name] = newId;
    scoreOf[newId] = plan.score;
    typeOf[newId] = plan.type;
  }
  const visibleIds = ascending(SEED_PLAN.map((plan) => ids[plan.name]));

  // A row that must be HIDDEN by the implicit `archived = 0` predicate.
  // DELETE is a SOFT archive: it sets archived=1, status=deleted.
  const toArchive = await memory.create("ast-teste live to be archived", {
    type: "fact" as never, score: 4, sessionId: mainSession,
  });
  ids.archived4 = toArchive.records[0].id;
  await memory.delete(ids.archived4);

  // A row in a DIFFERENT session, to prove `uuid` really confines the result.
  const inOther = await memory.create("ast-teste live other session", {
    type: "episodic" as never, score: 5, sessionId: otherSession,
  });
  ids.other5 = inOther.records[0].id;

  const scoped = (): QueryBuilder =>
    new QueryBuilder().whereEquals("uuid", mainSession).limit(1000);
  const idsFor = async (
    ast: ReturnType<QueryBuilder["build"]>,
  ): Promise<number[]> => (await memory.query(ast)).records.map((record) => record.id);

  // ── FIXTURE-DID-NOT-ENTER GUARD ────────────────────────────────────────
  // Verified to fire: with the seed intact but this read pointed at an unwritten
  // session, the whole file reports 0 passed / N cancelled and exits non-zero.
  assert.deepEqual(
    ascending(await idsFor(scoped().build())),
    visibleIds,
    "FIXTURE DID NOT ENTER: the seeded session is not queryable, so every " +
      "assertion in this file would be vacuous. Refusing to report a green run.",
  );

  return { memory, mainSession, otherSession, ids, visibleIds, scoreOf, typeOf, scoped, idsFor };
}

/** Delete every record the fixture created. Safe to call after a partial seed. */
export async function teardownAstFixture(fixture: AstFixture | undefined): Promise<void> {
  if (!fixture) return;
  for (const recordId of Object.values(fixture.ids)) {
    await fixture.memory.delete(recordId).catch(() => undefined);
  }
}

/**
 * Pull the server's own sentence out of the SDK's error message.
 *
 * Junior Tip [the message is an envelope, not a field]: the SDK does not parse
 * the server's `{"error": "..."}` body — it embeds the RAW body inside
 * `Invalid request (HTTP 400): <body>`, so every quote the server wrote arrives
 * backslash-escaped and the useful sentence is buried. A caller who wants to
 * SHOW the server's reason, or match on it, has to do this decode themselves.
 * Writing this helper is how we found that out.
 */
export function serverReason(rejection: Error): string {
  const bodyStart = rejection.message.indexOf("{");
  if (bodyStart < 0) return rejection.message;
  const body = JSON.parse(rejection.message.slice(bodyStart)) as { error?: string };
  return body.error ?? rejection.message;
}
