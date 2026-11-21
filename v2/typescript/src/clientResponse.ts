/**
 * AnhurDB TypeScript SDK — turning ONE `fetch` Response into a typed result or
 * a typed error.
 *
 * Split out of `client.ts` (house cut, ~300 lines) because that file carried
 * TWO independent copies of this mapping — one inside `request()`, one inside
 * `postMultipart()` — and the copies had already drifted: the JSON path built
 * its 404 and its 5xx WITHOUT the HTTP status while the multipart path passed
 * it. One domain, one file, one behaviour, so the next transport verb cannot
 * fork a third dialect.
 */

import { AnhurError, AnhurQueryError } from "./types.js";
import { authErrorFromResponse } from "./authErrors.js";
import { sessionRecordLimitFromResponse } from "./sessionLimit.js";
import { scopeErrorFromResponse } from "./scopeErrors.js";

/** Maximum response body size: 100 MB (memory-exhaustion protection). */
export const MAX_RESPONSE_SIZE = 100 * 1024 * 1024;

/**
 * How much of a FAILED response body is quoted inside the error message.
 * Bounded because a server stack trace is diagnostic, not a payload.
 */
const ERROR_BODY_EXCERPT_CHARS = 500;

/**
 * How much of a FAILED response body is READ for classification.
 *
 * Junior Tip [read more than you quote, 2026-11-20]: a partial tenant-library
 * delete (HTTP 500, ADR-0029) carries one outcome per library store, and the
 * server's sentence alone can pass 500 characters — reading only the quoted
 * excerpt cut the JSON short and lost every leg. Classification reads up to
 * 64 KiB (still bounded); messages still quote 500 characters.
 */
const ERROR_BODY_READ_CHARS = 64 * 1024;

/**
 * Read a failed response's body, bounded, never throwing.
 *
 * Junior Tip [why a read failure here must stay silent]: this runs on the path
 * that is ALREADY reporting a failure. If `.text()` itself rejects (connection
 * cut mid-body) and we let that escape, the caller would receive a generic
 * stream error instead of the typed "HTTP 503" it needs to branch on — the real
 * status would be lost to make room for a worse one. An empty excerpt is a
 * missing detail; a swallowed status is a missing contract.
 */
export async function readErrorBodyExcerpt(response: Response): Promise<string> {
  return response
    .text()
    .then((bodyText) => bodyText.slice(0, ERROR_BODY_READ_CHARS))
    .catch(() => "");
}

/**
 * Read a SUCCESSFUL response's body and enforce {@link MAX_RESPONSE_SIZE}.
 *
 * Junior Tip [why every verb must go through this, uploads included]: the cap
 * exists so a runaway/hostile response cannot exhaust the process heap. A cap
 * enforced on some verbs and not others is not a cap — it is a cap plus a hole,
 * and the hole was the multipart path, which is exactly the verb whose replies
 * are least predictable (server-side extraction summaries).
 */
export async function readCappedText(response: Response): Promise<string> {
  const bodyText = await response.text();
  if (bodyText.length > MAX_RESPONSE_SIZE) {
    throw new AnhurError(
      `Response exceeds maximum size (${MAX_RESPONSE_SIZE / (1024 * 1024)} MB)`,
    );
  }
  return bodyText;
}

/**
 * Map a non-2xx response to the typed AnhurDB error that describes it.
 *
 * `statusCode` is REQUIRED, not optional, and that is the whole point of this
 * function existing.
 *
 * Junior Tip [the status is the contract; the message is decoration]: an
 * `AnhurError` built without its status resolves `kind` through
 * `kindForStatus(undefined)`, which answers `"transport"` — "the request never
 * reached the server" — and `"transport"` is classified RETRYABLE. So a 404
 * built without its status announces itself as a retryable network glitch,
 * the exact opposite of `not_found`. Worse, it is silent: the message still
 * reads "HTTP 404", so a human debugging it sees the right number while every
 * caller that branches on `statusCode === 404` (the read-your-writes grace in
 * `waitForUpload`) sees `undefined` and takes the failure path. Never construct
 * one of these from a response without handing over the status.
 *
 * @param statusCode  - The HTTP status of the failed response.
 * @param requestPath - Path of the request, quoted in the 404 message. Never
 *                      the full URL: it can carry credentials into a log.
 * @param bodyText    - Bounded body text from {@link readErrorBodyExcerpt};
 *                      quoted in messages only up to 500 characters.
 */
export function typedErrorForResponse(
  statusCode: number,
  requestPath: string,
  bodyText: string,
): AnhurError {
  const bodyExcerpt = bodyText.slice(0, ERROR_BODY_EXCERPT_CHARS);
  if (statusCode === 401 || statusCode === 403) {
    // Typed so the server's reason (l1_only, child_suspended, insufficient
    // scope, ...) survives; still `instanceof AnhurAuthError` (authErrors.ts).
    return authErrorFromResponse(statusCode, bodyText);
  }
  if (statusCode === 400 || statusCode === 422) {
    return new AnhurQueryError(
      `Invalid request (HTTP ${statusCode}): ${bodyExcerpt}`,
      statusCode,
    );
  }
  if (statusCode === 404) {
    return new AnhurQueryError(
      `Resource not found (HTTP 404): ${requestPath}`,
      statusCode,
    );
  }
  // A full session (ADR-0033) is a typed, non-retryable refusal; any other
  // 409 keeps the generic shape below.
  const sessionLimitError = sessionRecordLimitFromResponse(statusCode, bodyText);
  if (sessionLimitError !== null) return sessionLimitError;
  // L2 library answers: collision / ambiguous upload id (409), partial library
  // delete (500, decoded from the full body so every leg survives).
  const scopeError = scopeErrorFromResponse(statusCode, bodyText);
  if (scopeError !== null) return scopeError;
  return new AnhurError(
    `Server error (HTTP ${statusCode}): ${bodyExcerpt}`,
    statusCode,
  );
}
