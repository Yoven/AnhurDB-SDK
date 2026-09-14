"""File-upload responses: the 202 accept and the status poll.

One domain: the two bodies ``POST /api/v1/upload`` and
``GET /api/v1/upload/{id}/status`` answer with. Both are fixed maps built
literally in the handler, so both field sets are closed — there is no
"whatever the record happens to carry" in either.
"""

from typing import Optional

from pydantic import BaseModel, ConfigDict, Field


class UploadResult(BaseModel):
    """Body of the HTTP **202** returned by ``POST /api/v1/upload``.

    Server truth: ``handler/upload.go:109-119`` — ``message, record_id, uuid,
    filename, mime, mime_detected, extension, size_bytes, status``.

    Junior Tip [there is no ``id``, only ``record_id``]: Go and TypeScript both
    declared an ``id`` here and neither ever received one. The consequence is
    not a crash, it is worse — ``result.id`` read as ``0``/``undefined``, and a
    caller who polled ``upload_status(result.id)`` got a 404 for a file that
    uploaded perfectly. Poll with ``record_id``.

    ``mime`` is what the CLIENT declared and ``mime_detected`` is what the
    SERVER sniffed. They are populated from the same value today, but the two
    keys exist because the contract allows them to disagree — do not collapse
    them into one field.

    202 means ACCEPTED, not finished: ``status`` here is the initial state.
    Use ``upload_status`` / ``wait_for_upload`` for the terminal one.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    message: str = Field(default="")
    record_id: int = Field(default=0)
    uuid: str = Field(default="")
    filename: str = Field(default="")
    mime: str = Field(default="")
    mime_detected: str = Field(default="")
    extension: str = Field(default="")
    size_bytes: int = Field(default=0)
    status: str = Field(default="")


class UploadStatusResult(BaseModel):
    """Body of ``GET /api/v1/upload/{id}/status``.

    Server truth: ``handler/upload.go:220-236`` — a seven-key map, always the
    same seven keys: ``record_id, uuid, status, type, summary, metadata,
    completed``.

    Junior Tip [a FAILED ingest is reported through ``status``, and only
    through ``status``]: there is no ``error`` key on any code path. Code that
    waited for ``payload["error"]`` to appear before declaring the upload dead
    would wait until the timeout on every genuine failure, then report a
    timeout — the wrong diagnosis, pointing at the network instead of at the
    file. ``status == "failed"`` (with ``completed`` still ``False``) is the
    failure signal.

    ``completed`` is the server's own terminal flag: it is ``True`` only for
    ``status`` in ``{"completed", "saved"}``. A ``"failed"`` upload is terminal
    for the CALLER but ``completed`` is ``False`` — the two questions are
    different and the SDK keeps both answers.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    record_id: int = Field(default=0)
    uuid: str = Field(default="")
    status: str = Field(default="")
    type: str = Field(default="")
    summary: str = Field(default="")
    metadata: Optional[str] = Field(default=None)
    completed: bool = Field(default=False)
