"""Multipart file upload over ``multipart/form-data``.

Domain, in one sentence: put ONE file plus its form fields on the wire and read
the answer back — the only request shape in this SDK that is not a JSON body.

Junior Tip [why upload is not just another verb, 2026-09-05]: every other
method here hands aiohttp a ``json=`` kwarg and lets it own the encoding. This
one builds ``FormData`` and must let aiohttp own the MIME BOUNDARY, which means
it must NOT inherit a session-level ``Content-Type: application/json`` — a
sticky JSON content type made uploads arrive as JSON and AnhurDB answered
HTTP 400 "failed to parse multipart form". That constraint belongs to this
domain alone, so the code and the constraint now live in one file, split out of
a ``connection.py`` that had reached 593 lines against a ~300-line cut.
"""

import aiohttp
import json
from typing import Any, Dict, Optional

from ..version import USER_AGENT
from .connection_guards import read_capped_body
from .exceptions import (
    AnhurError,
    AnhurConnectionError,
    AnhurQueryError,
    AnhurAuthError,
)


class MultipartUploadMixin:
    """``POST`` of a single file as ``multipart/form-data``. Mixed into
    ``HTTPConnection``.

    Reads ``self.base_url``, ``self.headers``, ``self._session``,
    ``self._timeout`` and ``self._max_response_size`` from the host connection,
    so it is a mixin rather than a free function: an upload is a use of an open
    connection, not a standalone act.
    """

    async def post_multipart(
        self,
        path: str,
        file_field: str,
        file_data: bytes,
        filename: str,
        extra_fields: Optional[Dict[str, str]] = None,
    ) -> Any:
        """Send a POST request with multipart/form-data (file upload).

        Args:
            path:         API path.
            file_field:   Form field name for the file.
            file_data:    Raw file bytes.
            filename:     Original filename (used for MIME detection).
            extra_fields: Additional string form fields.

        Returns:
            Parsed JSON response body."""
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

        # Auth only — FormData sets multipart/form-data + boundary.
        # Session must NOT carry a default Content-Type (see __init__).
        headers: Dict[str, str] = {
            "X-API-Key": self.api_key,
            "User-Agent": USER_AGENT,
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
                raw = await read_capped_body(response, self._max_response_size)
                body_text = raw.decode("utf-8", errors="replace")

                if response.status in (401, 403):
                    raise AnhurAuthError(f"Authentication failed (HTTP {response.status})")
                elif response.status in (400, 422):
                    raise AnhurQueryError(f"Invalid request (HTTP {response.status}): {body_text[:500]}")
                elif response.status == 404:
                    raise AnhurQueryError(f"Resource not found (HTTP 404): {path}")
                elif response.status == 409:
                    raise AnhurQueryError(f"Conflict (HTTP 409): {body_text[:500]}", status_code=409)
                elif response.status == 415:
                    raise AnhurQueryError(f"Unsupported media type (HTTP 415): {body_text[:500]}", status_code=415)
                elif response.status == 429:
                    raise AnhurError(f"Rate limited (HTTP 429): {body_text[:200]}", status_code=429)
                elif response.status in (301, 302, 303, 307, 308):
                    raise AnhurError(
                        f"Server returned redirect (HTTP {response.status}). "
                        f"Redirects are disabled to prevent credential leakage.",
                        status_code=response.status,
                    )
                elif response.status >= 500:
                    raise AnhurError(f"Server error (HTTP {response.status}): {body_text[:500]}", status_code=response.status)

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
