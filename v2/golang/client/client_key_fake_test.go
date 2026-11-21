package client

// client_key_fake_test.go — an in-process fake of the client-key surface.
//
// Every response shape below is copied from the AnhurDB handlers (branch
// feat/phase1-client-children), not from the SDK's own types, so a test that
// passes proves the SDK reads what the SERVER writes:
//
//	POST /children, GET /children, suspend/resume  server/handler/client_children.go
//	GET  /tenants, GET /children/{id}/tenants       server/handler/client_keys.go (writeTenantList)
//	POST /tenants, POST /children/{id}/tenants      server/handler/client_tenants.go (tenantCreatedBody)
//	POST /impersonate, /children/{id}/impersonate   server/handler/client_impersonate.go
//	POST /impersonate/revoke                        server/handler/client_keys.go
//	error envelope {"error","detail"}               server/handler/response.go (writeErrorCode)

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordedClientRequest is one request the SDK put on the wire.
type recordedClientRequest struct {
	method          string
	path            string
	apiKey          string
	tenantID        string
	hasTenantHeader bool
	body            map[string]interface{}
	rawBody         string
}

// fakeClientServer is a stateful stand-in for the AnhurDB client-key routes.
type fakeClientServer struct {
	server       *httptest.Server
	stateMutex   sync.Mutex
	requests     []recordedClientRequest
	childByRef   map[string]map[string]interface{}
	childStatus  map[string]string
	nextChildNum int
	// impersonateOverride, when set, replaces the impersonate response body.
	impersonateOverride func(requestBody map[string]interface{}) map[string]interface{}
}

// newFakeClientServer starts the fake; it is closed by t.Cleanup.
func newFakeClientServer(t *testing.T) *fakeClientServer {
	t.Helper()
	fake := &fakeClientServer{childByRef: map[string]map[string]interface{}{}, childStatus: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/client/children", fake.createChild)
	mux.HandleFunc("GET /api/v1/client/children", fake.listChildren)
	mux.HandleFunc("POST /api/v1/client/children/{child_id}/suspend", fake.setStatus("suspended"))
	mux.HandleFunc("POST /api/v1/client/children/{child_id}/resume", fake.setStatus("active"))
	mux.HandleFunc("GET /api/v1/client/children/{child_id}/tenants", fake.listTenants)
	mux.HandleFunc("GET /api/v1/client/tenants", fake.listTenants)
	mux.HandleFunc("POST /api/v1/client/children/{child_id}/tenants", fake.createTenant)
	mux.HandleFunc("POST /api/v1/client/tenants", fake.createTenant)
	mux.HandleFunc("POST /api/v1/client/children/{child_id}/impersonate", fake.impersonate)
	mux.HandleFunc("POST /api/v1/client/impersonate", fake.impersonate)
	mux.HandleFunc("POST /api/v1/client/impersonate/revoke", fake.revoke)
	fake.server = httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		fake.record(httpRequest)
		mux.ServeHTTP(responseWriter, httpRequest)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// record stores the request and restores its body for the handler.
func (fake *fakeClientServer) record(httpRequest *http.Request) {
	rawBody, _ := io.ReadAll(httpRequest.Body)
	httpRequest.Body = io.NopCloser(strings.NewReader(string(rawBody)))
	decodedBody := map[string]interface{}{}
	_ = json.Unmarshal(rawBody, &decodedBody)
	_, hasTenantHeader := httpRequest.Header["X-Tenant-Id"]
	fake.stateMutex.Lock()
	defer fake.stateMutex.Unlock()
	fake.requests = append(fake.requests, recordedClientRequest{
		method: httpRequest.Method, path: httpRequest.URL.Path, apiKey: httpRequest.Header.Get("X-API-Key"),
		tenantID: httpRequest.Header.Get("X-Tenant-ID"), hasTenantHeader: hasTenantHeader,
		body: decodedBody, rawBody: string(rawBody),
	})
}

// recorded returns a snapshot of the recorded requests.
func (fake *fakeClientServer) recorded() []recordedClientRequest {
	fake.stateMutex.Lock()
	defer fake.stateMutex.Unlock()
	return append([]recordedClientRequest(nil), fake.requests...)
}

// writeJSONBody writes a JSON answer like handler.writeJSON.
func writeJSONBody(responseWriter http.ResponseWriter, status int, body interface{}) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(status)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

// createChild mirrors CreateClientChild: 201 first, 200 created:false after.
func (fake *fakeClientServer) createChild(responseWriter http.ResponseWriter, httpRequest *http.Request) {
	var createRequest struct {
		ExternalRef string `json:"external_ref"`
	}
	if json.NewDecoder(httpRequest.Body).Decode(&createRequest) != nil {
		writeJSONBody(responseWriter, 400, map[string]string{"error": "invalid_json", "detail": "request body must be a JSON object"})
		return
	}
	fake.stateMutex.Lock()
	defer fake.stateMutex.Unlock()
	if existingChild, found := fake.childByRef[createRequest.ExternalRef]; found {
		summary := copySummary(existingChild)
		summary["status"] = fake.childStatus[summary["child_client_id"].(string)]
		summary["created"] = false
		writeJSONBody(responseWriter, 200, summary)
		return
	}
	fake.nextChildNum++
	childID := fmt.Sprintf("9b1d2c3e-4f5a-4b6c-8d7e-%012d", fake.nextChildNum)
	summary := map[string]interface{}{"child_client_id": childID, "external_ref": createRequest.ExternalRef,
		"status": "active", "created_at": "2026-11-21T12:00:00Z"}
	fake.childByRef[createRequest.ExternalRef] = summary
	fake.childStatus[childID] = "active"
	created := copySummary(summary)
	created["created"] = true
	writeJSONBody(responseWriter, 201, created)
}

// copySummary clones a child summary map.
func copySummary(summary map[string]interface{}) map[string]interface{} {
	clone := map[string]interface{}{}
	for fieldName, fieldValue := range summary {
		clone[fieldName] = fieldValue
	}
	return clone
}

// listChildren mirrors ListClientChildren.
func (fake *fakeClientServer) listChildren(responseWriter http.ResponseWriter, _ *http.Request) {
	fake.stateMutex.Lock()
	defer fake.stateMutex.Unlock()
	summaries := make([]map[string]interface{}, 0, len(fake.childByRef))
	for _, summary := range fake.childByRef {
		summaries = append(summaries, copySummary(summary))
	}
	writeJSONBody(responseWriter, 200, map[string]interface{}{"children": summaries})
}

// setStatus mirrors setClientChildStatus (404 child_not_found for unknown ids).
func (fake *fakeClientServer) setStatus(status string) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		childID := httpRequest.PathValue("child_id")
		fake.stateMutex.Lock()
		defer fake.stateMutex.Unlock()
		if _, found := fake.childStatus[childID]; !found {
			writeJSONBody(responseWriter, 404, map[string]string{"error": "child_not_found", "detail": "no child client with this id for the caller"})
			return
		}
		fake.childStatus[childID] = status
		writeJSONBody(responseWriter, 200, map[string]interface{}{"child_client_id": childID, "status": status})
	}
}

// listTenants mirrors writeTenantList.
func (fake *fakeClientServer) listTenants(responseWriter http.ResponseWriter, _ *http.Request) {
	writeJSONBody(responseWriter, 200, map[string]interface{}{"tenants": []map[string]string{{"tenant_id": "contabilidade"}, {"tenant_id": "geral"}}})
}

// createTenant mirrors tenantCreatedBody; max_rpm echoes clamped to 1200.
func (fake *fakeClientServer) createTenant(responseWriter http.ResponseWriter, httpRequest *http.Request) {
	var createRequest struct {
		TenantID string `json:"tenant_id"`
		Profile  struct {
			MaxRPM *int `json:"max_rpm"`
		} `json:"profile"`
	}
	_ = json.NewDecoder(httpRequest.Body).Decode(&createRequest)
	ownerClientID := httpRequest.PathValue("child_id")
	if ownerClientID == "" {
		ownerClientID = "parent-client"
	}
	responseBody := map[string]interface{}{"client_id": ownerClientID, "tenant_id": createRequest.TenantID,
		"composite_tenant": ownerClientID + "_" + createRequest.TenantID, "created": true}
	if createRequest.Profile.MaxRPM != nil {
		responseBody["max_rpm"] = min(*createRequest.Profile.MaxRPM, 1200)
	}
	writeJSONBody(responseWriter, 201, responseBody)
}

// impersonate mirrors impersonateForClient's 201 body.
func (fake *fakeClientServer) impersonate(responseWriter http.ResponseWriter, httpRequest *http.Request) {
	requestBody := map[string]interface{}{}
	_ = json.NewDecoder(httpRequest.Body).Decode(&requestBody)
	ownerClientID := httpRequest.PathValue("child_id")
	if ownerClientID == "" {
		ownerClientID = "parent-client"
	}
	if fake.impersonateOverride != nil {
		writeJSONBody(responseWriter, 201, fake.impersonateOverride(requestBody))
		return
	}
	scope, _ := requestBody["scope"].(string)
	if scope == "" {
		scope = "admin"
	}
	l1Only, _ := requestBody["l1_only"].(bool)
	tenantID, _ := requestBody["tenant_id"].(string)
	writeJSONBody(responseWriter, 201, map[string]interface{}{
		"api_key": "anhur_secret", "key_id": "ik_42", "client_id": ownerClientID, "tenant_id": tenantID,
		"composite_tenant": ownerClientID + "_" + tenantID, "scope": scope, "l1_only": l1Only,
		"kind": "impersonation", "expires_in": 900, "expires_at": "2026-11-21T13:00:00Z",
	})
}

// revoke mirrors RevokeImpersonation.
func (fake *fakeClientServer) revoke(responseWriter http.ResponseWriter, httpRequest *http.Request) {
	var revokeRequest struct {
		KeyID string `json:"key_id"`
	}
	_ = json.NewDecoder(httpRequest.Body).Decode(&revokeRequest)
	if revokeRequest.KeyID != "ik_42" {
		writeJSONBody(responseWriter, 404, map[string]string{"error": "key_not_found", "detail": "no impersonation key with this key_id"})
		return
	}
	writeJSONBody(responseWriter, 200, map[string]interface{}{"revoked": true, "key_id": revokeRequest.KeyID})
}
