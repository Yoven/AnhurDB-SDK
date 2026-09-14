/**
 * `GET /api/v1/profile` — the one call, and the two things it will not do.
 *
 * WHY THIS FILE EXISTS
 * --------------------
 * `memory.ts` is long past the ~300-line house cut, and house law forbids
 * growing a file already over it. Profiling is a real domain with one
 * endpoint, one required query parameter and two answers that look like
 * failures and are not — so it earns a file, next to the `ProfileResult`
 * shapes in `profileTypes.ts`, exactly as `sessionStats.ts` sits next to its
 * own row type.
 *
 * @module
 */

import type { HttpClient } from "./client.js";
import { AnhurError } from "./errors.js";
import { emptyProfile, type ProfileResult } from "./profileTypes.js";

/**
 * Fetch the profile for one container tag.
 *
 * Junior Tip [`tag` filters WITHIN your tenant — it does not select one]:
 * `AnhurDB/server/handler/profile.go:63-66` takes the tenant from the auth
 * middleware context (`getTenantID`) and reads `tag` only from the query
 * string, so no value of `tag` can reach another tenant's data. Measured live
 * on 2026-09-14 against `https://anhurdb.yoven.ai` with the owner key:
 *
 *     ?tag=<own container>   -> 200, total_records 3577
 *     ?tag=<other container> -> 200, total_records 6     (accepted and served)
 *     ?tag=totally-not-a-real-tag-xyz -> 200, all zeros, last_active ""
 *     ?tag=*                 -> 200, all zeros  (a literal tag, NOT a wildcard)
 *     (tag omitted)          -> 400 {"error":"tag: tag is required"}
 *
 * Two consequences this function encodes:
 *
 *   1. The tag is NEVER sent blank. An empty `tag` is a guaranteed 400, so a
 *      caller passing `""` or `"   "` gets this client's derived tag instead
 *      of a pointless round-trip into a validation error.
 *   2. An unknown tag is an EMPTY PROFILE WITH HTTP 200 — not a 404, not an
 *      error. It is returned as-is. Turning "no such tag" into an exception
 *      would hide a typo behind a fake server failure, and turning it into
 *      `null` would make every caller write a null check for a case the
 *      server considers ordinary.
 *
 * A REAL 404 on the route itself (an OSS build with no profiler) yields the
 * same all-zero profile, deliberately: both answers mean "nothing to show",
 * and neither is a transport failure the caller can act on. Any other status
 * is re-thrown untouched.
 *
 * Junior Tip [branch on the STATUS, never on the message text]: the 404 test
 * below used to read `err.message.includes("404")`. A 500 whose echoed body
 * merely mentioned 404 — a proxy error page, a record id `404`, a stack line —
 * was accepted as "OSS has no profile endpoint", and a genuine server failure
 * was reported to the caller as an empty profile. The status code is the
 * contract; the message is decoration.
 *
 * @param httpClient - Transport used for the GET.
 * @param tag        - Container tag to profile. Must already be non-empty.
 * @returns The profile; all-zero when the tag (or the endpoint) is unknown.
 * @throws {AnhurError} Anything that is not a 404 on the route.
 */
export async function fetchProfile(
  httpClient: HttpClient,
  tag: string,
): Promise<ProfileResult> {
  try {
    const data = await httpClient.get<ProfileResult>("/api/v1/profile", { tag });
    const fallback = emptyProfile();
    return {
      static: data.static ?? fallback.static,
      dynamic: data.dynamic ?? fallback.dynamic,
      stats: data.stats ?? fallback.stats,
    };
  } catch (err: unknown) {
    // Junior Tip [no invented `tag`/`status` keys — 3.0.0]: this branch used to
    // bolt `tag` and `status: "not_available"` onto the returned object. The
    // handler has never emitted either key; only `ProfileResult`'s old open
    // index signature let them compile. Callers branching on `profile.status`
    // were branching on an SDK fiction, and the fiction is now unrepresentable.
    if (err instanceof AnhurError && err.statusCode === 404) {
      return emptyProfile();
    }
    throw err;
  }
}
