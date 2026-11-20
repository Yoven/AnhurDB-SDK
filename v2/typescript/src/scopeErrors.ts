/**
 * AnhurDB TypeScript SDK — the three L2 tenant-library answers (ADR-0029) as
 * typed errors: a library store that collides with a registered tenant, an
 * upload id ambiguous across planes, and a library delete-by-file that failed
 * on one of its two stores. Parity: Go `client/scope_errors.go`, Python
 * `anhurdb/client/scope_errors.py`.
 *
 * Wire shapes (AnhurDB fix/l2-lib-scope, 4574c4d):
 *  - collision: HTTP 409 `{"error": "...tenant library store id is a registered
 *    tenant; rename one of the two tenants before using tenant_shared", "code":
 *    "tenant_library_collision"}` on EVERY door — search, by-id, delete-by-file
 *    and upload (`handler/search_scope_request.go` writeScopeLegError). Older
 *    servers sent no code and answered the upload with 400; both are still
 *    recognised by the sentence. An unreadable tenant registry is HTTP 503
 *    (upload included) and stays the ordinary retryable unavailable error.
 *  - ambiguous upload status: HTTP 409 `{"error": "record N is a file upload in
 *    more than one plane; pass ?scope=..."}` (`handler/upload_status_scope.go:186-191`).
 *  - partial delete: HTTP 500 `{"error", "partial": true, "legs": [...]}`
 *    (`handler/record_delete_file_library.go:53-57`).
 *
 * Junior Tip [the code first, the sentence only as a fallback]: the sentence
 * names the store and the fix and may be reworded; the `code` is the contract.
 * The sentence match stays ONLY for servers older than 4574c4d, which sent no
 * code. The session-record-limit 409 has its own code and prefix and is checked
 * FIRST in clientResponse.ts, so the two can never be confused.
 */

import { AnhurError, AnhurQueryError } from "./errors.js";
import type { DeleteFileLeg } from "./deleteFileTypes.js";

/** The machine-readable `code` of the collision refusal (server
 * `service.TenantLibraryCollisionErrorCode`). */
export const TENANT_LIBRARY_COLLISION_CODE = "tenant_library_collision";
const TENANT_LIBRARY_COLLISION_TEXT = "tenant library store id is a registered tenant";
const UPLOAD_STATUS_AMBIGUOUS_TEXT = "is a file upload in more than one plane";
const MESSAGE_EXCERPT_CHARS = 500;

/** The tenant library store `{c}_{t}_lib` is another registered tenant's
 * store; every tenant_shared call is refused. Never retryable: it heals only
 * when an operator renames one of the two tenants. `statusCode` is 409 (400 on
 * upload from servers older than 4574c4d). */
export class AnhurTenantLibraryCollisionError extends AnhurQueryError {
  readonly serverMessage: string;

  constructor(message: string, statusCode: number, serverMessage: string) {
    super(message, statusCode);
    this.name = "AnhurTenantLibraryCollisionError";
    this.serverMessage = serverMessage;
  }
}

/** An unscoped `uploadStatus` matched a file row in more than one plane (HTTP
 * 409). Pass `{ scope: upload.scope }`. Not retryable. */
export class AnhurUploadStatusAmbiguousError extends AnhurQueryError {
  readonly serverMessage: string;

  constructor(message: string, serverMessage: string) {
    super(message, 409, "conflict");
    this.name = "AnhurUploadStatusAmbiguousError";
    this.serverMessage = serverMessage;
  }
}

/** A tenant-library `deleteFile` ran on its two stores (two Raft groups, no
 * cross-group transaction) and at least one failed: HTTP 500 `partial: true`.
 * `legs` holds EVERY leg's outcome, the successful ones too.
 *
 * Junior Tip [retryable on purpose]: the server's delete is keyed by
 * (session, prefix) and is idempotent, and a leg already emptied is not
 * dispatched again, so repeating the same call is the documented recovery. */
export class AnhurLibraryDeletePartialError extends AnhurError {
  readonly legs: DeleteFileLeg[];
  readonly serverMessage: string;

  constructor(message: string, legs: DeleteFileLeg[], serverMessage: string) {
    super(message, 500, "server");
    this.name = "AnhurLibraryDeletePartialError";
    this.legs = legs;
    this.serverMessage = serverMessage;
  }
}

/** The parsed error body, or the raw text as `error` when it is not JSON. */
function parseErrorBody(bodyText: string): Record<string, unknown> {
  try {
    const parsed: unknown = JSON.parse(bodyText);
    if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>;
    }
  } catch {
    // fall through: plain text
  }
  return { error: bodyText };
}

/** Code first; the sentence only when the body carries NO code (an older
 * server). A body whose code names another refusal is never re-read by its text. */
function isTenantLibraryCollision(fields: Record<string, unknown>, serverMessage: string): boolean {
  if (typeof fields.code === "string" && fields.code !== "") {
    return fields.code === TENANT_LIBRARY_COLLISION_CODE;
  }
  return serverMessage.includes(TENANT_LIBRARY_COLLISION_TEXT);
}

/**
 * Return the typed L2 error for (status, FULL body), or null. The caller passes
 * the untruncated body: partial-delete legs can exceed the message excerpt.
 */
export function scopeErrorFromResponse(statusCode: number, bodyText: string): AnhurError | null {
  if (statusCode !== 400 && statusCode !== 409 && statusCode !== 500) return null;
  const fields = parseErrorBody(bodyText);
  const serverMessage = typeof fields.error === "string" ? fields.error.trim() : "";
  const excerpt = serverMessage.slice(0, MESSAGE_EXCERPT_CHARS);
  if ((statusCode === 400 || statusCode === 409) && isTenantLibraryCollision(fields, serverMessage)) {
    return new AnhurTenantLibraryCollisionError(
      `AnhurDB refused the tenant library (HTTP ${statusCode}): ${excerpt} ` +
        "(not retryable: rename one of the two tenants)",
      statusCode,
      serverMessage,
    );
  }
  if (statusCode === 409 && serverMessage.includes(UPLOAD_STATUS_AMBIGUOUS_TEXT)) {
    return new AnhurUploadStatusAmbiguousError(
      `Conflict (HTTP 409): ${excerpt} (pass { scope: upload.scope })`,
      serverMessage,
    );
  }
  if (statusCode === 500 && fields.partial === true) {
    const rawLegs = Array.isArray(fields.legs) ? fields.legs : [];
    const legs = rawLegs.filter(
      (leg): leg is DeleteFileLeg => leg !== null && typeof leg === "object",
    );
    return new AnhurLibraryDeletePartialError(
      `Server error (HTTP 500): ${excerpt} (partial tenant library delete; retry is safe)`,
      legs,
      serverMessage,
    );
  }
  return null;
}
