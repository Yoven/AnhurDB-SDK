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

    # Junior Tip [kind + retryable, 2026-08-13]: a benchmark run died on an
    # error whose message was the empty string and whose status was None — an
    # asyncio.TimeoutError that escaped the aiohttp.ClientError handler. Callers
    # cannot decide anything from that. Every error now carries a non-empty
    # message, a kind, and an explicit retryable flag, so nobody has to
    # re-derive "is this worth retrying" by parsing strings.
    #
    # Retryable does NOT mean safe to retry automatically for writes: a timeout
    # on a write means the server may or may not have committed it. The SDK
    # never auto-retries writes; idempotency is the caller's decision.
    KIND_AUTH = "auth"
    KIND_INVALID_REQUEST = "invalid_request"
    KIND_NOT_FOUND = "not_found"
    KIND_CONFLICT = "conflict"
    KIND_RATE_LIMITED = "rate_limited"
    KIND_UNAVAILABLE = "unavailable"
    KIND_TIMEOUT = "timeout"
    KIND_TRANSPORT = "transport"
    KIND_SERVER = "server"

    _RETRYABLE_KINDS = frozenset(
        {KIND_RATE_LIMITED, KIND_UNAVAILABLE, KIND_TIMEOUT, KIND_TRANSPORT, KIND_SERVER}
    )

    def __init__(
        self,
        message: str = "",
        status_code: Optional[int] = None,
        kind: Optional[str] = None,
    ):
        resolved_kind = kind or self._kind_from_status(status_code)
        if not message:
            message = self._default_message(resolved_kind, status_code)
        super().__init__(message)
        self.message = message
        self.status_code = status_code
        self.kind = resolved_kind
        self.retryable = resolved_kind in self._RETRYABLE_KINDS

    @staticmethod
    def _kind_from_status(status_code: Optional[int]) -> str:
        """Classify by HTTP status. No status means the request never landed."""
        if status_code is None:
            return AnhurError.KIND_TRANSPORT
        if status_code == 401 or status_code == 403:
            return AnhurError.KIND_AUTH
        if status_code == 404:
            return AnhurError.KIND_NOT_FOUND
        if status_code == 409:
            return AnhurError.KIND_CONFLICT
        if status_code == 429:
            return AnhurError.KIND_RATE_LIMITED
        if status_code == 503:
            return AnhurError.KIND_UNAVAILABLE
        if 400 <= status_code < 500:
            return AnhurError.KIND_INVALID_REQUEST
        if status_code >= 500:
            return AnhurError.KIND_SERVER
        return AnhurError.KIND_SERVER

    @staticmethod
    def _default_message(kind: str, status_code: Optional[int]) -> str:
        """A message is never empty — an unexplained failure is unactionable."""
        if kind == AnhurError.KIND_TIMEOUT:
            return "request timed out (the server may still have processed it)"
        if kind == AnhurError.KIND_TRANSPORT:
            return "could not reach AnhurDB"
        if kind == AnhurError.KIND_UNAVAILABLE:
            return "service temporarily unavailable — retry"
        if status_code is not None:
            return "AnhurDB request failed (HTTP %d)" % status_code
        return "AnhurDB request failed"


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


class AnhurSessionRecordLimitError(AnhurQueryError):
    """Raised when AnhurDB refuses a write because the target session already
    holds its maximum number of records (ADR-0033). Parity: Go
    ``*SessionRecordLimitError`` / ``ErrSessionRecordLimit``, TypeScript
    ``AnhurSessionRecordLimitError``.

    Recognised on both server generations: the new structured 409 body
    (``code == "session_record_limit"`` plus ``limit``/``count``/``session_uuid``)
    and the old text-only 409 (``"session has reached the maximum of N records"``).
    ``session_uuid`` and ``count`` are ``None`` when an old server omitted them;
    ``limit`` is read back from the server's own sentence when the field is absent.

    Junior Tip [non-retryable is not "drop it"]: repeating the same write into
    the same session gets the same refusal until the cap changes server-side,
    so ``retryable`` is False. A caller that must not lose the write keeps it
    and retries LATER; writing it into another session to dodge the cap would
    merge two conversations. Subclassing ``AnhurQueryError`` keeps every
    existing ``except AnhurQueryError`` / ``status_code == 409`` branch working.
    """

    CODE = "session_record_limit"

    def __init__(
        self,
        message: str,
        session_uuid: Optional[str] = None,
        limit: Optional[int] = None,
        count: Optional[int] = None,
        server_message: str = "",
    ):
        super().__init__(message, status_code=409, kind=AnhurError.KIND_CONFLICT)
        self.session_uuid = session_uuid
        self.limit = limit
        self.count = count
        self.server_message = server_message
        self.code = self.CODE
