package client

// tenant_header_test.go — an invalid X-Tenant-ID is refused with the SDK's typed
// validation error, and no request reaches the server.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestValidateTenantIDHeaderRule pins the server's identifier rule and the
// cross-SDK messages (Python and TypeScript assert the same strings).
func TestValidateTenantIDHeaderRule(t *testing.T) {
	const charsetMessage = "INVALID_PARAM: tenant_id contains an invalid character — allowed: letters, digits, '-' and '_'"
	testCases := []struct {
		name            string
		tenantID        string
		expectedMessage string
	}{
		{"empty means not configured", "", ""},
		{"uuid composite", "1a99ce02-e9b6-442a-8c10-d67b9f3446b8_prod", ""},
		{"exactly 133", strings.Repeat("a", 133), ""},
		{"incident library store (78)", "1a99ce02-e9b6-442a-8c10-d67b9f3446b8_kaveo-veterinaria-vacinacao-4d7d5ed7_lib", ""},
		{"slash would be stripped server-side", "acme/prod", charsetMessage},
		{"space", "acme prod", charsetMessage},
		{"crlf injection", "bad\r\nInjected: true", charsetMessage},
		{"null byte", "bad\x00id", charsetMessage},
		{"non ascii", "açme", charsetMessage},
		{"134 chars", strings.Repeat("a", 134), "INVALID_PARAM: tenant_id exceeds 133 characters (got 134)"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			validationError := validateTenantIDHeader(testCase.tenantID)
			if testCase.expectedMessage == "" {
				if validationError != nil {
					subTest.Fatalf("expected valid, got %v", validationError)
				}
				return
			}
			var apiError *APIError
			if !errors.As(validationError, &apiError) {
				subTest.Fatalf("expected *APIError, got %T %v", validationError, validationError)
			}
			if apiError.Error() != testCase.expectedMessage {
				subTest.Errorf("message mismatch:\n got  %q\n want %q", apiError.Error(), testCase.expectedMessage)
			}
			if apiError.Kind() != KindInvalidRequest {
				subTest.Errorf("expected invalid_request, got %s", apiError.Kind())
			}
		})
	}
}

// TestInvalidTenantIDNeverReachesServer proves the old silent drop is gone: the
// call fails typed and the server sees ZERO requests (a dropped header used to
// land the call on the master key's "default" tenant).
func TestInvalidTenantIDNeverReachesServer(t *testing.T) {
	var requestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requestCount.Add(1)
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"session_uuid":"s","ingest_key_prefix":"p","matched_count":0,"deleted_count":0,"dry_run":true,"purge":false}`))
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL), WithTenantID("acme/prod"))
	_, deleteError := mem.DeleteFile(context.Background(), "chat-42", "ef9976f1ef5d5176", WithDeleteFileDryRun(true))
	var apiError *APIError
	if !errors.As(deleteError, &apiError) || apiError.Kind() != KindInvalidRequest {
		t.Fatalf("expected invalid_request *APIError, got %T %v", deleteError, deleteError)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("invalid tenant must not reach the server, got %d requests", requestCount.Load())
	}

	// Same refusal for a value set directly on the exported field.
	connection := NewConnection(server.URL, "k", 0)
	connection.TenantID = "bad\r\nInjected: true"
	if _, getError := connection.Get(context.Background(), "/api/v1/health", nil); !errors.As(getError, &apiError) {
		t.Fatalf("expected *APIError from Get, got %T %v", getError, getError)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("invalid tenant must not reach the server, got %d requests", requestCount.Load())
	}
}

// TestValidTenantIDIsSent proves a valid tenant still reaches the wire.
func TestValidTenantIDIsSent(t *testing.T) {
	var receivedTenant atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		receivedTenant.Store(httpRequest.Header.Get("X-Tenant-ID"))
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{}`))
	}))
	defer server.Close()

	connection := NewConnection(server.URL, "k", 0)
	connection.TenantID = "client-1_prod"
	if _, getError := connection.Get(context.Background(), "/api/v1/health", nil); getError != nil {
		t.Fatalf("valid tenant must pass, got %v", getError)
	}
	if receivedTenant.Load() != "client-1_prod" {
		t.Fatalf("expected X-Tenant-ID client-1_prod, got %v", receivedTenant.Load())
	}
}
