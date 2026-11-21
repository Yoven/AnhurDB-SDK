package client

// client_key_test.go — wire and behaviour of ClientAPI against the fake in
// client_key_fake_test.go (shapes copied from the AnhurDB handlers).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testChildID = "9b1d2c3e-4f5a-4b6c-8d7e-0f1a2b3c4d5e"

// newTestClientAPI points a ClientAPI at the fake. WithTenantID is passed on
// purpose: the client-key surface must never send X-Tenant-ID.
func newTestClientAPI(fake *fakeClientServer) *ClientAPI {
	return NewClientAPI("client_key_secret", WithURL(fake.server.URL), WithTenantID("must-not-be-sent"))
}

func TestClientAPI_ImpersonateChildWire(t *testing.T) {
	fake := newFakeClientServer(t)
	api := newTestClientAPI(fake)
	impersonated, impersonateError := api.ImpersonateChild(context.Background(), testChildID, ImpersonateRequest{
		TenantID: "geral", Scope: KeyScopeWrite, L1Only: true, TTL: 900 * time.Second,
	})
	if impersonateError != nil {
		t.Fatalf("ImpersonateChild: %v", impersonateError)
	}
	requests := fake.recorded()
	if len(requests) != 1 {
		t.Fatalf("want 1 request, got %d", len(requests))
	}
	sentRequest := requests[0]
	if sentRequest.method != http.MethodPost || sentRequest.path != "/api/v1/client/children/"+testChildID+"/impersonate" {
		t.Errorf("wrong route: %s %s", sentRequest.method, sentRequest.path)
	}
	if sentRequest.apiKey != "client_key_secret" || sentRequest.hasTenantHeader {
		t.Errorf("headers: X-API-Key=%q, X-Tenant-ID present=%v", sentRequest.apiKey, sentRequest.hasTenantHeader)
	}
	wantBody := map[string]interface{}{"tenant_id": "geral", "scope": "write", "l1_only": true, "expires_in": 900.0, "ttl_seconds": 900.0}
	if !reflect.DeepEqual(sentRequest.body, wantBody) {
		t.Errorf("body = %v, want %v", sentRequest.body, wantBody)
	}
	if impersonated.APIKey != "anhur_secret" || impersonated.KeyID != "ik_42" || impersonated.Scope != KeyScopeWrite ||
		!impersonated.L1Only || impersonated.ExpiresIn != 900 || impersonated.ClientID != testChildID ||
		!impersonated.ExpiresAt.Equal(time.Date(2026, 11, 21, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("decoded token = %+v", impersonated)
	}
}

func TestClientAPI_PlainImpersonateOmitsOptionalFields(t *testing.T) {
	fake := newFakeClientServer(t)
	impersonated, impersonateError := newTestClientAPI(fake).Impersonate(context.Background(), ImpersonateRequest{TenantID: "it-process"})
	if impersonateError != nil {
		t.Fatalf("Impersonate: %v", impersonateError)
	}
	sentRequest := fake.recorded()[0]
	if sentRequest.path != "/api/v1/client/impersonate" || sentRequest.rawBody != `{"tenant_id":"it-process"}` {
		t.Errorf("plain impersonate wire: %s %s", sentRequest.path, sentRequest.rawBody)
	}
	if impersonated.Scope != KeyScopeAdmin || impersonated.L1Only {
		t.Errorf("echo must be the ceiling scope and l1_only=false, got %+v", impersonated)
	}
}

func TestClientAPI_LocalRefusalsSendNothing(t *testing.T) {
	testCases := []struct {
		name string
		call func(api *ClientAPI) error
	}{
		{"empty external ref", func(api *ClientAPI) error {
			_, callError := api.CreateChild(context.Background(), "  ")
			return callError
		}},
		{"non canonical child id", func(api *ClientAPI) error {
			_, callError := api.SuspendChild(context.Background(), "../tenants")
			return callError
		}},
		{"empty child id", func(api *ClientAPI) error {
			_, callError := api.ListChildTenants(context.Background(), "")
			return callError
		}},
		{"empty tenant", func(api *ClientAPI) error {
			_, callError := api.CreateTenant(context.Background(), CreateTenantRequest{TenantID: " "})
			return callError
		}},
		{"wildcard tenant", func(api *ClientAPI) error {
			_, callError := api.Impersonate(context.Background(), ImpersonateRequest{TenantID: "*"})
			return callError
		}},
		{"unknown scope", func(api *ClientAPI) error {
			_, callError := api.Impersonate(context.Background(), ImpersonateRequest{TenantID: "geral", Scope: "root"})
			return callError
		}},
		{"child without scope", func(api *ClientAPI) error {
			_, callError := api.ImpersonateChild(context.Background(), testChildID, ImpersonateRequest{TenantID: "geral"})
			return callError
		}},
		{"fractional ttl", func(api *ClientAPI) error {
			_, callError := api.Impersonate(context.Background(), ImpersonateRequest{TenantID: "geral", TTL: 1500 * time.Millisecond})
			return callError
		}},
		{"max_rpm below one", func(api *ClientAPI) error {
			zero := 0
			_, callError := api.CreateTenant(context.Background(), CreateTenantRequest{TenantID: "geral", Profile: &AgentProfile{MaxRPM: &zero}})
			return callError
		}},
		{"empty key id", func(api *ClientAPI) error {
			_, callError := api.RevokeImpersonation(context.Background(), "")
			return callError
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeClientServer(t)
			callError := testCase.call(newTestClientAPI(fake))
			var validationError *APIError
			if !errors.As(callError, &validationError) || validationError.StatusCode != 400 || validationError.Retryable() {
				t.Fatalf("want client-side 400 *APIError, got %v", callError)
			}
			if requestCount := len(fake.recorded()); requestCount != 0 {
				t.Errorf("want 0 requests, got %d", requestCount)
			}
		})
	}
}

func TestClientAPI_FailsClosedOnWeakEcho(t *testing.T) {
	testCases := []struct {
		name      string
		request   ImpersonateRequest
		override  map[string]interface{}
		wantError error
	}{
		{"l1_only absent", ImpersonateRequest{TenantID: "geral", Scope: KeyScopeWrite, L1Only: true},
			map[string]interface{}{"scope": "write"}, ErrL1OnlyNotHonoured},
		{"l1_only false", ImpersonateRequest{TenantID: "geral", Scope: KeyScopeWrite, L1Only: true},
			map[string]interface{}{"scope": "write", "l1_only": false}, ErrL1OnlyNotHonoured},
		{"scope higher", ImpersonateRequest{TenantID: "geral", Scope: KeyScopeReadOnly},
			map[string]interface{}{"scope": "admin", "l1_only": false}, ErrScopeNotHonoured},
		{"scope absent", ImpersonateRequest{TenantID: "geral", Scope: KeyScopeWrite},
			map[string]interface{}{"l1_only": false}, ErrScopeNotHonoured},
		{"scope lower is accepted", ImpersonateRequest{TenantID: "geral", Scope: KeyScopeAdmin},
			map[string]interface{}{"scope": "write", "l1_only": false}, nil},
		{"no api_key", ImpersonateRequest{TenantID: "geral", Scope: KeyScopeWrite},
			map[string]interface{}{"scope": "write", "api_key": ""}, ErrUnexpectedResponse},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeClientServer(t)
			fake.impersonateOverride = func(map[string]interface{}) map[string]interface{} {
				responseBody := map[string]interface{}{"api_key": "anhur_secret", "key_id": "ik_42", "tenant_id": "geral",
					"kind": "impersonation", "expires_in": 900, "expires_at": "2026-11-21T13:00:00Z"}
				for fieldName, fieldValue := range testCase.override {
					responseBody[fieldName] = fieldValue
				}
				return responseBody
			}
			impersonated, impersonateError := newTestClientAPI(fake).ImpersonateChild(context.Background(), testChildID, testCase.request)
			if testCase.wantError == nil {
				if impersonateError != nil || impersonated.Scope != KeyScopeWrite {
					t.Fatalf("want accepted lower scope, got %+v / %v", impersonated, impersonateError)
				}
				return
			}
			var responseError *ClientAPIResponseError
			if !errors.Is(impersonateError, testCase.wantError) || !errors.As(impersonateError, &responseError) || impersonated != nil {
				t.Fatalf("want %v, got %v", testCase.wantError, impersonateError)
			}
			if testCase.wantError != ErrUnexpectedResponse && responseError.KeyID != "ik_42" {
				t.Errorf("KeyID must name the minted token for revocation, got %q", responseError.KeyID)
			}
		})
	}
}

func TestClientAPI_CreateChildIdempotent(t *testing.T) {
	fake := newFakeClientServer(t)
	api := newTestClientAPI(fake)
	firstChild, firstError := api.CreateChild(context.Background(), "3f2c9a1e-ref")
	secondChild, secondError := api.CreateChild(context.Background(), "3f2c9a1e-ref")
	if firstError != nil || secondError != nil {
		t.Fatalf("CreateChild: %v / %v", firstError, secondError)
	}
	if !firstChild.Created || secondChild.Created || firstChild.ChildClientID != secondChild.ChildClientID {
		t.Errorf("want created true then false with the same id, got %+v then %+v", firstChild, secondChild)
	}
	if fake.recorded()[0].rawBody != `{"external_ref":"3f2c9a1e-ref"}` {
		t.Errorf("create child body = %s", fake.recorded()[0].rawBody)
	}
	children, listError := api.ListChildren(context.Background())
	if listError != nil || len(children) != 1 || children[0].ChildClientID != firstChild.ChildClientID || children[0].ExternalRef != "3f2c9a1e-ref" {
		t.Errorf("ListChildren = %+v / %v", children, listError)
	}
}

func TestClientAPI_CreateChildTenantSendsProfile(t *testing.T) {
	fake := newFakeClientServer(t)
	api := newTestClientAPI(fake)
	requestedMaxRPM := 5000
	tenant, createError := api.CreateChildTenant(context.Background(), testChildID, CreateTenantRequest{
		TenantID: "contabilidade", Description: "agent",
		Profile: &AgentProfile{Config: map[string]string{"search_top_k": "8"}, MaxRPM: &requestedMaxRPM},
	})
	if createError != nil {
		t.Fatalf("CreateChildTenant: %v", createError)
	}
	wantBody := map[string]interface{}{"tenant_id": "contabilidade", "description": "agent",
		"profile": map[string]interface{}{"config": map[string]interface{}{"search_top_k": "8"}, "max_rpm": 5000.0}}
	if sentBody := fake.recorded()[0].body; !reflect.DeepEqual(sentBody, wantBody) {
		t.Errorf("body = %v, want %v", sentBody, wantBody)
	}
	if tenant.MaxRPM == nil || *tenant.MaxRPM != 1200 || tenant.ClientID != testChildID || tenant.CompositeTenant != testChildID+"_contabilidade" {
		t.Errorf("decoded tenant = %+v", tenant)
	}

	emptyProfileTenant, emptyError := api.CreateTenant(context.Background(), CreateTenantRequest{TenantID: "geral", Profile: &AgentProfile{}})
	if emptyError != nil || emptyProfileTenant.MaxRPM != nil {
		t.Fatalf("empty profile: %+v / %v", emptyProfileTenant, emptyError)
	}
	if sentRaw := fake.recorded()[1].rawBody; sentRaw != `{"tenant_id":"geral","profile":{}}` {
		t.Errorf("empty profile must be sent as {}, got %s", sentRaw)
	}
	if _, noProfileError := api.CreateTenant(context.Background(), CreateTenantRequest{TenantID: "geral"}); noProfileError != nil {
		t.Fatal(noProfileError)
	}
	if sentRaw := fake.recorded()[2].rawBody; sentRaw != `{"tenant_id":"geral"}` {
		t.Errorf("nil profile must omit the key, got %s", sentRaw)
	}
}

func TestClientAPI_SuspendResumePaths(t *testing.T) {
	fake := newFakeClientServer(t)
	api := newTestClientAPI(fake)
	child, createError := api.CreateChild(context.Background(), "ref")
	if createError != nil {
		t.Fatal(createError)
	}
	suspended, suspendError := api.SuspendChild(context.Background(), child.ChildClientID)
	resumed, resumeError := api.ResumeChild(context.Background(), child.ChildClientID)
	if suspendError != nil || resumeError != nil || suspended.Status != "suspended" || resumed.Status != "active" {
		t.Fatalf("suspend/resume: %+v %v / %+v %v", suspended, suspendError, resumed, resumeError)
	}
	requests := fake.recorded()
	if requests[1].path != "/api/v1/client/children/"+child.ChildClientID+"/suspend" ||
		requests[2].path != "/api/v1/client/children/"+child.ChildClientID+"/resume" {
		t.Errorf("paths: %s, %s", requests[1].path, requests[2].path)
	}
	childTenants, listError := api.ListChildTenants(context.Background(), child.ChildClientID)
	if listError != nil || !reflect.DeepEqual(childTenants, []string{"contabilidade", "geral"}) {
		t.Errorf("ListChildTenants = %v / %v", childTenants, listError)
	}
}

func TestClientAPI_PlainListAndRevokeWire(t *testing.T) {
	fake := newFakeClientServer(t)
	api := newTestClientAPI(fake)
	tenantIDs, listError := api.ListTenants(context.Background())
	revokeResult, revokeError := api.RevokeImpersonation(context.Background(), "ik_42")
	if listError != nil || revokeError != nil || len(tenantIDs) != 2 || !revokeResult.Revoked || revokeResult.KeyID != "ik_42" {
		t.Fatalf("list/revoke: %v %v / %+v %v", tenantIDs, listError, revokeResult, revokeError)
	}
	requests := fake.recorded()
	if requests[0].method != http.MethodGet || requests[0].path != "/api/v1/client/tenants" {
		t.Errorf("list route: %s %s", requests[0].method, requests[0].path)
	}
	if requests[1].path != "/api/v1/client/impersonate/revoke" || requests[1].rawBody != `{"key_id":"ik_42"}` {
		t.Errorf("revoke wire: %s %s", requests[1].path, requests[1].rawBody)
	}
	_, unknownError := api.RevokeImpersonation(context.Background(), "ik_missing")
	var apiError *ClientAPIError
	if !errors.As(unknownError, &apiError) || apiError.Code != "key_not_found" || !errors.Is(unknownError, ErrNotFound) {
		t.Errorf("unknown key: %v", unknownError)
	}
}

func TestClientAPI_ImpersonatedKeyStringHidesSecret(t *testing.T) {
	token := &ImpersonatedKey{APIKey: "anhur_secret", KeyID: "ik_42", Scope: KeyScopeWrite}
	for _, rendered := range []string{fmt.Sprintf("%v", token), fmt.Sprintf("%+v", token), fmt.Sprintf("%s", *token)} {
		if strings.Contains(rendered, "anhur_secret") || !strings.Contains(rendered, "ik_42") {
			t.Errorf("rendering leaks or loses data: %s", rendered)
		}
	}
}
