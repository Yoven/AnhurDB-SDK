package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anhurdb/sdk-go/v2/models"
)

// TestRecentMemoriesEmptyEnvelope reproduces the empty-manifest crash: the
// server returns {"records": []} (an OBJECT). The old code fell through to a
// bare-array Unmarshal and errored. It must now yield an empty slice, no error.
func TestRecentMemoriesEmptyEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, `{"count":0,"has_more":false,"limit":5,"offset":0,"records":[]}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	records, err := mem.RecentMemories(context.Background(), 5)
	if err != nil {
		t.Fatalf("empty envelope must not error, got: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records, got %d", len(records))
	}
}

// TestRecentMemoriesBareArray verifies the bare-array shape still parses.
func TestRecentMemoriesBareArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		io.WriteString(responseWriter, `[]`)
	}))
	defer server.Close()
	mem := NewMemory("k", WithURL(server.URL))
	records, err := mem.RecentMemories(context.Background(), 5)
	if err != nil || len(records) != 0 {
		t.Fatalf("bare empty array failed: err=%v len=%d", err, len(records))
	}
}

// TestRecentMemoriesPopulatedEnvelope verifies envelope rows map into Record.
func TestRecentMemoriesPopulatedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		io.WriteString(responseWriter, `{"records":[{"id":7,"uuid":"u","type":"episodic","summary":"s","metadata":"{}","status":"saved"}]}`)
	}))
	defer server.Close()
	mem := NewMemory("k", WithURL(server.URL))
	records, err := mem.RecentMemories(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(records) != 1 || records[0].ID != 7 || records[0].Type != models.MemoryType("episodic") {
		t.Fatalf("bad mapping: %+v", records)
	}
}

// TestAddForwardsScoreTypeMetadataOSS verifies the OSS records path forwards
// caller score/type and merges metadata (container_tag still owned by SDK).
func TestAddForwardsScoreTypeMetadataOSS(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			// Force the OSS fallback path.
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
		WithScore(9),
		WithType("semantic"),
		WithMetadata(map[string]interface{}{"source": "import", "lang": "go"}),
	)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if result.ID != 123 {
		t.Fatalf("expected id 123, got %d", result.ID)
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

// TestAddDefaultsBackwardCompatible verifies Add(ctx, text) with no options
// still sends the historical defaults (score=5, type=episodic).
func TestAddDefaultsBackwardCompatible(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			http.NotFound(responseWriter, request)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &captured)
		io.WriteString(responseWriter, `{"id":1}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	if _, err := mem.Add(context.Background(), "hi"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if captured["score"] != float64(5) {
		t.Fatalf("default score should be 5, got %v", captured["score"])
	}
	if captured["type"] != "episodic" {
		t.Fatalf("default type should be episodic, got %v", captured["type"])
	}
}

// TestAddRetriesTransientThenSucceeds drives the full Add retry path against a
// server that returns not_leader twice (HTTP 500) then succeeds.
func TestAddRetriesTransientThenSucceeds(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/ingest") {
			http.NotFound(responseWriter, request)
			return
		}
		calls++
		if calls < 3 {
			responseWriter.WriteHeader(http.StatusInternalServerError)
			io.WriteString(responseWriter, `{"error":"not_leader"}`)
			return
		}
		io.WriteString(responseWriter, `{"id":555}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	result, err := mem.Add(context.Background(), "x")
	if err != nil {
		t.Fatalf("Add should have recovered after retries: %v", err)
	}
	if result.ID != 555 {
		t.Fatalf("expected id 555, got %d", result.ID)
	}
	// 2 ingest-404 probes are cached after the first, so record calls == 3.
	if calls != 3 {
		t.Fatalf("expected 3 record-create calls, got %d", calls)
	}
}
