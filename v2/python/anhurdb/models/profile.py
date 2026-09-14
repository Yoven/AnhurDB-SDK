"""The aggregated memory profile returned by ``GET /api/v1/profile``.

One domain: the three blocks the profile endpoint answers with, and nothing
else. The envelope is EXACTLY ``{static, dynamic, stats}`` — the handler
(``server/handler/profile.go:27-51``) declares a fixed Go struct, so there is
no fourth key on any code path.

Junior Tip [``stats.last_active`` really is ``last_active``, 2026-09-14]:
the session row from ``GET /api/v1/sessions/stats`` spells the same idea
``last_activity`` (see ``models/session.py``). Two different objects, two
different server spellings, and BOTH are correct on the wire. Do not
"harmonise" them here — renaming either one silently drops the value, because
``extra="ignore"`` makes a misspelled field look like an empty default instead
of raising.
"""

from typing import List

from pydantic import BaseModel, ConfigDict, Field


class ProfileStatic(BaseModel):
    """Slow-moving identity: what the tenant IS, distilled from its records."""

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    facts: List[str] = Field(default_factory=list)
    preferences: List[str] = Field(default_factory=list)
    decisions: List[str] = Field(default_factory=list)
    risks: List[str] = Field(default_factory=list)
    emotions: List[str] = Field(default_factory=list)
    highlight: List[str] = Field(default_factory=list)


class ProfileDynamic(BaseModel):
    """Fast-moving context: what the tenant has been DOING lately."""

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    recent_tasks: List[str] = Field(default_factory=list)
    recent_topics: List[str] = Field(default_factory=list)


class ProfileStats(BaseModel):
    """Counters for the container tag the profile was asked about."""

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    total_records: int = Field(default=0)
    sessions: int = Field(default=0)
    last_active: str = Field(default="")


class ProfileResult(BaseModel):
    """Full envelope of ``GET /api/v1/profile?tag=...``.

    Junior Tip [an unknown tag is an EMPTY profile, not an error]: verified
    live 2026-09-14 — ``?tag=totally-not-a-real-tag-xyz`` answers HTTP 200 with
    every counter at zero and ``last_active: ""``. The SDK must not translate
    that into an exception: a typo'd tag and a genuinely empty tag are
    indistinguishable to the server, and inventing a failure would hide the
    typo behind a fake outage. Only an OMITTED tag is an error (HTTP 400
    ``tag: tag is required``), which is why the client never sends an empty
    string.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    static: ProfileStatic = Field(default_factory=ProfileStatic)
    dynamic: ProfileDynamic = Field(default_factory=ProfileDynamic)
    stats: ProfileStats = Field(default_factory=ProfileStats)
