package client

// record_scope_test.go — every by-id method puts ?scope= on the wire exactly when
// WithRecordScope is given, refuses a non-addressable scope before any request, and
// the two plane-picking rules match the server.
//
// Server shapes copied from AnhurDB fix/l2-lib-scope:
//   - by-id routes wrapped in rh.ScopedByID: server/router.go (GET/PATCH/DELETE
//     /api/v1/records/{id}[/content|/topology|/grounding]); ?scope= read from the
//     query: server/handler/record_scoped_by_id.go:54,75.
//   - batch-content and supersede read ?scope= themselves:
//     server/handler/record_batch.go (resolveScopedByIDLeg) and
//     server/handler/record_supersede.go.
//   - upload status ?scope=: server/handler/upload_status_scope.go:147.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// recordedRequest is what the stub saw for one call.
type recordedRequest struct {
	method   string
	path     string
	rawQuery string
	scope    string
}

// scopeRecorder answers 200 with body for every request and records each one.
func scopeRecorder(t *testing.T, body string) (*Memory, func() []recordedRequest) {
	t.Helper()
	var recordedMutex sync.Mutex
	var recorded []recordedRequest
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		recordedMutex.Lock()
		recorded = append(recorded, recordedRequest{
			method:   httpRequest.Method,
			path:     httpRequest.URL.Path,
			rawQuery: httpRequest.URL.RawQuery,
			scope:    httpRequest.URL.Query().Get("scope"),
		})
		recordedMutex.Unlock()
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(body))
	}))
	t.Cleanup(stubServer.Close)
	return NewMemory("test-key", WithURL(stubServer.URL)), func() []recordedRequest {
		recordedMutex.Lock()
		defer recordedMutex.Unlock()
		return append([]recordedRequest(nil), recorded...)
	}
}

// byIDCall is one by-id method invoked with the given options.
type byIDCall struct {
	name       string
	wantMethod string
	wantPath   string
	invoke     func(ctx context.Context, memory *Memory, opts ...ByIDOption) error
}

func byIDCalls() []byIDCall {
	return []byIDCall{
		{"Get", http.MethodGet, "/api/v1/records/42", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			_, callErr := memory.Get(ctx, 42, opts...)
			return callErr
		}},
		{"ReadContent", http.MethodGet, "/api/v1/records/42/content", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			_, callErr := memory.ReadContent(ctx, 42, opts...)
			return callErr
		}},
		{"GetContext", http.MethodGet, "/api/v1/records/42/topology", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			_, callErr := memory.GetContext(ctx, 42, opts...)
			return callErr
		}},
		{"GetGrounding", http.MethodGet, "/api/v1/records/42/grounding", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			_, callErr := memory.GetGrounding(ctx, 42, 2, opts...)
			return callErr
		}},
		{"BatchReadContent", http.MethodPost, "/api/v1/records/batch-content", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			_, callErr := memory.BatchReadContent(ctx, []int64{42, 43}, opts...)
			return callErr
		}},
		{"Update", http.MethodPatch, "/api/v1/records/42", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			return memory.Update(ctx, 42, map[string]interface{}{"summary": "s"}, opts...)
		}},
		{"Delete", http.MethodDelete, "/api/v1/records/42", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			return memory.Delete(ctx, 42, opts...)
		}},
		{"Supersede", http.MethodPost, "/api/v1/records/supersede", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			return memory.Supersede(ctx, 42, 43, opts...)
		}},
		{"UploadStatus", http.MethodGet, "/api/v1/upload/42/status", func(ctx context.Context, memory *Memory, opts ...ByIDOption) error {
			_, callErr := memory.UploadStatus(ctx, 42, opts...)
			return callErr
		}},
	}
}

func TestByIDMethodsSendScopeOnlyWhenGiven(t *testing.T) {
	scopes := []RecordScope{"", RecordScopeSessions, RecordScopeTenantShared, RecordScopeTenantSharedLegacy, RecordScopeClientShared}
	for _, call := range byIDCalls() {
		for _, scope := range scopes {
			memory, recordedRequests := scopeRecorder(t, `{}`)
			var opts []ByIDOption
			if scope != "" {
				opts = append(opts, WithRecordScope(scope))
			}
			if callErr := call.invoke(context.Background(), memory, opts...); callErr != nil {
				t.Fatalf("%s scope=%q: %v", call.name, scope, callErr)
			}
			requests := recordedRequests()
			if len(requests) != 1 {
				t.Fatalf("%s scope=%q: want 1 request, got %d", call.name, scope, len(requests))
			}
			sentRequest := requests[0]
			if sentRequest.method != call.wantMethod || sentRequest.path != call.wantPath {
				t.Errorf("%s: sent %s %s, want %s %s", call.name, sentRequest.method, sentRequest.path, call.wantMethod, call.wantPath)
			}
			if sentRequest.scope != string(scope) {
				t.Errorf("%s: wire scope=%q, want %q (raw query %q)", call.name, sentRequest.scope, scope, sentRequest.rawQuery)
			}
			if scope == "" && sentRequest.rawQuery != "" && call.name != "GetGrounding" {
				t.Errorf("%s: no scope must leave the query empty (today's contract), got %q", call.name, sentRequest.rawQuery)
			}
		}
	}
}

func TestByIDMethodsRefuseNonAddressableScopeLocally(t *testing.T) {
	for _, call := range byIDCalls() {
		for _, badScope := range []RecordScope{"shared_all", "bogus"} {
			memory, recordedRequests := scopeRecorder(t, `{}`)
			callErr := call.invoke(context.Background(), memory, WithRecordScope(badScope))
			var apiErr *APIError
			if !errors.As(callErr, &apiErr) || apiErr.Kind() != KindInvalidRequest {
				t.Fatalf("%s scope=%q: want local invalid_request *APIError, got %v", call.name, badScope, callErr)
			}
			if len(recordedRequests()) != 0 {
				t.Errorf("%s scope=%q: a refused scope must not reach the wire", call.name, badScope)
			}
		}
	}
}

func TestGetGroundingKeepsMaxDepthBesideScope(t *testing.T) {
	memory, recordedRequests := scopeRecorder(t, `{}`)
	if _, groundingErr := memory.GetGrounding(context.Background(), 42, 4, WithRecordScope(RecordScopeClientShared)); groundingErr != nil {
		t.Fatal(groundingErr)
	}
	if rawQuery := recordedRequests()[0].rawQuery; rawQuery != "max_depth=4&scope=client_shared" {
		t.Errorf("raw query = %q", rawQuery)
	}
}

func TestTenantSharedScopeForHit(t *testing.T) {
	testCases := map[string]RecordScope{
		"shared-c1-t1_lib":   RecordScopeTenantShared,
		" shared-c1-t1_lib ": RecordScopeTenantShared,
		"shared-c1-t1":       RecordScopeTenantSharedLegacy,
		"shared-c1-t1_libx":  RecordScopeTenantSharedLegacy,
		"":                   RecordScopeTenantSharedLegacy,
	}
	for recordUUID, wantScope := range testCases {
		if gotScope := TenantSharedScopeForHit(recordUUID); gotScope != wantScope {
			t.Errorf("TenantSharedScopeForHit(%q) = %q, want %q", recordUUID, gotScope, wantScope)
		}
	}
}

// TestUploadStatusScope mirrors server/service/upload_destination.go:58-110
// (ResolveUploadDestination): "" + session → chat, "" alone → tenant_shared,
// controlplane → tenant_shared.
func TestUploadStatusScope(t *testing.T) {
	testCases := []struct {
		mode, sessionID string
		wantScope       RecordScope
	}{
		{"", "", RecordScopeTenantShared},
		{"", "sess-1", RecordScopeSessions},
		{"chat", "sess-1", RecordScopeSessions},
		{" Tenant_Shared ", "", RecordScopeTenantShared},
		{"client_shared", "", RecordScopeClientShared},
		{"controlplane", "", RecordScopeTenantShared},
	}
	for _, testCase := range testCases {
		if gotScope := UploadStatusScope(testCase.mode, testCase.sessionID); gotScope != testCase.wantScope {
			t.Errorf("UploadStatusScope(%q,%q) = %q, want %q", testCase.mode, testCase.sessionID, gotScope, testCase.wantScope)
		}
	}
}
