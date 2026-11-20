"""Recognise the three L2 tenant-library answers (ADR-0029) in an HTTP error.

One domain in one sentence: turn a collision refusal, an ambiguous upload id and
a partial library delete into typed exceptions. Parity: Go
``client/scope_errors.go``, TypeScript ``src/scopeErrors.ts``.

Wire shapes (AnhurDB fix/l2-lib-scope):
  - collision: ``{"error": "...tenant library store id is a registered tenant;
    rename one of the two tenants before using tenant_shared"}`` — HTTP 409 on
    search, by-id and delete-by-file (``handler/search_scope_request.go:35-44``),
    HTTP 400 on upload (``service/upload_destination.go:154-167``).
  - ambiguous upload status: HTTP 409 ``{"error": "record N is a file upload in
    more than one plane; pass ?scope=..."}`` (``handler/upload_status_scope.go:186-191``).
  - partial delete: HTTP 500 ``{"error", "partial": true, "legs": [...]}``
    (``handler/record_delete_file_library.go:53-57``).

Junior Tip [why the collision is matched by TEXT]: the server sends no machine
code for it, only ``ErrTenantLibraryCollision``'s sentence. The
session-record-limit 409 has its own ``code`` and prefix and is checked FIRST
by the transport, so the two can never be confused.
"""

import json
from typing import Any, Optional

from .exceptions import (
    AnhurError,
    AnhurLibraryDeletePartialError,
    AnhurTenantLibraryCollisionError,
    AnhurUploadStatusAmbiguousError,
)
from ..models.record import DeleteFileLeg

TENANT_LIBRARY_COLLISION_TEXT = "tenant library store id is a registered tenant"
UPLOAD_STATUS_AMBIGUOUS_TEXT = "is a file upload in more than one plane"


def _parse(body_text: str) -> dict:
    try:
        parsed: Any = json.loads(body_text) if body_text else None
    except json.JSONDecodeError:
        parsed = None
    if not isinstance(parsed, dict):
        return {"error": body_text}
    return parsed


def scope_error_from_response(status: int, body_text: str) -> Optional[AnhurError]:
    """Return the typed L2 error for (status, FULL body), or None.

    The caller passes the untruncated body: partial-delete legs can exceed the
    500-character excerpt used in messages.
    """
    if status not in (400, 409, 500):
        return None
    parsed = _parse(body_text)
    server_message = str(parsed.get("error") or "").strip()
    if status in (400, 409) and TENANT_LIBRARY_COLLISION_TEXT in server_message:
        return AnhurTenantLibraryCollisionError(
            f"AnhurDB refused the tenant library (HTTP {status}): {server_message[:500]} "
            "(not retryable: rename one of the two tenants)",
            status_code=status,
            server_message=server_message,
        )
    if status == 409 and UPLOAD_STATUS_AMBIGUOUS_TEXT in server_message:
        return AnhurUploadStatusAmbiguousError(
            f"Conflict (HTTP 409): {server_message[:500]} (pass scope=upload_result.scope)",
            server_message=server_message,
        )
    if status == 500 and parsed.get("partial") is True:
        raw_legs = parsed.get("legs")
        legs = [
            DeleteFileLeg.model_validate(leg)
            for leg in (raw_legs if isinstance(raw_legs, list) else [])
            if isinstance(leg, dict)
        ]
        return AnhurLibraryDeletePartialError(
            f"Server error (HTTP 500): {server_message[:500]} "
            "(partial tenant library delete; retry is safe)",
            legs=legs,
            server_message=server_message,
        )
    return None


__all__ = ["scope_error_from_response"]
