package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/models"
)

// TestRecentEmptyEnvelope reproduces the empty-manifest crash: the
// server returns {"records": []} (an OBJECT). The old code fell through to a
// bare-array Unmarshal and errored. It must now yield an empty slice, no error.
func TestRecentEmptyEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, `{"count":0,"has_more":false,"limit":5,"offset":0,"records":[]}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	records, err := mem.Recent(context.Background(), 5)
	if err != nil {
		t.Fatalf("empty envelope must not error, got: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records, got %d", len(records))
	}
}

// TestRecentBareArrayIsRejected proves the 4.0.0 removal: the server always
// sends the {"records","count"} envelope (server/handler/record_session.go:455), so
// a bare JSON array is now a decode error instead of a silently accepted page.
func TestRecentBareArrayIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		io.WriteString(responseWriter, `[{"id":7}]`)
	}))
	defer server.Close()
	mem := NewMemory("k", WithURL(server.URL))
	records, err := mem.Recent(context.Background(), 5)
	if err == nil {
		t.Fatalf("a bare array must not decode any more, got %d records", len(records))
	}
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("want the envelope decode error, got: %v", err)
	}
}

// TestRecentPopulatedEnvelope verifies envelope rows map into Record.
func TestRecentPopulatedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		io.WriteString(responseWriter, `{"records":[{"id":7,"uuid":"u","type":"episodic","summary":"s","metadata":"{}","status":"saved"}]}`)
	}))
	defer server.Close()
	mem := NewMemory("k", WithURL(server.URL))
	records, err := mem.Recent(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(records) != 1 || records[0].ID != 7 || records[0].Type != models.MemoryType("episodic") {
		t.Fatalf("bad mapping: %+v", records)
	}
}

// TestAddForwardsScoreTypeMetadataToRecords verifies that pinning score/type/
// metadata routes Add to POST /api/v1/records, forwards all three and merges
// metadata (container_tag still owned by the SDK).
func TestAddForwardsScoreTypeMetadataToRecords(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			t.Errorf("a pinned score/type/metadata must never reach /ingest")
			http.NotFound(responseWriter, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &captured)
		io.WriteString(responseWriter, `{"id":123}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	result, err := mem.Add(context.Background(), "hello",
		WithSessionID("test-sess"),
		WithScore(9),
		WithType("semantic"),
		WithMetadata(map[string]interface{}{"source": "import", "lang": "go"}),
	)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if result.ID != 123 || result.Mode != "records" {
		t.Fatalf("expected id 123 mode records, got %d %q", result.ID, result.Mode)
	}
	if captured["score"] != float64(9) {
		t.Fatalf("score not forwarded: %v", captured["score"])
	}
	if captured["type"] != "semantic" {
		t.Fatalf("type not forwarded: %v", captured["type"])
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(captured["metadata"].(string)), &meta); err != nil {
		t.Fatalf("metadata not valid JSON: %v", err)
	}
	if meta["source"] != "import" || meta["lang"] != "go" {
		t.Fatalf("caller metadata dropped: %v", meta)
	}
	if _, hasTag := meta["container_tag"]; !hasTag {
		t.Fatalf("container_tag must be preserved: %v", meta)
	}
}

// TestAddRegularModeSendsRecordDefaults verifies the records path with no
// score/type options still sends the historical defaults (score=5,
// type=episodic).
func TestAddRegularModeSendsRecordDefaults(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			t.Errorf("regular mode must never reach /ingest")
			http.NotFound(responseWriter, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &captured)
		io.WriteString(responseWriter, `{"id":1}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	if _, err := mem.Add(context.Background(), "hi", WithMode("regular"), WithSessionID("test-sess")); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if captured["score"] != float64(5) {
		t.Fatalf("default score should be 5, got %v", captured["score"])
	}
	if captured["type"] != "episodic" {
		t.Fatalf("default type should be episodic, got %v", captured["type"])
	}
}

// TestAddDoesNotRetryTransient pins the transparent-pipe contract: the SDK owns
// NO transport retry, so HTTP 500 from /ingest surfaces to the caller after
// exactly ONE ingest request and no write anywhere else.
func TestAddDoesNotRetryTransient(t *testing.T) {
	ingestCalls := 0
	recordCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			ingestCalls++
			responseWriter.WriteHeader(http.StatusInternalServerError)
			io.WriteString(responseWriter, `{"error":"transient server error"}`)
			return
		}
		recordCalls++
		io.WriteString(responseWriter, `{"id":1}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	if _, err := mem.Add(context.Background(), "x", WithSessionID("test-sess")); err == nil {
		t.Fatal("expected the 500 error to surface immediately, got nil")
	}
	if ingestCalls != 1 || recordCalls != 0 {
		t.Fatalf("SDK must not retry or reroute; got ingest=%d records=%d", ingestCalls, recordCalls)
	}
}

// TestAddIngestNotFoundNeverFallsBackToRecords proves the 4.0.0 removal of the
// "OSS fallback": POST /api/v1/ingest is mounted unconditionally
// (server/router.go:135), so a 404 is a real error. It must surface as
// ErrNotFound, must NOT write through /records, and must not be remembered —
// the next Add tries /ingest again (no sticky "ingest unavailable" state).
func TestAddIngestNotFoundNeverFallsBackToRecords(t *testing.T) {
	ingestCalls := 0
	recordCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			ingestCalls++
			http.NotFound(responseWriter, request)
			return
		}
		recordCalls++
		io.WriteString(responseWriter, `{"id":1}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	for attemptNumber := 1; attemptNumber <= 2; attemptNumber++ {
		result, addErr := mem.Add(context.Background(), "x", WithSessionID("test-sess"))
		if !errors.Is(addErr, ErrNotFound) {
			t.Fatalf("attempt %d: want ErrNotFound, got result=%+v err=%v", attemptNumber, result, addErr)
		}
	}
	if ingestCalls != 2 || recordCalls != 0 {
		t.Fatalf("want 2 ingest calls and 0 records calls, got ingest=%d records=%d", ingestCalls, recordCalls)
	}
}

// TestAddResultModeNamesTheDoor pins the 4.0.0 Mode values: "ingest" for the
// /ingest door, "records" for /records (Add regular and Create). The old
// "cloud"/"oss" values must never come back.
func TestAddResultModeNamesTheDoor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			io.WriteString(responseWriter, `{"id":3,"records":[{"id":3,"type":"episodic","summary":"x"}]}`)
			return
		}
		io.WriteString(responseWriter, `{"id":4}`)
	}))
	defer server.Close()
	mem := NewMemory("k", WithURL(server.URL))

	ingestResult, ingestErr := mem.Add(context.Background(), "x", WithSessionID("s"))
	regularResult, regularErr := mem.Add(context.Background(), "x", WithSessionID("s"), WithMode("regular"))
	createResult, createErr := mem.Create(context.Background(), "s", "x")
	if ingestErr != nil || regularErr != nil || createErr != nil {
		t.Fatalf("unexpected errors: %v / %v / %v", ingestErr, regularErr, createErr)
	}
	for _, modeCase := range []struct {
		label    string
		gotMode  string
		wantMode string
	}{
		{"Add ingest", ingestResult.Mode, "ingest"},
		{"Add regular", regularResult.Mode, "records"},
		{"Create", createResult.Mode, "records"},
	} {
		if modeCase.gotMode != modeCase.wantMode {
			t.Errorf("%s: Mode = %q, want %q", modeCase.label, modeCase.gotMode, modeCase.wantMode)
		}
	}
}

// TestCreateSessionOmitsSessionIDLetsServerGenerate verifies POST /api/v1/sessions
// with no WithCreateSessionID sends an empty/omitted session_id (server generates),
// matching Python create_session() / MCP create_session / REST {}.
func TestCreateSessionOmitsSessionIDLetsServerGenerate(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/sessions" || request.Method != http.MethodPost {
			http.NotFound(responseWriter, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &captured)
		io.WriteString(responseWriter, `{"session_id":"registered-sess-1","metadata":{}}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	registeredSessionID, err := mem.CreateSession(context.Background())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if _, hasSessionID := captured["session_id"]; hasSessionID {
		t.Fatalf("omit WithCreateSessionID must not send session_id; got %v", captured["session_id"])
	}
	if registeredSessionID != "registered-sess-1" {
		t.Fatalf("expected registered id registered-sess-1, got %q", registeredSessionID)
	}
	if mem.SessionID() != "registered-sess-1" {
		t.Fatalf("client sessionUUID not updated: %q", mem.SessionID())
	}
}

// TestCreateSessionWithExplicitIDRegistersCallerUUID verifies WithCreateSessionID
// forwards the caller-chosen id (NewSession → CreateSession pattern).
func TestCreateSessionWithExplicitIDRegistersCallerUUID(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/sessions" || request.Method != http.MethodPost {
			http.NotFound(responseWriter, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &captured)
		chosenSessionID, _ := captured["session_id"].(string)
		io.WriteString(responseWriter, fmt.Sprintf(`{"session_id":%q,"metadata":{}}`, chosenSessionID))
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	mem.NewSession()
	localSessionID := mem.SessionID()
	registeredSessionID, err := mem.CreateSession(context.Background(), WithCreateSessionID(localSessionID))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if captured["session_id"] != localSessionID {
		t.Fatalf("expected session_id %q in payload, got %v", localSessionID, captured["session_id"])
	}
	if registeredSessionID != localSessionID {
		t.Fatalf("expected registered id %q, got %q", localSessionID, registeredSessionID)
	}
}

// TestAddWithoutCreateSessionFailsLoud verifies session-first: Add before
// CreateSession does not send an unregistered local uuid.
func TestAddWithoutCreateSessionFailsLoud(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		t.Fatalf("unexpected HTTP call before CreateSession: %s %s", request.Method, request.URL.Path)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	_, err := mem.Add(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected Add without CreateSession to fail")
	}
	if !strings.Contains(err.Error(), "create a session first") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestTryIngestAlwaysSendsSessionID verifies ingest payloads always include
// session_id after CreateSession (client default or WithSessionID override).
func TestTryIngestAlwaysSendsSessionID(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/sessions" {
			io.WriteString(responseWriter, `{"session_id":"registered-sess","metadata":{}}`)
			return
		}
		if !strings.HasSuffix(request.URL.Path, "/ingest") {
			http.NotFound(responseWriter, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &captured)
		io.WriteString(responseWriter, `{"id":9,"records":[{"id":9,"type":"episodic","summary":"hi"}]}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	if _, err := mem.CreateSession(context.Background()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if _, err := mem.Add(context.Background(), "hi"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if captured["session_id"] != mem.SessionID() {
		t.Fatalf("ingest must always send session_id; got %v want %q", captured["session_id"], mem.SessionID())
	}

	captured = nil
	overrideSessionID := "override-session-42"
	if _, err := mem.Add(context.Background(), "again", WithSessionID(overrideSessionID)); err != nil {
		t.Fatalf("Add with WithSessionID failed: %v", err)
	}
	if captured["session_id"] != overrideSessionID {
		t.Fatalf("WithSessionID override not forwarded: got %v", captured["session_id"])
	}
}

// TestAddWithModeRegularUsesRecordsPath verifies WithMode("regular") skips ingest
// even when ingest is available.
func TestAddWithModeRegularUsesRecordsPath(t *testing.T) {
	ingestCalls := 0
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			ingestCalls++
			io.WriteString(responseWriter, `{"id":1}`)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &captured)
		io.WriteString(responseWriter, `{"id":55}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	// Regular mode still requires a registered session (or explicit WithSessionID).
	result, err := mem.Add(context.Background(), "direct episodic",
		WithMode("regular"),
		WithSessionID("explicit-sess"),
	)
	if err != nil {
		t.Fatalf("Add regular failed: %v", err)
	}
	if ingestCalls != 0 {
		t.Fatalf("regular mode must not call ingest, got %d calls", ingestCalls)
	}
	if result.ID != 55 {
		t.Fatalf("expected record id 55, got %d", result.ID)
	}
	if captured["uuid"] != "explicit-sess" {
		t.Fatalf("records path must use explicit session uuid, got %v", captured["uuid"])
	}
}
