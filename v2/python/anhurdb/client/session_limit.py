"""Recognise the per-session record cap refusal in a 409 answer.

Domain, in one sentence: turn "this session is full" into a typed
``AnhurSessionRecordLimitError`` carrying the session, the cap and the count,
so the refusal can never be mistaken for an outage or a generic conflict.

Wire contract (ADR-0033, AnhurDB):
  - NEW servers: HTTP 409 ``{"error": "session has reached the maximum of N
    records ...", "code": "session_record_limit", "limit": N, "count": M,
    "session_uuid": "..."}``
  - OLD servers (production until the deploy): HTTP 409
    ``{"error": "session has reached the maximum of N records"}`` (or the same
    sentence as plain text).

gRPC (ResourceExhausted + ErrorInfo ``SESSION_RECORD_LIMIT``; legacy
AlreadyExists with the same text) is part of the contract too, but this SDK has
no gRPC transport, so there is no gRPC path to classify here.
"""

import json
import re
from typing import Any, Optional

from .exceptions import AnhurSessionRecordLimitError

SESSION_RECORD_LIMIT_CODE = AnhurSessionRecordLimitError.CODE
SESSION_RECORD_LIMIT_TEXT_PREFIX = "session has reached the maximum of"
_LIMIT_IN_TEXT = re.compile(r"maximum of (\d+) records")


def _optional_int(value: Any) -> Optional[int]:
    """An int from the wire, or None. ``bool`` is an int subclass — refuse it."""
    if isinstance(value, int) and not isinstance(value, bool):
        return value
    return None


def _text(value: Optional[int]) -> str:
    return "unknown" if value is None else str(value)


def session_record_limit_from_response(
    status: int, body_text: str
) -> Optional[AnhurSessionRecordLimitError]:
    """Return the typed refusal, or None when the answer is anything else."""
    if status != 409:
        return None
    parsed: Any = None
    try:
        parsed = json.loads(body_text) if body_text else None
    except json.JSONDecodeError:
        parsed = None
    if not isinstance(parsed, dict):
        # A plain-text body (old server path, a proxy) can still carry the text.
        parsed = {"error": body_text}
    server_message = str(parsed.get("error") or "").strip()
    code = parsed.get("code")
    if code != SESSION_RECORD_LIMIT_CODE and not server_message.startswith(
        SESSION_RECORD_LIMIT_TEXT_PREFIX
    ):
        return None

    limit = _optional_int(parsed.get("limit"))
    if limit is None:
        # Junior Tip [the server's own number, not a guess]: an old server
        # writes N into the sentence. Count has no such source and stays None.
        text_match = _LIMIT_IN_TEXT.search(server_message)
        if text_match:
            limit = int(text_match.group(1))
    count = _optional_int(parsed.get("count"))
    session_value = parsed.get("session_uuid")
    session_uuid = session_value if isinstance(session_value, str) and session_value else None

    message = (
        "Conflict (HTTP 409): session record limit reached "
        f"(session={session_uuid or 'unknown'}, limit={_text(limit)}, "
        f"count={_text(count)}): {server_message[:500]}"
    )
    return AnhurSessionRecordLimitError(
        message,
        session_uuid=session_uuid,
        limit=limit,
        count=count,
        server_message=server_message,
    )
