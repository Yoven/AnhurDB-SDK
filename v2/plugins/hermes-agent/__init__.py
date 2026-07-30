"""AnhurDB memory plugin for the Hermes Agent.

Entry point: ``register(ctx)``. It hands Hermes one
``AnhurDBMemoryProvider`` (a ``MemoryProvider``) plus the two explicit memory
tools, and nothing else.

Junior Tip [as palavras ``MemoryProvider`` e ``register_memory_provider``
PRECISAM aparecer nos primeiros 8 KB deste arquivo]: a descoberta de memory
providers do Hermes é um scan de texto —
``plugins/memory/__init__.py:_is_memory_provider_dir`` lê
``__init__.py[:8192]`` e procura essas duas strings; o parser de manifesto faz
o mesmo para inferir ``kind: exclusive``. Um docstring que cresça e empurre
ambas para além do limite faz o plugin sumir da lista SEM erro nenhum. Se
precisar escrever mais, escreva no README.

Load paths (both real, both exercised by the tests):
  * ``register(ctx)``  — the collector in ``_load_provider_from_dir``
  * class scan          — the fallback that instantiates the provider directly
"""

from __future__ import annotations

import logging
from typing import Any, Optional

from .provider import AnhurDBMemoryProvider
from .schemas import MEMORY_TOOL_SCHEMAS
from .tools import build_registry_handlers

logger = logging.getLogger(__name__)

__all__ = ["AnhurDBMemoryProvider", "get_provider", "register"]

# One provider per process. Hermes may call register() more than once (plugin
# discovery + memory discovery); handing out two instances would mean two disk
# queues and two SDK clients racing on the same session.
_provider_singleton: Optional[AnhurDBMemoryProvider] = None


def get_provider() -> AnhurDBMemoryProvider:
    """Return the process-wide provider instance, creating it on first use."""
    global _provider_singleton
    if _provider_singleton is None:
        _provider_singleton = AnhurDBMemoryProvider()
    return _provider_singleton


def register(ctx: Any) -> None:
    """Register the memory provider and its tools with Hermes.

    ``ctx`` is either the real ``PluginContext`` or the lightweight collector
    used by memory-provider discovery, which implements only
    ``register_memory_provider`` / ``register_tool`` / ``register_hook`` /
    ``register_cli_command`` as no-ops. Everything is therefore probed with
    ``getattr`` before use, and no registration failure is allowed to prevent
    the others.
    """
    provider = get_provider()

    register_memory_provider = getattr(ctx, "register_memory_provider", None)
    if not callable(register_memory_provider):
        logger.error(
            "anhurdb-memory: this plugin context has no register_memory_provider "
            "— the plugin is being loaded as a generic plugin instead of a "
            "memory provider. Set `kind: exclusive` in plugin.yaml and install "
            "it as a memory provider (see README)."
        )
        return
    register_memory_provider(provider)

    # Tool registration is a BONUS path: under memory-provider discovery the
    # collector's register_tool is a no-op and the tools reach the model via
    # get_tool_schemas() instead. Registering here as well means the tools also
    # work when the general plugin loader is the one doing the loading —
    # `inject_memory_provider_tools` skips names already in the registry, so
    # the model never sees a duplicate.
    register_tool = getattr(ctx, "register_tool", None)
    if not callable(register_tool):
        return
    handlers = build_registry_handlers(provider)
    for schema in MEMORY_TOOL_SCHEMAS:
        tool_name = schema["name"]
        try:
            register_tool(
                name=tool_name,
                toolset="memory",
                schema=schema,
                handler=handlers[tool_name],
                description=schema.get("description", ""),
            )
        except Exception as registration_error:  # noqa: BLE001
            # Loud, but never fatal: the provider itself is already registered,
            # so automatic memory keeps working even if a tool name is refused.
            logger.error(
                "anhurdb-memory: could not register tool %s (%s) — automatic "
                "memory still works; explicit recall via this tool does not",
                tool_name,
                registration_error,
            )
