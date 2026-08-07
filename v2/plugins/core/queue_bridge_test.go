package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// selectiveFakeServer is a session-first AnhurDB that can REJECT writes for chosen
// sessions, so the drain's failure handling can be observed instead of assumed.
type selectiveFakeServer struct {
	mu               sync.Mutex
	rejectedSessions map[string]bool
	deliveredOrder   []string // "session|content", in the order the server accepted them
}

func newSelectiveFakeServer(rejected ...string) *selectiveFakeServer {
	fake := &selectiveFakeServer{rejectedSessions: map[string]bool{}}
	for _, sessionID := range rejected {
		fake.rejectedSessions[sessionID] = true
	}
	return fake
}

func (fake *selectiveFakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"session_id": payload["session_id"].(string)})
	})
	mux.HandleFunc("/api/v1/ingest", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		sessionID, _ := payload["session_id"].(string)
		content, _ := payload["content"].(string)

		fake.mu.Lock()
		rejected := fake.rejectedSessions[sessionID]
		if !rejected {
			fake.deliveredOrder = append(fake.deliveredOrder, sessionID+"|"+content)
		}
		fake.mu.Unlock()

		if rejected {
			http.Error(responseWriter, `{"error":"session has reached the maximum of 500 records"}`, http.StatusConflict)
			return
		}
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{"session_id": sessionID, "id": 1})
	})
	return mux
}

func (fake *selectiveFakeServer) delivered() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string{}, fake.deliveredOrder...)
}

// accept stops rejecting a session, so a test can prove recovery after an outage.
func (fake *selectiveFakeServer) accept(sessionID string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	delete(fake.rejectedSessions, sessionID)
}

// newTestConfig builds a config on a fresh state dir with the queue directory in place.
func newTestConfig(t *testing.T) config {
	t.Helper()
	cfg := config{stateDir: t.TempDir()}
	if mkdirErr := os.MkdirAll(cfg.queueDir(), 0o755); mkdirErr != nil {
		t.Fatalf("creating queue dir: %v", mkdirErr)
	}
	return cfg
}

func newTestMemory(t *testing.T, serverURL string) *client.Memory {
	t.Helper()
	return client.NewMemory("test-key",
		client.WithURL(serverURL),
		client.WithUserID("test-container"),
		client.WithTimeout(5*time.Second),
	)
}

// chunkFor builds a chunk carrying the session header the engine writes, since the
// header is the ONLY proof of ownership a queued chunk has.
func chunkFor(sessionID string, body string) string {
	return "Claude Code session " + sessionID + " — conversation excerpt (2026-07-30T00:00:00Z):\n" + body
}

// TestMigrationPreservesConversationOrder is the migration's whole point: the flat-file
// queue encoded the instant in the FILENAME, and the state queue orders by created_at.
// If migration stamped everything "now", turns that were correctly ordered on disk would
// come out shuffled — the migration would destroy exactly what it exists to save.
func TestMigrationPreservesConversationOrder(t *testing.T) {
	fake := newSelectiveFakeServer()
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newTestConfig(t)

	// Escritos fora de ordem DE PROPÓSITO: o nome do arquivo é a verdade, não a
	// ordem em que o teste os criou.
	seedQueueChunk(t, cfg, "20260730T000003.000000000-1-3.txt", chunkFor("sess-A", "terceiro"))
	seedQueueChunk(t, cfg, "20260730T000001.000000000-1-1.txt", chunkFor("sess-A", "primeiro"))
	seedQueueChunk(t, cfg, "20260730T000002.000000000-1-2.txt", chunkFor("sess-A", "segundo"))

	backlog := flushQueue(context.Background(), cfg, newTestMemory(t, server.URL))

	deliveredContents := fake.delivered()
	if len(deliveredContents) != 3 {
		t.Fatalf("entregues = %d, want 3: %v", len(deliveredContents), deliveredContents)
	}
	for index, expectedBody := range []string{"primeiro", "segundo", "terceiro"} {
		if !strings.HasSuffix(deliveredContents[index], expectedBody) {
			t.Errorf("posicao %d = %q, want algo terminando em %q — a migracao embaralhou a conversa",
				index, deliveredContents[index], expectedBody)
		}
	}
	if backlog.chunkCount != 0 {
		t.Errorf("chunkCount = %d apos dreno completo, want 0", backlog.chunkCount)
	}

	// Nenhum arquivo órfão: o que migrou saiu do disco, senão migraria de novo e duplicaria.
	leftoverEntries, _ := os.ReadDir(cfg.queueDir())
	for _, leftover := range leftoverEntries {
		if strings.HasSuffix(leftover.Name(), ".txt") {
			t.Errorf("arquivo antigo sobrou apos migracao: %s (migraria de novo = duplicata)", leftover.Name())
		}
	}
}

// TestFailureBlocksItsOwnSessionOnly pins the head-of-line rule: within one session the
// order IS the conversation, so a failure must stop that session; across sessions there
// is no order to protect, and blocking everything would let one permanently-rejected
// session (HTTP 409, the real 2026-07-03 case) freeze the entire queue forever.
func TestFailureBlocksItsOwnSessionOnly(t *testing.T) {
	fake := newSelectiveFakeServer("sess-blocked")
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newTestConfig(t)

	seedQueueChunk(t, cfg, "20260730T000001.000000000-1-1.txt", chunkFor("sess-blocked", "turno-1"))
	seedQueueChunk(t, cfg, "20260730T000002.000000000-1-2.txt", chunkFor("sess-blocked", "turno-2"))
	seedQueueChunk(t, cfg, "20260730T000003.000000000-1-3.txt", chunkFor("sess-open", "outra-conversa"))

	backlog := flushQueue(context.Background(), cfg, newTestMemory(t, server.URL))

	deliveredContents := fake.delivered()
	if len(deliveredContents) != 1 || !strings.HasSuffix(deliveredContents[0], "outra-conversa") {
		t.Fatalf("entregues = %v, want so a sessao aberta — uma sessao travada nao pode congelar as outras",
			deliveredContents)
	}
	// turno-2 nao pode ter sido TENTADO: entregá-lo com turno-1 preso escreveria a
	// conversa fora de ordem, e nenhuma consolidacao posterior conserta isso.
	for _, delivered := range deliveredContents {
		if strings.HasSuffix(delivered, "turno-2") {
			t.Error("turno-2 passou na frente do turno-1 preso — a conversa foi gravada fora de ordem")
		}
	}
	if backlog.chunkCount != 2 {
		t.Errorf("chunkCount = %d, want 2 (os dois turnos da sessao travada seguem na fila)", backlog.chunkCount)
	}
	if backlog.lastFlushErr == nil {
		t.Error("lastFlushErr vazio — o recall precisa poder dizer POR QUE esta preso")
	}
}

// TestBlockedItemIsNotPunishedForItsNeighbour: the second turn of a blocked session was
// never attempted, so it must come back WITHOUT a retry charge. Charging it would inflate
// the backoff for an error that was not its own and delay recovery.
func TestBlockedItemIsNotPunishedForItsNeighbour(t *testing.T) {
	fake := newSelectiveFakeServer("sess-blocked")
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newTestConfig(t)

	seedQueueChunk(t, cfg, "20260730T000001.000000000-1-1.txt", chunkFor("sess-blocked", "turno-1"))
	seedQueueChunk(t, cfg, "20260730T000002.000000000-1-2.txt", chunkFor("sess-blocked", "turno-2"))

	flushQueue(context.Background(), cfg, newTestMemory(t, server.URL))

	store, _ := queueStoreFor(cfg)
	var untriedRetryCount int
	if scanErr := store.database.QueryRow(
		`SELECT retry_count FROM pending_memory WHERE content LIKE '%turno-2%'`,
	).Scan(&untriedRetryCount); scanErr != nil {
		t.Fatalf("lendo o turno nao tentado: %v", scanErr)
	}
	if untriedRetryCount != 0 {
		t.Errorf("retry_count do turno nao tentado = %d, want 0 — ele nao falhou, so esperou",
			untriedRetryCount)
	}
}

// TestQueueRecoversWhenTheBackendComesBack is the promise the whole design exists for:
// an outage delays memory, it never destroys it.
func TestQueueRecoversWhenTheBackendComesBack(t *testing.T) {
	fake := newSelectiveFakeServer("sess-A")
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newTestConfig(t)

	seedQueueChunk(t, cfg, "20260730T000001.000000000-1-1.txt", chunkFor("sess-A", "turno-1"))
	seedQueueChunk(t, cfg, "20260730T000002.000000000-1-2.txt", chunkFor("sess-A", "turno-2"))

	firstBacklog := flushQueue(context.Background(), cfg, newTestMemory(t, server.URL))
	if firstBacklog.chunkCount != 2 {
		t.Fatalf("durante a queda: chunkCount = %d, want 2", firstBacklog.chunkCount)
	}

	// Backend volta. O backoff do item que falhou ainda vale, então o teste o zera —
	// o que se prova aqui é a recuperação, não a espera (o backoff tem teste próprio).
	fake.accept("sess-A")
	store, _ := queueStoreFor(cfg)
	if _, resetErr := store.database.Exec(
		`UPDATE pending_memory SET next_attempt_at = ?`, "2000-01-01T00:00:00Z",
	); resetErr != nil {
		t.Fatalf("zerando o backoff: %v", resetErr)
	}

	secondBacklog := flushQueue(context.Background(), cfg, newTestMemory(t, server.URL))
	deliveredContents := fake.delivered()
	if len(deliveredContents) != 2 {
		t.Fatalf("apos a recuperacao entregues = %v, want os 2 turnos", deliveredContents)
	}
	if !strings.HasSuffix(deliveredContents[0], "turno-1") || !strings.HasSuffix(deliveredContents[1], "turno-2") {
		t.Errorf("ordem apos recuperacao = %v, want turno-1 antes de turno-2", deliveredContents)
	}
	if secondBacklog.chunkCount != 0 {
		t.Errorf("chunkCount = %d apos recuperacao, want 0", secondBacklog.chunkCount)
	}
}

// TestFlatFileIsTheFloorWhenTheStoreCannotOpen is today's central lesson made executable:
// a safety net that shares a failure mode with the thing it protects is not a net. If the
// state queue cannot open, the memory must STILL land on disk.
func TestFlatFileIsTheFloorWhenTheStoreCannotOpen(t *testing.T) {
	cfg := newTestConfig(t)
	// queue.db ocupado por um DIRETORIO: o SQLite nao consegue abrir, e nada no
	// caminho de escrita pode depender disso.
	if mkdirErr := os.MkdirAll(filepath.Join(cfg.stateDir, "queue.db"), 0o755); mkdirErr != nil {
		t.Fatalf("bloqueando o caminho do banco: %v", mkdirErr)
	}

	queueChunk(cfg, chunkFor("sess-A", "memoria que nao pode sumir"))

	queueEntries, readErr := os.ReadDir(cfg.queueDir())
	if readErr != nil {
		t.Fatalf("lendo a fila de arquivos: %v", readErr)
	}
	foundOnDisk := false
	for _, queueEntry := range queueEntries {
		if !strings.HasSuffix(queueEntry.Name(), ".txt") {
			continue
		}
		savedBytes, _ := os.ReadFile(filepath.Join(cfg.queueDir(), queueEntry.Name()))
		if strings.Contains(string(savedBytes), "memoria que nao pode sumir") {
			foundOnDisk = true
		}
	}
	if !foundOnDisk {
		t.Fatal("banco indisponivel custou uma memoria — o piso de arquivo achatado nao rodou")
	}
}

// TestMarkerlessChunkIsQuarantinedAtTheDoor: the session is extracted when the content is
// in hand, not re-derived on every drain. A chunk whose owner cannot be proven never
// becomes deliverable in the first place.
func TestMarkerlessChunkIsQuarantinedAtTheDoor(t *testing.T) {
	cfg := newTestConfig(t)
	queueChunk(cfg, "texto orfao, sem cabecalho de sessao em lugar nenhum")

	store, storeErr := queueStoreFor(cfg)
	if storeErr != nil {
		t.Fatalf("abrindo a fila: %v", storeErr)
	}
	quarantinedItems, listErr := store.ListQuarantined()
	if listErr != nil {
		t.Fatalf("listando quarentena: %v", listErr)
	}
	if len(quarantinedItems) != 1 {
		t.Fatalf("itens em quarentena = %d, want 1", len(quarantinedItems))
	}
	if quarantinedItems[0].Content != "texto orfao, sem cabecalho de sessao em lugar nenhum" {
		t.Error("conteudo do chunk em quarentena foi alterado — ele tem de ser preservado INTEIRO")
	}
	claimed, _ := store.ClaimNextBatch(10)
	if len(claimed) != 0 {
		t.Error("chunk sem sessao entrou na fila de envio — adivinhar a sessao funde conversas")
	}
}
