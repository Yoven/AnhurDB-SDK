"""
AnhurDB Python SDK — The Cognitive Database for AI Agents.

Two entry points:

- **Memory**: Simple 3-method API (add, search, profile) — start here.
- **AnhurClient**: Full API covering all 40+ AnhurDB endpoints.

Quick start::

    from anhurdb import Memory

    async with Memory(api_key="anhur_xxx", url="https://anhurdb.yoven.ai") as mem:
        session_id = await mem.create_session()
        await mem.add(
            "User is a data scientist",
            mode="ingest",
            session_id=session_id,
        )
        hits = await mem.search("what does the user do?")

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

from .models import (
    MemoryType,
    MemoryStatus,
    Record,
    SearchResult,
    CreateRequest,
    SessionStats,
    EntityModel,
    EntityEdge,
)
from .client import Memory, AnhurClient
from .client.exceptions import (
    AnhurError,
    AnhurAuthError,
    AnhurQueryError,
    AnhurConnectionError,
)

__all__ = [
    # Client classes
    "Memory",
    "AnhurClient",
    # Models
    "MemoryType",
    "MemoryStatus",
    "Record",
    "SearchResult",
    "CreateRequest",
    "SessionStats",
    "EntityModel",
    "EntityEdge",
    # Exceptions
    "AnhurError",
    "AnhurAuthError",
    "AnhurQueryError",
    "AnhurConnectionError",
]
