/**
 * Session filter (ADR-0014) — the `sessions` argument every search carries.
 *
 * One grammar, three SDKs, one server. The error strings in this module are
 * byte-identical to the Go and Python SDKs and to the AnhurDB server, so a
 * single support answer covers every client.
 *
 * @packageDocumentation
 */

import { AnhurError } from "./types.js";

/**
 * Selects every session inside the current scope boundary.
 *
 * Junior Tip [why a wildcard instead of an optional argument]: the defect this
 * model replaces was a session filter that was silently NOT applied — the
 * caller asked for one chat and got five. Making the argument optional with an
 * implicit "all" default reintroduces exactly that class of bug: a client that
 * forgets the field, or computes an empty one, widens the query to the whole
 * tenant with no signal at all. Requiring the caller to spell out `"*"` turns
 * widening into a deliberate, auditable choice.
 */
export const SESSION_WILDCARD = "*";

/**
 * Caps how many explicit sessions one request may name.
 *
 * Junior Tip: this is a product limit, not a technical one — the server's
 * vendored SQLite accepts ~32k bound parameters. The cap exists so that "I need
 * thousands of sessions" surfaces as a design conversation (a grouping label)
 * instead of an ever-growing IN clause. The SDK enforces it locally so the
 * caller learns before paying for the round trip; the server enforces the same
 * number independently, because a client-side check is a convenience and never
 * a guarantee.
 */
export const MAX_SESSION_FILTER_UUIDS = 1000;

/**
 * Returns the wildcard filter — every session inside the search scope.
 *
 * Prefer it over a hand-written `["*"]` so the intent reads at the call site:
 *
 * ```ts
 * const hits = await mem.search("what does this user do?", sessionsAll());
 * ```
 *
 * Mirrors Go `client.SessionsAll()` and Python `sessions_all()`.
 */
export function sessionsAll(): string[] {
  return [SESSION_WILDCARD];
}

/**
 * Validates the caller's `sessions` argument and returns the wire value.
 *
 * The contract, in full (ADR-0014 D1/D2):
 *
 * ```
 * ["*"]                  → every session in the scope boundary
 * ["uuid-a"]             → exactly that session
 * ["uuid-a","uuid-b"]    → exactly those, up to MAX_SESSION_FILTER_UUIDS
 * []                     → error: ambiguous
 * undefined / null       → error: the argument is mandatory
 * ["*","uuid-a"]         → error: contradictory
 * ```
 *
 * Junior Tip [why empty and contradictory are errors, not lenient defaults]:
 * every rejected case here is a caller that does not know what it wants.
 * Guessing on their behalf is how a scoping bug becomes a data-exposure bug.
 * Failing gives them a message they can act on; guessing gives them results
 * they cannot audit. The runtime type checks are not redundant with the
 * compiler: plain-JavaScript callers and JSON-driven agents reach this function
 * with whatever they happen to hold.
 *
 * @throws {AnhurError} with an `INVALID_PARAM: ...` message on every rejection.
 */
export function normalizeSessions(rawSessions: readonly string[]): string[] {
  if (rawSessions === undefined || rawSessions === null) {
    throw new AnhurError(
      `INVALID_PARAM: 'sessions' is required; use ["*"] for every session in scope`,
    );
  }
  if (!Array.isArray(rawSessions)) {
    throw new AnhurError("INVALID_PARAM: 'sessions' must be a list of strings");
  }
  if (rawSessions.length === 0) {
    throw new AnhurError(
      `INVALID_PARAM: 'sessions' cannot be empty; use ["*"] for every session in scope`,
    );
  }

  const cleaned: string[] = [];
  let wildcardSeen = false;
  for (const rawSession of rawSessions) {
    if (typeof rawSession !== "string") {
      throw new AnhurError(
        "INVALID_PARAM: 'sessions' must be a list of strings",
      );
    }
    const session = rawSession.trim();
    if (session === "") {
      throw new AnhurError("INVALID_PARAM: 'sessions' contains an empty entry");
    }
    if (session === SESSION_WILDCARD) {
      wildcardSeen = true;
      continue;
    }
    cleaned.push(session);
  }

  if (wildcardSeen) {
    if (cleaned.length > 0) {
      throw new AnhurError(
        `INVALID_PARAM: 'sessions' mixes "${SESSION_WILDCARD}" with ` +
          `${cleaned.length} explicit session(s); the wildcard must stand alone`,
      );
    }
    return [SESSION_WILDCARD];
  }

  // Junior Tip: dedup before the cap so a caller repeating the same session is
  // not punished for it, and so the server never binds the same value twice.
  const deduplicated: string[] = [];
  const seen = new Set<string>();
  for (const session of cleaned) {
    if (seen.has(session)) continue;
    seen.add(session);
    deduplicated.push(session);
  }

  if (deduplicated.length > MAX_SESSION_FILTER_UUIDS) {
    throw new AnhurError(
      `INVALID_PARAM: at most ${MAX_SESSION_FILTER_UUIDS} sessions per request ` +
        `(got ${deduplicated.length}); use ["*"] for all`,
    );
  }

  return deduplicated;
}

/**
 * Appends the resolved filter to a GET query string as repeated keys.
 *
 * Junior Tip [repeated key, not a comma-joined value]: session uuids are opaque
 * caller-supplied strings, so any in-band separator is a value that some caller
 * will eventually put inside an id. `?sessions=a&sessions=b` is the standard
 * multi-valued encoding, it needs no escaping convention, and it is what
 * `URLSearchParams` / `r.URL.Query()["sessions"]` produce and read on both ends.
 */
export function appendSessionsQueryParam(
  params: Array<[string, string]>,
  sessions: readonly string[],
): void {
  for (const session of sessions) {
    params.push(["sessions", session]);
  }
}
