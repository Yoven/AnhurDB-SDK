"""
AnhurDB Python SDK — The Cognitive Database for AI Agents.

One client: **Memory** — dead-simple to start (add, search, profile) and
carrying the full 40+ endpoint surface. (``AnhurClient``, its deprecated alias,
was removed in 4.0.0.)

Quick start::

    from anhurdb import Memory, sessions_all

    async with Memory(api_key="anhur_xxx", url="https://anhurdb.yoven.ai") as mem:
        session_id = await mem.create_session()
        await mem.add(
            "User is a data scientist",
            mode="ingest",
            session_id=session_id,
        )
        hits = await mem.search("what does the user do?", sessions_all())

Full API (same class)::

    from anhurdb import Memory, MemoryType

    async with Memory(api_key="anhur_xxx", url="https://anhurdb.yoven.ai") as mem:
        session_id = await mem.create_session()
        await mem.create(session_id, "...", type=MemoryType.FACT)
        entities = await mem.search_entities(query="Google")
"""

from .version import __version__
from .models import (
    MemoryType,
    MemoryStatus,
    Record,
    SearchResult,
    SearchResponse,
    SearchHitSignals,
    LegScoreSummary,
    RelatedNode,
    RetrievalMeta,
    CreateRequest,
    DeleteFileResult,
    DeleteFileLeg,
    FileSessionCandidate,
    SessionStats,
    EntityModel,
    EntityEdge,
    # Typed response envelopes (3.0.0) — see the __all__ note below.
    AddResult,
    RecordSummary,
    ProfileResult,
    ProfileStatic,
    ProfileDynamic,
    ProfileStats,
    WalkResult,
    WalkEdge,
    ContextResult,
    GroundingResult,
    GroundingTarget,
    GroundingAnchor,
    GroundingConsolidation,
    ManifestResult,
    UploadResult,
    UploadStatusResult,
    EntitiesPage,
    EntityGraphResult,
    EntityGraphNode,
    EntityGraphEdge,
    EntityTimelineResult,
    SmartSearchResponse,
    SmartSearchHit,
)
from .client import Memory
from .client.client_key import ClientAPI
from .client.client_key_types import (
    AgentProfile,
    ChildClient,
    ChildStatus,
    ClientTenant,
    ImpersonatedKey,
    KeyScope,
    RevokeResult,
)
from .client.client_key_errors import (
    AnhurClientAPIError,
    AnhurClientAPIResponseError,
)
from .client.session_filter import (
    MAX_SESSION_FILTER_UUIDS,
    SESSION_WILDCARD,
    sessions_all,
)
from .client.exceptions import (
    AnhurError,
    AnhurAuthError,
    AnhurQueryError,
    AnhurConnectionError,
    AnhurUploadWaitTimeout,
    AnhurSessionRecordLimitError,
    AnhurTenantLibraryCollisionError,
    AnhurUploadStatusAmbiguousError,
    AnhurLibraryDeletePartialError,
    AnhurUnexpectedResponseError,
)
from .client.record_scope import (
    RECORD_SCOPES,
    SCOPE_CLIENT_SHARED,
    SCOPE_SESSIONS,
    SCOPE_TENANT_SHARED,
    SCOPE_TENANT_SHARED_LEGACY,
    tenant_shared_scope_for_hit,
    upload_status_scope,
)
from .client.search_mode import (
    SEARCH_MODES,
    SEARCH_MODE_BALANCED,
    SEARCH_MODE_FAST,
    SEARCH_MODE_SEMANTIC,
)

__all__ = [
    # Version (PEP 396) — the same string the User-Agent carries.
    "__version__",
    # Client class
    "Memory",
    # Client-key surface (ADR-0034): child clients, tenants, impersonation.
    "ClientAPI",
    "AgentProfile",
    "ChildClient",
    "ChildStatus",
    "ClientTenant",
    "ImpersonatedKey",
    "KeyScope",
    "RevokeResult",
    "AnhurClientAPIError",
    "AnhurClientAPIResponseError",
    # Models
    "MemoryType",
    "MemoryStatus",
    "Record",
    "SearchResult",
    # Junior Tip [2026-09-05 — why these five were added here]: they were all
    # already part of the RETURN and RAISE contract of public methods
    # (``search_with_retrieval()`` returns ``SearchResponse``; a hit carries
    # ``SearchHitSignals``/``RelatedNode``; the envelope carries
    # ``RetrievalMeta``/``LegScoreSummary``; ``wait_for_upload()`` raises
    # ``AnhurUploadWaitTimeout``) and yet none of them could be imported from
    # the package root. A caller could not annotate the value we hand back,
    # nor write ``except AnhurUploadWaitTimeout``, without reaching into a
    # private-looking submodule path. A type you can receive but cannot name
    # is not a public API.
    "SearchResponse",
    "SearchHitSignals",
    "LegScoreSummary",
    "RelatedNode",
    "RetrievalMeta",
    "CreateRequest",
    "DeleteFileResult",
    "DeleteFileLeg",
    "FileSessionCandidate",
    "SessionStats",
    "EntityModel",
    "EntityEdge",
    # Junior Tip [2026-09-14 — why twenty-two more names appear here]: 3.0.0
    # replaced ``Dict[str, Any]`` with a real model on twelve response
    # concepts. Every one of them is now something a public method HANDS BACK,
    # and the rule this package already follows is that a type you can receive
    # but cannot name is not a public API. The field sets are the LIVE server
    # responses, so a name here is also the answer to "what does this endpoint
    # actually send" without opening a handler.
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
    # Search modes (ADR-0031) — the exact three values ``search(mode=...)``
    # accepts, exported so callers validate against the SDK instead of
    # retyping the strings.
    "SEARCH_MODES",
    "SEARCH_MODE_FAST",
    "SEARCH_MODE_BALANCED",
    "SEARCH_MODE_SEMANTIC",
    # Session filter (ADR-0014) — every search takes a mandatory `sessions`
    "sessions_all",
    "SESSION_WILDCARD",
    "MAX_SESSION_FILTER_UUIDS",
    # Exceptions
    "AnhurError",
    "AnhurAuthError",
    "AnhurQueryError",
    "AnhurConnectionError",
    "AnhurSessionRecordLimitError",
    "AnhurUploadWaitTimeout",
    # L2 tenant library (ADR-0029) — typed refusals and the by-id scope.
    "AnhurTenantLibraryCollisionError",
    "AnhurUploadStatusAmbiguousError",
    "AnhurLibraryDeletePartialError",
    "AnhurUnexpectedResponseError",
    "RECORD_SCOPES",
    "SCOPE_SESSIONS",
    "SCOPE_TENANT_SHARED",
    "SCOPE_TENANT_SHARED_LEGACY",
    "SCOPE_CLIENT_SHARED",
    "tenant_shared_scope_for_hit",
    "upload_status_scope",
]
