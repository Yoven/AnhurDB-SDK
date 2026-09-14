/**
 * Auto-pagination for `GET /api/v1/sessions/stats`.
 *
 * WHY THIS FILE EXISTS
 * --------------------
 * The endpoint is paged and says so honestly in its envelope:
 *
 *     no params   -> 50 sessions, has_more: true,  next_offset: 50
 *     ?limit=500  -> 99 sessions, has_more: false, next_offset: 0
 *
 * (measured live against the owner tenant on 2026-09-14.)
 *
 * `Memory.listSessions()` used to send no paging params at all and return
 * whatever the first page happened to hold, discarding `has_more`. Callers
 * asking for "all my sessions" silently got 50 of 99 — no error, no warning,
 * no way to tell a small tenant from a truncated answer. The Go SDK
 * (`client/client.go`, `ListSessions`) has always paged correctly; this module
 * is TypeScript catching up, deliberately mirroring Go's page size and its
 * advance rule so the three SDKs agree byte-for-byte on the wire.
 *
 * It lives in its own file because `memory.ts` is long past the ~300-line house
 * cut, and house law forbids growing a file that is already over it.
 */

import type { HttpClient } from "./client.js";
import { AnhurError } from "./errors.js";

/**
 * Aggregate stats for a single session — one row of `sessions[]`.
 *
 * GROUND TRUTH: `AnhurDB/server/database/list_sessions.go:37-43`, the Go
 * struct the handler marshals verbatim. Re-proved live on 2026-09-14 against
 * `https://anhurdb.yoven.ai`: `GET /api/v1/sessions/stats?limit=2` answered
 * rows with keys `[last_activity, record_count, summary, types, uuid]`.
 *
 * Junior Tip [the key is `last_activity`, and this SDK said `last_active` —
 * 2026-09-14]: the wrong spelling had been in `types.ts` since the type was
 * written. Nothing failed, because `row.last_active` on a row that does not
 * carry it is simply `undefined` — so every caller sorting or displaying "last
 * activity" got a blank or an epoch date, silently, forever. Go
 * (`client/types.go:82`) had it right all along. The near-miss is the danger:
 * a name that is obviously a typo gets caught, a name that is plausible does
 * not. Note this is a DIFFERENT object from the profile's `stats` block, which
 * really does spell it `last_active` (`handler/profile.go:50`) — the server
 * uses both spellings, for two different things. Do not unify them.
 *
 * Junior Tip [`types` and `summary` were missing entirely]: the server sends a
 * per-type histogram (`{"episodic": 12, "fact": 3}`) and, when a consolidated
 * summary exists for the session, its text. Both are exactly what a caller
 * needs to decide whether a session is worth opening — and neither was
 * reachable from TypeScript without an `as any`. `summary` is `omitempty`
 * server-side, so it is optional here; `types` is always present.
 */
export interface SessionStats {
  uuid: string;
  record_count: number;
  /** Record count per memory type, e.g. `{"episodic": 12, "fact": 3}`. */
  types: Record<string, number>;
  /** RFC3339 timestamp of the newest record in the session. */
  last_activity: string;
  /** Latest consolidated summary, when the session has one (`omitempty`). */
  summary?: string;
}

/**
 * Page size requested for every `sessions/stats` call.
 *
 * 500 is the server's documented maximum and the exact value Go's
 * `listSessionsPageLimit` uses. Keeping the three SDKs on one number means a
 * server-side paging change is caught by all of them at once instead of by
 * whichever one happened to ask for the unlucky size.
 */
export const SESSION_STATS_PAGE_LIMIT = 500;

/**
 * Hard ceiling on pages followed in one `listSessions()` call.
 *
 * Junior Tip [why a ceiling AND a monotonic check, 2026-09-14]: `while
 * (has_more)` is a loop whose exit condition is controlled entirely by the
 * remote end. A server bug that answers `has_more: true, next_offset: 0`
 * forever turns an SDK call into an infinite loop that allocates until the
 * process dies. Two independent brakes guard that:
 *
 *   1. the offset must strictly INCREASE every iteration (mirroring Go), so a
 *      stuck `next_offset` falls back to `offset + limit` and still advances;
 *   2. this ceiling, so even a server that advances forever terminates.
 *
 * 1000 pages x 500 rows = 500,000 sessions, several orders of magnitude past
 * any real tenant, so the ceiling cannot truncate an honest answer.
 *
 * Hitting it THROWS rather than returning the partial list. Returning what we
 * have would recreate the exact defect this module exists to fix: a short
 * answer that looks complete. Silent loss is the failure mode this project has
 * been bitten by most; a loud error is the correct trade.
 */
export const SESSION_STATS_MAX_PAGES = 1000;

/** The envelope shape `GET /api/v1/sessions/stats` returns. */
interface SessionStatsEnvelope {
  sessions?: SessionStats[] | null;
  has_more?: boolean;
  next_offset?: number;
}

/**
 * Fetch EVERY session's aggregate stats, following the server's pagination.
 *
 * Requests `limit=500&offset=N` and keeps going while the envelope reports
 * `has_more: true`, concatenating the pages in server order.
 *
 * Two response shapes are accepted, matching Go:
 *   - the envelope `{ "sessions": [...], "has_more": ..., "next_offset": ... }`
 *   - a legacy BARE ARRAY `[...]`, which is treated as one complete page.
 *
 * Junior Tip [the bare array is not dead code]: older deployments answer with a
 * naked array and no envelope. TypeScript used to read `data.sessions` off it,
 * find `undefined`, and return an EMPTY list — a tenant full of sessions
 * reported as having none. Go handles the array explicitly; so does this.
 *
 * @param httpClient - Transport used for the GET calls.
 * @returns Every session in the tenant, in server order.
 * @throws {AnhurError} kind `"server"` when the server never stops paging.
 */
export async function fetchAllSessionStats(
  httpClient: HttpClient,
): Promise<SessionStats[]> {
  const allSessions: SessionStats[] = [];
  let pageOffset = 0;

  for (let pagesFetched = 0; pagesFetched < SESSION_STATS_MAX_PAGES; pagesFetched += 1) {
    const responseBody = await httpClient.get<SessionStatsEnvelope | SessionStats[] | null>(
      "/api/v1/sessions/stats",
      {
        limit: String(SESSION_STATS_PAGE_LIMIT),
        offset: String(pageOffset),
      },
    );

    // Legacy bare array: one page, no envelope, nothing more to follow.
    if (Array.isArray(responseBody)) {
      return allSessions.concat(responseBody);
    }
    // Empty body or JSON `null`: no sessions, and not an error.
    if (responseBody === null || typeof responseBody !== "object") {
      return allSessions;
    }

    const envelope = responseBody as SessionStatsEnvelope;
    if (envelope.sessions) {
      allSessions.push(...envelope.sessions);
    }
    if (envelope.has_more !== true) {
      return allSessions;
    }

    // Prefer the server's next_offset, but only when it actually moved
    // forward; otherwise step by our own page size. Identical to Go.
    const advertisedNextOffset = envelope.next_offset;
    pageOffset =
      typeof advertisedNextOffset === "number" && advertisedNextOffset > pageOffset
        ? advertisedNextOffset
        : pageOffset + SESSION_STATS_PAGE_LIMIT;
  }

  throw new AnhurError(
    `listSessions: GET /api/v1/sessions/stats still reported has_more=true after ` +
      `${SESSION_STATS_MAX_PAGES} pages of ${SESSION_STATS_PAGE_LIMIT} ` +
      `(${SESSION_STATS_MAX_PAGES * SESSION_STATS_PAGE_LIMIT} sessions read, last ` +
      `offset ${pageOffset}). The server is not terminating its own pagination; ` +
      `returning the partial list here would hide that behind a plausible-looking ` +
      `answer, which is the bug this pagination was added to fix.`,
    undefined,
    "server",
  );
}
