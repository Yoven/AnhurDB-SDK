/**
 * AnhurDB TypeScript SDK — the error taxonomy every failure arrives as.
 *
 * Split out of `types.ts` (house cut, ~300 lines; `types.ts` was 808) because
 * the error contract is a domain of its own: it is the ONE thing every caller
 * writes a `catch` block against, and it changes for reasons that have nothing
 * to do with the request/response shapes that fill the rest of `types.ts`.
 * `types.ts` re-exports everything here, so no import path changed.
 *
 * Junior Tip [why the classification and not the class is the contract]: there
 * are five error classes but nine `kind`s, and callers branch on `kind` and
 * `retryable`, never on the class name or the message text. A message is
 * decoration for a human; `kind` is the machine-readable reason. Any new
 * failure the SDK can produce MUST arrive with the right `kind`, including the
 * ones the SDK raises BEFORE any HTTP happens — see `AnhurQueryError`.
 */

/** Base error for all AnhurDB SDK errors.
 *
 * `statusCode` carries the HTTP status when the error came from an HTTP
 * response (undefined otherwise) — callers branch on the REAL status instead
 * of parsing the message (e.g. `waitForUpload` treats a transient 404 as
 * "pending"). Additive and backward-compatible. */
/** Failure classification, so callers branch on meaning instead of on strings. */
export type AnhurErrorKind =
  | "auth"
  | "invalid_request"
  | "not_found"
  | "conflict"
  | "rate_limited"
  | "unavailable"
  | "timeout"
  | "transport"
  | "server";

const RETRYABLE_KINDS: ReadonlySet<AnhurErrorKind> = new Set([
  "rate_limited",
  "unavailable",
  "timeout",
  "transport",
  "server",
]);

/** Classify an HTTP status. `undefined` means the request never reached the server. */
export function kindForStatus(statusCode?: number): AnhurErrorKind {
  if (statusCode === undefined) return "transport";
  if (statusCode === 401 || statusCode === 403) return "auth";
  if (statusCode === 404) return "not_found";
  if (statusCode === 409) return "conflict";
  if (statusCode === 429) return "rate_limited";
  if (statusCode === 503) return "unavailable";
  if (statusCode >= 400 && statusCode < 500) return "invalid_request";
  return "server";
}

export class AnhurError extends Error {
  readonly statusCode?: number;
  readonly kind: AnhurErrorKind;
  /** Whether repeating the same call could give a different result.
   *
   * Junior Tip [not the same as "safe to retry"]: a timeout on a WRITE means
   * the server may or may not have committed it. The SDK never auto-retries
   * writes — idempotency is the caller's decision. See SDK_ERROR_CONTRACT.md. */
  readonly retryable: boolean;

  constructor(message: string, statusCode?: number, kind?: AnhurErrorKind) {
    const resolvedKind = kind ?? kindForStatus(statusCode);
    // A message is never empty: an unexplained failure is unactionable.
    super(message || defaultMessageFor(resolvedKind, statusCode));
    this.name = "AnhurError";
    this.statusCode = statusCode;
    this.kind = resolvedKind;
    this.retryable = RETRYABLE_KINDS.has(resolvedKind);
  }
}

function defaultMessageFor(kind: AnhurErrorKind, statusCode?: number): string {
  if (kind === "timeout")
    return "request timed out (the server may still have processed it)";
  if (kind === "transport") return "could not reach AnhurDB";
  if (kind === "unavailable") return "service temporarily unavailable — retry";
  return statusCode !== undefined
    ? `AnhurDB request failed (HTTP ${statusCode})`
    : "AnhurDB request failed";
}

/** Raised when authentication fails (invalid API key, expired token). */
export class AnhurAuthError extends AnhurError {
  constructor(message: string, statusCode?: number) {
    super(message, statusCode);
    this.name = "AnhurAuthError";
  }
}

/** Raised when a request is malformed — by the server (HTTP 400/422/404) or by
 * the SDK's own client-side guards BEFORE the request is sent.
 *
 * Junior Tip [why `kind` is a parameter here, 2026-09-14]: with only
 * `(message, statusCode)`, a client-side rejection had no status to hand over,
 * so `kindForStatus(undefined)` classified it `"transport"` — "never reached
 * the server" — and `"transport"` is RETRYABLE. A caller with a retry loop
 * would therefore replay a query that is structurally impossible, forever,
 * because the SDK told it the failure was a network blip. Passing
 * `"invalid_request"` explicitly makes a locally-rejected query carry the exact
 * shape a server 400 carries (`kind` `"invalid_request"`, `retryable` false),
 * minus a `statusCode` it honestly does not have — one catch block, one
 * meaning. Same pattern `AnhurConnectionError` already uses below. */
export class AnhurQueryError extends AnhurError {
  constructor(message: string, statusCode?: number, kind?: AnhurErrorKind) {
    super(message, statusCode, kind);
    this.name = "AnhurQueryError";
  }
}

/** Raised when the SDK cannot reach the AnhurDB server. */
export class AnhurConnectionError extends AnhurError {
  constructor(message: string, statusCode?: number, kind: AnhurErrorKind = "transport") {
    super(message, statusCode, kind);
    this.name = "AnhurConnectionError";
  }
}

/** Raised by `waitForUpload` when the upload did not reach a terminal status
 * within the timeout. Parity: Go `ErrUploadWaitTimeout` / Python
 * `AnhurUploadWaitTimeout`. */
export class AnhurUploadWaitTimeout extends AnhurError {
  constructor(message: string) {
    super(message);
    this.name = "AnhurUploadWaitTimeout";
  }
}
