package client

// client_key_errors_test.go — how ClientAPI reports refusals, transport
// failures and concurrent use. Error bodies are the server's real bytes:
// writeErrorCode (server/handler/response.go), ClientKeyOnly's http.Error
// (server/middleware/auth.go) and handleDispatchError's not_leader envelope.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// newStaticClientServer answers every request with one status, headers and body.
func newStaticClientServer(t *testing.T, status int, contentType, responseBody string, extraHeaders map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", contentType)
		for headerName, headerValue := range extraHeaders {
			responseWriter.Header().Set(headerName, headerValue)
		}
		responseWriter.WriteHeader(status)
		_, _ = io.WriteString(responseWriter, responseBody)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestClientAPI_ErrorCodeExposed(t *testing.T) {
	testCases := []struct {
		name          string
		status        int
		contentType   string
		body          string
		headers       map[string]string
		wantCode      string
		wantSentinel  error
		wantRetryable bool
		wantNotLeader bool
		wantMessage   string
	}{
		{name: "child suspended", status: 403, contentType: "application/json",
			body:     `{"error":"child_suspended","detail":"child client is suspended"}`,
			wantCode: "child_suspended", wantSentinel: ErrUnauthorized,
			wantMessage: "Client API error (HTTP 403) on POST /api/v1/client/children/" + testChildID + "/impersonate: child_suspended: child client is suspended"},
		{name: "child not found", status: 404, contentType: "application/json",
			body:     `{"error":"child_not_found","detail":"no child client with this id for the caller"}`,
			wantCode: "child_not_found", wantSentinel: ErrNotFound},
		{name: "invalid scope", status: 400, contentType: "application/json",
			body:     `{"error":"invalid_scope","detail":"scope must be one of admin, write, read_only"}`,
			wantCode: "invalid_scope", wantSentinel: ErrInvalidQuery},
		{name: "child limit is a hard ceiling", status: 429, contentType: "application/json",
			body:     `{"error":"child_limit","detail":"child client limit reached for this client"}`,
			wantCode: "child_limit", wantRetryable: false},
		{name: "frozen client key sentence", status: 403, contentType: "text/plain; charset=utf-8",
			body:     "{\"error\":\"client API key required\"}\n",
			wantCode: "client API key required", wantSentinel: ErrUnauthorized},
		{name: "not leader", status: 503, contentType: "application/json",
			body:     `{"error":"not the leader (node-2)","error_type":"not_leader","detail":"retry"}`,
			headers:  map[string]string{"X-Anhur-Dispatch-Class": "not_leader"},
			wantCode: "not the leader (node-2)", wantSentinel: ErrServerError, wantRetryable: true, wantNotLeader: true},
		{name: "non json body", status: 502, contentType: "text/html", body: "<html>bad gateway</html>",
			wantCode: "", wantSentinel: ErrServerError, wantRetryable: true,
			wantMessage: "Client API error (HTTP 502) on POST /api/v1/client/children/" + testChildID + "/impersonate: <html>bad gateway</html>"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := newStaticClientServer(t, testCase.status, testCase.contentType, testCase.body, testCase.headers)
			api := NewClientAPI("client_key_secret", WithURL(server.URL))
			_, impersonateError := api.ImpersonateChild(context.Background(), testChildID, ImpersonateRequest{TenantID: "geral", Scope: KeyScopeWrite})
			var apiError *ClientAPIError
			if !errors.As(impersonateError, &apiError) {
				t.Fatalf("want *ClientAPIError, got %T %v", impersonateError, impersonateError)
			}
			if apiError.Code != testCase.wantCode || apiError.StatusCode() != testCase.status {
				t.Errorf("code/status = %q/%d", apiError.Code, apiError.StatusCode())
			}
			if testCase.wantSentinel != nil && !errors.Is(impersonateError, testCase.wantSentinel) {
				t.Errorf("errors.Is(%v) false", testCase.wantSentinel)
			}
			if apiError.Retryable() != testCase.wantRetryable || apiError.IsNotLeader() != testCase.wantNotLeader {
				t.Errorf("retryable=%v notLeader=%v", apiError.Retryable(), apiError.IsNotLeader())
			}
			if testCase.wantMessage != "" && apiError.Error() != testCase.wantMessage {
				t.Errorf("message = %q", apiError.Error())
			}
		})
	}
}

func TestClientAPI_EmptyKeyAndCancelledContext(t *testing.T) {
	if _, emptyKeyError := NewClientAPI("").ListTenants(context.Background()); !errors.Is(emptyKeyError, ErrEmptyAPIKey) {
		t.Errorf("empty key: want ErrEmptyAPIKey, got %v", emptyKeyError)
	}
	if _, injectionError := NewClientAPI("bad\r\nkey").ListTenants(context.Background()); !errors.Is(injectionError, ErrEmptyAPIKey) {
		t.Errorf("CRLF key: want ErrEmptyAPIKey, got %v", injectionError)
	}
	fake := newFakeClientServer(t)
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	_, cancelledError := newTestClientAPI(fake).ListTenants(cancelledContext)
	var transportError *TransportError
	if !errors.As(cancelledError, &transportError) || transportError.Kind() != KindTimeout {
		t.Errorf("cancelled context: want timeout *TransportError, got %v", cancelledError)
	}
}

func TestClientAPI_RedirectIsNotFollowed(t *testing.T) {
	server := newStaticClientServer(t, http.StatusFound, "text/plain", "", map[string]string{"Location": "http://evil.example/steal"})
	_, redirectError := NewClientAPI("client_key_secret", WithURL(server.URL), WithHTTPClient(&http.Client{})).ListTenants(context.Background())
	var apiError *ClientAPIError
	if !errors.As(redirectError, &apiError) || apiError.Status != http.StatusFound {
		t.Fatalf("want *ClientAPIError 302, got %v", redirectError)
	}
}

func TestClientAPI_ConcurrentUse(t *testing.T) {
	fake := newFakeClientServer(t)
	api := newTestClientAPI(fake)
	const workerCount = 16
	var waitGroup sync.WaitGroup
	workerErrors := make(chan error, workerCount)
	for workerIndex := 0; workerIndex < workerCount; workerIndex++ {
		waitGroup.Add(1)
		go func(currentWorker int) {
			defer waitGroup.Done()
			_, impersonateError := api.ImpersonateChild(context.Background(), testChildID,
				ImpersonateRequest{TenantID: fmt.Sprintf("tenant-%d", currentWorker), Scope: KeyScopeReadOnly})
			workerErrors <- impersonateError
		}(workerIndex)
	}
	waitGroup.Wait()
	close(workerErrors)
	for workerError := range workerErrors {
		if workerError != nil {
			t.Errorf("concurrent impersonate: %v", workerError)
		}
	}
	if requestCount := len(fake.recorded()); requestCount != workerCount {
		t.Errorf("want %d requests, got %d", workerCount, requestCount)
	}
}
