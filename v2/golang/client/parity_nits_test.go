package client

// parity_nits_test.go — the four cross-SDK alignments of 2026-11-21 (audit rows
// 18-21), each proved through the real transport so the test fails if the Go
// answer drifts from the Python and TypeScript one:
//   - 404 is a typed *NotFoundError (status + path) that still unwraps to ErrNotFound;
//   - the two L2 409 errors excerpt the server sentence to 500 characters in Error();
//   - DeleteFile's empty-argument refusals are the typed invalid_request error;
//   - an unknown upload mode is the typed invalid_request error, never sent.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNotFoundIsTypedAndKeepsTheSentinel pins the 404 contract: the message is
// byte-identical to Python/TypeScript ("Resource not found (HTTP 404): <path>",
// path without base URL or query) and errors.Is(err, ErrNotFound) stays true.
func TestNotFoundIsTypedAndKeepsTheSentinel(t *testing.T) {
	requestedPath := ""
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requestedPath = httpRequest.URL.Path
		responseWriter.WriteHeader(http.StatusNotFound)
	}))
	defer stubServer.Close()

	memory := NewMemory("test-key", WithURL(stubServer.URL))
	_, getErr := memory.Get(context.Background(), 42)
	if !errors.Is(getErr, ErrNotFound) {
		t.Fatalf("404 must still match ErrNotFound, got %v", getErr)
	}
	var notFoundErr *NotFoundError
	if !errors.As(getErr, &notFoundErr) {
		t.Fatalf("404 must be a *NotFoundError, got %T", getErr)
	}
	if notFoundErr.StatusCode() != 404 || notFoundErr.Kind() != KindNotFound || notFoundErr.Retryable() {
		t.Errorf("want status 404, kind not_found, not retryable; got %d %s %v",
			notFoundErr.StatusCode(), notFoundErr.Kind(), notFoundErr.Retryable())
	}
	if requestedPath == "" || notFoundErr.Path != requestedPath {
		t.Errorf("Path = %q, want the requested path %q", notFoundErr.Path, requestedPath)
	}
	wantMessage := "Resource not found (HTTP 404): " + requestedPath
	if getErr.Error() != wantMessage {
		t.Errorf("message = %q, want %q", getErr.Error(), wantMessage)
	}
}

// TestScopeConflictMessagesAreExcerpted proves Error() quotes at most 500
// characters of the server sentence (Python/TypeScript cut there too) while
// ServerMessage keeps it verbatim.
func TestScopeConflictMessagesAreExcerpted(t *testing.T) {
	longTail := strings.Repeat("y", 900)
	testCases := []struct {
		name string
		body string
	}{
		{"collision", `{"error":"store c1_t1_lib: ` + longTail + `","code":"tenant_library_collision"}`},
		{"ambiguous", `{"error":"record 42 is a file upload in more than one plane; ` + longTail + `"}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			memory := sessionLimitStubServer(t, 409, testCase.body)
			_, getErr := memory.Get(context.Background(), 42)
			if getErr == nil {
				t.Fatal("409 must surface as an error")
			}
			if strings.Count(getErr.Error(), "y") > 500 {
				t.Errorf("Error() must excerpt the server sentence to 500 characters, got %d chars", len(getErr.Error()))
			}
			var collisionErr *TenantLibraryCollisionError
			var ambiguousErr *UploadStatusAmbiguousError
			switch {
			case errors.As(getErr, &collisionErr):
				if !strings.HasSuffix(collisionErr.ServerMessage, longTail) {
					t.Error("ServerMessage must stay verbatim")
				}
			case errors.As(getErr, &ambiguousErr):
				if !strings.HasSuffix(ambiguousErr.ServerMessage, longTail) {
					t.Error("ServerMessage must stay verbatim")
				}
			default:
				t.Fatalf("want a typed scope error, got %T", getErr)
			}
		})
	}
}

// assertLocalValidationError checks the typed invalid_request shape shared by
// every refusal the SDK raises before sending a request.
func assertLocalValidationError(t *testing.T, gotErr error, wantMessage string) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(gotErr, &apiErr) {
		t.Fatalf("want *APIError, got %T (%v)", gotErr, gotErr)
	}
	if apiErr.StatusCode != 400 || apiErr.Kind() != KindInvalidRequest || apiErr.Retryable() {
		t.Errorf("want 400 / invalid_request / not retryable, got %d / %s / %v",
			apiErr.StatusCode, apiErr.Kind(), apiErr.Retryable())
	}
	if gotErr.Error() != wantMessage {
		t.Errorf("message = %q, want %q", gotErr.Error(), wantMessage)
	}
}

// TestLocalRefusalsAreTypedValidationErrors covers DeleteFile's empty arguments
// and UploadFile's unknown mode; none of them may reach the server.
func TestLocalRefusalsAreTypedValidationErrors(t *testing.T) {
	serverWasCalled := false
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		serverWasCalled = true
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer stubServer.Close()
	memory := NewMemory("test-key", WithURL(stubServer.URL))
	backgroundContext := context.Background()

	_, missingSessionErr := memory.DeleteFile(backgroundContext, "  ", "ef9976f1ef5d5176")
	assertLocalValidationError(t, missingSessionErr, "INVALID_PARAM: DeleteFile: sessionUUID is required")

	_, missingPrefixErr := memory.DeleteFile(backgroundContext, "chat-42", "\t")
	assertLocalValidationError(t, missingPrefixErr, "INVALID_PARAM: DeleteFile: ingestKeyPrefix is required")

	_, unknownModeErr := memory.UploadFile(backgroundContext, "a.txt", []byte("hi"), "", 0, WithUploadMode("Library"))
	assertLocalValidationError(t, unknownModeErr,
		"INVALID_PARAM: upload: mode 'Library' is not an upload mode (want chat|tenant_shared|client_shared)")

	if serverWasCalled {
		t.Error("local validation must not reach the server")
	}
}
