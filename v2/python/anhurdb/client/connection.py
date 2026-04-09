"""
HTTP connection layer for the AnhurDB Python SDK.

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
"""

import aiohttp
import json
import re
from typing import Any, Dict, Optional
from urllib.parse import urlencode

from .exceptions import (
    AnhurError,
    AnhurConnectionError,
    AnhurQueryError,
    AnhurAuthError,
)

# Maximum response body size: 100 MB.
# Prevents memory exhaustion from malicious or misconfigured servers.
_MAX_RESPONSE_SIZE = 100 * 1024 * 1024

# Regex for validating header values — rejects CRLF injection attempts.
_HEADER_SAFE = re.compile(r"^[\x20-\x7E]+$")


def _validate_header_value(value: str, name: str) -> None:
    """
    Validate a string is safe to use as an HTTP header value.

    Rejects any string containing control characters (CR, LF, null)
    that could enable HTTP header injection (response splitting).

    Args:
        value: The header value to validate.
        name:  Human-readable field name for error messages.

    Raises:
        ValueError: If the value contains unsafe characters.
    """
    if not value:
        return
    if not _HEADER_SAFE.match(value):
        raise ValueError(
            f"{name} contains invalid characters for HTTP header. "
            f"Only printable ASCII (0x20-0x7E) is allowed."
        )


class HTTPConnection:
    """
    Asynchronous HTTP transport for AnhurDB.

    Attributes:
        base_url:  Server root URL (e.g. ``http://localhost:8080``).
        api_key:   API key sent via ``X-API-Key`` header.
        tenant_id: Optional tenant ID for multi-tenant deployments.
        mode:      ``"rest"`` (direct REST) or ``"mcp"`` (MCP tunnel).
    """

    # -- MCP tool name mapping (used only in ``mode="mcp"``) ----------------
    _MCP_TOOL_MAP: Dict[str, str] = {
        "/api/v1/records":    "create_memory",
        "/api/v1/query":      "execute_ast",
        "/v2/records":        "create_memory",
        "/v2/search/ast":     "execute_ast",
    }

    def __init__(
        self,
        base_url: str,
        api_key: str,
        tenant_id: str = "",
        mode: str = "rest",
        timeout: float = 30.0,
        max_response_size: int = _MAX_RESPONSE_SIZE,
    ):
        """
        Initialise the connection.

        Args:
            base_url:          Server URL (trailing slash stripped automatically).
            api_key:           AnhurDB API key (required).
            tenant_id:         Optional tenant identifier for ``X-Tenant-ID``.
            mode:              Transport — ``"rest"`` (default) or ``"mcp"``.
            timeout:           Request timeout in seconds (default: 30).
            max_response_size: Maximum response body size in bytes (default: 100 MB).

        Raises:
            ValueError: If tenant_id contains header-injection characters.
        """
        # Validate inputs against injection.
        _validate_header_value(api_key, "api_key")
        _validate_header_value(tenant_id, "tenant_id")

        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.tenant_id = tenant_id
        self.mode = mode
        self._timeout = aiohttp.ClientTimeout(total=timeout)
        self._max_response_size = max_response_size

        # Standard headers. Uses X-API-Key (not Bearer) to match the
        # Go server's auth middleware and the TypeScript/Go SDKs.
        self.headers: Dict[str, str] = {
            "Content-Type": "application/json",
            "X-API-Key": self.api_key,
            "User-Agent": "AnhurSDK-Python/2.1",
        }
        if self.tenant_id:
            self.headers["X-Tenant-ID"] = self.tenant_id

        self._session: Optional[aiohttp.ClientSession] = None

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

    async def get(self, path: str, params: Optional[Dict[str, str]] = None) -> Any:
        """
        Send a GET request.

        Args:
            path:   API path (e.g. ``/api/v1/manifest``).
            params: Optional query-string parameters.

        Returns:
            Parsed JSON response body.
        """
        return await self._request("GET", path, params=params)

    async def post(self, path: str, json_data: Any = None) -> Any:
        """
        Send a POST request with a JSON body.

        Args:
            path:      API path (e.g. ``/api/v1/records``).
            json_data: Request body (dict or Pydantic-serialisable object).

        Returns:
            Parsed JSON response body.
        """
        if self.mode == "mcp" and path in self._MCP_TOOL_MAP:
            return await self._mcp_tunnel(path, json_data or {})
        return await self._request("POST", path, body=json_data)

    async def patch(self, path: str, json_data: Any = None) -> Any:
        """
        Send a PATCH request with a JSON body.

        Args:
            path:      API path (e.g. ``/api/v1/records/42``).
            json_data: Partial fields to update.

        Returns:
            Parsed JSON response body.
        """
        return await self._request("PATCH", path, body=json_data)

    async def delete(self, path: str) -> Any:
        """
        Send a DELETE request.

        Args:
            path: API path (e.g. ``/api/v1/records/42``).

        Returns:
            Parsed JSON response body (usually empty).
        """
        return await self._request("DELETE", path)

    # -- Internal request engine --------------------------------------------

    async def _request(
        self,
        method: str,
        path: str,
        body: Any = None,
        params: Optional[Dict[str, str]] = None,
    ) -> Any:
        """
        Execute an HTTP request and return parsed JSON.

        Security:
          - Response body capped at ``max_response_size`` to prevent OOM.
          - Error messages never include the API key.
          - Redirects are disabled (header leak protection).

        Raises:
            AnhurAuthError: On 401/403.
            AnhurQueryError: On 400/404/422.
            AnhurError: On 5xx.
            AnhurConnectionError: On network failure or timeout.
        """
        session = self._session
        if session is None:
            raise AnhurConnectionError(
                "Connection not established. Use 'async with AnhurClient(...)' "
                "or call 'await client.connect()' first."
            )

        # Build URL with optional query string.
        url = f"{self.base_url}{path}"
        if params:
            url += "?" + urlencode(params)

        try:
            async with session.request(
                method,
                url,
                json=body,
                allow_redirects=False,
            ) as response:
                # SECURITY: Cap response size to prevent memory exhaustion.
                raw = await response.content.read(self._max_response_size + 1)
                if len(raw) > self._max_response_size:
                    raise AnhurError(
                        f"Response exceeds maximum size "
                        f"({self._max_response_size // (1024*1024)} MB)"
                    )
                body_text = raw.decode("utf-8", errors="replace")

                # Map HTTP status codes to typed exceptions.
                # SECURITY: Error messages include status + server body but
                # never the API key or full URL (which could leak in logs).
                if response.status in (401, 403):
                    raise AnhurAuthError(
                        f"Authentication failed (HTTP {response.status})"
                    )
                elif response.status in (400, 422):
                    raise AnhurQueryError(
                        f"Invalid request (HTTP {response.status}): {body_text[:500]}"
                    )
                elif response.status == 404:
                    raise AnhurQueryError(
                        f"Resource not found (HTTP 404): {path}"
                    )
                elif response.status in (301, 302, 303, 307, 308):
                    # Redirects are disabled for security. Log the attempt.
                    raise AnhurError(
                        f"Server returned redirect (HTTP {response.status}). "
                        f"Redirects are disabled to prevent credential leakage."
                    )
                elif response.status >= 500:
                    raise AnhurError(
                        f"Server error (HTTP {response.status}): "
                        f"{body_text[:500]}"
                    )

                if not body_text:
                    return {}

                try:
                    return json.loads(body_text)
                except json.JSONDecodeError:
                    return {"message": body_text[:1000]}

        except aiohttp.ClientError as exc:
            # SECURITY: Do not include the full URL in error messages
            # as it could be logged and contains the server address.
            raise AnhurConnectionError(
                f"Failed to connect to AnhurDB: {type(exc).__name__}"
            ) from exc

    # -- MCP tunnel (legacy/alternative transport) --------------------------

    async def _mcp_tunnel(self, endpoint: str, json_data: Dict[str, Any]) -> Any:
        """
        Route a request through the MCP gateway at ``/api/v1/mcp/direct``.

        The server unwraps the MCP tool call, executes it, and returns the
        result in MCP format: ``{"content": [{"text": "{...JSON...}"}]}``.
        """
        tool_name = self._MCP_TOOL_MAP.get(endpoint)
        if not tool_name:
            raise AnhurQueryError(
                f"No MCP tool mapping for endpoint: {endpoint}"
            )

        args = {"api_key": self.api_key, **json_data}
        payload = {"tool": tool_name, "args": args}

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
