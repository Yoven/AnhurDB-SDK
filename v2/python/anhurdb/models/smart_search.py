"""The smart-search envelope — a purely LEXICAL result set.

One domain: ``GET /api/v1/search/smart``. This endpoint is DuckDB/Parquet FTS
fused with SQLite FTS5 by Reciprocal Rank Fusion (k=60). No embedding is
computed and no vector is consulted, which is why it answers without a warm
model and why its scores mean something different from ``search()``'s.

Reference arm: TypeScript ``src/smartSearchResults.ts`` — these models mirror
it field for field, including the nullable ``results``.
"""

from typing import List, Optional

from pydantic import BaseModel, ConfigDict, Field


class SmartSearchHit(BaseModel):
    """One lexical hit — a FLAT record projection, not a ``{record, score}`` pair.

    Junior Tip [why ``relevance`` must never be compared with ``similarity``]:
    ``relevance`` here is BM25 x cognitive decay — an unbounded lexical score
    whose scale depends on the corpus and on term rarity. ``SearchResult.similarity``
    (``models/search.py``) is a cosine in [-1, 1]. Merging or thresholding the
    two against one shared cutoff is meaningless, and it is exactly the mistake
    that made a "similarity floor" appear to revoke whole retrieval legs in this
    project's history. Different unit, different type, deliberately.

    ``metadata`` is the RAW JSON string as stored, not a parsed object — this
    route returns the record column verbatim. Parse it yourself if you need it.

    ``bm25`` is ``omitempty``: only the FTS leg fills it, so ``None`` means
    "this hit came from the other leg", not "the score was zero".

    ``provenance`` / ``scope`` / ``leg_relevance`` appear only on the shared
    planes (``handler/search_scope_smart_merge.go:17-32``) and are ``None`` on
    a plain session-scoped search.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: int = Field(default=0)
    uuid: str = Field(default="")
    type: str = Field(default="")
    summary: str = Field(default="")
    metadata: str = Field(default="")
    score: float = Field(default=0.0)
    weight: float = Field(default=0.0)
    status: str = Field(default="")
    relevance: float = Field(default=0.0)
    bm25: Optional[float] = Field(default=None)
    created_at: str = Field(default="")
    updated_at: str = Field(default="")

    provenance: Optional[str] = Field(default=None)
    scope: Optional[str] = Field(default=None)
    leg_relevance: Optional[float] = Field(default=None)


class SmartSearchResponse(BaseModel):
    """Envelope of ``GET /api/v1/search/smart``.

    Junior Tip [``results`` is genuinely ``None`` on the ordinary "no matches"
    answer]: the handler marshals a Go slice, and a nil slice serialises as
    JSON ``null`` (``handler/search_smart.go:216-224``). Declaring
    ``List[...] = []`` would quietly turn the server's ``null`` into an empty
    list — which reads the same to a ``for`` loop but erases the difference
    between "the search ran and found nothing" and "the key was absent". The
    same ``None != []`` discipline ``SearchResponse.leg_scores`` already uses.

    ``bundle_hash`` / ``bundle_ordering`` identify the exact result bundle the
    server assembled; ``bundle_ordering`` is ``"smart_relevance"`` on this
    route. They are how a caller proves two queries got the identical bundle.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    results: Optional[List[SmartSearchHit]] = Field(default=None)
    count: int = Field(default=0)
    scope: str = Field(default="")
    bundle_hash: str = Field(default="")
    bundle_ordering: str = Field(default="")
