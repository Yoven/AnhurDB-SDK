package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// session_limit_test.go — a full session (ADR-0033) must be LOUD and NAMED on
// every path — first attempt, queue drain, `status` — while the queue keeps
// every chunk (nothing dropped, nothing moved to another session).

// fullSessionServer registers sessions and refuses every ingest with a 409.
type fullSessionServer struct {
	mu            sync.Mutex
	refusalBody   string
	ingestedCount int
}

func (fake *fullSessionServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{"session_id": payload["session_id"]})
	})
	mux.HandleFunc("/api/v1/ingest", func(responseWriter http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		fake.ingestedCount++
		fake.mu.Unlock()
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusConflict)
		_, _ = responseWriter.Write([]byte(fake.refusalBody))
	})
	return mux
}

const structuredRefusal = `{"error":"session has reached the maximum of 500 records","code":"session_record_limit","limit":500,"count":503,"session_uuid":"sess-full"}`
const legacyRefusal = `{"error":"session has reached the maximum of 500 records"}`

// readPluginLog returns the whole plugin.log written under cfg.
func readPluginLog(t *testing.T, cfg config) string {
	t.Helper()
	logBytes, _ := os.ReadFile(cfg.logPath())
	return string(logBytes)
}

// statusReport runs the status subcommand into a string.
func statusReport(cfg config) string {
	var output bytes.Buffer
	cmdStatus(cfg, &output)
	return output.String()
}

// The drain path, against a NEW server: one clear line, the chunk stays queued
// with a readable cause, and status names the session and both numbers.
func TestDrainRefusedBySessionLimitIsNamedAndKept(t *testing.T) {
	fake := &fullSessionServer{refusalBody: structuredRefusal}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newTestConfig(t)
	queueChunk(cfg, chunkFor("sess-full", "turn one"))

	backlog := flushQueue(context.Background(), cfg, newTestMemory(t, server.URL))

	if backlog.chunkCount != 1 {
		t.Fatalf("chunkCount = %d, want 1 — a refused chunk must stay queued", backlog.chunkCount)
	}
	pluginLog := readPluginLog(t, cfg)
	if !strings.Contains(pluginLog, "SESSION RECORD LIMIT: session=sess-full limit=500 count=503") {
		t.Errorf("missing the clear session-limit line:\n%s", pluginLog)
	}
	report := statusReport(cfg)
	for _, wanted := range []string{
		"SESSION RECORD LIMIT: session=sess-full chunks=1 limit=500 count=503",
		"verdict: DEGRADED — session record limit: session sess-full (1 chunk(s) queued, limit 500, count 503)",
	} {
		if !strings.Contains(report, wanted) {
			t.Errorf("status lacks %q:\n%s", wanted, report)
		}
	}
}

// The FIRST attempt (persist), against an OLD text-only server: the line says
// "count=unknown" rather than inventing a number, every chunk is queued with
// the cause, and only ONE request is spent on a session that refuses them all.
func TestFirstAttemptRefusalQueuesAllChunksWithCause(t *testing.T) {
	shortenRetryBackoffForTest(t)
	fake := &fullSessionServer{refusalBody: legacyRefusal}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newTestConfig(t)
	cfg.maxChunkChars = 20

	allDelivered := deliverSessionText(context.Background(), cfg, newTestMemory(t, server.URL),
		"sess-old", "first paragraph here\n\nsecond paragraph here\n\nthird paragraph here")

	if allDelivered {
		t.Fatal("a refused session reported success")
	}
	if fake.ingestedCount != 1 {
		t.Errorf("ingest requests = %d, want 1 (the rest go straight to the queue)", fake.ingestedCount)
	}
	pluginLog := readPluginLog(t, cfg)
	if strings.Count(pluginLog, "SESSION RECORD LIMIT:") != 1 {
		t.Errorf("want exactly one clear session-limit line, log:\n%s", pluginLog)
	}
	if !strings.Contains(pluginLog, "deliver: chunk not delivered") ||
		!strings.Contains(pluginLog, "session=sess-old limit=500 count=unknown") {
		t.Errorf("first-attempt lines missing:\n%s", pluginLog)
	}
	store, storeErr := queueStoreFor(cfg)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	pendingItems, _ := store.ListPending()
	if len(pendingItems) < 2 {
		t.Fatalf("pending = %d, want every chunk kept", len(pendingItems))
	}
	for _, item := range pendingItems {
		if item.SessionID != "sess-old" || !strings.HasPrefix(item.LastError, "SESSION RECORD LIMIT: session=sess-old") {
			t.Errorf("item kept under session %q with cause %q", item.SessionID, item.LastError)
		}
	}
	if report := statusReport(cfg); !strings.Contains(report, "limit 500, count unknown") {
		t.Errorf("status must name the old-server numbers honestly:\n%s", report)
	}
}

// Rows queued BEFORE this change carry only the raw SDK error. The owner's live
// queue may hold exactly those, so status must still name them.
func TestStatusRecognisesLegacyQueueCause(t *testing.T) {
	cfg := newTestConfig(t)
	store, storeErr := queueStoreFor(cfg)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	if enqueueErr := store.EnqueueChunk("sess-legacy", chunkFor("sess-legacy", "x")); enqueueErr != nil {
		t.Fatal(enqueueErr)
	}
	claimed, _ := store.ClaimNextBatch(10)
	legacyCause := &legacyError{text: `AnhurDB API error (HTTP 409): {"error":"session has reached the maximum of 500 records"}`}
	if failErr := store.MarkFailed(claimed[0].ID, 0, legacyCause); failErr != nil {
		t.Fatal(failErr)
	}

	report := statusReport(cfg)

	if !strings.Contains(report, "session=sess-legacy chunks=1 limit=500 count=unknown") ||
		!strings.Contains(report, "verdict: DEGRADED") {
		t.Errorf("legacy session-limit row not named:\n%s", report)
	}
}

// An empty state dir is OK — and status must not CREATE queue.db to say so.
func TestStatusOnEmptyStateIsOKAndSideEffectFree(t *testing.T) {
	cfg := newTestConfig(t)

	report := statusReport(cfg)

	if !strings.Contains(report, "verdict: OK") {
		t.Errorf("empty state should be OK:\n%s", report)
	}
	if _, statErr := os.Stat(cfg.stateDir + "/queue.db"); !os.IsNotExist(statErr) {
		t.Error("status created queue.db — a diagnostic must not change what it inspects")
	}
	if _, statErr := os.Stat(cfg.logPath()); !os.IsNotExist(statErr) {
		t.Error("status wrote plugin.log — it would feed the watcher's failure stream")
	}
}

// legacyError is a plain error with fixed text.
type legacyError struct{ text string }

func (legacy *legacyError) Error() string { return legacy.text }
