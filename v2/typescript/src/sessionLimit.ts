/**
 * Recognise the per-session record cap refusal in a 409 answer.
 *
 * Domain, in one sentence: turn "this session is full" into a typed
 * `AnhurSessionRecordLimitError` carrying the session, the cap and the count,
 * so the refusal can never be mistaken for an outage or a generic conflict.
 *
 * Wire contract (ADR-0033, AnhurDB):
 *  - NEW servers: HTTP 409 `{"error": "session has reached the maximum of N
 *    records ...", "code": "session_record_limit", "limit": N, "count": M,
 *    "session_uuid": "..."}`
 *  - OLD servers (production until the deploy): HTTP 409
 *    `{"error": "session has reached the maximum of N records"}`.
 *
 * gRPC (ResourceExhausted + ErrorInfo `SESSION_RECORD_LIMIT`; legacy
 * AlreadyExists with the same text) is part of the contract too, but this SDK
 * has no gRPC transport, so there is no gRPC path to classify here.
 */

import { AnhurSessionRecordLimitError } from "./errors.js";

export const SESSION_RECORD_LIMIT_TEXT_PREFIX = "session has reached the maximum of";
const LIMIT_IN_TEXT = /maximum of (\d+) records/;

/** A finite integer from the wire, or null — never a coerced guess. */
function optionalInteger(value: unknown): number | null {
  return typeof value === "number" && Number.isInteger(value) ? value : null;
}

/** First capture of `pattern` in `text`, or null. */
function captured(pattern: RegExp, text: string): string | null {
  const match = pattern.exec(text);
  return match ? match[1] : null;
}

/**
 * Return the typed refusal, or null when the answer is anything else.
 *
 * Junior Tip [why a regex fallback exists]: the transport quotes at most 500
 * characters of a failed body. A long server sentence could cut the JSON short
 * and make `JSON.parse` throw; the refusal must still be recognised then, so
 * the fields are read back from the raw text when parsing fails.
 */
export function sessionRecordLimitFromResponse(
  statusCode: number,
  bodyText: string,
): AnhurSessionRecordLimitError | null {
  if (statusCode !== 409) return null;

  let serverMessage = bodyText.trim();
  let code: unknown;
  let limit: number | null = null;
  let count: number | null = null;
  let sessionUuid: string | null = null;
  try {
    const parsed: unknown = JSON.parse(bodyText);
    if (parsed !== null && typeof parsed === "object") {
      const fields = parsed as Record<string, unknown>;
      serverMessage = typeof fields.error === "string" ? fields.error.trim() : "";
      code = fields.code;
      limit = optionalInteger(fields.limit);
      count = optionalInteger(fields.count);
      sessionUuid =
        typeof fields.session_uuid === "string" && fields.session_uuid !== ""
          ? fields.session_uuid
          : null;
    }
  } catch {
    // Plain text, or JSON cut short by the excerpt cap: read the raw text.
    code = captured(/"code"\s*:\s*"([^"]*)"/, bodyText) ?? undefined;
    const limitText = captured(/"limit"\s*:\s*(\d+)/, bodyText);
    const countText = captured(/"count"\s*:\s*(\d+)/, bodyText);
    limit = limitText === null ? null : Number(limitText);
    count = countText === null ? null : Number(countText);
    sessionUuid = captured(/"session_uuid"\s*:\s*"([^"]+)"/, bodyText);
    const quoted = captured(/"error"\s*:\s*"([^"]*)/, bodyText);
    if (quoted !== null) serverMessage = quoted.trim();
  }

  if (
    code !== AnhurSessionRecordLimitError.CODE &&
    !serverMessage.startsWith(SESSION_RECORD_LIMIT_TEXT_PREFIX)
  ) {
    return null;
  }
  if (limit === null) {
    // The server's own number, written into its sentence — not a guess.
    const limitText = captured(LIMIT_IN_TEXT, serverMessage);
    limit = limitText === null ? null : Number(limitText);
  }
  const text = (value: number | null) => (value === null ? "unknown" : String(value));
  // Server sentence first: log readers truncate, and it is what a human
  // recognises. Same layout as the Go and Python SDKs.
  return new AnhurSessionRecordLimitError(
    `Conflict (HTTP 409): ${serverMessage} (session record limit: ` +
      `session=${sessionUuid ?? "unknown"}, limit=${text(limit)}, count=${text(count)})`,
    { sessionUuid, limit, count, serverMessage },
  );
}
