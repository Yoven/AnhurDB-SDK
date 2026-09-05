"""The single HTTP round trip, and the status-code to exception mapping.

Domain, in one sentence: send exactly ONE request and turn whatever came back
into either a parsed body or the right typed exception.

Junior Tip [why the mapping is the domain, 2026-09-05]: the public verbs above
(``get``/``post``/``patch``/``delete``) are grammar — they choose a method and a
path and nothing else. Everything that decides what an ANSWER MEANS lives here:
401 is ``AnhurAuthError``, 409/415 are ``AnhurQueryError``, 429 is a retryable
``AnhurError``, a transport failure is ``AnhurConnectionError`` with the URL
stripped out (the URL carries the server address and would end up in logs). One
place to read when the question is "why did I get this exception?", split out of
a ``connection.py`` that had reached 593 lines against a ~300-line cut.

Junior Tip [there is NO retry loop here, and that is deliberate]: the transport
issues one request and surfaces the outcome verbatim. A retry hidden in the
transport turns a non-idempotent call (ingest, create) into a duplicate the
caller never asked for and cannot see. Retrying is the caller's decision, which
is exactly why every error above carries a ``Retryable`` classification.
"""

import asyncio
import aiohttp
import json
from typing import Any, Optional
from urllib.parse import urlencode

from .connection_guards import QueryParams, read_capped_body
from .exceptions import (
    AnhurError,
    AnhurConnectionError,
    AnhurQueryError,
    AnhurAuthError,
)


class RequestExecutionMixin:
    """One request, one answer, one typed outcome. Mixed into
    ``HTTPConnection``.

    Reads ``self.base_url``, ``self.headers``, ``self._session``,
    ``self._timeout`` and ``self._max_response_size`` from the host connection.
    """

    async def _request(
        self,
        method: str,
        path: str,
        body: Any = None,
        params: Optional[QueryParams] = None,
        raw_text: bool = False,
    ) -> Any:
        """Execute a single HTTP request and return parsed JSON.

        Issues exactly one request and surfaces the result — success, typed HTTP
        error, or connection failure — with no client-side retry.

        Security:
          - Response body capped at ``max_response_size`` to prevent OOM.
          - Error messages never include the API key.
          - Redirects are disabled (header leak protection).

        Args:
            raw_text:  When True, a non-JSON 2xx body is returned as the decoded
                       string rather than wrapped in ``{"message": ...}``.

        Raises:
            AnhurAuthError: On 401/403.
            AnhurQueryError: On 400/404/409/415/422.
            AnhurError: On 429, redirect (3xx), or 5xx.
            AnhurConnectionError: On network failure or timeout."""
        session = self._session
        if session is None:
            raise AnhurConnectionError(
                "Connection not established. Use 'async with AnhurClient(...)' "
                "or call 'await client.connect()' first."
            )
        if self._before_request is not None:
            await self._before_request()
            session = self._session
            if session is None:
                raise AnhurConnectionError("Connection closed during token refresh")

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
                # SECURITY: cap preservado DENTRO de _read_capped_body — que lê
                # até EOF em loop (o read(n) único truncava; ver a Junior Tip
                # do helper e o incidente de busca vazia de 2026-07-30).
                raw = await read_capped_body(response, self._max_response_size)
                body_text = raw.decode("utf-8", errors="replace")

                # Map HTTP status codes to typed exceptions.
                # SECURITY: Error messages include status + server body but
                # never the API key or full URL (which could leak in logs).
                if response.status in (401, 403):
                    raise AnhurAuthError(
                        f"Authentication failed (HTTP {response.status})",
                        status_code=response.status,
                    )
                elif response.status in (400, 422):
                    raise AnhurQueryError(
                        f"Invalid request (HTTP {response.status}): {body_text[:500]}",
                        status_code=response.status,
                    )
                elif response.status == 404:
                    raise AnhurQueryError(
                        f"Resource not found (HTTP 404): {path}",
                        status_code=404,
                    )
                elif response.status == 409:
                    raise AnhurQueryError(
                        f"Conflict (HTTP 409): {body_text[:500]}",
                        status_code=409,
                    )
                elif response.status == 415:
                    raise AnhurQueryError(
                        f"Unsupported media type (HTTP 415): {body_text[:500]}",
                        status_code=415,
                    )
                elif response.status == 429:
                    raise AnhurError(
                        f"Rate limited (HTTP 429): {body_text[:200]}",
                        status_code=429,
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
                        f"{body_text[:500]}",
                        status_code=response.status,
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

        except asyncio.TimeoutError as exc:
            # Junior Tip [MUST come before ClientError, 2026-08-13]:
            # asyncio.TimeoutError is NOT a subclass of aiohttp.ClientError, so
            # aiohttp's total-timeout escaped this handler entirely and reached
            # callers raw — with str(exc) == '' and no status. A benchmark run
            # aborted on exactly that empty error. On a write, a timeout means
            # the server may or may not have committed; the SDK never retries
            # writes for you.
            raise AnhurConnectionError(
                "request timed out (the server may still have processed it)",
                kind=AnhurError.KIND_TIMEOUT,
            ) from exc
        except aiohttp.ClientError as exc:
            # SECURITY: Do not include the full URL in error messages
            # as it could be logged and contains the server address.
            raise AnhurConnectionError(
                f"Failed to connect to AnhurDB: {type(exc).__name__}",
                kind=AnhurError.KIND_TRANSPORT,
            ) from exc

    # -- MCP tunnel (legacy/alternative transport) --------------------------
