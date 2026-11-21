"""Shapes of the client-key surface (``/api/v1/client/*``, ADR-0034).

One domain: the request and response objects of ``ClientAPI``. Every field
name is the server's JSON name, read from the AnhurDB handlers
(``server/handler/client_*.go``); each class names the handler that writes
it. Parity: Go ``client/client_key_types.go``, TypeScript
``src/clientKeyTypes.ts``.
"""

from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Dict, Optional


class KeyScope:
    """The RBAC scopes an impersonation token can be minted with
    (``validRequestedScope`` in ``server/handler/client_impersonate.go``)."""

    ADMIN = "admin"
    WRITE = "write"
    READ_ONLY = "read_only"


_SCOPE_RANK = {KeyScope.READ_ONLY: 1, KeyScope.WRITE: 2, KeyScope.ADMIN: 3}


def scope_rank(scope: str) -> int:
    """Order scopes read_only < write < admin; 0 means unknown.

    Junior Tip [why the SDK ranks scopes]: the server never refuses a scope
    above the caller's ceiling — it silently LOWERS it. A scope ABOVE the one
    requested can only come from a bug or an impostor, and handing that token
    to an agent would grant more than asked. Ranking accepts "lower" and
    refuses "higher" without string matching."""
    return _SCOPE_RANK.get(scope, 0)


@dataclass
class ChildClient:
    """One child client (``childSummary``, ``server/handler/client_children.go``).

    ``created`` is True only on the call that created it (HTTP 201) and always
    False in ``list_children``."""

    child_client_id: str
    external_ref: str
    status: str
    created: bool
    created_at: str


@dataclass
class ChildStatus:
    """Answer of suspend/resume: the status re-read after the change."""

    child_client_id: str
    status: str


@dataclass
class AgentProfile:
    """Optional per-tenant configuration sent on tenant creation
    (``normalizeAgentProfile``, ``server/handler/client_tenant_profile.go``).

    ``config`` takes only the 16 numeric ControlPlane keys, values as numeric
    STRINGS; ``max_rpm`` must be >= 1 and is clamped server-side
    (``ANHUR_AGENT_PROFILE_MAX_RPM``, default 1200) — read the effective value
    from ``ClientTenant.max_rpm``."""

    config: Dict[str, str] = field(default_factory=dict)
    max_rpm: Optional[int] = None

    def to_wire(self) -> Dict[str, Any]:
        """``{"config":…, "max_rpm":…}``; empty config and None are omitted,
        so ``AgentProfile()`` is sent as ``{}`` (valid, applies nothing)."""
        wire: Dict[str, Any] = {}
        if self.config:
            wire["config"] = dict(self.config)
        if self.max_rpm is not None:
            wire["max_rpm"] = self.max_rpm
        return wire


@dataclass
class ClientTenant:
    """Answer of tenant creation (``tenantCreatedBody``,
    ``server/handler/client_tenants.go``). No credential is returned.

    ``client_id`` is the OWNER (the caller, or the child on the child route);
    ``max_rpm`` is the effective clamped value, None when none was sent."""

    client_id: str
    tenant_id: str
    composite_tenant: str
    created: bool
    max_rpm: Optional[int] = None


@dataclass
class ImpersonatedKey:
    """A minted token (``impersonateForClient``,
    ``server/handler/client_impersonate.go``).

    Callers MUST rely on ``scope`` and ``l1_only`` as ECHOED here: the server
    lowers a scope above the client key's ceiling. The SDK already refuses an
    echo weaker than the request (``AnhurClientAPIResponseError``).
    ``api_key`` is the secret — never log it."""

    api_key: str
    key_id: str
    client_id: str
    tenant_id: str
    composite_tenant: str
    kind: str
    scope: str
    l1_only: bool
    expires_in: int
    expires_at: datetime

    def __repr__(self) -> str:
        # SECURITY: the dataclass repr would print the secret into any log.
        return (
            f"ImpersonatedKey(key_id={self.key_id!r}, tenant_id={self.tenant_id!r}, "
            f"scope={self.scope!r}, l1_only={self.l1_only!r}, expires_at={self.expires_at!r})"
        )


@dataclass
class RevokeResult:
    """Answer of ``revoke_impersonation`` (``server/handler/client_keys.go``)."""

    revoked: bool
    key_id: str


__all__ = [
    "KeyScope",
    "scope_rank",
    "ChildClient",
    "ChildStatus",
    "AgentProfile",
    "ClientTenant",
    "ImpersonatedKey",
    "RevokeResult",
]
