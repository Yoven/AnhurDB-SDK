package client

// client_key_children.go — child clients and tenant listing on the client-key
// surface (ADR-0034). Server truth: server/handler/client_children.go and
// server/handler/client_keys.go (writeTenantList), routes in
// server/router_client.go.
//
//	POST /api/v1/client/children                     CreateChild
//	GET  /api/v1/client/children                     ListChildren
//	POST /api/v1/client/children/{child_id}/suspend  SuspendChild
//	POST /api/v1/client/children/{child_id}/resume   ResumeChild
//	GET  /api/v1/client/children/{child_id}/tenants  ListChildTenants
//	GET  /api/v1/client/tenants                      ListTenants

import (
	"context"
	"net/http"
	"strings"
)

const clientChildrenPath = "/api/v1/client/children"

// CreateChild creates the caller's child for externalRef, or confirms the
// existing one. Idempotent per (caller, externalRef): the first call answers
// Created=true (HTTP 201), every later one Created=false (HTTP 200) with the
// SAME ChildClientID. A new ref beyond the cap is *ClientAPIError 429
// child_limit (not retryable).
//
// Junior Tip [externalRef is the idempotency key — never a display name]: a
// retry after a timeout must land on the same child. Use a stable opaque id
// minted once per account (e.g. a random UUID stored with the account), never
// a slug that a future account could reuse.
func (api *ClientAPI) CreateChild(ctx context.Context, externalRef string) (*ChildClient, error) {
	if strings.TrimSpace(externalRef) == "" {
		return nil, newValidationError("INVALID_PARAM: client API: external_ref is required")
	}
	responseBody, sendError := api.send(ctx, http.MethodPost, clientChildrenPath, map[string]string{"external_ref": externalRef})
	if sendError != nil {
		return nil, sendError
	}
	child := &ChildClient{}
	if decodeError := decodeObject(http.MethodPost, clientChildrenPath, responseBody, child); decodeError != nil {
		return nil, decodeError
	}
	if !isCanonicalUUID(child.ChildClientID) {
		return nil, unexpectedClientResponse(http.MethodPost, clientChildrenPath, missingField("child_client_id"))
	}
	return child, nil
}

// ListChildren returns the caller's children ordered by (created_at, id).
// A child created moments ago may be missing when the read lands on a
// follower that has not applied it yet.
func (api *ClientAPI) ListChildren(ctx context.Context) ([]ChildClient, error) {
	responseBody, sendError := api.send(ctx, http.MethodGet, clientChildrenPath, nil)
	if sendError != nil {
		return nil, sendError
	}
	var envelope struct {
		Children *[]ChildClient `json:"children"`
	}
	if decodeError := decodeObject(http.MethodGet, clientChildrenPath, responseBody, &envelope); decodeError != nil {
		return nil, decodeError
	}
	if envelope.Children == nil {
		return nil, unexpectedClientResponse(http.MethodGet, clientChildrenPath, missingField("children"))
	}
	return *envelope.Children, nil
}

// SuspendChild sets the child's status to "suspended" (absolute, idempotent).
// Every token of the child is refused with 403 child_suspended within the
// server's 5 s cache window; nothing is revoked.
func (api *ClientAPI) SuspendChild(ctx context.Context, childID string) (*ChildStatus, error) {
	return api.setChildStatus(ctx, childID, "suspend")
}

// ResumeChild sets the child's status to "active"; the SAME tokens work again.
func (api *ClientAPI) ResumeChild(ctx context.Context, childID string) (*ChildStatus, error) {
	return api.setChildStatus(ctx, childID, "resume")
}

// setChildStatus posts to /suspend or /resume and decodes the re-read status.
func (api *ClientAPI) setChildStatus(ctx context.Context, childID, action string) (*ChildStatus, error) {
	if validationError := requireCanonicalChildID(childID); validationError != nil {
		return nil, validationError
	}
	requestPath := clientChildrenPath + "/" + childID + "/" + action
	responseBody, sendError := api.send(ctx, http.MethodPost, requestPath, nil)
	if sendError != nil {
		return nil, sendError
	}
	childStatus := &ChildStatus{}
	if decodeError := decodeObject(http.MethodPost, requestPath, responseBody, childStatus); decodeError != nil {
		return nil, decodeError
	}
	if childStatus.ChildClientID == "" || childStatus.Status == "" {
		return nil, unexpectedClientResponse(http.MethodPost, requestPath, missingField("status"))
	}
	return childStatus, nil
}

// ListTenants returns the tenant ids the caller's own client owns. The
// reserved L3 name "shared" is never listed.
func (api *ClientAPI) ListTenants(ctx context.Context) ([]string, error) {
	return api.listTenantIDs(ctx, "/api/v1/client/tenants")
}

// ListChildTenants returns the tenant ids a child owns.
func (api *ClientAPI) ListChildTenants(ctx context.Context, childID string) ([]string, error) {
	if validationError := requireCanonicalChildID(childID); validationError != nil {
		return nil, validationError
	}
	return api.listTenantIDs(ctx, clientChildrenPath+"/"+childID+"/tenants")
}

// listTenantIDs decodes {"tenants":[{"tenant_id":…}]}.
func (api *ClientAPI) listTenantIDs(ctx context.Context, requestPath string) ([]string, error) {
	responseBody, sendError := api.send(ctx, http.MethodGet, requestPath, nil)
	if sendError != nil {
		return nil, sendError
	}
	var envelope struct {
		Tenants *[]struct {
			TenantID string `json:"tenant_id"`
		} `json:"tenants"`
	}
	if decodeError := decodeObject(http.MethodGet, requestPath, responseBody, &envelope); decodeError != nil {
		return nil, decodeError
	}
	if envelope.Tenants == nil {
		return nil, unexpectedClientResponse(http.MethodGet, requestPath, missingField("tenants"))
	}
	tenantIDs := make([]string, 0, len(*envelope.Tenants))
	for _, tenantEntry := range *envelope.Tenants {
		tenantIDs = append(tenantIDs, tenantEntry.TenantID)
	}
	return tenantIDs, nil
}
