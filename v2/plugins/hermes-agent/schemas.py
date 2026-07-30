"""Tool schemas for explicit memory access.

OpenAI function-calling shape (``name`` / ``description`` / ``parameters``),
which is what ``MemoryProvider.get_tool_schemas`` must return and what
``agent/memory_manager.py:normalize_tool_schema`` expects.

Junior Tip [por que o prefixo ``anhurdb_`` e por que ele importa]: o
MemoryManager REJEITA qualquer ferramenta de provider cujo nome colida com uma
core tool do Hermes (``clarify``, ``delegate_task``, ...) — e rejeita em
silêncio para o usuário, com um warning no log. Um nome genérico como
``recall`` ou ``search`` é um convite a essa colisão hoje ou na próxima versão
do host. Prefixo estável = ferramenta que continua existindo.
"""

from __future__ import annotations

from typing import Any, Dict, List

RECALL_TOOL_NAME = "anhurdb_recall"
SEARCH_TOOL_NAME = "anhurdb_search"

# Bound so a careless call cannot pull a whole tenant into the context window.
MAX_TOOL_RESULT_LIMIT = 25

RECALL_TOOL_SCHEMA: Dict[str, Any] = {
    "name": RECALL_TOOL_NAME,
    "description": (
        "Recall relevant long-term memories from AnhurDB across ALL past "
        "sessions of this user. Use it when the answer may depend on "
        "something said in an earlier conversation."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "What to recall, in natural language.",
            },
            "limit": {
                "type": "integer",
                "description": (
                    f"Maximum memories to return (default 8, max "
                    f"{MAX_TOOL_RESULT_LIMIT})."
                ),
            },
        },
        "required": ["query"],
    },
}

SEARCH_TOOL_SCHEMA: Dict[str, Any] = {
    "name": SEARCH_TOOL_NAME,
    "description": (
        "Search long-term memory in AnhurDB, optionally narrowed to one "
        "cognitive type (fact, decision, preference, episodic, ...). Use it "
        "when you want a specific kind of memory rather than general recall."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "Search text.",
            },
            "memory_type": {
                "type": "string",
                "description": (
                    "Optional cognitive type filter, e.g. fact, decision, "
                    "preference, episodic, risk."
                ),
            },
            "limit": {
                "type": "integer",
                "description": (
                    f"Maximum results (default 8, max {MAX_TOOL_RESULT_LIMIT})."
                ),
            },
        },
        "required": ["query"],
    },
}

MEMORY_TOOL_SCHEMAS: List[Dict[str, Any]] = [
    RECALL_TOOL_SCHEMA,
    SEARCH_TOOL_SCHEMA,
]
