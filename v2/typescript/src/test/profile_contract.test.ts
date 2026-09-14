/**
 * `GET /api/v1/profile` — the tag the SDK sends, and the shape it promises.
 *
 * Covers SPEC §3.5 (ProfileResult had two phantom fields and an open index
 * signature) and §6 (TypeScript could not express a container tag at all).
 * Field sets re-proved live on 2026-09-14 against https://anhurdb.yoven.ai.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import type { ProfileResult } from "../types.js";
import { recordWire } from "./wireRecorder.js";

/**
 * COMPILE-TIME guard, same rationale as wire_shapes.test.ts: `ProfileResult`
 * used to carry `[key: string]: unknown`, which is what let the SDK's own 404
 * fallback invent `tag` and `status` and still typecheck. With the index
 * signature gone, `keyof ProfileResult` is exactly the three block names, and
 * re-adding the signature (or either phantom key) makes these `never`.
 */
type PhantomKey<T, K extends string> = K extends keyof T ? never : true;
const profileHasNoTag: PhantomKey<ProfileResult, "tag"> = true;
const profileHasNoStatus: PhantomKey<ProfileResult, "status"> = true;
void [profileHasNoTag, profileHasNoStatus];

/** The exact envelope the live server answered on 2026-09-14. */
const LIVE_PROFILE = {
  static: {
    facts: ["f"], preferences: ["p"], decisions: ["d"],
    risks: ["r"], emotions: ["e"], highlight: ["h"],
  },
  dynamic: { recent_tasks: ["t"], recent_topics: ["topic"] },
  stats: { total_records: 3577, sessions: 31, last_active: "2026-09-14T00:00:00Z" },
};

describe("ProfileResult models the three CLOSED blocks profile.go declares", () => {
  it("reads every inner field without a cast", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { value } = await recordWire(LIVE_PROFILE, () => memory.profile());
    const profile: ProfileResult = value;

    // OLD TYPE: static/dynamic/stats were `Record<string, unknown>`, so every
    // line below was a compile error ("Property 'facts' does not exist on
    // type 'unknown'") and callers reached these fields through `as any`.
    assert.deepEqual(profile.static.facts, ["f"]);
    assert.deepEqual(profile.static.highlight, ["h"]);
    assert.deepEqual(profile.dynamic.recent_topics, ["topic"]);
    assert.equal(profile.stats.total_records, 3577);
    assert.equal(profile.stats.sessions, 31);
    // `last_active` HERE, `last_activity` on a session row — two spellings,
    // two objects, both live-confirmed. See sessionStats.ts.
    assert.equal(profile.stats.last_active, "2026-09-14T00:00:00Z");
  });

  it("returns exactly three top-level keys — no invented `tag`/`status`", async () => {
    const memory = new Memory({ apiKey: "key", userId: "u" });
    const { value } = await recordWire(LIVE_PROFILE, () => memory.profile());
    assert.deepEqual(Object.keys(value).sort(), ["dynamic", "static", "stats"]);
  });
});

describe("profile(containerTag) — the tag is an IN-TENANT filter", () => {
  it("sends the caller's tag verbatim as ?tag", async () => {
    const memory = new Memory({ apiKey: "key", userId: "own-tag" });
    // OLD SIGNATURE: `profile()` took no argument at all, so this call did not
    // compile and a TypeScript caller could only ever profile its own tag —
    // even though handler/profile.go has always read `tag` from the query
    // string (live: another container's tag answered 200 with ITS 6 records).
    const { requests } = await recordWire(LIVE_PROFILE,
      () => memory.profile("hermes-1"));
    assert.equal(requests.length, 1);
    assert.equal(requests[0].url.pathname, "/api/v1/profile");
    assert.equal(requests[0].url.searchParams.get("tag"), "hermes-1");
  });

  it("falls back to the client's own tag when none is given", async () => {
    const memory = new Memory({ apiKey: "key", userId: "own-tag" });
    const { requests } = await recordWire(LIVE_PROFILE, () => memory.profile());
    assert.equal(requests[0].url.searchParams.get("tag"), "own-tag");
  });

  it("never puts an EMPTY tag on the wire — that is a guaranteed 400", async () => {
    const memory = new Memory({ apiKey: "key", userId: "own-tag" });
    for (const blankTag of ["", "   "]) {
      const { requests } = await recordWire(LIVE_PROFILE,
        () => memory.profile(blankTag));
      assert.equal(
        requests[0].url.searchParams.get("tag"), "own-tag",
        `a blank tag (${JSON.stringify(blankTag)}) must not reach the server: ` +
          `live, ?tag= answered 400 {"error":"tag: tag is required"}`,
      );
    }
  });

  it("returns an unknown tag's EMPTY profile as data, not as an error", async () => {
    // Live 2026-09-14: ?tag=totally-not-a-real-tag-xyz -> HTTP 200, all zeros.
    const emptyEnvelope = {
      static: {
        facts: [], preferences: [], decisions: [],
        risks: [], emotions: [], highlight: [],
      },
      dynamic: { recent_tasks: [], recent_topics: [] },
      stats: { total_records: 0, sessions: 0, last_active: "" },
    };
    const memory = new Memory({ apiKey: "key", userId: "own-tag" });
    const { value } = await recordWire(emptyEnvelope,
      () => memory.profile("totally-not-a-real-tag-xyz"));
    assert.equal(value.stats.total_records, 0);
    assert.equal(value.stats.last_active, "");
    assert.deepEqual(value.static.facts, []);
  });
});
