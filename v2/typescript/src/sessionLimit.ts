/**
 * Recognise the per-session record cap refusal in a 409 answer.
 *
 * Domain, in one sentence: turn "this session is full" into a typed
 * `AnhurSessionRecordLimitError` carrying the session, the cap and the count,
 * so the refusal can never be mistaken for an outage or a generic conflict.
 *
 * Wire contract (ADR-0033, AnhurDB): HTTP 409
 * `{"error": "session has reached the maximum of N records ...", "code":
 * "session_record_limit", "limit": N, "count": M, "session_uuid": "..."}`.
 * Every create path sends it (`handler/service_bridge.go:81-82,109-125`; sole
 * producer `service/record_session_cap.go:178`), and none of the four fields
 * is omitempty.
 *
 * Junior Tip [the code is the contract, never the sentence — 4.0.0]: this
 * file used to ALSO match the sentence prefix and read N back out of it with a
 * regex, for servers that predated the code. AnhurDB is the only server and it
 * always sends the code, so that branch only had one remaining effect: turning
 * any 409 that happened to start with the same words into a typed refusal with
 * invented `null` fields. A 409 without the code is now an ordinary conflict.
 *
 * gRPC (ResourceExhausted + ErrorInfo `SESSION_RECORD_LIMIT`,
 * `grpc/service_errors.go:39-40,123`) is part of the contract too, but this
 * SDK has no gRPC transport, so there is no gRPC path to classify here.
 */

import { AnhurSessionRecordLimitError } from "./errors.js";

/** How much of the server sentence the message quotes (same cap as Python). */
const SERVER_MESSAGE_EXCERPT_CHARS = 500;

/** The four wire fields this file reads, already validated. */
interface SessionLimitBody {
  error: string;
  limit: number;
  count: number;
  session_uuid: string;
}

/**
 * Validate a parsed 409 body against the session-limit contract.
 *
 * Junior Tip [why a malformed body is NOT typed]: the server always sends
 * `limit`, `count` and `session_uuid`, so the typed error declares them
 * non-nullable. A body carrying the code but missing one of them breaks the
 * contract; fabricating `0` or `""` would hand the caller a number the server
 * never said. Such a body stays the generic 409 `AnhurError`, whose message
 * quotes the raw body for diagnosis.
 */
function readSessionLimitBody(bodyText: string): SessionLimitBody | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(bodyText);
  } catch {
    return null;
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return null;
  const fields = parsed as Record<string, unknown>;
  if (fields.code !== AnhurSessionRecordLimitError.CODE) return null;
  if (!Number.isInteger(fields.limit) || !Number.isInteger(fields.count)) return null;
  if (typeof fields.session_uuid !== "string") return null;
  return {
    error: typeof fields.error === "string" ? fields.error.trim() : "",
    limit: fields.limit as number,
    count: fields.count as number,
    session_uuid: fields.session_uuid,
  };
}

/**
 * Return the typed refusal, or null when the answer is anything else.
 *
 * @param statusCode - HTTP status of the failed response.
 * @param bodyText   - Body as read by `readErrorBodyExcerpt` (up to 64 KiB,
 *                     far beyond this ~200-byte answer, so it is never cut).
 */
export function sessionRecordLimitFromResponse(
  statusCode: number,
  bodyText: string,
): AnhurSessionRecordLimitError | null {
  if (statusCode !== 409) return null;
  const body = readSessionLimitBody(bodyText);
  if (body === null) return null;

  // Server sentence first: log readers truncate, and it is what a human
  // recognises. Byte-identical layout to the Python SDK.
  return new AnhurSessionRecordLimitError(
    `Conflict (HTTP 409): ${body.error.slice(0, SERVER_MESSAGE_EXCERPT_CHARS)} ` +
      `(session record limit: session=${body.session_uuid}, ` +
      `limit=${body.limit}, count=${body.count})`,
    {
      sessionUuid: body.session_uuid,
      limit: body.limit,
      count: body.count,
      serverMessage: body.error,
    },
  );
}
