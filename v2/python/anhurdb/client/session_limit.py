"""Recognise the per-session record cap refusal in a 409 answer.

Domain, in one sentence: turn "this session is full" into a typed
``AnhurSessionRecordLimitError`` carrying the session, the cap and the count,
so the refusal can never be mistaken for an outage or a generic conflict.

Wire contract (ADR-0033, AnhurDB): HTTP 409
``{"error": "session has reached the maximum of N records ...", "code":
"session_record_limit", "limit": N, "count": M, "session_uuid": "..."}`` — all
five keys always present (``server/handler/service_bridge.go:81-82,109-125``;
only producer ``server/service/record_session_cap.go:178``).

Junior Tip [code-only since 4.0.0]: earlier SDKs also matched the sentence
prefix and read N back out of it with a regex, for servers that sent no
``code``. No such server exists any more, and the sentence is prose the server
may reword — a contract keyed on prose breaks the day a comma moves. The
``code`` is the contract; a 409 without it is the generic conflict.

gRPC (ResourceExhausted + ErrorInfo ``SESSION_RECORD_LIMIT``,
``server/grpc/service_errors.go:39-40,123``) is part of the contract too, but
this SDK has no gRPC transport, so there is no gRPC path to classify here.
"""

import json
from typing import Any, Optional

from .exceptions import AnhurSessionRecordLimitError

SESSION_RECORD_LIMIT_CODE = AnhurSessionRecordLimitError.CODE


def _wire_int(value: Any) -> Optional[int]:
    """An int from the wire, or None. ``bool`` is an int subclass — refuse it."""
    if isinstance(value, int) and not isinstance(value, bool):
        return value
    return None


def session_record_limit_from_response(
    status: int, body_text: str
) -> Optional[AnhurSessionRecordLimitError]:
    """Return the typed refusal, or None when the answer is anything else.

    Junior Tip [a malformed body is NOT typed]: ``limit``, ``count`` and
    ``session_uuid`` are required on the typed error because the server always
    sends them. A body that carries the code but lacks one of them breaks the
    contract; inventing ``0`` or ``""`` would hand the caller a number the
    server never said. Such a body stays the generic 409 ``AnhurQueryError``,
    whose message quotes the raw body for diagnosis. Same rule as the
    TypeScript SDK (``src/sessionLimit.ts``).
    """
    if status != 409:
        return None
    try:
        parsed: Any = json.loads(body_text) if body_text else None
    except json.JSONDecodeError:
        return None
    if not isinstance(parsed, dict):
        return None
    if parsed.get("code") != SESSION_RECORD_LIMIT_CODE:
        return None

    limit = _wire_int(parsed.get("limit"))
    count = _wire_int(parsed.get("count"))
    session_uuid = parsed.get("session_uuid")
    if limit is None or count is None or not isinstance(session_uuid, str):
        return None
    server_message = str(parsed.get("error") or "").strip()

    # Server sentence first: log readers truncate, and it is what a human
    # recognises. Byte-identical layout to the TypeScript SDK.
    message = (
        f"Conflict (HTTP 409): {server_message[:500]} (session record limit: "
        f"session={session_uuid}, limit={limit}, count={count})"
    )
    return AnhurSessionRecordLimitError(
        message,
        session_uuid=session_uuid,
        limit=limit,
        count=count,
        server_message=server_message,
    )
