"""The MCP-gateway transport mode (``mode="mcp"``).

Domain, in one sentence: turn a REST call into an MCP tool call for clients
that can only reach the MCP gateway, and turn the tool's answer back into the
JSON the REST caller expected.

Junior Tip [why the tunnel is a file of its own, 2026-09-05]: this is a whole
SECOND protocol — a tool-name map, a per-tool declared-argument schema, an
envelope to unwrap — and it runs for exactly one value of ``mode``. Living
inside ``connection.py`` it made the default REST path look far more
complicated than it is, and it was the largest single block of a file already
593 lines long, far past the project's ~300-line cut. The seam is real: nothing
here is reachable unless the caller asked for ``mode="mcp"``.

Junior Tip [why the tables travel WITH the code that reads them]: the tool map
and the declared-argument table are the tunnel's contract with the MCP surface
(AnhurDB/docs/MCP_TOOLS.md). Splitting a contract from its only consumer is how
one half gets updated and the other does not — so they stay in one file, even
though that is what keeps this file near, rather than well under, the cut.
"""

import json
from typing import Any, Dict

from .exceptions import AnhurQueryError


class McpTunnelMixin:
    """``mode="mcp"`` transport. Mixed into ``HTTPConnection``.

    Depends on the host connection for ``_request`` (the tunnel posts a normal
    REST body to ``/api/v1/mcp/direct``) — which is why this is a mixin and not
    a free function: it is one transport mode of a connection, not a utility.
    """

    # -- MCP tool name mapping (used only in ``mode="mcp"``) ----------------
    # Junior Tip [os nomes seguem a superficie MCP, que foi cortada de 47 para 22
    # em 2026-07-28]: `execute_ast` deixou de existir — foi absorvida por `query`,
    # que aceita o mesmo AST no argumento `ast`. Um mapa apontando para tool morta
    # nao falha no import nem nos testes: quebra em runtime, so no modo mcp, e so
    # para quem usa AST. Se a superficie mudar de novo, este mapa tem de mudar
    # junto — a referencia canonica e AnhurDB/docs/MCP_TOOLS.md.
    _MCP_TOOL_MAP: Dict[str, str] = {
        "/api/v1/records":    "create_memory",
        "/api/v1/query":      "query",
        "/v2/records":        "create_memory",
        "/v2/search/ast":     "query",
    }

    # Fields DECLARED by each MCP tool schema (mcp-server register22_*.go plus
    # the ambient api_key/tenant_id added by declareAmbientArguments).
    #
    # Junior Tip [why the tunnel must filter — ADR-0013 D1 strict params]: the
    # MCP server closes every tool schema (additionalProperties:false) and
    # answers any undeclared argument with INVALID_PARAM. The REST payload for
    # ``POST /api/v1/records`` is a full ``CreateRequest.model_dump()`` and
    # therefore carries REST-only fields (prefix / main_ids / consolidated /
    # consolidate_id) that ``create_memory`` does not declare — sent as-is the
    # tunnel fails 100% of the time. The tunnel sends ONLY declared fields.
    # Dropping an EMPTY default loses nothing; a NON-EMPTY undeclared value is
    # information the MCP surface cannot carry, so we raise instead of
    # swallowing it (silent loss is this project's number-one failure mode).
    # Tools without an entry here (``query``) pass their args through
    # unchanged. If the MCP surface changes, this table must change with it —
    # canonical reference: AnhurDB/docs/MCP_TOOLS.md.
    _MCP_TOOL_DECLARED_ARGS: Dict[str, frozenset] = {
        "create_memory": frozenset({
            "session_id", "uuid", "metadata", "type", "summary", "content",
            "score", "weight", "status", "vector", "dimension", "related_ids",
            "valid_from", "valid_until", "api_key", "tenant_id",
        }),
    }

    async def _mcp_tunnel(self, endpoint: str, json_data: Dict[str, Any]) -> Any:
        """Route a request through the MCP gateway at ``/api/v1/mcp/direct``.

        The server unwraps the MCP tool call, executes it, and returns the
        result in MCP format: ``{"content": [{"text": "{...JSON...}"}]}``."""
        tool_name = self._MCP_TOOL_MAP.get(endpoint)
        if not tool_name:
            raise AnhurQueryError(
                f"No MCP tool mapping for endpoint: {endpoint}"
            )

        # Strict-schema projection: send ONLY fields the tool declares (see the
        # _MCP_TOOL_DECLARED_ARGS Junior Tip). Empty defaults are dropped;
        # a non-empty undeclared value fails LOUD before the round trip.
        declared_fields = self._MCP_TOOL_DECLARED_ARGS.get(tool_name)
        if declared_fields is not None:
            undeclared_non_empty = sorted(
                field_name
                for field_name, field_value in json_data.items()
                if field_name not in declared_fields and field_value
            )
            if undeclared_non_empty:
                raise AnhurQueryError(
                    f"MCP tool '{tool_name}' does not declare field(s) "
                    f"{', '.join(undeclared_non_empty)} — the MCP tunnel cannot "
                    f"carry them. Use mode='rest' to send these fields."
                )
            json_data = {
                field_name: field_value
                for field_name, field_value in json_data.items()
                if field_name in declared_fields
            }

        # Auth stays on the X-API-Key header only — never duplicate the key in
        # the JSON body (avoids accidental logging/proxy capture of credentials).
        # The MCP gateway translates the header into args["api_key"] server-side
        # (handleMCPDirect), satisfying wrapTool's tool-level auth contract.
        payload = {"tool": tool_name, "args": json_data}

        result = await self._request("POST", "/api/v1/mcp/direct", body=payload)

        if isinstance(result, dict):
            if result.get("isError"):
                raise AnhurQueryError(
                    f"MCP tool error: {str(result.get('error', 'unknown'))[:500]}"
                )
            content = result.get("content", [])
            if content and isinstance(content, list):
                text = content[0].get("text", "{}")
                try:
                    return json.loads(text)
                except json.JSONDecodeError:
                    return {"message": text[:1000]}

        return result
