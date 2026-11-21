"""The two typed errors of the client-key surface, and the local refusals.

- ``AnhurClientAPIError``          the server REFUSED (non-2xx); ``code`` is
  ``body.error``.
- ``AnhurClientAPIResponseError``  the server said yes (2xx) but the answer
  cannot be trusted: malformed, or an impersonation echo weaker than asked.

Messages are byte-identical to Go (``client/client_key_errors.go``) and
TypeScript (``src/clientKeyErrors.ts``).
"""

import json
import re
from typing import Optional

from .client_key_types import scope_rank
from .exceptions import AnhurError, AnhurQueryError

RESPONSE_CODE_UNEXPECTED = "unexpected_response"
RESPONSE_CODE_L1_ONLY_NOT_HONOURED = "l1_only_not_honoured"
RESPONSE_CODE_SCOPE_NOT_HONOURED = "scope_not_honoured"

_EXCERPT_CHARACTERS = 500
_HARD_CEILING_CODES = frozenset({"child_limit", "tenant_limit"})


class AnhurClientAPIError(AnhurError):
    """A non-2xx answer from ``/api/v1/client/*``.

    ``code`` is ``body.error`` verbatim: a machine code on the ADR-0034
    envelope (``child_suspended``, ``child_not_found``, ``child_limit``,
    ``tenant_limit``, ``invalid_scope``, ``invalid_tenant_id``,
    ``invalid_profile``, ``invalid_child_id``, ``key_not_found``,
    ``key_not_owned``, …) or a frozen sentence for a few refusals ("client API
    key required", "tenant not owned by this client"). ``error_type`` is
    ``not_leader`` on the provably-uncommitted 503.

    Junior Tip [why the transport's generic mapping is bypassed]: the shared
    request path turns every 401/403 into ``AnhurAuthError`` WITHOUT the body,
    so a 403 ``child_suspended`` would read like a bad key. This surface reads
    the body on every status; ``kind`` still follows the status.

    Junior Tip [429 is not always "slow down"]: ``child_limit`` and
    ``tenant_limit`` are caps — the same call is refused until an operator
    raises the cap — so ``retryable`` is False for them."""

    def __init__(
        self,
        status_code: int,
        method: str,
        path: str,
        code: str = "",
        detail: str = "",
        error_type: str = "",
        body: str = "",
    ):
        prefix = f"Client API error (HTTP {status_code}) on {method} {path}: "
        if code and detail:
            message = f"{prefix}{code}: {detail}"
        elif code:
            message = prefix + code
        elif body:
            message = prefix + body
        else:
            message = prefix + "(empty body)"
        super().__init__(message, status_code=status_code)
        self.code = code
        self.detail = detail
        self.error_type = error_type
        self.method = method
        self.path = path
        self.body = body
        if code in _HARD_CEILING_CODES:
            self.retryable = False

    @property
    def is_not_leader(self) -> bool:
        """True for the provably-uncommitted 503 (safe to retry at once)."""
        return self.error_type == "not_leader"

    @classmethod
    def from_body(cls, status_code: int, method: str, path: str, body_text: str) -> "AnhurClientAPIError":
        """Decode ``{"error","detail","error_type"}`` when the body is a JSON
        object (whatever its Content-Type — ClientKeyOnly answers text/plain
        with a JSON body); otherwise keep a bounded excerpt."""
        try:
            envelope = json.loads(body_text)
        except ValueError:
            envelope = None
        if isinstance(envelope, dict) and isinstance(envelope.get("error"), str) and envelope["error"]:
            detail = envelope.get("detail")
            error_type = envelope.get("error_type")
            return cls(
                status_code, method, path,
                code=envelope["error"],
                detail=detail if isinstance(detail, str) else "",
                error_type=error_type if isinstance(error_type, str) else "",
            )
        return cls(status_code, method, path, body=body_text.strip()[:_EXCERPT_CHARACTERS])


class AnhurClientAPIResponseError(AnhurError):
    """A 2xx answer the SDK refuses to hand over (kind ``server``, not retryable).

    ``key_id`` names the minted token when one exists: it stays valid until it
    expires, so a careful caller revokes it with ``revoke_impersonation``."""

    def __init__(self, method: str, path: str, code: str, reason: str, key_id: str = ""):
        super().__init__(
            f"Untrusted client API response from {method} {path}: {code}: {reason}",
            kind=AnhurError.KIND_SERVER,
        )
        self.retryable = False
        self.method = method
        self.path = path
        self.code = code
        self.reason = reason
        self.key_id = key_id


def unexpected_response(method: str, path: str, reason: str) -> AnhurClientAPIResponseError:
    """The unexpected_response refusal."""
    return AnhurClientAPIResponseError(method, path, RESPONSE_CODE_UNEXPECTED, reason)


def missing_field(field_name: str) -> str:
    """Reason used when a required response field is absent."""
    return f'"{field_name}" is missing or invalid'


# ── Local refusals (400, nothing is sent) ──────────────────────────────────
#
# Junior Tip [why refuse locally what the server also refuses]: these inputs
# can never succeed. Refusing them before the request keeps them out of the
# server's audit log and, for the child id, out of the URL path — a
# non-canonical id spliced into a path could address another route.

_CANONICAL_UUID = re.compile(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")


def invalid_param(message: str) -> AnhurQueryError:
    """The typed client-side refusal (status 400, ``invalid_request``)."""
    return AnhurQueryError(f"INVALID_PARAM: client API: {message}", status_code=400)


def is_canonical_uuid(candidate: object) -> bool:
    """36-character hyphenated hex, either case (``isCanonicalChildID``)."""
    return isinstance(candidate, str) and _CANONICAL_UUID.match(candidate) is not None


def require_canonical_child_id(child_id: str) -> None:
    if not is_canonical_uuid(child_id):
        raise invalid_param(f'child_id must be a canonical UUID (got "{child_id}")')


def require_tenant_id(tenant_id: str) -> None:
    trimmed_tenant_id = (tenant_id or "").strip()
    if not trimmed_tenant_id:
        raise invalid_param("tenant_id is required")
    if trimmed_tenant_id == "*":
        raise invalid_param('tenant_id must not be "*"')


def require_scope(scope: Optional[str], allow_empty: bool) -> None:
    if not scope:
        if allow_empty:
            return
        raise invalid_param("scope is required on the child impersonate route")
    if scope_rank(scope) == 0:
        raise invalid_param(f'scope must be one of admin, write, read_only (got "{scope}")')


__all__ = [
    "AnhurClientAPIError",
    "AnhurClientAPIResponseError",
    "RESPONSE_CODE_UNEXPECTED",
    "RESPONSE_CODE_L1_ONLY_NOT_HONOURED",
    "RESPONSE_CODE_SCOPE_NOT_HONOURED",
]
