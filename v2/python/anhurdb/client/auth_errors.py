"""What a 401/403 on a DATA route means, and the one decoder of the server's
``{"error","detail","error_type"}`` refusal envelope.

Domain, in one sentence: turn an authentication/authorization refusal into a
typed exception that keeps the server's reason instead of collapsing every
refusal into "authentication failed".

Messages are byte-identical to Go (``client/auth_refused.go``) and TypeScript
(``src/authErrors.ts``) whenever the body carries a code.
"""

import json
from typing import NamedTuple, Optional

from .exceptions import AnhurAuthError

# Codes the server puts in ``body.error`` on a 401/403 (AnhurDB
# server/middleware/auth*.go, handler/service_bridge.go). Prefer the
# ``AnhurAuthRefusedError`` predicates over comparing these strings.
AUTH_CODE_L1_ONLY = "l1_only"
AUTH_CODE_CHILD_SUSPENDED = "child_suspended"
AUTH_CODE_IMPERSONATION_REQUIRED = "client_key_requires_impersonation"
AUTH_CODE_INSUFFICIENT_SCOPE_PREFIX = "insufficient scope"
AUTH_CODE_INVALID_KEY = "invalid or inactive API key"


class ErrorEnvelope(NamedTuple):
    """The decoded refusal body. ``code`` is never empty."""

    code: str
    detail: str
    error_type: str


def decode_error_envelope(body_text: str) -> Optional[ErrorEnvelope]:
    """Read ``{"error","detail","error_type"}`` when the body is a JSON object
    whose ``error`` is a non-empty string; ``None`` otherwise.

    The Content-Type is ignored on purpose: ``http.Error`` on the server
    answers ``text/plain`` even when the body is JSON. A mistyped optional
    field is dropped on its own, never at the cost of the code (Go and
    TypeScript decode field by field the same way)."""
    try:
        envelope = json.loads(body_text)
    except ValueError:
        return None
    if not isinstance(envelope, dict):
        return None
    code = envelope.get("error")
    if not isinstance(code, str) or not code:
        return None
    detail = envelope.get("detail")
    error_type = envelope.get("error_type")
    return ErrorEnvelope(
        code=code,
        detail=detail if isinstance(detail, str) else "",
        error_type=error_type if isinstance(error_type, str) else "",
    )


class AnhurAuthRefusedError(AnhurAuthError):
    """Every HTTP 401/403 answer on a data route.

    ``code`` is ``body.error`` verbatim (``""`` for a non-JSON body),
    ``detail`` and ``error_type`` likewise; ``status_code`` is 401 or 403,
    ``kind`` is ``auth``, ``retryable`` is False. Parity: Go
    ``*AuthRefusedError``, TypeScript ``AnhurAuthRefusedError``.

    Junior Tip [why a subclass and not a new family, 2026-11-21]: the
    transport used to raise a bare ``AnhurAuthError("Authentication failed
    (HTTP 403)")`` for EVERY 401/403 and drop the body, so a scope refusal, an
    ``l1_only`` token on the wrong plane and a suspended child all read as a
    bad key. The server always said why; this keeps it. Subclassing keeps
    every existing ``except AnhurAuthError`` working unchanged."""

    def __init__(self, status_code: int, code: str = "", detail: str = "", error_type: str = ""):
        if code and detail:
            message = f"Authentication failed (HTTP {status_code}): {code}: {detail}"
        elif code:
            message = f"Authentication failed (HTTP {status_code}): {code}"
        else:
            message = f"Authentication failed (HTTP {status_code})"
        super().__init__(message, status_code=status_code)
        self.code = code
        self.detail = detail
        self.error_type = error_type

    @property
    def is_l1_only(self) -> bool:
        """403 for an ``l1_only`` token outside the L1 plane."""
        return self.code == AUTH_CODE_L1_ONLY

    @property
    def is_child_suspended(self) -> bool:
        """403 for a key whose child client is suspended."""
        return self.code == AUTH_CODE_CHILD_SUSPENDED

    @property
    def is_insufficient_scope(self) -> bool:
        """403 for a key whose scope does not allow the operation.

        Junior Tip [prefix, not equality]: this refusal is a frozen SENTENCE,
        not a machine code; its stable head survives a rewording after the
        dash."""
        return self.code.startswith(AUTH_CODE_INSUFFICIENT_SCOPE_PREFIX)

    @property
    def is_impersonation_required(self) -> bool:
        """403 for a client key used on a data route."""
        return self.code == AUTH_CODE_IMPERSONATION_REQUIRED

    @property
    def is_invalid_key(self) -> bool:
        """401 for an unknown, revoked or inactive key."""
        return self.code == AUTH_CODE_INVALID_KEY


def auth_error_from_response(status_code: int, body_text: str) -> AnhurAuthRefusedError:
    """The typed refusal for a 401/403 body (both transport paths use it)."""
    envelope = decode_error_envelope(body_text)
    if envelope is None:
        return AnhurAuthRefusedError(status_code)
    return AnhurAuthRefusedError(
        status_code, code=envelope.code, detail=envelope.detail, error_type=envelope.error_type
    )


__all__ = [
    "AUTH_CODE_CHILD_SUSPENDED",
    "AUTH_CODE_IMPERSONATION_REQUIRED",
    "AUTH_CODE_INSUFFICIENT_SCOPE_PREFIX",
    "AUTH_CODE_INVALID_KEY",
    "AUTH_CODE_L1_ONLY",
    "AnhurAuthRefusedError",
    "ErrorEnvelope",
    "auth_error_from_response",
    "decode_error_envelope",
]
