"""The single round trip of the client-key surface, and its session lifetime.

One domain: hold an aiohttp session authenticated with a CLIENT key and send
ONE request to ``/api/v1/client/*``, returning the parsed 2xx object or the
typed error. ``ClientAPI`` (``client_key.py``) builds every method on top.

Security, same as the data-plane transport (``connection_request.py``):
redirects disabled, response capped at 100 MB, the key never in a message,
the URL never in a transport error.

Junior Tip [never X-Tenant-ID with a client key]: a client key is not bound to
a tenant — the tenant travels in the BODY of each call. Sending X-Tenant-ID
would invite a future server change to act on a tenant the body did not name,
so this session never carries that header.

Junior Tip [no retry here]: a 503 ``not_leader`` is reported
(``AnhurClientAPIError.is_not_leader``) and the caller decides; a retry hidden
in the transport would repeat a non-idempotent mint the caller cannot see.
"""

import asyncio
import json
from typing import Any, Dict, Optional

import aiohttp

from ..version import USER_AGENT
from .connection_guards import MAX_RESPONSE_SIZE, read_capped_body, validate_header_value
from .client_key_errors import AnhurClientAPIError, unexpected_response
from .exceptions import AnhurConnectionError, AnhurError


class ClientKeyTransport:
    """Session lifetime (``async with``) and ``_send`` for ``ClientAPI``."""

    def __init__(self, client_key: str, url: str, timeout: float = 30.0):
        if not client_key:
            raise ValueError("client_key is required.")
        validate_header_value(client_key, "client_key")
        self.base_url = url.rstrip("/")
        self._headers = {"X-API-Key": client_key, "User-Agent": USER_AGENT}
        self._timeout = aiohttp.ClientTimeout(total=timeout)
        self._session: Optional[aiohttp.ClientSession] = None

    async def connect(self) -> None:
        """Open the HTTP session (idempotent)."""
        if self._session is None:
            self._session = aiohttp.ClientSession(headers=self._headers, timeout=self._timeout)

    async def close(self) -> None:
        """Close the HTTP session and release its connections."""
        if self._session is not None:
            await self._session.close()
            self._session = None

    async def __aenter__(self) -> "ClientKeyTransport":
        await self.connect()
        return self

    async def __aexit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        await self.close()

    async def _send(self, method: str, path: str, body: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        """Send ONE request; return the 2xx JSON object.

        Raises:
            AnhurClientAPIError: any non-2xx status (code = ``body.error``).
            AnhurClientAPIResponseError: a 2xx body that is not a JSON object.
            AnhurConnectionError: no answer (timeout or transport failure)."""
        session = self._session
        if session is None:
            raise AnhurConnectionError(
                "Connection not established. Use 'async with ClientAPI(...)' "
                "or call 'await api.connect()' first."
            )
        try:
            async with session.request(
                method, self.base_url + path, json=body, allow_redirects=False
            ) as response:
                raw_body = await read_capped_body(response, MAX_RESPONSE_SIZE)
                body_text = raw_body.decode("utf-8", errors="replace")
                if not 200 <= response.status < 300:
                    raise AnhurClientAPIError.from_body(response.status, method, path, body_text)
        except asyncio.TimeoutError as exc:
            raise AnhurConnectionError(
                "request timed out (the server may still have processed it)",
                kind=AnhurError.KIND_TIMEOUT,
            ) from exc
        except aiohttp.ClientError as exc:
            # SECURITY: never quote the URL (server address ends up in logs).
            raise AnhurConnectionError(
                f"Failed to connect to AnhurDB: {type(exc).__name__}",
                kind=AnhurError.KIND_TRANSPORT,
            ) from exc
        try:
            parsed = json.loads(body_text)
        except ValueError:
            parsed = None
        if not isinstance(parsed, dict):
            raise unexpected_response(method, path, "response is not a JSON object")
        return parsed


__all__ = ["ClientKeyTransport"]
