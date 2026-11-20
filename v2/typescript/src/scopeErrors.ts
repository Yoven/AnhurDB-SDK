/**
 * AnhurDB TypeScript SDK — the three L2 tenant-library answers (ADR-0029) as
 * typed errors: a library store that collides with a registered tenant, an
 * upload id ambiguous across planes, and a library delete-by-file that failed
 * on one of its two stores. Parity: Go `client/scope_errors.go`, Python
 * `anhurdb/client/scope_errors.py`.
 *
 * Wire shapes (AnhurDB fix/l2-lib-scope):
 *  - collision: `{"error": "...tenant library store id is a registered tenant;
 *    rename one of the two tenants before using tenant_shared"}` — HTTP 409 on
 *    search, by-id and delete-by-file (`handler/search_scope_request.go:35-44`),
 *    HTTP 400 on upload (`service/upload_destination.go:154-167`).
 *  - ambiguous upload status: HTTP 409 `{"error": "record N is a file upload in
 *    more than one plane; pass ?scope=..."}` (`handler/upload_status_scope.go:186-191`).
 *  - partial delete: HTTP 500 `{"error", "partial": true, "legs": [...]}`
 *    (`handler/record_delete_file_library.go:53-57`).
 *
 * Junior Tip [why the collision is matched by TEXT]: the server sends no
 * machine code for it, only `ErrTenantLibraryCollision`'s sentence. The
 * session-record-limit 409 has its own `code` and prefix and is checked FIRST
 * in clientResponse.ts, so the two can never be confused.
 */

import { AnhurError, AnhurQueryError } from "./errors.js";
import type { DeleteFileLeg } from "./deleteFileTypes.js";

const TENANT_LIBRARY_COLLISION_TEXT = "tenant library store id is a registered tenant";
const UPLOAD_STATUS_AMBIGUOUS_TEXT = "is a file upload in more than one plane";
const MESSAGE_EXCERPT_CHARS = 500;

/** The tenant library store `{c}_{t}_lib` is another registered tenant's
 * store; every tenant_shared call is refused. Never retryable: it heals only
 * when an operator renames one of the two tenants. `statusCode` is 409, or 400
 * on upload. */
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

/**
 * Return the typed L2 error for (status, FULL body), or null. The caller passes
 * the untruncated body: partial-delete legs can exceed the message excerpt.
 */
export function scopeErrorFromResponse(statusCode: number, bodyText: string): AnhurError | null {
  if (statusCode !== 400 && statusCode !== 409 && statusCode !== 500) return null;
  const fields = parseErrorBody(bodyText);
  const serverMessage = typeof fields.error === "string" ? fields.error.trim() : "";
  const excerpt = serverMessage.slice(0, MESSAGE_EXCERPT_CHARS);
  if ((statusCode === 400 || statusCode === 409) && serverMessage.includes(TENANT_LIBRARY_COLLISION_TEXT)) {
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
