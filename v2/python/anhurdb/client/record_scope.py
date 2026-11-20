"""Which STORE a record id names (ADR-0029, L2 tenant library).

One domain in one sentence: the closed set of planes a by-id call may address,
the local check for it, and the two rules that pick the right plane for a
search hit or an upload. Parity: Go ``client/record_scope.go``, TypeScript
``src/recordScope.ts``.

Junior Tip [why a by-id call needs a plane at all — server fix/l2-lib-scope,
2026-11-20]: a record id is a SQLite rowid PER STORE. Since tenant-library
uploads land in ``{c}_{t}_lib``, id 42 from a tenant_shared hit names a library
row there — and the SAME 42 names an unrelated chat row in ``{c}_{t}``. Without
a scope the server reads (or PATCHes, archives, supersedes) the caller's own
store, i.e. the wrong record, and answers 200. With a scope the server resolves
the store from the authenticated caller and proves every id is a live row of
that plane, or answers 404 (``server/service/record_scoped_by_id.go:23-52``).
No scope keeps the old behaviour.
"""

from typing import Dict, Optional

from .exceptions import AnhurQueryError

SCOPE_SESSIONS = "sessions"
SCOPE_TENANT_SHARED = "tenant_shared"
SCOPE_TENANT_SHARED_LEGACY = "tenant_shared_legacy"
SCOPE_CLIENT_SHARED = "client_shared"

#: Every value a by-id call accepts. ``shared_all`` is absent on purpose: it
#: names TWO stores and the server refuses it for by-id as well.
RECORD_SCOPES = frozenset(
    {SCOPE_SESSIONS, SCOPE_TENANT_SHARED, SCOPE_TENANT_SHARED_LEGACY, SCOPE_CLIENT_SHARED}
)

_TENANT_LIBRARY_SESSION_SUFFIX = "_lib"


def tenant_shared_scope_for_hit(record_uuid: str) -> str:
    """By-id scope for a hit of a ``tenant_shared`` search.

    ``tenant_shared`` when the hit's uuid (its session) ends in ``_lib`` — a row
    of the L2 library store — otherwise ``tenant_shared_legacy``.

    Junior Tip [why the search scope alone is not enough]: one tenant_shared
    search reads TWO stores and merges them, so "tenant_shared" does not say
    which store a given hit came from. The session uuid does; the server admits
    a row into a leg with the same suffix rule.
    """
    if (record_uuid or "").strip().endswith(_TENANT_LIBRARY_SESSION_SUFFIX):
        return SCOPE_TENANT_SHARED
    return SCOPE_TENANT_SHARED_LEGACY


def upload_status_scope(mode: Optional[str], session_id: Optional[str]) -> str:
    """Plane an upload lands in, mirroring the server's ``ResolveUploadDestination``:
    chat (or a session without a mode) → ``sessions``, ``client_shared`` →
    ``client_shared``, anything else (default, tenant_shared) → ``tenant_shared``."""
    normalized_mode = (mode or "").strip().lower()
    if not normalized_mode and (session_id or "").strip():
        normalized_mode = "chat"
    if normalized_mode == "chat":
        return SCOPE_SESSIONS
    if normalized_mode == SCOPE_CLIENT_SHARED:
        return SCOPE_CLIENT_SHARED
    return SCOPE_TENANT_SHARED


def resolve_record_scope(method: str, scope: Optional[str]) -> Optional[str]:
    """Validate ``scope`` locally; ``None``/blank means "no scope".

    Raises:
        AnhurQueryError: HTTP-400-classified (invalid_request, not retryable)
            for ``shared_all`` or any unknown value — before any request.
    """
    trimmed_scope = (scope or "").strip()
    if not trimmed_scope:
        return None
    if trimmed_scope not in RECORD_SCOPES:
        raise AnhurQueryError(
            f"INVALID_PARAM: {method}: scope {trimmed_scope!r} is not addressable by id "
            "(want sessions|tenant_shared|tenant_shared_legacy|client_shared)",
            status_code=400,
        )
    return trimmed_scope


def scope_params(scope: Optional[str]) -> Optional[Dict[str, str]]:
    """``{"scope": scope}`` or ``None`` — no scope leaves the request unchanged."""
    return {"scope": scope} if scope else None


__all__ = [
    "SCOPE_SESSIONS",
    "SCOPE_TENANT_SHARED",
    "SCOPE_TENANT_SHARED_LEGACY",
    "SCOPE_CLIENT_SHARED",
    "RECORD_SCOPES",
    "tenant_shared_scope_for_hit",
    "upload_status_scope",
    "resolve_record_scope",
    "scope_params",
]
