/**
 * The two typed errors of the client-key surface, and the local refusals.
 *
 * - `AnhurClientAPIError`          the server REFUSED (non-2xx); `code` is `body.error`.
 * - `AnhurClientAPIResponseError`  the server said yes (2xx) but the answer
 *   cannot be trusted: malformed, or an impersonation echo weaker than asked.
 *
 * Messages are byte-identical to Go (`client/client_key_errors.go`) and Python
 * (`anhurdb/client/client_key_errors.py`).
 */

import { AnhurError, AnhurQueryError } from "./errors.js";
import { scopeRank } from "./clientKeyTypes.js";

export const RESPONSE_CODE_UNEXPECTED = "unexpected_response";
export const RESPONSE_CODE_L1_ONLY_NOT_HONOURED = "l1_only_not_honoured";
export const RESPONSE_CODE_SCOPE_NOT_HONOURED = "scope_not_honoured";

const EXCERPT_CHARACTERS = 500;
const HARD_CEILING_CODES: ReadonlySet<string> = new Set(["child_limit", "tenant_limit"]);

/**
 * A non-2xx answer from `/api/v1/client/*`.
 *
 * `code` is `body.error` verbatim: a machine code on the ADR-0034 envelope
 * (`child_suspended`, `child_not_found`, `child_limit`, `tenant_limit`,
 * `invalid_scope`, `invalid_tenant_id`, `invalid_profile`, `invalid_child_id`,
 * `key_not_found`, `key_not_owned`, …) or a frozen sentence for a few refusals
 * ("client API key required", "tenant not owned by this client").
 * `errorType` is `not_leader` on the provably-uncommitted 503.
 *
 * Junior Tip [why the shared status mapping is bypassed]: `typedErrorForResponse`
 * turns every 401/403 into `AnhurAuthError` WITHOUT the body, so a 403
 * `child_suspended` would read like a bad key. This surface reads the body on
 * every status; `kind` still follows the status.
 *
 * Junior Tip [429 is not always "slow down"]: `child_limit` and `tenant_limit`
 * are caps, so `retryable` is false for them.
 */
export class AnhurClientAPIError extends AnhurError {
  readonly code: string;
  readonly detail: string;
  readonly errorType: string;
  readonly method: string;
  readonly path: string;
  readonly body: string;

  constructor(
    statusCode: number,
    method: string,
    path: string,
    fields: { code?: string; detail?: string; errorType?: string; body?: string },
  ) {
    const code = fields.code ?? "";
    const detail = fields.detail ?? "";
    const body = fields.body ?? "";
    const prefix = `Client API error (HTTP ${statusCode}) on ${method} ${path}: `;
    let message: string;
    if (code && detail) message = `${prefix}${code}: ${detail}`;
    else if (code) message = prefix + code;
    else if (body) message = prefix + body;
    else message = prefix + "(empty body)";
    super(message, statusCode);
    this.name = "AnhurClientAPIError";
    this.code = code;
    this.detail = detail;
    this.errorType = fields.errorType ?? "";
    this.method = method;
    this.path = path;
    this.body = body;
    // `retryable` is set by AnhurError from the status; only the caps differ.
    if (HARD_CEILING_CODES.has(code)) (this as { retryable: boolean }).retryable = false;
  }

  /** True for the provably-uncommitted 503 (safe to retry at once). */
  get isNotLeader(): boolean {
    return this.errorType === "not_leader";
  }

  /** Decode `{"error","detail","error_type"}` when the body is a JSON object
   * (whatever its Content-Type — ClientKeyOnly answers text/plain with a JSON
   * body); otherwise keep a bounded excerpt. */
  static fromBody(statusCode: number, method: string, path: string, bodyText: string): AnhurClientAPIError {
    let envelope: unknown = null;
    try {
      envelope = JSON.parse(bodyText);
    } catch {
      envelope = null;
    }
    if (envelope !== null && typeof envelope === "object" && !Array.isArray(envelope)) {
      const fields = envelope as Record<string, unknown>;
      if (typeof fields.error === "string" && fields.error) {
        return new AnhurClientAPIError(statusCode, method, path, {
          code: fields.error,
          detail: typeof fields.detail === "string" ? fields.detail : "",
          errorType: typeof fields.error_type === "string" ? fields.error_type : "",
        });
      }
    }
    return new AnhurClientAPIError(statusCode, method, path, {
      body: bodyText.trim().slice(0, EXCERPT_CHARACTERS),
    });
  }
}

/** A 2xx answer the SDK refuses to hand over (kind `server`, not retryable).
 * `keyId` names the minted token when one exists: it stays valid until it
 * expires, so a careful caller revokes it with `revokeImpersonation`. */
export class AnhurClientAPIResponseError extends AnhurError {
  readonly method: string;
  readonly path: string;
  readonly code: string;
  readonly reason: string;
  readonly keyId: string;
  override readonly retryable = false;

  constructor(method: string, path: string, code: string, reason: string, keyId = "") {
    super(`Untrusted client API response from ${method} ${path}: ${code}: ${reason}`, undefined, "server");
    this.name = "AnhurClientAPIResponseError";
    this.method = method;
    this.path = path;
    this.code = code;
    this.reason = reason;
    this.keyId = keyId;
  }
}

/** The unexpected_response refusal. */
export function unexpectedResponse(method: string, path: string, reason: string): AnhurClientAPIResponseError {
  return new AnhurClientAPIResponseError(method, path, RESPONSE_CODE_UNEXPECTED, reason);
}

/** Reason used when a required response field is absent. */
export function missingField(fieldName: string): string {
  return `"${fieldName}" is missing or invalid`;
}

// ── Local refusals (400, nothing is sent) ──────────────────────────────────
//
// Junior Tip [why refuse locally what the server also refuses]: these inputs
// can never succeed. Refusing them before the request keeps them out of the
// server's audit log and, for the child id, out of the URL path — a
// non-canonical id spliced into a path could address another route.

const CANONICAL_UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

/** The typed client-side refusal (status 400, `invalid_request`). */
export function invalidParam(message: string): AnhurQueryError {
  return new AnhurQueryError(`INVALID_PARAM: client API: ${message}`, 400);
}

/** 36-character hyphenated hex, either case (`isCanonicalChildID`). */
export function isCanonicalUuid(candidate: unknown): candidate is string {
  return typeof candidate === "string" && CANONICAL_UUID.test(candidate);
}

export function requireCanonicalChildId(childId: string): void {
  if (!isCanonicalUuid(childId)) {
    throw invalidParam(`child_id must be a canonical UUID (got "${childId}")`);
  }
}

export function requireTenantId(tenantId: string): void {
  const trimmedTenantId = (tenantId ?? "").trim();
  if (!trimmedTenantId) throw invalidParam("tenant_id is required");
  if (trimmedTenantId === "*") throw invalidParam('tenant_id must not be "*"');
}

export function requireScope(scope: string | undefined, allowEmpty: boolean): void {
  if (!scope) {
    if (allowEmpty) return;
    throw invalidParam("scope is required on the child impersonate route");
  }
  if (scopeRank(scope) === 0) {
    throw invalidParam(`scope must be one of admin, write, read_only (got "${scope}")`);
  }
}
