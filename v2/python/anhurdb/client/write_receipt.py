"""Turn the two write doors' answers into one ``AddResult``.

One domain: normalising what ``POST /api/v1/ingest`` and
``POST /api/v1/records`` answer into the single receipt every write method
returns, and stamping ``mode`` with the door that actually ran.

Split out of ``client/__init__.py`` in 4.0.0 (house 300-line cut). The model
itself lives in ``models/add.py``; this file only builds it.
"""

from typing import Any

from ..models import AddResult, RecordSummary

# ``AddResult.mode`` values — the endpoint that wrote, nothing else. They were
# "cloud"/"oss" before 4.0.0 (see the Junior Tip on ``AddResult``).
ADD_MODE_INGEST = "ingest"
ADD_MODE_RECORDS = "records"


def _add_result_from_record(
    data: Any,
    *,
    session_id: str,
    record_type: str,
    summary: str,
) -> AddResult:
    """Normalise a ``POST /api/v1/records`` response into an ``AddResult``.

    Junior Tip [why the SDK synthesises ``records`` here]: ``/records`` echoes
    the CREATED RECORD, not a write receipt — there is no ``records`` array in
    its body. ``/ingest`` does send one. Without this adapter the same public
    method answered with two different shapes depending on which door it took,
    and ``result["records"][0]`` was a ``KeyError`` waiting for the first
    caller who pinned a score. The summary and type are the ones WE sent, so
    the synthesised entry is a faithful description of what was written — not a
    guess.
    """
    record = data if isinstance(data, dict) else {}
    record_id = record.get("id", 0)
    return AddResult(
        session_id=record.get("session_id") or session_id,
        records=[
            RecordSummary(
                id=record_id if isinstance(record_id, int) else 0,
                type=record.get("type") or record_type,
                summary=record.get("summary") or summary,
            )
        ],
        mode=ADD_MODE_RECORDS,
        id=record_id if isinstance(record_id, int) else None,
        status=record.get("status"),
    )


def _add_result_from_ingest(data: Any, *, session_id: str) -> AddResult:
    """Normalise a ``POST /api/v1/ingest`` response into an ``AddResult``.

    The server always answers ``{"session_id", "records": [{id, type,
    summary}, ...]}`` (``server/handler/ingest.go:35-38,76-87``; ``records`` is
    a non-nil slice, so ``[]`` when nothing was extracted).

    Junior Tip [no invented record — 4.0.0]: before 4.0.0 a body without
    ``records`` was answered with a fabricated ``{"id": <id or 0>, "type":
    "episodic"}`` entry. Go never did that, and no server omits the key; a
    receipt must describe what the server SAID it wrote, so a missing key is an
    empty list, never a made-up row."""
    body = data if isinstance(data, dict) else {}
    rows = body.get("records") or []
    return AddResult(
        session_id=body.get("session_id") or session_id,
        records=[RecordSummary.model_validate(row) for row in rows],
        mode=ADD_MODE_INGEST,
    )


__all__ = [
    "ADD_MODE_INGEST",
    "ADD_MODE_RECORDS",
    "_add_result_from_record",
    "_add_result_from_ingest",
]
