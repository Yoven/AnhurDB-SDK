/**
 * What a 401/403 on a DATA route means, and the one decoder of the server's
 * `{"error","detail","error_type"}` refusal envelope.
 *
 * Domain, in one sentence: turn an authentication/authorization refusal into a
 * typed error that keeps the server's reason instead of collapsing every
 * refusal into "authentication failed".
 *
 * Messages are byte-identical to Go (`client/auth_refused.go`) and Python
 * (`anhurdb/client/auth_errors.py`) whenever the body carries a code.
 */

import { AnhurAuthError } from "./errors.js";

/** Codes the server puts in `body.error` on a 401/403 (AnhurDB
 * `server/middleware/auth*.go`, `handler/service_bridge.go`). Prefer the
 * `AnhurAuthRefusedError` getters over comparing these strings. */
export const AUTH_CODE_L1_ONLY = "l1_only";
export const AUTH_CODE_CHILD_SUSPENDED = "child_suspended";
export const AUTH_CODE_IMPERSONATION_REQUIRED = "client_key_requires_impersonation";
export const AUTH_CODE_INSUFFICIENT_SCOPE_PREFIX = "insufficient scope";
export const AUTH_CODE_INVALID_KEY = "invalid or inactive API key";

/** The decoded refusal body. `code` is never empty. */
export interface ErrorEnvelope {
  code: string;
  detail: string;
  errorType: string;
}

/**
 * Read `{"error","detail","error_type"}` when the body is a JSON object whose
 * `error` is a non-empty string; `null` otherwise.
 *
 * The Content-Type is ignored on purpose: `http.Error` on the server answers
 * `text/plain` even when the body is JSON. A mistyped optional field is
 * dropped on its own, never at the cost of the code (Go and Python decode
 * field by field the same way).
 */
export function decodeErrorEnvelope(bodyText: string): ErrorEnvelope | null {
  let envelope: unknown;
  try {
    envelope = JSON.parse(bodyText);
  } catch {
    return null;
  }
  if (envelope === null || typeof envelope !== "object" || Array.isArray(envelope)) return null;
  const fields = envelope as Record<string, unknown>;
  if (typeof fields.error !== "string" || !fields.error) return null;
  return {
    code: fields.error,
    detail: typeof fields.detail === "string" ? fields.detail : "",
    errorType: typeof fields.error_type === "string" ? fields.error_type : "",
  };
}

/**
 * Every HTTP 401/403 answer on a data route.
 *
 * `code` is `body.error` verbatim (`""` for a non-JSON body), `detail` and
 * `errorType` likewise; `statusCode` is 401 or 403, `kind` is `auth`,
 * `retryable` is false. Parity: Go `*AuthRefusedError`, Python
 * `AnhurAuthRefusedError`.
 *
 * Junior Tip [why a subclass and not a new family, 2026-11-21]: the transport
 * used to throw a bare `AnhurAuthError("Authentication failed (HTTP 403)")`
 * for EVERY 401/403 and drop the body, so a scope refusal, an `l1_only` token
 * on the wrong plane and a suspended child all read as a bad key. The server
 * always said why; this keeps it. Extending `AnhurAuthError` keeps every
 * existing `instanceof AnhurAuthError` branch working unchanged.
 */
export class AnhurAuthRefusedError extends AnhurAuthError {
  readonly code: string;
  readonly detail: string;
  readonly errorType: string;

  constructor(statusCode: number, fields: { code?: string; detail?: string; errorType?: string } = {}) {
    const code = fields.code ?? "";
    const detail = fields.detail ?? "";
    let message: string;
    if (code && detail) message = `Authentication failed (HTTP ${statusCode}): ${code}: ${detail}`;
    else if (code) message = `Authentication failed (HTTP ${statusCode}): ${code}`;
    else message = `Authentication failed (HTTP ${statusCode})`;
    super(message, statusCode);
    this.name = "AnhurAuthRefusedError";
    this.code = code;
    this.detail = detail;
    this.errorType = fields.errorType ?? "";
  }

  /** 403 for an `l1_only` token outside the L1 plane. */
  get isL1Only(): boolean {
    return this.code === AUTH_CODE_L1_ONLY;
  }

  /** 403 for a key whose child client is suspended. */
  get isChildSuspended(): boolean {
    return this.code === AUTH_CODE_CHILD_SUSPENDED;
  }

  /** 403 for a key whose scope does not allow the operation.
   *
   * Junior Tip [prefix, not equality]: this refusal is a frozen SENTENCE, not
   * a machine code; its stable head survives a rewording after the dash. */
  get isInsufficientScope(): boolean {
    return this.code.startsWith(AUTH_CODE_INSUFFICIENT_SCOPE_PREFIX);
  }

  /** 403 for a client key used on a data route. */
  get isImpersonationRequired(): boolean {
    return this.code === AUTH_CODE_IMPERSONATION_REQUIRED;
  }

  /** 401 for an unknown, revoked or inactive key. */
  get isInvalidKey(): boolean {
    return this.code === AUTH_CODE_INVALID_KEY;
  }
}

/** The typed refusal for a 401/403 body (every transport verb uses it). */
export function authErrorFromResponse(statusCode: number, bodyText: string): AnhurAuthRefusedError {
  const envelope = decodeErrorEnvelope(bodyText);
  if (envelope === null) return new AnhurAuthRefusedError(statusCode);
  return new AnhurAuthRefusedError(statusCode, envelope);
}
