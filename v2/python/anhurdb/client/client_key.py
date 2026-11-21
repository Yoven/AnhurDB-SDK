"""``ClientAPI`` — the handle for a CLIENT key on ``/api/v1/client/*`` (ADR-0034).

One domain: the eleven client-key operations (child clients, tenants with an
agent profile, scoped/l1_only impersonation, revoke). Routes from
``server/router_client.go``; shapes from ``server/handler/client_*.go``.
Parity: Go ``client/client_key*.go``, TypeScript ``src/clientKeyApi.ts``.

    async with ClientAPI(client_key, url="http://localhost:8000") as api:
        child = await api.create_child(account_ref)
        await api.create_child_tenant(child.child_client_id, "geral",
                                      profile=AgentProfile(config={"search_top_k": "8"}))
        token = await api.impersonate_child(child.child_client_id, "geral",
                                            scope=KeyScope.WRITE, l1_only=True, ttl_seconds=3600)
"""

from datetime import datetime
from typing import Any, Dict, List, Optional

from .client_key_errors import (
    RESPONSE_CODE_L1_ONLY_NOT_HONOURED,
    RESPONSE_CODE_SCOPE_NOT_HONOURED,
    AnhurClientAPIResponseError,
    invalid_param,
    is_canonical_uuid,
    missing_field,
    require_canonical_child_id,
    require_scope,
    require_tenant_id,
    unexpected_response,
)
from .client_key_transport import ClientKeyTransport
from .client_key_types import (
    AgentProfile,
    ChildClient,
    ChildStatus,
    ClientTenant,
    ImpersonatedKey,
    RevokeResult,
    scope_rank,
)

_CHILDREN_PATH = "/api/v1/client/children"


def _child_from(summary: Any, require_canonical_id: bool) -> ChildClient:
    """Build a ChildClient from one ``childSummary`` object.

    Raises ValueError when it is not an object, or (create only) when its id
    is not a canonical UUID — the id is spliced into later URL paths."""
    if not isinstance(summary, dict):
        raise ValueError("child")
    if require_canonical_id and not is_canonical_uuid(summary.get("child_client_id")):
        raise ValueError("child_client_id")
    return ChildClient(
        child_client_id=str(summary.get("child_client_id") or ""),
        external_ref=str(summary.get("external_ref") or ""),
        status=str(summary.get("status") or ""),
        created=summary.get("created") is True,
        created_at=str(summary.get("created_at") or ""),
    )


class ClientAPI(ClientKeyTransport):
    """Client-key operations. Use as ``async with ClientAPI(key, url=...)``.

    Every method sends at most ONE request and never retries. Refusals raise
    ``AnhurClientAPIError`` (``code`` = server ``body.error``); inputs that can
    never succeed raise ``AnhurQueryError`` (400) before any request."""

    def __init__(self, client_key: str, url: Optional[str] = None, timeout: float = 30.0):
        from . import DEFAULT_CLOUD_URL

        super().__init__(client_key, url or DEFAULT_CLOUD_URL, timeout)

    # ── Children ────────────────────────────────────────────────────────

    async def create_child(self, external_ref: str) -> ChildClient:
        """Create the caller's child for ``external_ref`` or confirm it.

        Idempotent per (caller, ref): ``created`` True once (HTTP 201), then
        False with the SAME id. Use a stable opaque id per account, never a
        reusable slug. A new ref beyond the cap is 429 ``child_limit``."""
        if not (external_ref or "").strip():
            raise invalid_param("external_ref is required")
        answer = await self._send("POST", _CHILDREN_PATH, {"external_ref": external_ref})
        try:
            return _child_from(answer, require_canonical_id=True)
        except ValueError:
            raise unexpected_response("POST", _CHILDREN_PATH, missing_field("child_client_id")) from None

    async def list_children(self) -> List[ChildClient]:
        """The caller's children, ordered by (created_at, id)."""
        answer = await self._send("GET", _CHILDREN_PATH)
        summaries = answer.get("children")
        if not isinstance(summaries, list):
            raise unexpected_response("GET", _CHILDREN_PATH, missing_field("children"))
        try:
            return [_child_from(summary, require_canonical_id=False) for summary in summaries]
        except ValueError:
            raise unexpected_response("GET", _CHILDREN_PATH, "response is not a JSON object") from None

    async def suspend_child(self, child_id: str) -> ChildStatus:
        """Status "suspended" (absolute). The child's tokens get 403
        ``child_suspended`` within the server's 5 s cache; nothing is revoked."""
        return await self._set_child_status(child_id, "suspend")

    async def resume_child(self, child_id: str) -> ChildStatus:
        """Status "active"; the SAME tokens work again."""
        return await self._set_child_status(child_id, "resume")

    async def _set_child_status(self, child_id: str, action: str) -> ChildStatus:
        require_canonical_child_id(child_id)
        path = f"{_CHILDREN_PATH}/{child_id}/{action}"
        answer = await self._send("POST", path)
        if not answer.get("child_client_id") or not answer.get("status"):
            raise unexpected_response("POST", path, missing_field("status"))
        return ChildStatus(child_client_id=str(answer["child_client_id"]), status=str(answer["status"]))

    # ── Tenants ─────────────────────────────────────────────────────────

    async def list_tenants(self) -> List[str]:
        """Tenant ids of the caller's own client (``shared`` is never listed)."""
        return await self._list_tenant_ids("/api/v1/client/tenants")

    async def list_child_tenants(self, child_id: str) -> List[str]:
        """Tenant ids a child owns."""
        require_canonical_child_id(child_id)
        return await self._list_tenant_ids(f"{_CHILDREN_PATH}/{child_id}/tenants")

    async def _list_tenant_ids(self, path: str) -> List[str]:
        answer = await self._send("GET", path)
        entries = answer.get("tenants")
        if not isinstance(entries, list):
            raise unexpected_response("GET", path, missing_field("tenants"))
        return [str(entry.get("tenant_id", "")) for entry in entries if isinstance(entry, dict)]

    async def create_tenant(
        self, tenant_id: str, description: str = "", profile: Optional[AgentProfile] = None
    ) -> ClientTenant:
        """Create (or confirm) a tenant owned by the caller; the profile is
        (re)applied either way."""
        return await self._create_tenant("/api/v1/client/tenants", tenant_id, description, profile)

    async def create_child_tenant(
        self, child_id: str, tenant_id: str, description: str = "", profile: Optional[AgentProfile] = None
    ) -> ClientTenant:
        """Create (or confirm) a tenant owned by the CHILD."""
        require_canonical_child_id(child_id)
        return await self._create_tenant(f"{_CHILDREN_PATH}/{child_id}/tenants", tenant_id, description, profile)

    async def _create_tenant(
        self, path: str, tenant_id: str, description: str, profile: Optional[AgentProfile]
    ) -> ClientTenant:
        require_tenant_id(tenant_id)
        if profile is not None and profile.max_rpm is not None and (
            isinstance(profile.max_rpm, bool) or not isinstance(profile.max_rpm, int) or profile.max_rpm < 1
        ):
            raise invalid_param("profile.max_rpm must be >= 1")
        body: Dict[str, Any] = {"tenant_id": tenant_id}
        if description:
            body["description"] = description
        if profile is not None:
            body["profile"] = profile.to_wire()
        answer = await self._send("POST", path, body)
        if not answer.get("tenant_id") or not answer.get("composite_tenant"):
            raise unexpected_response("POST", path, missing_field("composite_tenant"))
        max_rpm = answer.get("max_rpm")
        return ClientTenant(
            client_id=str(answer.get("client_id") or ""),
            tenant_id=str(answer["tenant_id"]),
            composite_tenant=str(answer["composite_tenant"]),
            created=answer.get("created") is True,
            max_rpm=max_rpm if isinstance(max_rpm, int) and not isinstance(max_rpm, bool) else None,
        )

    # ── Impersonation ───────────────────────────────────────────────────

    async def impersonate(
        self, tenant_id: str, scope: str = "", l1_only: bool = False, description: str = "", ttl_seconds: int = 0
    ) -> ImpersonatedKey:
        """Mint a token for one of the caller's tenants; ``scope=""`` means
        the client key's ceiling. ``ttl_seconds=0`` lets the server default
        (3600 s, capped at 24 h)."""
        require_scope(scope, allow_empty=True)
        return await self._impersonate("/api/v1/client/impersonate", tenant_id, scope, l1_only, description, ttl_seconds)

    async def impersonate_child(
        self, child_id: str, tenant_id: str, scope: str, l1_only: bool = False, description: str = "", ttl_seconds: int = 0
    ) -> ImpersonatedKey:
        """Mint a token for one of a child's tenants; ``scope`` is mandatory."""
        require_canonical_child_id(child_id)
        require_scope(scope, allow_empty=False)
        path = f"{_CHILDREN_PATH}/{child_id}/impersonate"
        return await self._impersonate(path, tenant_id, scope, l1_only, description, ttl_seconds)

    async def _impersonate(
        self, path: str, tenant_id: str, scope: str, l1_only: bool, description: str, ttl_seconds: int
    ) -> ImpersonatedKey:
        """Validate, post, and fail closed on an echo weaker than the request.

        Junior Tip [fail closed on the echo]: a token is a capability. If the
        caller asked for l1_only or a scope and the answer does not prove it,
        handing the token over lets an agent act with MORE power than asked —
        silently. The SDK refuses and returns ``key_id`` so the caller can
        revoke. A LOWER scope is accepted (the server lowers to the ceiling)."""
        require_tenant_id(tenant_id)
        if isinstance(ttl_seconds, bool) or not isinstance(ttl_seconds, int) or ttl_seconds < 0:
            raise invalid_param("ttl must be a whole number of seconds >= 1")
        body: Dict[str, Any] = {"tenant_id": tenant_id}
        if scope:
            body["scope"] = scope
        if l1_only:
            body["l1_only"] = True
        if description:
            body["description"] = description
        if ttl_seconds:
            body["expires_in"] = ttl_seconds
            body["ttl_seconds"] = ttl_seconds
        answer = await self._send("POST", path, body)
        api_key, key_id = answer.get("api_key"), answer.get("key_id")
        if not isinstance(api_key, str) or not api_key or not isinstance(key_id, str) or not key_id:
            raise unexpected_response("POST", path, missing_field("api_key"))
        try:
            expires_at = datetime.fromisoformat(str(answer.get("expires_at") or "").replace("Z", "+00:00"))
        except ValueError:
            raise unexpected_response("POST", path, missing_field("expires_at")) from None
        echoed_l1_only = answer.get("l1_only") is True
        raw_scope = answer.get("scope")
        echoed_scope = raw_scope if isinstance(raw_scope, str) else ""
        if l1_only and not echoed_l1_only:
            raise AnhurClientAPIResponseError(
                "POST", path, RESPONSE_CODE_L1_ONLY_NOT_HONOURED,
                f"l1_only was requested but the response echoes l1_only=false (key_id={key_id})", key_id=key_id,
            )
        if scope and (scope_rank(echoed_scope) == 0 or scope_rank(echoed_scope) > scope_rank(scope)):
            raise AnhurClientAPIResponseError(
                "POST", path, RESPONSE_CODE_SCOPE_NOT_HONOURED,
                f'scope "{scope}" was requested but the response echoes "{echoed_scope}" (key_id={key_id})',
                key_id=key_id,
            )
        expires_in = answer.get("expires_in")
        return ImpersonatedKey(
            api_key=api_key,
            key_id=key_id,
            client_id=str(answer.get("client_id") or ""),
            tenant_id=str(answer.get("tenant_id") or ""),
            composite_tenant=str(answer.get("composite_tenant") or ""),
            kind=str(answer.get("kind") or ""),
            scope=echoed_scope,
            l1_only=echoed_l1_only,
            expires_in=expires_in if isinstance(expires_in, int) and not isinstance(expires_in, bool) else 0,
            expires_at=expires_at,
        )

    async def revoke_impersonation(self, key_id: str) -> RevokeResult:
        """Revoke a token minted by the caller's client or one of its
        children. 404 ``key_not_found``, 403 ``key_not_owned``."""
        if not (key_id or "").strip():
            raise invalid_param("key_id is required")
        path = "/api/v1/client/impersonate/revoke"
        answer = await self._send("POST", path, {"key_id": key_id})
        if answer.get("revoked") is not True:
            raise unexpected_response("POST", path, missing_field("revoked"))
        return RevokeResult(revoked=True, key_id=str(answer.get("key_id") or ""))


__all__ = ["ClientAPI"]
