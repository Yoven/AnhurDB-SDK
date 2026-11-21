package client

// scope_errors_test.go — the L2 library error answers arrive TYPED through the real
// transport, the upload plane travels from UploadFile to the status poll, and
// delete-by-file exposes its legs.
//
// Server shapes copied from AnhurDB fix/l2-lib-scope:
//   - collision (4574c4d): server/service/search_scope_admit.go GuardTenantLibraryCollision
//     sentence + code "tenant_library_collision", HTTP 409 on every door including upload
//     (handler/search_scope_request.go writeScopeLegError, handler/upload.go). The
//     pre-4574c4d bodies (no code; 400 on upload) are kept to prove they are NO
//     LONGER typed (the 4.0.0 removal of the sentence fallback).
//   - ambiguous upload status: server/handler/upload_status_scope.go:188-189.
//   - partial delete: server/handler/record_delete_file_library.go:53-57 with legs from
//     service/record_delete_file_library.go:34-44 and the message of :54-67.
//   - upload 202 body: server/handler/upload.go writeJSON(StatusAccepted, ...).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const collisionBody409 = `{"error":"store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared","code":"tenant_library_collision"}`
const collisionUploadBody409 = `{"error":"tenant_shared upload: store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared","code":"tenant_library_collision"}`

// Pre-4574c4d bodies: no code; the upload door answered 400. Since 4.0.0 both
// are plain *APIError answers.
const preCodeCollisionBody409 = `{"error":"store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared"}`
const preCodeCollisionBody400 = `{"error":"tenant_shared upload: store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared"}`
const ambiguousBody = `{"error":"record 42 is a file upload in more than one plane; pass ?scope=sessions|tenant_shared|tenant_shared_legacy|client_shared"}`

// partialDeleteBody is deliberately longer than the 500-character message excerpt.
var partialDeleteBody = `{"error":"tenant library delete-by-file partially failed — retry is safe (idempotent): primary deleted=3 matched=3; legacy FAILED: ` +
	strings.Repeat("x", 600) + `","partial":true,"legs":[` +
	`{"generation":"primary","session_uuid":"shared-c1-t1_lib","matched_count":3,"deleted_count":3},` +
	`{"generation":"legacy","session_uuid":"shared-c1-t1","matched_count":0,"deleted_count":0,"error":"database error"}]}`

func TestCollisionIsTypedOn409OnEveryDoor(t *testing.T) {
	searchMemory := sessionLimitStubServer(t, 409, collisionBody409)
	_, searchErr := searchMemory.Search(context.Background(), "q", SessionsAll(), WithScope("tenant_shared"))
	assertCollision(t, searchErr, 409, KindConflict)

	getMemory := sessionLimitStubServer(t, 409, collisionBody409)
	_, getErr := getMemory.Get(context.Background(), 42, WithRecordScope(RecordScopeTenantShared))
	assertCollision(t, getErr, 409, KindConflict)

	deleteMemory := sessionLimitStubServer(t, 409, collisionBody409)
	_, deleteErr := deleteMemory.DeleteFile(context.Background(), "", "ef9976f1ef5d5176", WithDeleteFileScope(RecordScopeTenantShared))
	assertCollision(t, deleteErr, 409, KindConflict)

	uploadMemory := sessionLimitStubServer(t, 409, collisionUploadBody409)
	_, uploadErr := uploadMemory.UploadFile(context.Background(), "a.txt", []byte("hi"), "", 0, WithUploadMode("tenant_shared"))
	assertCollision(t, uploadErr, 409, KindConflict)
}

// TestCollisionCodeWinsOverText: the code is the contract — a reworded sentence is still
// a collision, and a body whose code names another refusal is never re-read by its text.
func TestCollisionCodeWinsOverText(t *testing.T) {
	rewordedMemory := sessionLimitStubServer(t, 409, `{"error":"library store is taken","code":"tenant_library_collision"}`)
	_, rewordedErr := rewordedMemory.Get(context.Background(), 42, WithRecordScope(RecordScopeTenantShared))
	assertCollision(t, rewordedErr, 409, KindConflict)

	otherCodeMemory := sessionLimitStubServer(t, 409, `{"error":"tenant library store id is a registered tenant","code":"something_else"}`)
	_, otherCodeErr := otherCodeMemory.Get(context.Background(), 42)
	if errors.Is(otherCodeErr, ErrTenantLibraryCollision) {
		t.Errorf("a foreign code must not be re-read as a collision by its text: %v", otherCodeErr)
	}
}

// TestCollisionSentenceWithoutCodeIsNotTyped proves the 4.0.0 removal: the collision
// is recognised by code on a 409 ONLY. The pre-4574c4d bodies — the sentence with no
// code on a 409, and the upload door's 400 — are now ordinary *APIError answers.
func TestCollisionSentenceWithoutCodeIsNotTyped(t *testing.T) {
	getMemory := sessionLimitStubServer(t, 409, preCodeCollisionBody409)
	_, getErr := getMemory.Get(context.Background(), 42, WithRecordScope(RecordScopeTenantShared))
	assertPlainAPIError(t, getErr, 409, KindConflict)

	uploadMemory := sessionLimitStubServer(t, 400, preCodeCollisionBody400)
	_, uploadErr := uploadMemory.UploadFile(context.Background(), "a.txt", []byte("hi"), "", 0, WithUploadMode("tenant_shared"))
	assertPlainAPIError(t, uploadErr, 400, KindInvalidRequest)

	// The code itself on a 400 is not the contract either: only 409 carries it.
	codeOn400Memory := sessionLimitStubServer(t, 400, collisionUploadBody409)
	_, codeOn400Err := codeOn400Memory.UploadFile(context.Background(), "a.txt", []byte("hi"), "", 0, WithUploadMode("tenant_shared"))
	assertPlainAPIError(t, codeOn400Err, 400, KindInvalidRequest)
}

// assertPlainAPIError requires an untyped *APIError with the given status and kind.
func assertPlainAPIError(t *testing.T, gotErr error, wantStatus int, wantKind ErrorKind) {
	t.Helper()
	var collisionErr *TenantLibraryCollisionError
	if errors.As(gotErr, &collisionErr) || errors.Is(gotErr, ErrTenantLibraryCollision) {
		t.Fatalf("must not be typed as a collision any more: %v", gotErr)
	}
	var apiErr *APIError
	if !errors.As(gotErr, &apiErr) || apiErr.StatusCode != wantStatus || apiErr.Kind() != wantKind {
		t.Fatalf("want plain *APIError %d (%s), got %T %v", wantStatus, wantKind, gotErr, gotErr)
	}
}

// TestUploadRegistryUnavailableIsPlainUnavailable: an unreadable tenant registry is 503
// on upload (was 400) — an ordinary retryable unavailable *APIError, never a collision.
func TestUploadRegistryUnavailableIsPlainUnavailable(t *testing.T) {
	uploadMemory := sessionLimitStubServer(t, 503, `{"error":"tenant_shared upload: tenant library store c1_t1_lib: tenant registry unavailable: database is locked"}`)
	_, uploadErr := uploadMemory.UploadFile(context.Background(), "a.txt", []byte("hi"), "", 0, WithUploadMode("tenant_shared"))
	var apiErr *APIError
	if !errors.As(uploadErr, &apiErr) || apiErr.StatusCode != 503 || apiErr.Kind() != KindUnavailable || !apiErr.Retryable() {
		t.Fatalf("want retryable unavailable *APIError 503, got %T %v", uploadErr, uploadErr)
	}
	if errors.Is(uploadErr, ErrTenantLibraryCollision) {
		t.Error("a registry outage must never read as a collision")
	}
}

func assertCollision(t *testing.T, gotErr error, wantStatus int, wantKind ErrorKind) {
	t.Helper()
	var collisionErr *TenantLibraryCollisionError
	if !errors.As(gotErr, &collisionErr) || !errors.Is(gotErr, ErrTenantLibraryCollision) {
		t.Fatalf("want *TenantLibraryCollisionError, got %T %v", gotErr, gotErr)
	}
	if collisionErr.StatusCode() != wantStatus || collisionErr.Kind() != wantKind || collisionErr.Retryable() {
		t.Errorf("status=%d kind=%s retryable=%v", collisionErr.StatusCode(), collisionErr.Kind(), collisionErr.Retryable())
	}
	var apiErr *APIError
	if !errors.As(gotErr, &apiErr) || apiErr.StatusCode != wantStatus {
		t.Errorf("must still unwrap to the *APIError %d", wantStatus)
	}
	if errors.Is(gotErr, ErrSessionRecordLimit) {
		t.Error("a collision must never read as a session record limit")
	}
}

func TestSessionLimitAndGenericConflictStayDistinct(t *testing.T) {
	limitMemory := sessionLimitStubServer(t, 409, `{"error":"session has reached the maximum of 500 records","code":"session_record_limit"}`)
	_, limitErr := limitMemory.Get(context.Background(), 1)
	if !errors.Is(limitErr, ErrSessionRecordLimit) || errors.Is(limitErr, ErrTenantLibraryCollision) || errors.Is(limitErr, ErrUploadStatusAmbiguous) {
		t.Errorf("session limit misclassified: %v", limitErr)
	}
	otherMemory := sessionLimitStubServer(t, 409, `{"error":"record already superseded"}`)
	_, otherErr := otherMemory.Get(context.Background(), 1)
	var apiErr *APIError
	if !errors.As(otherErr, &apiErr) || errors.Is(otherErr, ErrTenantLibraryCollision) || errors.Is(otherErr, ErrUploadStatusAmbiguous) || errors.Is(otherErr, ErrSessionRecordLimit) {
		t.Errorf("generic 409 must stay a plain *APIError, got %T %v", otherErr, otherErr)
	}
}

func TestUploadStatusAmbiguousIsTypedAndStopsTheWait(t *testing.T) {
	memory := sessionLimitStubServer(t, 409, ambiguousBody)
	_, statusErr := memory.UploadStatus(context.Background(), 42)
	var ambiguousErr *UploadStatusAmbiguousError
	if !errors.As(statusErr, &ambiguousErr) || ambiguousErr.Retryable() || ambiguousErr.Kind() != KindConflict {
		t.Fatalf("want non-retryable *UploadStatusAmbiguousError, got %T %v", statusErr, statusErr)
	}

	startedAt := time.Now()
	_, waitErr := memory.WaitForUpload(context.Background(), 42, WaitUploadOptions{Timeout: 5 * time.Second, Interval: time.Second})
	if !errors.Is(waitErr, ErrUploadStatusAmbiguous) {
		t.Fatalf("WaitForUpload must surface the ambiguity, got %v", waitErr)
	}
	if time.Since(startedAt) > 500*time.Millisecond {
		t.Errorf("a refusal must end the wait at once, took %s", time.Since(startedAt))
	}
}

func TestUploadRecordsPlaneAndWaitPassesIt(t *testing.T) {
	var statusScopes []string
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		if httpRequest.Method == http.MethodPost {
			responseWriter.WriteHeader(http.StatusAccepted)
			_, _ = responseWriter.Write([]byte(`{"message":"File accepted for processing","record_id":42,"uuid":"shared-c1-t1_lib","filename":"a.txt","mime":"text/plain","mime_detected":"text/plain","extension":".txt","size_bytes":2,"status":"processing"}`))
			return
		}
		statusScopes = append(statusScopes, httpRequest.URL.Query().Get("scope"))
		_, _ = responseWriter.Write([]byte(`{"record_id":42,"uuid":"shared-c1-t1_lib","status":"completed","type":"file","summary":"","metadata":"{}","completed":true}`))
	}))
	t.Cleanup(stubServer.Close)
	memory := NewMemory("test-key", WithURL(stubServer.URL))

	for _, testCase := range []struct {
		opts      []UploadOption
		sessionID string
		episodic  int64
		wantScope RecordScope
	}{
		{nil, "", 0, RecordScopeTenantShared},
		{[]UploadOption{WithUploadMode("client_shared")}, "", 0, RecordScopeClientShared},
		{nil, "sess-1", 7, RecordScopeSessions},
	} {
		uploadResult, uploadErr := memory.UploadFile(context.Background(), "a.txt", []byte("hi"), testCase.sessionID, testCase.episodic, testCase.opts...)
		if uploadErr != nil {
			t.Fatal(uploadErr)
		}
		if uploadResult.Scope != testCase.wantScope {
			t.Fatalf("UploadResult.Scope = %q, want %q", uploadResult.Scope, testCase.wantScope)
		}
		if _, waitErr := memory.WaitForUpload(context.Background(), uploadResult.RecordID, WaitUploadOptions{Scope: uploadResult.Scope}); waitErr != nil {
			t.Fatal(waitErr)
		}
		if lastScope := statusScopes[len(statusScopes)-1]; lastScope != string(testCase.wantScope) {
			t.Errorf("status poll scope = %q, want %q", lastScope, testCase.wantScope)
		}
	}
}

func TestDeleteFileExposesLegs(t *testing.T) {
	var sentQuery string
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		sentQuery = httpRequest.URL.RawQuery
		_, _ = responseWriter.Write([]byte(`{"session_uuid":"shared-c1-t1_lib","ingest_key_prefix":"ef9976f1ef5d5176","matched_count":3,"deleted_count":3,"deleted_ids":[1,2,3],"dry_run":false,"purge":true,` +
			`"legs":[{"generation":"primary","session_uuid":"shared-c1-t1_lib","matched_count":3,"deleted_count":3},{"generation":"legacy","session_uuid":"shared-c1-t1","dispatch_skipped":true,"matched_count":0,"deleted_count":0}]}`))
	}))
	t.Cleanup(stubServer.Close)
	memory := NewMemory("test-key", WithURL(stubServer.URL))

	deleteResult, deleteErr := memory.DeleteFile(context.Background(), "", "ef9976f1ef5d5176", WithDeleteFileScope(RecordScopeTenantShared))
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if sentQuery != "dry_run=false&ingest_key_prefix=ef9976f1ef5d5176&scope=tenant_shared" {
		t.Errorf("query = %q (empty session must not be sent with scope=tenant_shared)", sentQuery)
	}
	if len(deleteResult.Legs) != 2 || deleteResult.Legs[0].Generation != "primary" || deleteResult.Legs[0].DeletedCount != 3 || !deleteResult.Legs[1].DispatchSkipped {
		t.Errorf("legs = %+v", deleteResult.Legs)
	}
}

func TestDeleteFileScopeValidation(t *testing.T) {
	memory := sessionLimitStubServer(t, 200, `{}`)
	for _, badScope := range []RecordScope{RecordScopeSessions, RecordScopeTenantSharedLegacy, "shared_all"} {
		_, deleteErr := memory.DeleteFile(context.Background(), "s1", "ef9976f1ef5d5176", WithDeleteFileScope(badScope))
		var apiErr *APIError
		if !errors.As(deleteErr, &apiErr) || apiErr.Kind() != KindInvalidRequest {
			t.Errorf("scope %q: want local invalid_request, got %v", badScope, deleteErr)
		}
	}
	if _, deleteErr := memory.DeleteFile(context.Background(), "", "ef9976f1ef5d5176", WithDeleteFileScope(RecordScopeClientShared)); deleteErr == nil {
		t.Error("client_shared delete-by-file still needs the session")
	}
}

func TestLibraryDeletePartialCarriesEveryLeg(t *testing.T) {
	memory := sessionLimitStubServer(t, 500, partialDeleteBody)
	_, deleteErr := memory.DeleteFile(context.Background(), "", "ef9976f1ef5d5176", WithDeleteFileScope(RecordScopeTenantShared))
	var partialErr *LibraryDeletePartialError
	if !errors.As(deleteErr, &partialErr) || !errors.Is(deleteErr, ErrLibraryDeletePartial) {
		t.Fatalf("want *LibraryDeletePartialError, got %T %v", deleteErr, deleteErr)
	}
	if len(partialErr.Legs) != 2 || partialErr.Legs[1].Error != "database error" || partialErr.Legs[0].DeletedCount != 3 {
		t.Errorf("legs = %+v", partialErr.Legs)
	}
	if !partialErr.Retryable() || partialErr.Kind() != KindServer || partialErr.StatusCode() != 500 {
		t.Errorf("classification: retryable=%v kind=%s", partialErr.Retryable(), partialErr.Kind())
	}
	if len(partialErr.Error()) > 700 {
		t.Errorf("message must stay bounded, got %d chars", len(partialErr.Error()))
	}

	plain500 := sessionLimitStubServer(t, 500, `{"error":"internal error"}`)
	_, plainErr := plain500.DeleteFile(context.Background(), "s1", "ef9976f1ef5d5176")
	if errors.Is(plainErr, ErrLibraryDeletePartial) {
		t.Error("a plain 500 is not a partial delete")
	}
}
