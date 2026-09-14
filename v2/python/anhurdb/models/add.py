"""The write acknowledgement every create path answers with.

One domain: what the SDK hands back after a record was written, no matter
which of the three write doors was used (``POST /api/v1/ingest``,
``POST /api/v1/records`` directly, or the caller-owned-session variant).

Junior Tip [why the SDK owns this shape instead of echoing the server]:
the two write endpoints answer with DIFFERENT bodies — ``/ingest`` returns
``{session_id, records[]}`` while ``/records`` returns the created record
itself (``{id, status, ...}``). Before this model both shapes leaked to the
caller as a bare ``Dict[str, Any]``, so ``result["records"]`` worked on one
path and raised ``KeyError`` on the other for the SAME public method. The
model normalises the two into one contract and records WHICH door was used in
``mode``; a caller that needs the raw record still has ``id``.
"""

from typing import List, Optional

from pydantic import BaseModel, ConfigDict, Field


class RecordSummary(BaseModel):
    """A lightweight descriptor of one record created by a write.

    Matches Go ``client.RecordSummary`` and TypeScript ``AddRecordSummary``
    field for field: ``id``, ``type``, ``summary``.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: int = Field(default=0)
    type: str = Field(default="")
    summary: str = Field(default="")


class AddResult(BaseModel):
    """Result of ``add`` / ``create`` / ``create_in_session``.

    ``mode`` is ``"cloud"`` when the server's extraction pipeline
    (``POST /api/v1/ingest``) accepted the text and may have produced MORE than
    one record, and ``"oss"`` when the SDK wrote exactly one record through
    ``POST /api/v1/records``. It is the only way a caller can tell whether
    ``records`` is "everything the extractor found" or "the single row I asked
    for", so it is part of the contract, not a debug field.

    Junior Tip [why ``id`` and ``status`` are Optional]: they exist only on the
    ``/records`` door, where the server echoes the created record. On the
    ingest door the server never sends them, and defaulting them to ``0``/``""``
    would turn "the server did not say" into "the id is zero" — the same
    absence-as-value bug this project keeps paying for. ``None`` means the door
    used does not carry the field.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    session_id: str = Field(default="")
    records: List[RecordSummary] = Field(default_factory=list)
    mode: str = Field(default="")

    id: Optional[int] = Field(default=None)
    status: Optional[str] = Field(default=None)
