"""Client-key impersonation: mint and refresh a short-lived tenant token.

One domain: ``POST /api/v1/client/impersonate`` and the auto-refresh that keeps
the minted token valid for the life of a ``Memory`` built by
``Memory.from_client_key``. The long-lived client key only ever reaches
``/api/v1/client/*``; every data-plane call uses the minted token.

Split out of ``client/__init__.py`` in 4.0.0 (house 300-line cut). The
``from_client_key`` factory stays on ``Memory`` because it must call the
concrete constructor; the token lifecycle it installs lives here.
"""

import json
from datetime import datetime, timezone
from typing import Any, Dict, Optional

from .connection import HTTPConnection
from .exceptions import AnhurError
from ..version import USER_AGENT

# Impersonation refresh skew (seconds before expires_at).
_IMPERSONATE_REFRESH_SKEW_SECONDS = 60


async def _impersonate_tenant(
    *,
    base_url: str,
    client_key: str,
    tenant_id: str,
    expires_in: int = 3600,
) -> Dict[str, Any]:
    """Mint a short-lived tenant token via POST /api/v1/client/impersonate."""
    import aiohttp

    if expires_in < 1:
        expires_in = 1
    if expires_in > 86400:
        expires_in = 86400
    url = base_url.rstrip("/") + "/api/v1/client/impersonate"
    headers = {
        "X-API-Key": client_key,
        "Content-Type": "application/json",
        "User-Agent": USER_AGENT,
    }
    payload = {"tenant_id": tenant_id, "expires_in": expires_in}
    timeout = aiohttp.ClientTimeout(total=30)
    async with aiohttp.ClientSession(timeout=timeout) as session:
        async with session.post(url, json=payload, headers=headers, allow_redirects=False) as response:
            body = await response.read()
            if response.status >= 400:
                detail = body.decode("utf-8", errors="replace")[:400]
                raise AnhurError(
                    f"impersonate failed HTTP {response.status}: {detail}"
                )
            parsed: Dict[str, Any] = json.loads(body.decode("utf-8"))
            return parsed


def _parse_expires_at(expires_raw: str) -> datetime:
    """Parse the server's RFC3339 ``expires_at``; unparseable means "now".

    Junior Tip [why "now" and not "far future"]: an expiry we cannot read is
    treated as already expired, so the next request re-mints. Guessing a later
    instant would keep sending a token the server may already refuse."""
    try:
        return datetime.fromisoformat(expires_raw.replace("Z", "+00:00"))
    except ValueError:
        return datetime.now(timezone.utc)


class ImpersonationMixin:
    """Token state and refresh for a ``Memory`` built from a client key."""

    _connection: HTTPConnection
    _client_key: Optional[str]
    _impersonate_tenant_id: Optional[str]
    _impersonate_expires_at: Optional[datetime]
    _impersonate_expires_in: int

    async def _ensure_impersonation_fresh(self) -> None:
        """Re-mint when within skew of expires_at (no-op for tenant keys)."""
        if not self._client_key or not self._impersonate_tenant_id:
            return
        now = datetime.now(timezone.utc)
        expires_at = self._impersonate_expires_at
        if expires_at is not None and expires_at.tzinfo is None:
            expires_at = expires_at.replace(tzinfo=timezone.utc)
        if expires_at is not None:
            remaining = (expires_at - now).total_seconds()
            if remaining > _IMPERSONATE_REFRESH_SKEW_SECONDS:
                return
        minted = await _impersonate_tenant(
            base_url=self._connection.base_url,
            client_key=self._client_key,
            tenant_id=self._impersonate_tenant_id,
            expires_in=self._impersonate_expires_in,
        )
        temp_key = minted.get("api_key") or ""
        if not temp_key.startswith("anhur_"):
            raise AnhurError("impersonate refresh missing api_key")
        self._connection.set_api_key(temp_key)
        self._impersonate_expires_at = _parse_expires_at(minted.get("expires_at") or "")


__all__ = ["ImpersonationMixin", "_impersonate_tenant", "_parse_expires_at"]
