"""
AnhurDB Python SDK — The Cognitive Database for AI Agents.

Two entry points:

- **Memory**: Simple 3-method API (add, search, profile) — start here.
- **AnhurClient**: Full API covering all 40+ AnhurDB endpoints.

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

Full API::

    from anhurdb import AnhurClient, CreateRequest, MemoryType

    async with AnhurClient(api_key="anhur_xxx", url="https://anhurdb.yoven.ai") as client:
        session_id = await client.create_session()
        await client.create(CreateRequest(
            session_id=session_id,
            type=MemoryType.FACT,
            content="...",
        ))
        entities = await client.search_entities(query="Google")
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
    SessionStats,
    EntityModel,
    EntityEdge,
)
from .client import Memory, AnhurClient
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
    # Client classes
    "Memory",
    "AnhurClient",
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
    "SessionStats",
    "EntityModel",
    "EntityEdge",
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
    "AnhurUploadWaitTimeout",
]
