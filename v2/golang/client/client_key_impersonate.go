package client

// client_key_impersonate.go — tenant creation and short-lived tokens on the
// client-key surface. Server truth: server/handler/client_tenants.go,
// client_tenant_profile.go, client_impersonate.go, client_keys.go.
//
//	POST /api/v1/client/tenants                            CreateTenant
//	POST /api/v1/client/children/{child_id}/tenants        CreateChildTenant
//	POST /api/v1/client/impersonate                        Impersonate
//	POST /api/v1/client/children/{child_id}/impersonate    ImpersonateChild
//	POST /api/v1/client/impersonate/revoke                 RevokeImpersonation

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// CreateTenant creates (or confirms) a tenant owned by the caller's client
// and (re)applies its optional profile. Created=false means it already
// existed; the profile was re-applied anyway (idempotent upsert).
func (api *ClientAPI) CreateTenant(ctx context.Context, request CreateTenantRequest) (*ClientTenant, error) {
	return api.createTenant(ctx, "/api/v1/client/tenants", request)
}

// CreateChildTenant creates (or confirms) a tenant owned by the CHILD.
// A suspended child answers *ClientAPIError 403 child_suspended.
func (api *ClientAPI) CreateChildTenant(ctx context.Context, childID string, request CreateTenantRequest) (*ClientTenant, error) {
	if validationError := requireCanonicalChildID(childID); validationError != nil {
		return nil, validationError
	}
	return api.createTenant(ctx, clientChildrenPath+"/"+childID+"/tenants", request)
}

// createTenant validates locally, posts and decodes the tenant answer.
func (api *ClientAPI) createTenant(ctx context.Context, requestPath string, request CreateTenantRequest) (*ClientTenant, error) {
	if validationError := requireTenantID(request.TenantID); validationError != nil {
		return nil, validationError
	}
	if request.Profile != nil && request.Profile.MaxRPM != nil && *request.Profile.MaxRPM < 1 {
		return nil, newValidationError("INVALID_PARAM: client API: profile.max_rpm must be >= 1")
	}
	responseBody, sendError := api.send(ctx, http.MethodPost, requestPath, request)
	if sendError != nil {
		return nil, sendError
	}
	tenant := &ClientTenant{}
	if decodeError := decodeObject(http.MethodPost, requestPath, responseBody, tenant); decodeError != nil {
		return nil, decodeError
	}
	if tenant.TenantID == "" || tenant.CompositeTenant == "" {
		return nil, unexpectedClientResponse(http.MethodPost, requestPath, missingField("composite_tenant"))
	}
	return tenant, nil
}

// Impersonate mints a token for one of the caller's own tenants. An empty
// Scope means the client key's ceiling.
func (api *ClientAPI) Impersonate(ctx context.Context, request ImpersonateRequest) (*ImpersonatedKey, error) {
	if validationError := requireScope(request.Scope, true); validationError != nil {
		return nil, validationError
	}
	return api.impersonate(ctx, "/api/v1/client/impersonate", request)
}

// ImpersonateChild mints a token for one of a child's tenants. Scope is
// mandatory; a suspended child answers 403 child_suspended.
func (api *ClientAPI) ImpersonateChild(ctx context.Context, childID string, request ImpersonateRequest) (*ImpersonatedKey, error) {
	if validationError := requireCanonicalChildID(childID); validationError != nil {
		return nil, validationError
	}
	if validationError := requireScope(request.Scope, false); validationError != nil {
		return nil, validationError
	}
	return api.impersonate(ctx, clientChildrenPath+"/"+childID+"/impersonate", request)
}

// impersonateWireRequest is the body impersonateRequest decodes server-side.
// expires_in AND ttl_seconds carry the same value (the server reads
// expires_in first); l1_only is sent only when true.
type impersonateWireRequest struct {
	TenantID    string `json:"tenant_id"`
	Scope       string `json:"scope,omitempty"`
	L1Only      bool   `json:"l1_only,omitempty"`
	Description string `json:"description,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	TTLSeconds  int    `json:"ttl_seconds,omitempty"`
}

// impersonateWireResponse uses pointers so an ABSENT echo is distinguishable
// from a false/empty one.
type impersonateWireResponse struct {
	APIKey          string  `json:"api_key"`
	KeyID           string  `json:"key_id"`
	ClientID        string  `json:"client_id"`
	TenantID        string  `json:"tenant_id"`
	CompositeTenant string  `json:"composite_tenant"`
	Scope           *string `json:"scope"`
	L1Only          *bool   `json:"l1_only"`
	Kind            string  `json:"kind"`
	ExpiresIn       int     `json:"expires_in"`
	ExpiresAt       string  `json:"expires_at"`
}

// impersonate validates the shared fields, posts, and checks the echo.
//
// Junior Tip [fail closed on the echo]: a token is a capability. If the
// caller asked for l1_only or a scope and the answer does not prove it was
// honoured, handing the token over would let an agent act with MORE power
// than requested — silently. The SDK refuses instead and returns the key_id
// so the caller can revoke it. A LOWER scope than requested is accepted: the
// server lowers to the client key's ceiling by contract.
func (api *ClientAPI) impersonate(ctx context.Context, requestPath string, request ImpersonateRequest) (*ImpersonatedKey, error) {
	if validationError := requireTenantID(request.TenantID); validationError != nil {
		return nil, validationError
	}
	if request.TTL < 0 || request.TTL%time.Second != 0 {
		return nil, newValidationError("INVALID_PARAM: client API: ttl must be a whole number of seconds >= 1")
	}
	ttlSeconds := int(request.TTL / time.Second)
	wireRequest := impersonateWireRequest{
		TenantID:    request.TenantID,
		Scope:       string(request.Scope),
		L1Only:      request.L1Only,
		Description: request.Description,
		ExpiresIn:   ttlSeconds,
		TTLSeconds:  ttlSeconds,
	}
	responseBody, sendError := api.send(ctx, http.MethodPost, requestPath, wireRequest)
	if sendError != nil {
		return nil, sendError
	}
	var wireResponse impersonateWireResponse
	if decodeError := decodeObject(http.MethodPost, requestPath, responseBody, &wireResponse); decodeError != nil {
		return nil, decodeError
	}
	if wireResponse.APIKey == "" || wireResponse.KeyID == "" {
		return nil, unexpectedClientResponse(http.MethodPost, requestPath, missingField("api_key"))
	}
	expiresAt, parseError := time.Parse(time.RFC3339, wireResponse.ExpiresAt)
	if parseError != nil {
		return nil, unexpectedClientResponse(http.MethodPost, requestPath, missingField("expires_at"))
	}
	echoedL1Only := wireResponse.L1Only != nil && *wireResponse.L1Only
	echoedScope := ""
	if wireResponse.Scope != nil {
		echoedScope = *wireResponse.Scope
	}
	if honourError := checkImpersonationEcho(requestPath, request, echoedL1Only, echoedScope, wireResponse.KeyID); honourError != nil {
		return nil, honourError
	}
	return &ImpersonatedKey{
		APIKey:          wireResponse.APIKey,
		KeyID:           wireResponse.KeyID,
		ClientID:        wireResponse.ClientID,
		TenantID:        wireResponse.TenantID,
		CompositeTenant: wireResponse.CompositeTenant,
		Kind:            wireResponse.Kind,
		Scope:           KeyScope(echoedScope),
		L1Only:          echoedL1Only,
		ExpiresIn:       wireResponse.ExpiresIn,
		ExpiresAt:       expiresAt,
	}, nil
}

// checkImpersonationEcho refuses an echo weaker than the request.
func checkImpersonationEcho(requestPath string, request ImpersonateRequest, echoedL1Only bool, echoedScope, keyID string) error {
	if request.L1Only && !echoedL1Only {
		return &ClientAPIResponseError{
			Method: http.MethodPost, Path: requestPath, Code: ResponseCodeL1OnlyNotHonoured, KeyID: keyID,
			Reason: "l1_only was requested but the response echoes l1_only=false (key_id=" + keyID + ")",
		}
	}
	if request.Scope != "" {
		echoedRank := scopeRank(KeyScope(echoedScope))
		if echoedRank == 0 || echoedRank > scopeRank(request.Scope) {
			return &ClientAPIResponseError{
				Method: http.MethodPost, Path: requestPath, Code: ResponseCodeScopeNotHonoured, KeyID: keyID,
				Reason: "scope \"" + string(request.Scope) + "\" was requested but the response echoes \"" + echoedScope + "\" (key_id=" + keyID + ")",
			}
		}
	}
	return nil
}

// RevokeImpersonation revokes a token minted by the caller's client or by one
// of its children. Errors: 404 key_not_found (unknown or not an impersonation
// token), 403 key_not_owned.
func (api *ClientAPI) RevokeImpersonation(ctx context.Context, keyID string) (*RevokeResult, error) {
	if strings.TrimSpace(keyID) == "" {
		return nil, newValidationError("INVALID_PARAM: client API: key_id is required")
	}
	const requestPath = "/api/v1/client/impersonate/revoke"
	responseBody, sendError := api.send(ctx, http.MethodPost, requestPath, map[string]string{"key_id": keyID})
	if sendError != nil {
		return nil, sendError
	}
	revokeResult := &RevokeResult{}
	if decodeError := decodeObject(http.MethodPost, requestPath, responseBody, revokeResult); decodeError != nil {
		return nil, decodeError
	}
	if !revokeResult.Revoked {
		return nil, unexpectedClientResponse(http.MethodPost, requestPath, missingField("revoked"))
	}
	return revokeResult, nil
}
