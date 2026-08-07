from typing import Optional


class AnhurError(Exception):
    """Base exception for all AnhurDB errors.

    ``status_code`` carries the HTTP status when the error came from an HTTP
    response (``None`` otherwise). It exists so callers can branch on the REAL
    status instead of parsing the message string — e.g. ``wait_for_upload``
    treats a transient 404 (read-your-writes lag) as "pending" without string
    matching. Additive and backward-compatible: ``AnhurError("msg")`` still
    works everywhere.
    """

    def __init__(self, message: str = "", status_code: Optional[int] = None):
        super().__init__(message)
        self.status_code = status_code


class AnhurAuthError(AnhurError):
    """Raised when authentication fails (e.g., invalid API key)."""
    pass


class AnhurQueryError(AnhurError):
    """Raised when a query is invalid or rejected by the server."""
    pass


class AnhurConnectionError(AnhurError):
    """Raised when the client cannot reach the AnhurDB server."""
    pass


class AnhurUploadWaitTimeout(AnhurError):
    """Raised by ``wait_for_upload`` when the upload did not reach a terminal
    status within the timeout. Parity: Go ``ErrUploadWaitTimeout`` /
    TypeScript ``AnhurUploadWaitTimeout``."""
    pass
