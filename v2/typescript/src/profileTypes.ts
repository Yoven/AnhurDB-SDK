/**
 * AnhurDB TypeScript SDK — what `GET /api/v1/profile` ANSWERS.
 *
 * Its own file because `types.ts` is long past the ~300-line house cut, and
 * house law forbids growing a file that is already over it. The profile is a
 * real domain: three fixed blocks, one endpoint, one handler.
 *
 * GROUND TRUTH: `AnhurDB/server/handler/profile.go:27-51` declares
 * `profileResponse{Static, Dynamic, Stats}` with `profileStatic`,
 * `profileDynamic` and `profileStats` as CLOSED Go structs — not maps. The
 * handler cannot emit a fourth top-level key or an unlisted inner key.
 * Re-proved live on 2026-09-14 against `https://anhurdb.yoven.ai`:
 *
 *     GET /api/v1/profile?tag=<own>  -> keys [dynamic, static, stats]
 *       static  [decisions, emotions, facts, highlight, preferences, risks]
 *       dynamic [recent_tasks, recent_topics]
 *       stats   [last_active, sessions, total_records]
 *
 * @module
 */

/**
 * The `static` block — slow-moving facts the profiler has distilled.
 *
 * Every member is a plain string array; the server initialises each to `[]`,
 * so an empty profile is six empty arrays, never `undefined`.
 */
export interface ProfileStatic {
  facts: string[];
  preferences: string[];
  decisions: string[];
  risks: string[];
  emotions: string[];
  highlight: string[];
}

/** The `dynamic` block — what the container has been doing lately. */
export interface ProfileDynamic {
  recent_tasks: string[];
  recent_topics: string[];
}

/**
 * The `stats` block — three aggregate counters.
 *
 * Junior Tip [`last_active` here is NOT the session row's `last_activity`]:
 * the server genuinely uses two spellings for two different objects —
 * `profileStats.LastActive` is `json:"last_active"`
 * (`handler/profile.go:50`), while a session row is `json:"last_activity"`
 * (`database/list_sessions.go:41`). Both were confirmed live on the same day.
 * "Fixing" one to match the other produces a field the server never sends.
 */
export interface ProfileStats {
  total_records: number;
  sessions: number;
  last_active: string;
}

/**
 * Value returned by `Memory.profile()`.
 *
 * Junior Tip [why there is no `[key: string]: unknown` index signature any
 * more, 2026-09-14]: this interface used to type the three blocks as
 * `Record<string, unknown>` AND carry an open index signature. The signature
 * was not a forward-compatibility hatch — it was a hiding place. It let the
 * SDK's own 404 fallback invent `tag` and `status: "not_available"`, two keys
 * `profile.go` has never emitted, and the compiler accepted them as if the
 * server had sent them. Callers branching on `profile.status` were branching
 * on an SDK fiction. The blocks are closed Go structs; this type says so.
 *
 * Junior Tip [an unknown tag is an EMPTY profile with HTTP 200, not a 404]:
 * live on 2026-09-14, `?tag=totally-not-a-real-tag-xyz` answered 200 with
 * every array empty and `last_active: ""`. Do not translate that into an
 * exception or a null — swallowing it would hide a typo'd tag behind a fake
 * failure. An empty profile IS the server's answer.
 */
export interface ProfileResult {
  static: ProfileStatic;
  dynamic: ProfileDynamic;
  stats: ProfileStats;
}

/**
 * The all-zero profile the SDK returns when the server has no profile
 * endpoint at all (OSS build answering a REAL 404 on the route).
 *
 * It is byte-for-byte the shape the hosted server returns for an unknown tag,
 * so a caller cannot tell "no such tag" from "no such endpoint" by shape —
 * and must not need to: both mean "nothing to show", and neither is an error.
 */
export function emptyProfile(): ProfileResult {
  return {
    static: {
      facts: [],
      preferences: [],
      decisions: [],
      risks: [],
      emotions: [],
      highlight: [],
    },
    dynamic: { recent_tasks: [], recent_topics: [] },
    stats: { total_records: 0, sessions: 0, last_active: "" },
  };
}
