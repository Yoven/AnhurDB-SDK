"""
Public model exports for the AnhurDB Python SDK.

All data classes used in request/response payloads are re-exported here
so users can import from a single location::

    from anhurdb.models import Record, MemoryType, EntityModel
"""

from .add import AddResult, RecordSummary
from .entity import (
    EntitiesPage,
    EntityGraphEdge,
    EntityGraphNode,
    EntityGraphResult,
    EntityTimelineResult,
)
from .enums import MemoryType, MemoryStatus
from .graph import (
    ContextResult,
    GroundingAnchor,
    GroundingConsolidation,
    GroundingResult,
    GroundingTarget,
    WalkEdge,
    WalkResult,
)
from .manifest import ManifestResult
from .profile import (
    ProfileDynamic,
    ProfileResult,
    ProfileStatic,
    ProfileStats,
)
from .record import (
    CreateRequest,
    DeleteFileResult,
    EntityEdge,
    EntityModel,
    Record,
)
from .search import (
    LegScoreSummary,
    RelatedNode,
    RetrievalMeta,
    SearchHitSignals,
    SearchResponse,
    SearchResult,
)
from .session import SessionStats
from .smart_search import SmartSearchHit, SmartSearchResponse
from .upload import UploadResult, UploadStatusResult

__all__ = [
    "MemoryType",
    "MemoryStatus",
    "Record",
    "SearchResult",
    "SearchResponse",
    "SearchHitSignals",
    "LegScoreSummary",
    "RelatedNode",
    "RetrievalMeta",
    "CreateRequest",
    "DeleteFileResult",
    "SessionStats",
    "EntityModel",
    "EntityEdge",
    # Typed response envelopes (3.0.0). Before this release every one of these
    # concepts reached the caller as a bare ``Dict[str, Any]``: the field names
    # existed only in a docstring, a typo in a subscript was a runtime KeyError
    # and a phantom field nobody had ever received read like a safety net.
    "AddResult",
    "RecordSummary",
    "ProfileResult",
    "ProfileStatic",
    "ProfileDynamic",
    "ProfileStats",
    "WalkResult",
    "WalkEdge",
    "ContextResult",
    "GroundingResult",
    "GroundingTarget",
    "GroundingAnchor",
    "GroundingConsolidation",
    "ManifestResult",
    "UploadResult",
    "UploadStatusResult",
    "EntitiesPage",
    "EntityGraphResult",
    "EntityGraphNode",
    "EntityGraphEdge",
    "EntityTimelineResult",
    "SmartSearchResponse",
    "SmartSearchHit",
]
