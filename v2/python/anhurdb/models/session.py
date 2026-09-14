"""Session aggregates — one row of ``GET /api/v1/sessions/stats``."""

from typing import Dict, Optional

from pydantic import BaseModel, ConfigDict, Field


class SessionStats(BaseModel):
    """
    Represents aggregated metadata regarding a specific cognitive session inside AnhurDB.

    Server truth: ``server/database/list_sessions.go:38`` plus the live row
    captured 2026-09-14 — ``uuid, record_count, types, last_activity,
    summary?``.

    Junior Tip [the key is ``last_activity``, not ``last_active``]: the PROFILE
    stats block (``models/profile.py``) spells the same idea ``last_active``,
    and BOTH spellings are correct — they are two different objects on two
    different routes. TypeScript declared ``last_active`` here and therefore
    read ``undefined`` on every session, forever, with no error. Do not
    harmonise them.

    Junior Tip [why every field has a default, 2026-09-14]: this is a READ
    model. A required field turns one malformed or partial row into a
    ``ValidationError`` that destroys the ENTIRE page of sessions — the same
    all-or-nothing failure ``Record`` already guards against by keeping
    ``type``/``status`` as plain strings. Missing counters read as zero, which
    is the honest answer for "the server did not say".
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    uuid: str = Field(default="")
    record_count: int = Field(default=0)
    types: Dict[str, int] = Field(default_factory=dict)
    last_activity: str = Field(default="")
    summary: Optional[str] = Field(default=None)
