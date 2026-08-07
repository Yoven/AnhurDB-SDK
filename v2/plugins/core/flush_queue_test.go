package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// fakeAnhurServer is a minimal session-first AnhurDB: it accepts POST /api/v1/sessions and
// POST /api/v1/ingest and records every write, so tests can assert EXACTLY which session each
// chunk landed in. The property under test here is attribution, not delivery.
type fakeAnhurServer struct {
	mu                 sync.Mutex
	registeredSessions []string
	ingestedPayloads   []map[string]interface{}
}

func (fake *fakeAnhurServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		sessionID, _ := payload["session_id"].(string)
		fake.mu.Lock()
		fake.registeredSessions = append(fake.registeredSessions, sessionID)
		fake.mu.Unlock()
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"session_id": sessionID})
	})
	mux.HandleFunc("/api/v1/ingest", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		fake.mu.Lock()
		fake.ingestedPayloads = append(fake.ingestedPayloads, payload)
		fake.mu.Unlock()
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{"session_id": payload["session_id"], "id": 1})
	})
	return mux
}

// ingestCount returns how many ingest writes the fake server has received so far.
func (fake *fakeAnhurServer) ingestCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.ingestedPayloads)
}

// seedQueueChunk writes one chunk file into the flush queue with an explicit, sortable name so
// the test controls the drain order (flushQueue processes ReadDir's name-sorted output).
func seedQueueChunk(t *testing.T, cfg config, fileName string, chunkBody string) {
	t.Helper()
	if writeErr := os.WriteFile(filepath.Join(cfg.queueDir(), fileName), []byte(chunkBody), 0o600); writeErr != nil {
		t.Fatalf("seeding queue chunk %s: %v", fileName, writeErr)
	}
}

// quarantinedChunks reads back everything parked in quarantine for this state dir, so the
// tests below assert against the chunk's CONTENT and REASON rather than against a file path.
func quarantinedChunks(t *testing.T, cfg config) []queueItem {
	t.Helper()
	store, storeErr := queueStoreFor(cfg)
	if storeErr != nil {
		t.Fatalf("queue store unavailable: %v", storeErr)
	}
	quarantinedItems, listErr := store.ListQuarantined()
	if listErr != nil {
		t.Fatalf("listing quarantined chunks: %v", listErr)
	}
	return quarantinedItems
}

// TestFlushQueue_MarkerlessChunkIsQuarantinedNotInherited pins the inviolable rule
// "1 Claude session = 1 AnhurDB session" at the queue-drain boundary.
//
// Junior Tip [why the marked chunk goes FIRST, 2026-07-30]: the historical bug needed a
// precondition — a previous chunk's CreateSession leaves its session REGISTERED on the shared
// client.Memory, and the SDK's resolveWriteSessionID silently falls back to that registered
// session when Add carries no explicit id. Ordering the queue so the marked chunk drains first
// reproduces exactly that precondition. Before the 2026-07-30 fix, the markerless chunk was
// ingested into "sess-with-marker" and deleted from disk: a cross-conversation merge with zero
// trace. This test fails loudly if that behaviour ever comes back.
func TestFlushQueue_MarkerlessChunkIsQuarantinedNotInherited(t *testing.T) {
	fake := &fakeAnhurServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	cfg := config{stateDir: t.TempDir()}
	if mkdirErr := os.MkdirAll(cfg.queueDir(), 0o755); mkdirErr != nil {
		t.Fatalf("creating queue dir: %v", mkdirErr)
	}

	markedChunk := "Claude Code session sess-with-marker — conversation excerpt (2026-07-30T00:00:00Z):\nUSER: hello"
	markerlessChunk := "orphan text with no session header anywhere"
	markedName := "20260730T000000.000000001-1-1.txt"
	markerlessName := "20260730T000000.000000002-1-2.txt"
	seedQueueChunk(t, cfg, markedName, markedChunk)
	seedQueueChunk(t, cfg, markerlessName, markerlessChunk)

	mem := client.NewMemory("test-key",
		client.WithURL(server.URL),
		client.WithUserID("test-container"),
		client.WithTimeout(5*time.Second),
	)
	backlog := flushQueue(context.Background(), cfg, mem)

	// The marked chunk drained into ITS OWN session and left the queue.
	fake.mu.Lock()
	ingested := append([]map[string]interface{}{}, fake.ingestedPayloads...)
	fake.mu.Unlock()
	if len(ingested) != 1 {
		t.Fatalf("want exactly 1 ingest (the marked chunk), got %d: %v", len(ingested), ingested)
	}
	if got := ingested[0]["session_id"]; got != "sess-with-marker" {
		t.Errorf("marked chunk ingested under session %v, want sess-with-marker", got)
	}
	if got, _ := ingested[0]["content"].(string); got != markedChunk {
		t.Errorf("marked chunk content mutated on flush")
	}
	if _, statErr := os.Stat(filepath.Join(cfg.queueDir(), markedName)); !os.IsNotExist(statErr) {
		t.Errorf("flushed chunk still sitting in the queue")
	}

	// THE invariant: the markerless chunk must not have been written under ANY session —
	// least of all the session the previous chunk registered on the shared client.
	for _, payload := range ingested {
		if content, _ := payload["content"].(string); content == markerlessChunk {
			t.Fatalf("markerless chunk was persisted under session %v — cross-conversation merge", payload["session_id"])
		}
	}

	// It was quarantined INTACT (no silent loss) and removed from the retry queue.
	//
	// Junior Tip [a quarentena mudou de lugar, não de sentido, 2026-07-31]: até a fila
	// com estado, quarentena era um ARQUIVO em queue/quarantine/. Agora é um ESTADO numa
	// linha. O invariante testado é o mesmo — o chunk é preservado inteiro, nunca é
	// reenviado, e continua visível até um humano resolver — então este teste segue a
	// prova para onde ela vive, em vez de fixar o mecanismo antigo.
	quarantinedItems := quarantinedChunks(t, cfg)
	if len(quarantinedItems) != 1 {
		t.Fatalf("quarantined chunks = %d, want 1", len(quarantinedItems))
	}
	if quarantinedItems[0].Content != markerlessChunk {
		t.Errorf("quarantined chunk content mutated")
	}
	if quarantinedItems[0].LastError == "" {
		t.Error("quarantined chunk carries no reason — a human cannot resolve what it cannot explain")
	}
	if _, statErr := os.Stat(filepath.Join(cfg.queueDir(), markerlessName)); !os.IsNotExist(statErr) {
		t.Errorf("markerless chunk left as a queue file after quarantine (would migrate/retry forever)")
	}

	// And it is surfaced in the backlog recall renders, separate from the retryable count.
	if backlog.quarantinedChunkCount != 1 {
		t.Errorf("quarantinedChunkCount = %d, want 1", backlog.quarantinedChunkCount)
	}
	if backlog.oldestQuarantined != "2026-07-30T00:00:00Z" {
		t.Errorf("oldestQuarantined = %q, want the chunk's queue stamp 2026-07-30T00:00:00Z", backlog.oldestQuarantined)
	}
	if backlog.chunkCount != 0 {
		t.Errorf("chunkCount = %d, want 0 (quarantined chunks are not retryable backlog)", backlog.chunkCount)
	}

	// A second flush keeps the quarantine visible WITHOUT resending or re-touching anything.
	secondBacklog := flushQueue(context.Background(), cfg, mem)
	if secondBacklog.quarantinedChunkCount != 1 {
		t.Errorf("second flush lost the quarantine warning: count = %d, want 1", secondBacklog.quarantinedChunkCount)
	}
	if got := fake.ingestCount(); got != 1 {
		t.Errorf("second flush re-sent chunks: total ingests = %d, want 1", got)
	}
}

// TestFlushQueue_MarkerlessChunkAloneMakesNoWrites pins the other pre-fix failure mode: with no
// session registered on the client yet, the markerless Add failed inside the SDK ("session_id is
// required") and the chunk retried forever as permanent fake backlog. Now a chunk with no
// provable owner must be quarantined WITHOUT a single network write — it never reaches the
// server at all.
func TestFlushQueue_MarkerlessChunkAloneMakesNoWrites(t *testing.T) {
	fake := &fakeAnhurServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	cfg := config{stateDir: t.TempDir()}
	if mkdirErr := os.MkdirAll(cfg.queueDir(), 0o755); mkdirErr != nil {
		t.Fatalf("creating queue dir: %v", mkdirErr)
	}
	markerlessChunk := "orphan chunk, fresh client, nothing registered"
	markerlessName := "20260730T000001.000000001-1-1.txt"
	seedQueueChunk(t, cfg, markerlessName, markerlessChunk)

	mem := client.NewMemory("test-key",
		client.WithURL(server.URL),
		client.WithUserID("test-container"),
		client.WithTimeout(5*time.Second),
	)
	backlog := flushQueue(context.Background(), cfg, mem)

	fake.mu.Lock()
	sessionCalls, ingestCalls := len(fake.registeredSessions), len(fake.ingestedPayloads)
	fake.mu.Unlock()
	if sessionCalls != 0 || ingestCalls != 0 {
		t.Errorf("markerless chunk caused network writes (sessions=%d ingests=%d), want none", sessionCalls, ingestCalls)
	}
	quarantinedItems := quarantinedChunks(t, cfg)
	if len(quarantinedItems) != 1 || quarantinedItems[0].Content != markerlessChunk {
		t.Errorf("markerless chunk not quarantined intact: %+v", quarantinedItems)
	}
	if backlog.quarantinedChunkCount != 1 || backlog.chunkCount != 0 {
		t.Errorf("backlog = {quarantined:%d retryable:%d}, want {1, 0}", backlog.quarantinedChunkCount, backlog.chunkCount)
	}
}
