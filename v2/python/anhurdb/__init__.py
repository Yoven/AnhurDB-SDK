"""
AnhurDB Python SDK — The Cognitive Database for AI Agents.

Two entry points:

- **Memory**: Simple 3-method API (add, search, profile) — start here.
- **AnhurClient**: Full API covering all 40+ AnhurDB endpoints.

Quick start::

    from anhurdb import Memory

    async with Memory(api_key="anhur_xxx") as mem:
        await mem.add("User is a data scientist")
        hits = await mem.search("what does the user do?")

Full API::

    from anhurdb import AnhurClient, CreateRequest, MemoryType

    async with AnhurClient(api_key="anhur_xxx") as client:
        await client.create(CreateRequest(uuid="s1", content="..."))
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
