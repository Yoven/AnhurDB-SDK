"""Tool handlers for explicit memory access.

Contract (Hermes tool handlers):
  * signature ``handler(args: dict, **kwargs) -> str``
  * ALWAYS returns a JSON string — success and error alike
  * NEVER raises
  * reads arguments with ``.get()``, never by indexing

Junior Tip [por que "nunca levanta" é regra, e não estilo]: o modelo escolhe
os argumentos. Um ``args["query"]`` vira ``KeyError`` na primeira vez que ele
chamar a ferramenta sem o campo, e o host devolve um erro genérico que não
ensina nada nem ao modelo nem ao usuário. Devolvendo JSON com ``error``, o
modelo lê a mensagem e corrige a chamada sozinho no turno seguinte.
"""

from __future__ import annotations

import json
import logging
from typing import Any, Callable, Dict, List

from .schemas import (
    MAX_TOOL_RESULT_LIMIT,
    RECALL_TOOL_NAME,
    SEARCH_TOOL_NAME,
)

logger = logging.getLogger(__name__)

DEFAULT_TOOL_RESULT_LIMIT = 8


def tool_error(message: str, **extra_fields: Any) -> str:
    """JSON error payload. Mirrors ``tools.registry.tool_error`` in Hermes."""
    payload: Dict[str, Any] = {"error": str(message)}
    payload.update(extra_fields)
    return json.dumps(payload, ensure_ascii=False)


def tool_result(**fields: Any) -> str:
    """JSON success payload."""
    return json.dumps(fields, ensure_ascii=False, default=str)


def handle_recall(provider: Any, args: Dict[str, Any]) -> str:
    """``anhurdb_recall`` — hybrid recall across every past session."""
    query = _clean_string(args.get("query"))
    if not query:
        return tool_error("query is required and must be a non-empty string")
    limit = _clean_limit(args.get("limit"))

    runtime = getattr(provider, "runtime", None)
    if runtime is None:
        return tool_error(
            "AnhurDB memory is not available: " + _availability_reason(provider)
        )
    try:
        hits = runtime.search_memories(
            query, limit, provider.config.http_timeout_seconds
        )
    except Exception as recall_error:  # noqa: BLE001 — reported as data, never raised
        logger.error("anhurdb-memory: recall failed: %s", recall_error)
        return tool_error(f"recall failed: {recall_error}")
    return tool_result(query=query, count=len(hits), memories=_shrink(hits))


def handle_search(provider: Any, args: Dict[str, Any]) -> str:
    """``anhurdb_search`` — same plane, optionally narrowed to one type."""
    query = _clean_string(args.get("query"))
    if not query:
        return tool_error("query is required and must be a non-empty string")
    limit = _clean_limit(args.get("limit"))
    memory_type = _clean_string(args.get("memory_type"))

    runtime = getattr(provider, "runtime", None)
    if runtime is None:
        return tool_error(
            "AnhurDB memory is not available: " + _availability_reason(provider)
        )
    try:
        if memory_type:
            hits = runtime.search_by_type(
                query, limit, memory_type, provider.config.http_timeout_seconds
            )
        else:
            hits = runtime.search_memories(
                query, limit, provider.config.http_timeout_seconds
            )
    except Exception as search_error:  # noqa: BLE001 — reported as data, never raised
        logger.error("anhurdb-memory: search failed: %s", search_error)
        return tool_error(f"search failed: {search_error}")
    return tool_result(
        query=query,
        memory_type=memory_type,
        count=len(hits),
        memories=_shrink(hits),
    )


def dispatch(provider: Any, tool_name: str, args: Dict[str, Any]) -> str:
    """Route one tool call. Unknown names answer with JSON, not an exception."""
    safe_args = args if isinstance(args, dict) else {}
    if tool_name == RECALL_TOOL_NAME:
        return handle_recall(provider, safe_args)
    if tool_name == SEARCH_TOOL_NAME:
        return handle_search(provider, safe_args)
    return tool_error(f"unknown AnhurDB memory tool: {tool_name}")


def build_registry_handlers(provider: Any) -> Dict[str, Callable[..., str]]:
    """Handlers for ``ctx.register_tool`` — same logic as ``dispatch``.

    Junior Tip [uma lógica, duas portas]: o Hermes pode chamar estas
    ferramentas por DOIS caminhos — o registry global (``ctx.register_tool``)
    e o roteamento do MemoryManager (``handle_tool_call``). Duas
    implementações divergiriam no primeiro bugfix; por isso as duas portas
    entram no mesmo ``dispatch``.
    """

    def make_handler(tool_name: str) -> Callable[..., str]:
        def handler(args: Dict[str, Any], **_ignored: Any) -> str:
            try:
                return dispatch(provider, tool_name, args)
            except Exception as handler_error:  # noqa: BLE001 — last line of defence
                logger.error(
                    "anhurdb-memory: handler for %s raised (this is a bug): %s",
                    tool_name,
                    handler_error,
                )
                return tool_error(f"{tool_name} failed: {handler_error}")

        handler.__name__ = f"{tool_name}_handler"
        return handler

    return {
        RECALL_TOOL_NAME: make_handler(RECALL_TOOL_NAME),
        SEARCH_TOOL_NAME: make_handler(SEARCH_TOOL_NAME),
    }


def _availability_reason(provider: Any) -> str:
    reason = getattr(provider, "availability_reason", "")
    return str(reason) if reason else "no reason recorded"


def _clean_string(raw_value: Any) -> str:
    if not isinstance(raw_value, str):
        return ""
    return raw_value.strip()


def _clean_limit(raw_value: Any) -> int:
    """Coerce the model's ``limit`` into a sane bounded integer."""
    try:
        requested_limit = int(raw_value)
    except (TypeError, ValueError):
        return DEFAULT_TOOL_RESULT_LIMIT
    if requested_limit < 1:
        return DEFAULT_TOOL_RESULT_LIMIT
    return min(requested_limit, MAX_TOOL_RESULT_LIMIT)


def _shrink(hits: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """Trim each hit to what the model can act on, capping summary length."""
    shrunk: List[Dict[str, Any]] = []
    for hit in hits or []:
        if not isinstance(hit, dict):
            continue
        summary = str(hit.get("summary", "") or "")
        shrunk.append(
            {
                "id": hit.get("id", 0),
                "type": hit.get("type", ""),
                "summary": summary[:1000],
                "similarity": round(float(hit.get("similarity", 0.0) or 0.0), 4),
            }
        )
    return shrunk
