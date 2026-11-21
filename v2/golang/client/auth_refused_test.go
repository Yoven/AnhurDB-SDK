package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every 401/403 on a data route must arrive as *AuthRefusedError through the
// REAL transport (a genuine Memory.Add against a stub server), carrying the
// server's code, while errors.Is(err, ErrUnauthorized) stays true. The bodies
// are the ones AnhurDB actually sends (server/middleware/auth*.go,
// handler/service_bridge.go). gRPC is not covered: this SDK has no gRPC
// transport.

// authStubServer answers every request with the given status, content type and body.
func authStubServer(t *testing.T, statusCode int, contentType, body string) *Memory {
	t.Helper()
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", contentType)
		responseWriter.WriteHeader(statusCode)
		_, _ = responseWriter.Write([]byte(body))
	}))
	t.Cleanup(stubServer.Close)
	return NewMemory("test-key", WithURL(stubServer.URL))
}

func TestDataRouteAuthRefusalIsTyped(t *testing.T) {
	testCases := []struct {
		name          string
		status        int
		contentType   string
		body          string
		wantCode      string
		wantDetail    string
		wantMessage   string
		wantPredicate func(*AuthRefusedError) bool
	}{
		{
			name: "l1_only (writeErrorCode, application/json)", status: 403, contentType: "application/json",
			body:          `{"error":"l1_only","detail":"token restricted to L1 sessions"}`,
			wantCode:      "l1_only",
			wantDetail:    "token restricted to L1 sessions",
			wantMessage:   "Authentication failed (HTTP 403): l1_only: token restricted to L1 sessions",
			wantPredicate: (*AuthRefusedError).IsL1Only,
		},
		{
			name: "child_suspended (http.Error, text/plain JSON body)", status: 403, contentType: "text/plain; charset=utf-8",
			body:          "{\"error\":\"child_suspended\",\"detail\":\"client is suspended\"}\n",
			wantCode:      "child_suspended",
			wantDetail:    "client is suspended",
			wantMessage:   "Authentication failed (HTTP 403): child_suspended: client is suspended",
			wantPredicate: (*AuthRefusedError).IsChildSuspended,
		},
		{
			name: "insufficient scope sentence", status: 403, contentType: "text/plain; charset=utf-8",
			body:          "{\"error\":\"insufficient scope — key does not have permission for this operation\"}\n",
			wantCode:      "insufficient scope — key does not have permission for this operation",
			wantMessage:   "Authentication failed (HTTP 403): insufficient scope — key does not have permission for this operation",
			wantPredicate: (*AuthRefusedError).IsInsufficientScope,
		},
		{
			name: "client key on a data route", status: 403, contentType: "text/plain; charset=utf-8",
			body:          "{\"error\":\"client_key_requires_impersonation\"}\n",
			wantCode:      "client_key_requires_impersonation",
			wantMessage:   "Authentication failed (HTTP 403): client_key_requires_impersonation",
			wantPredicate: (*AuthRefusedError).IsImpersonationRequired,
		},
		{
			name: "invalid key 401", status: 401, contentType: "text/plain; charset=utf-8",
			body:          "{\"error\":\"invalid or inactive API key\"}\n",
			wantCode:      "invalid or inactive API key",
			wantMessage:   "Authentication failed (HTTP 401): invalid or inactive API key",
			wantPredicate: (*AuthRefusedError).IsInvalidKey,
		},
		{
			name: "non-JSON body keeps today's message", status: 403, contentType: "text/plain",
			body:        "Forbidden",
			wantMessage: ErrUnauthorized.Error(),
		},
		{
			name: "JSON without a string error keeps today's message", status: 401, contentType: "application/json",
			body:        `{"error":5}`,
			wantMessage: ErrUnauthorized.Error(),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			memory := authStubServer(t, testCase.status, testCase.contentType, testCase.body)

			_, addErr := memory.Add(context.Background(), "turn text", WithSessionID("s-1"))

			if !errors.Is(addErr, ErrUnauthorized) {
				t.Fatalf("errors.Is(err, ErrUnauthorized) must stay true, got %T: %v", addErr, addErr)
			}
			var authError *AuthRefusedError
			if !errors.As(addErr, &authError) {
				t.Fatalf("expected *AuthRefusedError, got %T: %v", addErr, addErr)
			}
			if authError.Status != testCase.status || authError.StatusCode() != testCase.status {
				t.Errorf("Status = %d, want %d", authError.Status, testCase.status)
			}
			if authError.Code != testCase.wantCode || authError.Detail != testCase.wantDetail {
				t.Errorf("Code/Detail = %q/%q, want %q/%q", authError.Code, authError.Detail, testCase.wantCode, testCase.wantDetail)
			}
			if addErr.Error() != testCase.wantMessage {
				t.Errorf("message = %q, want %q", addErr.Error(), testCase.wantMessage)
			}
			if authError.Kind() != KindAuth || authError.Retryable() {
				t.Errorf("want kind auth, not retryable; got %s retryable=%v", authError.Kind(), authError.Retryable())
			}
			assertOnlyPredicate(t, authError, testCase.wantPredicate)
		})
	}
}

// assertOnlyPredicate checks that wantPredicate (nil = none) is the ONLY
// convenience predicate that answers true, so no two refusals can be confused.
func assertOnlyPredicate(t *testing.T, authError *AuthRefusedError, wantPredicate func(*AuthRefusedError) bool) {
	t.Helper()
	predicates := map[string]func(*AuthRefusedError) bool{
		"IsL1Only":                (*AuthRefusedError).IsL1Only,
		"IsChildSuspended":        (*AuthRefusedError).IsChildSuspended,
		"IsInsufficientScope":     (*AuthRefusedError).IsInsufficientScope,
		"IsImpersonationRequired": (*AuthRefusedError).IsImpersonationRequired,
		"IsInvalidKey":            (*AuthRefusedError).IsInvalidKey,
	}
	trueCount := 0
	for predicateName, predicate := range predicates {
		if predicate(authError) {
			trueCount++
			if wantPredicate == nil {
				t.Errorf("%s is true for code %q, want no predicate true", predicateName, authError.Code)
			}
		}
	}
	if wantPredicate != nil && (!wantPredicate(authError) || trueCount != 1) {
		t.Errorf("want exactly the expected predicate true for code %q, %d were true", authError.Code, trueCount)
	}
}

// A mistyped optional field must not cost the code — Python and TypeScript
// keep it, and the client-key surface shares the decoder.
func TestErrorEnvelopeKeepsCodeWhenOptionalFieldIsMistyped(t *testing.T) {
	testCases := []struct {
		name        string
		body        string
		wantDecoded bool
		wantCode    string
		wantDetail  string
	}{
		{name: "detail is a number", body: `{"error":"l1_only","detail":5}`, wantDecoded: true, wantCode: "l1_only"},
		{name: "error_type is an object", body: `{"error":"x","detail":"d","error_type":{}}`, wantDecoded: true, wantCode: "x", wantDetail: "d"},
		{name: "empty error", body: `{"error":""}`},
		{name: "array body", body: `["l1_only"]`},
		{name: "null body", body: `null`},
		{name: "empty body", body: ``},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			envelope, decoded := decodeErrorEnvelope([]byte(testCase.body))
			if decoded != testCase.wantDecoded || envelope.code != testCase.wantCode || envelope.detail != testCase.wantDetail {
				t.Errorf("got decoded=%v code=%q detail=%q, want %v %q %q",
					decoded, envelope.code, envelope.detail, testCase.wantDecoded, testCase.wantCode, testCase.wantDetail)
			}
			clientError := clientAPIErrorFromBody(403, "POST", "/api/v1/client/impersonate", []byte(testCase.body))
			if clientError.Code != testCase.wantCode {
				t.Errorf("ClientAPIError.Code = %q, want %q", clientError.Code, testCase.wantCode)
			}
		})
	}
}
