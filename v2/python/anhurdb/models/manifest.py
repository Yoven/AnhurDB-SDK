"""The manifest page — a flat, importance-ranked slice of records.

One domain: the two manifest routes, which share one envelope.

- ``GET /api/v1/manifest``                      (cross-session overview)
- ``GET /api/v1/chats/{uuid}/manifest``         (one session)
"""

from typing import List

from pydantic import BaseModel, ConfigDict, Field

from .record import Record


class ManifestResult(BaseModel):
    """Envelope of both manifest routes: ``{records, count, limit, offset, has_more}``.

    Junior Tip [there is deliberately NO ``next_offset`` here, 2026-09-14]:
    ``GET /api/v1/sessions/stats`` and ``GET /api/v1/entities/list`` both send a
    cursor (``handler/record_session.go:141``, ``handler/entity.go:207``), but
    neither manifest route does (``handler/search_aux.go:225-229``,
    ``handler/record_session.go:307-311``). Modelling a cursor the server never
    sends would give every caller a field that is permanently ``0`` and read
    like a page-one answer — so paging here is ``offset + count``, computed by
    the caller, and ``has_more`` is the only truth about whether more exists.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    records: List[Record] = Field(default_factory=list)
    count: int = Field(default=0)
    limit: int = Field(default=0)
    offset: int = Field(default=0)
    has_more: bool = Field(default=False)
