"""HTTP connection layer for the AnhurDB Python SDK.

Domain, in one sentence: hold an open session to AnhurDB and expose the four
REST verbs over it.

Provides two transport modes:
  1. **REST Direct** (default) — calls AnhurDB REST endpoints directly,
     matching the TypeScript and Go SDKs. This is the recommended mode.
  2. **MCP Tunnel** — routes requests through ``/api/v1/mcp/direct``,
     useful when the client only has access to the MCP gateway.

Security hardening:
  - ``X-API-Key`` header for auth (matches server middleware).
  - Request timeout: 30s default (prevents indefinite hangs — OWASP API4).
  - Redirect disabled: prevents ``X-API-Key`` leak on cross-origin redirects
    (mitigates CVE-2026-34518).
  - Response size capped at 100 MB (prevents memory exhaustion DoS).
  - Header injection protection: tenant_id validated against CRLF injection.
  - API key never included in error messages or URLs.
  - HTTP 409 (Conflict) raises ``AnhurQueryError`` in both REST and
    multipart paths (e.g. max_session_records exceeded).
  - HTTP 415 (Unsupported Media Type) raises ``AnhurQueryError`` in both
    REST and multipart paths.
  - HTTP 429 (Rate Limited) raises ``AnhurError`` so callers can retry.

Junior Tip [where the rest of the transport went, 2026-09-05]: this file was
593 lines — nearly double this project's ~300-line cut — and the ADR-0031
parity pass grew it again. It was split by DOMAIN, not by line count, into four
neighbours, each named by the one responsibility it owns:

  - ``connection_guards.py``      what the transport REFUSES (header injection,
                                  oversized bodies).
  - ``connection_request.py``     ONE round trip, and which exception a status
                                  code becomes.
  - ``connection_multipart.py``   file upload, the only non-JSON request shape.
  - ``connection_mcp_tunnel.py``  the second transport mode, ``mode="mcp"``.

What is LEFT here is the connection itself: its configuration, its lifetime
(``connect``/``close``/``async with``) and the four public verbs. The three
behavioural pieces come back in as mixins because each one needs the live
session, the headers and the timeouts this class owns — a free function would
have to be handed all of them on every call. ``QueryParams`` is re-exported
below so that ``from .connection import QueryParams`` keeps working for anyone
who already wrote it.
"""

import aiohttp
from typing import Any, Dict, Optional

from ..version import USER_AGENT
from .connection_guards import (
    MAX_RESPONSE_SIZE,
    QueryParams,
    validate_header_value,
)
from .connection_mcp_tunnel import McpTunnelMixin
from .connection_multipart import MultipartUploadMixin
from .connection_request import RequestExecutionMixin

# Re-exported for import compatibility: ``QueryParams`` has always been
# reachable as ``anhurdb.client.connection.QueryParams`` and callers wrote that
# path. Moving a name is not a reason to break an import that already exists.
__all__ = ["HTTPConnection", "QueryParams"]

# The transport issues exactly ONE HTTP request per call and surfaces the
# outcome verbatim — no client-side retry loop.
# ClientSession. aiohttp merges session defaults into every request; a sticky
# application/json made FormData uploads arrive as JSON and AnhurDB returned
# HTTP 400 "failed to parse multipart form". REST JSON bodies still get the
# correct type via the ``json=`` kwarg on ``session.request``.



class HTTPConnection(RequestExecutionMixin, MultipartUploadMixin, McpTunnelMixin):
    """Asynchronous HTTP transport for AnhurDB.

    Attributes:
        base_url:  Server root URL (e.g. ``http://localhost:8080``).
        api_key:   API key sent via ``X-API-Key`` header.
        tenant_id: Optional tenant ID for multi-tenant deployments.
        mode:      ``"rest"`` (direct REST) or ``"mcp"`` (MCP tunnel)."""

    def __init__(
        self,
        base_url: str,
        api_key: str,
        tenant_id: str = "",
        mode: str = "rest",
        timeout: float = 30.0,
        max_response_size: int = MAX_RESPONSE_SIZE,
    ):
        """Initialise the connection.

        Args:
            base_url:          Server URL (trailing slash stripped automatically).
            api_key:           AnhurDB API key (required).
            tenant_id:         Optional tenant identifier for ``X-Tenant-ID``.
            mode:              Transport — ``"rest"`` (default) or ``"mcp"``.
            timeout:           Request timeout in seconds (default: 30).
            max_response_size: Maximum response body size in bytes (default: 100 MB).

        Raises:
            ValueError: If tenant_id contains header-injection characters."""
        # Validate inputs against injection.
        validate_header_value(api_key, "api_key")
        validate_header_value(tenant_id, "tenant_id")

        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.tenant_id = tenant_id
        self.mode = mode
        self._timeout = aiohttp.ClientTimeout(total=timeout)
        self._max_response_size = max_response_size

        # Auth/UA only on the session. Do NOT pin Content-Type here —
        # REST JSON calls rely on aiohttp's ``json=`` kwarg to set
        # application/json, and multipart uploads need FormData to own
        # the boundary (session-level application/json breaks ParseMultipartForm).
        self.headers: Dict[str, str] = {
            "X-API-Key": self.api_key,
            "User-Agent": USER_AGENT,
        }
        if self.tenant_id:
            self.headers["X-Tenant-ID"] = self.tenant_id

        self._session: Optional[aiohttp.ClientSession] = None
        # Optional async callable invoked before each request (impersonation refresh).
        self._before_request: Optional[Any] = None

    def set_api_key(self, api_key: str) -> None:
        """Replace the active API key (impersonation refresh).

        Junior Tip: also updates the live ClientSession headers when connected.
        """
        validate_header_value(api_key, "api_key")
        self.api_key = api_key
        self.headers["X-API-Key"] = api_key
        if self._session is not None:
            self._session.headers["X-API-Key"] = api_key

    # -- Lifecycle ----------------------------------------------------------

    async def connect(self) -> None:
        """Open the underlying HTTP session (idempotent)."""
        if self._session is None:
            self._session = aiohttp.ClientSession(
                headers=self.headers,
                timeout=self._timeout,
            )

    async def close(self) -> None:
        """Close the underlying HTTP session and release resources."""
        if self._session is not None:
            await self._session.close()
            self._session = None

    async def __aenter__(self) -> "HTTPConnection":
        await self.connect()
        return self

    async def __aexit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        await self.close()

    # -- Public HTTP verbs --------------------------------------------------

    async def get(
        self,
        path: str,
        params: Optional[QueryParams] = None,
        raw_text: bool = False,
    ) -> Any:
        """Send a GET request.

        Args:
            path:      API path (e.g. ``/api/v1/manifest``).
            params:    Optional query-string parameters — a mapping, or a
                       sequence of (key, value) pairs when the same key repeats
                       (the ADR-0014 ``sessions`` filter).
            raw_text:  When True, a non-JSON body is returned as the decoded
                       string instead of being wrapped in ``{"message": ...}``.
                       Used by ``read_content`` for plain-text records.

        Returns:
            Parsed JSON response body, or the raw string when ``raw_text`` is
            set and the body is not JSON."""
        return await self._request(
            "GET", path, params=params, raw_text=raw_text
        )

    async def post(
        self,
        path: str,
        json_data: Any = None,
    ) -> Any:
        """Send a POST request with a JSON body.

        Args:
            path:      API path (e.g. ``/api/v1/records``).
            json_data: Request body (dict or Pydantic-serialisable object).

        Returns:
            Parsed JSON response body."""
        if self.mode == "mcp" and path in self._MCP_TOOL_MAP:
            return await self._mcp_tunnel(path, json_data or {})
        return await self._request("POST", path, body=json_data)

    async def patch(self, path: str, json_data: Any = None) -> Any:
        """Send a PATCH request with a JSON body.

        Args:
            path:      API path (e.g. ``/api/v1/records/42``).
            json_data: Partial fields to update.

        Returns:
            Parsed JSON response body."""
        return await self._request("PATCH", path, body=json_data)

    async def delete(
        self,
        path: str,
        params: Optional[QueryParams] = None,
    ) -> Any:
        """Send a DELETE request.

        Junior Tip [por que DELETE aceita query e devolve corpo]: ``DELETE
        /api/v1/records/{id}`` responde vazio, mas ``DELETE
        /api/v1/records/by-file`` responde a CONTAGEM do que foi apagado — e a
        contagem é a resposta ao usuário ("apaguei 511"), não um detalhe.
        Os parâmetros viajam na URL porque corpo em DELETE é descartado por
        vários proxies e clientes.

        Args:
            path:   API path (e.g. ``/api/v1/records/42``).
            params: Optional query-string parameters.

        Returns:
            Parsed JSON response body (empty dict when the server sends none)."""
        return await self._request("DELETE", path, params=params)
