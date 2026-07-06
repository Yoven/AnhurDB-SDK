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
  - HTTP 409 (Conflict) raises ``AnhurQueryError`` in both REST and
    multipart paths (e.g. max_session_records exceeded).
  - HTTP 415 (Unsupported Media Type) raises ``AnhurQueryError`` in both
    REST and multipart paths.
  - HTTP 429 (Rate Limited) raises ``AnhurError`` so callers can retry.
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

# Request header that opts a single read into read-your-writes (RYW)
# consistency: "do not serve this read until this node's local applied Raft
# index for the tenant has reached N". The value is the ``raft_index`` a caller
# received from its own prior write. The AnhurDB server's MinIndexBarrier
# middleware blocks the read until the node has replicated up to that index.
#
# Junior Tip [parity with Go/TS SDKs, 2026-06-17]: this is an HTTP request
# HEADER (verified against server/middleware/min_index.go), not a query
# parameter or body field. Reads WITHOUT it keep their default eventually-
# consistent, load-balanced behaviour at zero cost — only the caller that
# actually needs RYW (e.g. an ACK-first pipeline agent reading its own
# just-written record) sets it. The Go SDK calls the same header
# ``X-Anhur-Min-Index`` via WithMinIndex; the TS SDK via ``{ minIndex }``.
_HEADER_MIN_INDEX = "X-Anhur-Min-Index"

# Regex for validating header values — rejects CRLF injection attempts.
_HEADER_SAFE = re.compile(r"^[\x20-\x7E]+$")

# Junior Tip [transparent pipe — router owns retry, 2026-07-06]: this transport
# deliberately has NO retry loop. It issues exactly ONE HTTP request per call
# and surfaces the outcome verbatim. The SINGLE owner of retry in the AnhurDB
# stack is the ROUTER (the write path that fronts the Raft leader); it replays
# idempotent writes across a leadership handoff where it can prove the previous
# attempt never committed. A client-side transport retry is the WIDEST possible
# net — it re-fires GETs and every 5xx marker match, which (a) double-counts
# reads, (b) can mask a genuine server bug behind silent replays, and (c)
# duplicates work the router already does correctly and closer to the log. So we
# removed the former ``_MAX_WRITE_ATTEMPTS`` / ``_RETRYABLE_METHODS`` /
# ``_TRANSIENT_500_MARKERS`` / ``_is_transient_cluster_error`` / exponential-
# backoff machinery entirely. Callers that truly need a retry decide it at their
# own layer with full context; the pipe just carries bytes.


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

    async def get(
        self,
        path: str,
        params: Optional[Dict[str, str]] = None,
        raw_text: bool = False,
        min_index: Optional[int] = None,
    ) -> Any:
        """
        Send a GET request.

        Args:
            path:      API path (e.g. ``/api/v1/manifest``).
            params:    Optional query-string parameters.
            raw_text:  When True, a non-JSON body is returned as the decoded
                       string instead of being wrapped in ``{"message": ...}``.
                       Used by ``read_content`` for plain-text records.
            min_index: Optional read-your-writes barrier. When set to a positive
                       Raft index (the ``raft_index`` from a prior write), the
                       ``X-Anhur-Min-Index`` header is sent so the server blocks
                       this read until the node has applied that index. ``None``
                       or ``0`` keeps the default eventually-consistent read.

        Returns:
            Parsed JSON response body, or the raw string when ``raw_text`` is
            set and the body is not JSON.
        """
        return await self._request(
            "GET", path, params=params, raw_text=raw_text, min_index=min_index
        )

    async def post(
        self,
        path: str,
        json_data: Any = None,
        min_index: Optional[int] = None,
    ) -> Any:
        """
        Send a POST request with a JSON body.

        Args:
            path:      API path (e.g. ``/api/v1/records``).
            json_data: Request body (dict or Pydantic-serialisable object).
            min_index: Optional read-your-writes barrier for READ-shaped POST
                       endpoints (global search, graph walk, batch-content). The
                       server's MinIndexBarrier middleware wraps the whole API,
                       so it honours ``X-Anhur-Min-Index`` on POST reads too.
                       Plain writes leave this ``None`` (they PRODUCE the index,
                       not consume one).

        Returns:
            Parsed JSON response body.
        """
        if self.mode == "mcp" and path in self._MCP_TOOL_MAP:
            return await self._mcp_tunnel(path, json_data or {})
        return await self._request("POST", path, body=json_data, min_index=min_index)

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

    async def post_multipart(
        self,
        path: str,
        file_field: str,
        file_data: bytes,
        filename: str,
        extra_fields: Optional[Dict[str, str]] = None,
    ) -> Any:
        """
        Send a POST request with multipart/form-data (file upload).

        Args:
            path:         API path.
            file_field:   Form field name for the file.
            file_data:    Raw file bytes.
            filename:     Original filename (used for MIME detection).
            extra_fields: Additional string form fields.

        Returns:
            Parsed JSON response body.
        """
        session = self._session
        if session is None:
            raise AnhurConnectionError(
                "Connection not established. Use 'async with' or call connect() first."
            )

        form = aiohttp.FormData()
        form.add_field(file_field, file_data, filename=filename)
        if extra_fields:
            for key, value in extra_fields.items():
                form.add_field(key, value)

        # Build auth headers without Content-Type (aiohttp sets multipart boundary).
        headers = {
            "X-API-Key": self.api_key,
            "User-Agent": "AnhurSDK-Python/2.1",
        }
        if self.tenant_id:
            headers["X-Tenant-ID"] = self.tenant_id

        url = f"{self.base_url}{path}"
        try:
            async with session.post(
                url,
                data=form,
                headers=headers,
                allow_redirects=False,
            ) as response:
                raw = await response.content.read(self._max_response_size + 1)
                if len(raw) > self._max_response_size:
                    raise AnhurError(
                        f"Response exceeds maximum size ({self._max_response_size // (1024*1024)} MB)"
                    )
                body_text = raw.decode("utf-8", errors="replace")

                if response.status in (401, 403):
                    raise AnhurAuthError(f"Authentication failed (HTTP {response.status})")
                elif response.status in (400, 422):
                    raise AnhurQueryError(f"Invalid request (HTTP {response.status}): {body_text[:500]}")
                elif response.status == 404:
                    raise AnhurQueryError(f"Resource not found (HTTP 404): {path}")
                elif response.status == 409:
                    raise AnhurQueryError(f"Conflict (HTTP 409): {body_text[:500]}")
                elif response.status == 415:
                    raise AnhurQueryError(f"Unsupported media type (HTTP 415): {body_text[:500]}")
                elif response.status == 429:
                    raise AnhurError(f"Rate limited (HTTP 429): {body_text[:200]}")
                elif response.status in (301, 302, 303, 307, 308):
                    raise AnhurError(
                        f"Server returned redirect (HTTP {response.status}). "
                        f"Redirects are disabled to prevent credential leakage."
                    )
                elif response.status >= 500:
                    raise AnhurError(f"Server error (HTTP {response.status}): {body_text[:500]}")

                if not body_text:
                    return {}
                try:
                    return json.loads(body_text)
                except json.JSONDecodeError:
                    return {"message": body_text[:1000]}

        except aiohttp.ClientError as exc:
            raise AnhurConnectionError(
                f"Failed to connect to AnhurDB: {type(exc).__name__}"
            ) from exc

    # -- Internal request engine --------------------------------------------

    async def _request(
        self,
        method: str,
        path: str,
        body: Any = None,
        params: Optional[Dict[str, str]] = None,
        raw_text: bool = False,
        min_index: Optional[int] = None,
    ) -> Any:
        """
        Execute a SINGLE HTTP request and return parsed JSON.

        Transparent pipe (Junior Tip [router owns retry, 2026-07-06]): this
        makes exactly ONE request and surfaces the result — success, typed HTTP
        error, or connection failure — with NO retry. The router is the single
        retry owner in the stack (it can prove an idempotent write never
        committed before replaying it across a leadership handoff). Every 5xx,
        including a leadership-handoff body, now surfaces immediately to the
        caller instead of being silently replayed here.

        Security:
          - Response body capped at ``max_response_size`` to prevent OOM.
          - Error messages never include the API key.
          - Redirects are disabled (header leak protection).

        Args:
            raw_text:  When True, a non-JSON 2xx body is returned as the decoded
                       string rather than wrapped in ``{"message": ...}``.
            min_index: When a positive int, adds the ``X-Anhur-Min-Index`` read
                       barrier header (read-your-writes). ``None`` or ``0``
                       omits it, preserving the default eventually-consistent,
                       load-balanced read.

        Raises:
            AnhurAuthError: On 401/403.
            AnhurQueryError: On 400/404/409/415/422.
            AnhurError: On 429, redirect (3xx), or 5xx.
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

        # Per-request headers: only the RYW barrier is set here; auth/tenant
        # headers live on the session. aiohttp merges these over the session
        # defaults. We render the index in base-10 to match the server's
        # strconv.ParseUint decode. ``min_index`` falsy (None/0) → header off.
        request_headers: Optional[Dict[str, str]] = None
        if min_index:
            request_headers = {_HEADER_MIN_INDEX: str(min_index)}

        # Single request, no retry — the router is the only retry owner (see the
        # transparent-pipe Junior Tip on this method and at module level).
        try:
            async with session.request(
                method,
                url,
                json=body,
                headers=request_headers,
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
                elif response.status == 409:
                    raise AnhurQueryError(
                        f"Conflict (HTTP 409): {body_text[:500]}"
                    )
                elif response.status == 415:
                    raise AnhurQueryError(
                        f"Unsupported media type (HTTP 415): {body_text[:500]}"
                    )
                elif response.status == 429:
                    raise AnhurError(
                        f"Rate limited (HTTP 429): {body_text[:200]}"
                    )
                elif response.status in (301, 302, 303, 307, 308):
                    # Redirects are disabled for security. Log the attempt.
                    raise AnhurError(
                        f"Server returned redirect (HTTP {response.status}). "
                        f"Redirects are disabled to prevent credential leakage."
                    )
                elif response.status >= 500:
                    # No retry here: surface the server error verbatim. The
                    # router replays idempotent writes across a leadership
                    # handoff; a transport-level replay would be the widest,
                    # least-informed retry (it would re-fire reads too).
                    raise AnhurError(
                        f"Server error (HTTP {response.status}): "
                        f"{body_text[:500]}"
                    )

                if not body_text:
                    return {}

                try:
                    return json.loads(body_text)
                except json.JSONDecodeError:
                    # Plain-text body. ``read_content`` wants it verbatim;
                    # everyone else gets the legacy ``{"message": ...}``
                    # envelope for backward compatibility.
                    if raw_text:
                        return body_text
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
