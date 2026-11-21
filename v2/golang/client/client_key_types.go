package client

// client_key_types.go — the request and response shapes of the client-key
// surface (/api/v1/client/*, ADR-0034). Every JSON tag below is the server's
// field name, read from the AnhurDB handlers (server/handler/client_*.go);
// the comment on each type names the handler that writes or reads it.

import "time"

// KeyScope is the RBAC scope an impersonation token is minted with.
// The server accepts exactly these three values (plus "" on the plain route,
// meaning "the client key's own ceiling"), validRequestedScope in
// server/handler/client_impersonate.go.
type KeyScope string

const (
	// KeyScopeAdmin is the widest scope.
	KeyScopeAdmin KeyScope = "admin"
	// KeyScopeWrite allows reads and writes, no admin routes.
	KeyScopeWrite KeyScope = "write"
	// KeyScopeReadOnly allows reads only.
	KeyScopeReadOnly KeyScope = "read_only"
)

// scopeRank orders scopes read_only < write < admin; 0 means "unknown".
//
// Junior Tip [why the SDK ranks scopes at all]: the server never refuses a
// scope above the caller's ceiling — it silently LOWERS it (ScopeAtMost). A
// scope ABOVE the one requested can only come from a server bug or something
// posing as the server, and handing that token to an agent would grant more
// than the caller asked for. Ranking lets the SDK accept "lower" and refuse
// "higher" without string matching.
func scopeRank(scope KeyScope) int {
	switch scope {
	case KeyScopeReadOnly:
		return 1
	case KeyScopeWrite:
		return 2
	case KeyScopeAdmin:
		return 3
	default:
		return 0
	}
}

// ChildClient is one child client of the calling client key
// (childSummary in server/handler/client_children.go).
type ChildClient struct {
	// ChildClientID is the lowercase canonical UUID the server minted.
	ChildClientID string `json:"child_client_id"`
	// ExternalRef is the caller's idempotency reference, trimmed by the server.
	ExternalRef string `json:"external_ref"`
	// Status is "active" or "suspended".
	Status string `json:"status"`
	// Created is true only on the call that created the child (HTTP 201);
	// always false in ListChildren.
	Created bool `json:"created"`
	// CreatedAt is the RFC3339 creation instant, as the server wrote it.
	CreatedAt string `json:"created_at"`
}

// ChildStatus is the answer of SuspendChild and ResumeChild
// (setClientChildStatus in server/handler/client_children.go).
type ChildStatus struct {
	ChildClientID string `json:"child_client_id"`
	// Status is the status re-read AFTER the change: "suspended" or "active".
	Status string `json:"status"`
}

// AgentProfile is the optional per-tenant configuration sent on tenant
// creation (normalizeAgentProfile in server/handler/client_tenant_profile.go).
//
// Config accepts only the 16 numeric keys of the ControlPlane tenant defaults
// and every value is a numeric STRING; the server refuses anything else with
// 400 invalid_profile. MaxRPM, when set, must be >= 1 and is clamped by the
// server to ANHUR_AGENT_PROFILE_MAX_RPM (default 1200) — read the effective
// value back from ClientTenant.MaxRPM.
type AgentProfile struct {
	Config map[string]string `json:"config,omitempty"`
	MaxRPM *int              `json:"max_rpm,omitempty"`
}

// CreateTenantRequest is the body of CreateTenant and CreateChildTenant.
type CreateTenantRequest struct {
	// TenantID is required; "shared" (any case) is refused by the server.
	TenantID string `json:"tenant_id"`
	// Description is free text stored on the ownership marker.
	Description string `json:"description,omitempty"`
	// Profile is optional; nil sends no "profile" key, an empty profile sends
	// "profile":{} (valid, applies nothing).
	Profile *AgentProfile `json:"profile,omitempty"`
}

// ClientTenant is the answer of CreateTenant and CreateChildTenant
// (tenantCreatedBody in server/handler/client_tenants.go). No credential is
// returned: use Impersonate/ImpersonateChild for a token.
type ClientTenant struct {
	// ClientID is the OWNER: the caller on the plain route, the child on the
	// child route.
	ClientID        string `json:"client_id"`
	TenantID        string `json:"tenant_id"`
	CompositeTenant string `json:"composite_tenant"`
	// Created is true on HTTP 201, false when the tenant already existed (the
	// profile is re-applied either way).
	Created bool `json:"created"`
	// MaxRPM is the EFFECTIVE (clamped) max_rpm; nil when the request sent none.
	MaxRPM *int `json:"max_rpm,omitempty"`
}

// ImpersonateRequest describes the short-lived token to mint.
type ImpersonateRequest struct {
	// TenantID is required; "", "*" and "shared" are refused.
	TenantID string
	// Scope is the requested scope. Empty is allowed on the plain route only
	// (it means the client key's ceiling); the child route requires one.
	Scope KeyScope
	// L1Only restricts the token to L1 (chat) planes; it is sent only when true.
	L1Only bool
	// Description is free text stored on the token row.
	Description string
	// TTL is the token lifetime, whole seconds. Zero lets the server default
	// (3600 s); the server caps every token at 24 h.
	TTL time.Duration
}

// ImpersonatedKey is a minted token (impersonateForClient in
// server/handler/client_impersonate.go).
//
// Callers MUST rely on Scope and L1Only as ECHOED here, not on what they
// requested: the server lowers a scope above the client key's ceiling. The
// SDK already refuses an echo that is weaker than the request (see
// ErrScopeNotHonoured, ErrL1OnlyNotHonoured).
type ImpersonatedKey struct {
	// APIKey is the secret token. Never log it.
	APIKey          string
	KeyID           string
	ClientID        string
	TenantID        string
	CompositeTenant string
	Kind            string
	Scope           KeyScope
	L1Only          bool
	ExpiresIn       int
	ExpiresAt       time.Time
}

// RevokeResult is the answer of RevokeImpersonation
// (RevokeImpersonation in server/handler/client_keys.go).
type RevokeResult struct {
	Revoked bool   `json:"revoked"`
	KeyID   string `json:"key_id"`
}
