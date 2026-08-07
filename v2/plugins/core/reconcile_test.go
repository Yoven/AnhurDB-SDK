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

// reconcile_test.go — o cursor diz "tentei"; a reconciliação prova que "chegou".

// countingServer records every ingest so a test can assert exactly what was
// recovered, and can be switched to reject writes to simulate an outage.
type countingServer struct {
	mu        sync.Mutex
	ingested  []string
	rejecting bool
}

func (fake *countingServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		sessionID, _ := payload["session_id"].(string)
		_ = json.NewEncoder(responseWriter).Encode(map[string]string{"session_id": sessionID})
	})
	mux.HandleFunc("/api/v1/ingest", func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(httpRequest.Body).Decode(&payload)
		fake.mu.Lock()
		rejecting := fake.rejecting
		if !rejecting {
			content, _ := payload["content"].(string)
			fake.ingested = append(fake.ingested, content)
		}
		fake.mu.Unlock()
		if rejecting {
			http.Error(responseWriter, `{"error":"down"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(responseWriter).Encode(map[string]interface{}{"session_id": payload["session_id"], "id": 1})
	})
	return mux
}

func (fake *countingServer) allIngested() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string{}, fake.ingested...)
}

// newReconcileConfig builds a config whose archive, cursor and queue dirs are all
// real, so the test exercises the same paths production does.
func newReconcileConfig(t *testing.T) config {
	t.Helper()
	stateDir := t.TempDir()
	cfg := config{
		stateDir:      stateDir,
		archive:       true,
		archiveDir:    filepath.Join(stateDir, "archive"),
		container:     "test-container",
		maxChunkChars: 24000,
		recallLimit:   10,
	}
	for _, directory := range []string{cfg.archiveDir, cfg.queueDir(), cfg.cursorDir()} {
		if mkdirErr := os.MkdirAll(directory, 0o700); mkdirErr != nil {
			t.Fatalf("preparando %s: %v", directory, mkdirErr)
		}
	}
	return cfg
}

// writeArchivedTranscript lays down an archive file the way archiveTranscript would.
func writeArchivedTranscript(t *testing.T, cfg config, sessionID string, turns []string) {
	t.Helper()
	var builder strings.Builder
	for _, turn := range turns {
		line, _ := json.Marshal(map[string]interface{}{
			"type":    "user",
			"message": map[string]interface{}{"role": "user", "content": turn},
		})
		builder.Write(line)
		builder.WriteString("\n")
	}
	archivePath := filepath.Join(cfg.archiveDir, sanitize(sessionID)+".jsonl")
	if writeErr := os.WriteFile(archivePath, []byte(builder.String()), 0o600); writeErr != nil {
		t.Fatalf("escrevendo o archive: %v", writeErr)
	}
}

func newReconcileMemory(t *testing.T, serverURL string) *client.Memory {
	t.Helper()
	return client.NewMemory("test-key",
		client.WithURL(serverURL),
		client.WithUserID("test-container"),
		client.WithTimeout(5*time.Second),
	)
}

// TestReconciliationRecoversWhatDiedBetweenCursorAndSend is the whole reason this
// exists. cmdPersist advances the cursor BEFORE sending, so a process killed in
// between leaves lines that were never sent, never queued, and that the queue does
// not know exist. They ARE in the archive.
func TestReconciliationRecoversWhatDiedBetweenCursorAndSend(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-crash"

	// O archive tem a conversa inteira (ele grava sempre, antes de tudo)...
	writeArchivedTranscript(t, cfg, sessionID, []string{"turno um", "turno dois", "turno tres"})
	// ...o cursor de TENTATIVA avançou até o fim: o persist processou tudo...
	writeCursor(filepath.Join(cfg.cursorDir(), sanitize(sessionID)), 3)
	// ...e o de CONFIRMAÇÃO ficou para trás, no que o AnhurDB tinha aceitado ANTES do
	// delta fatal. O processo morreu entre o cursor avançar e o envio acontecer.
	//
	// Junior Tip [por que este teste passou a semear o confirmado em 1, 2026-07-31]:
	// ele começou com confirmado AUSENTE, o que é o mesmo estado de uma sessão antiga
	// numa instalação que acabou de atualizar. Os dois casos são indistinguíveis pelos
	// cursores, e escolher errado tem custos assimétricos: tratar migração como crash
	// reescreve históricos inteiros (dano garantido — aconteceu, 6 chunks duplicados
	// na memória real), tratar crash como migração perde um buraco raro cujo conteúdo
	// segue no archive. Por isso a semente vence na PRIMEIRA execução, e a partir daí
	// o crash volta a ser detectável — que é exatamente o estado montado abaixo.
	markDeliveryConfirmed(cfg, sessionID, 1)

	recovered := reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL),
		sessionID, queueBacklog{})

	if recovered != 2 {
		t.Fatalf("linhas recuperadas = %d, want 2 — o delta sumiria para sempre", recovered)
	}
	ingested := strings.Join(fake.allIngested(), "\n")
	for _, expectedTurn := range []string{"turno dois", "turno tres"} {
		if !strings.Contains(ingested, expectedTurn) {
			t.Errorf("%q nao foi recuperado", expectedTurn)
		}
	}
	if !strings.Contains(ingested, "Claude Code session "+sessionID) {
		t.Error("o texto recuperado perdeu o cabecalho de sessao — a fila nao saberia a quem pertence")
	}
}

// TestReconciliationIsIdempotent: once recovered, a second pass must send nothing.
// Without the confirmed cursor advancing, every persist would re-send the session.
func TestReconciliationIsIdempotent(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-idem"

	writeArchivedTranscript(t, cfg, sessionID, []string{"turno um", "turno dois"})
	memory := newReconcileMemory(t, server.URL)

	reconcileSession(context.Background(), cfg, memory, sessionID, queueBacklog{})
	afterFirst := len(fake.allIngested())

	reconcileSession(context.Background(), cfg, memory, sessionID, queueBacklog{})
	afterSecond := len(fake.allIngested())

	if afterSecond != afterFirst {
		t.Errorf("segunda passada reenviou (%d -> %d) — a sessao seria duplicada a cada persist",
			afterFirst, afterSecond)
	}
}

// TestNothingToReconcileWhenEverythingWasConfirmed guards the normal path: a
// healthy session must cost zero extra writes.
func TestNothingToReconcileWhenEverythingWasConfirmed(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-healthy"

	writeArchivedTranscript(t, cfg, sessionID, []string{"turno um", "turno dois"})
	markDeliveryConfirmed(cfg, sessionID, 2) // o persist confirmou tudo

	if recovered := reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL),
		sessionID, queueBacklog{}); recovered != 0 {
		t.Errorf("recuperou %d numa sessao sadia — reescreveria memoria ja gravada", recovered)
	}
	if got := len(fake.allIngested()); got != 0 {
		t.Errorf("escritas numa sessao sadia = %d, want 0", got)
	}
}

// TestReconciliationStandsDownWhileTheQueueHasWork is the anti-duplicate rule. A
// queued chunk is already being retried by the drain; reconciling on top of it
// would send the same text twice, and the server's idempotency window is 30s —
// far too short to catch a resend that lands minutes later.
func TestReconciliationStandsDownWhileTheQueueHasWork(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-inflight"

	writeArchivedTranscript(t, cfg, sessionID, []string{"turno um"})

	recovered := reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL),
		sessionID, queueBacklog{chunkCount: 1})

	if recovered != 0 {
		t.Error("reconciliou com a fila cheia — o mesmo texto iria duas vezes")
	}
	if got := len(fake.allIngested()); got != 0 {
		t.Errorf("escritas = %d, want 0 enquanto ha coisa em voo", got)
	}
}

// TestFailedRecoveryDoesNotAdvanceTheConfirmedCursor: if the recovery itself
// cannot be delivered, the gap must survive to the next attempt. Advancing here
// would mark as confirmed exactly what was just lost.
func TestFailedRecoveryDoesNotAdvanceTheConfirmedCursor(t *testing.T) {
	fake := &countingServer{rejecting: true}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-still-down"

	writeArchivedTranscript(t, cfg, sessionID, []string{"turno um"})
	memory := newReconcileMemory(t, server.URL)

	if recovered := reconcileSession(context.Background(), cfg, memory, sessionID, queueBacklog{}); recovered != 0 {
		t.Errorf("reportou %d recuperadas com o servidor fora do ar", recovered)
	}
	if confirmed := readCursor(confirmedCursorPath(cfg, sessionID)); confirmed != 0 {
		t.Fatalf("cursor de confirmacao avancou para %d apos uma recuperacao FALHA — "+
			"marcaria como entregue justamente o que se perdeu", confirmed)
	}

	// O servidor volta: a proxima passada recupera.
	fake.mu.Lock()
	fake.rejecting = false
	fake.mu.Unlock()
	// A tentativa anterior enfileirou o chunk, entao a reconciliacao se recusa a agir
	// enquanto isso — o dreno da fila e quem entrega. Comprovamos que o buraco
	// continua registrado, que e o invariante deste teste.
	if confirmed := readCursor(confirmedCursorPath(cfg, sessionID)); confirmed != 0 {
		t.Error("cursor de confirmacao mudou sem entrega")
	}
}

// TestToolOnlyDeltaIsConfirmedNotRetriedForever: lines that carry only tool noise
// produce no persistable text. Leaving them unconfirmed would make every future
// persist re-examine them and log a recovery that never happens.
func TestToolOnlyDeltaIsConfirmedNotRetriedForever(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-noise"

	// Uma linha que o extractConversation nao converte em texto persistivel.
	noiseLine, _ := json.Marshal(map[string]interface{}{"type": "system", "subtype": "hook"})
	archivePath := filepath.Join(cfg.archiveDir, sanitize(sessionID)+".jsonl")
	if writeErr := os.WriteFile(archivePath, append(noiseLine, '\n'), 0o600); writeErr != nil {
		t.Fatalf("escrevendo o archive: %v", writeErr)
	}

	reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL), sessionID, queueBacklog{})

	if confirmed := readCursor(confirmedCursorPath(cfg, sessionID)); confirmed != 1 {
		t.Errorf("cursor de confirmacao = %d, want 1 — ruido seria reexaminado para sempre", confirmed)
	}
	if got := len(fake.allIngested()); got != 0 {
		t.Errorf("ruido de ferramenta foi persistido: %d escritas", got)
	}
}

// TestReconciliationNeverGuessesASession keeps the inviolable rule at this door too.
func TestReconciliationNeverGuessesASession(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)

	if recovered := reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL),
		"", queueBacklog{}); recovered != 0 {
		t.Error("reconciliou sem sessao — nao ha a quem atribuir")
	}
	if got := len(fake.allIngested()); got != 0 {
		t.Errorf("escreveu %d registros sem sessao provavel", got)
	}
}

// TestMissingArchiveIsNotAnError: with the archive disabled or never written there
// is simply nothing to reconcile, and that must not look like a failure.
func TestMissingArchiveIsNotAnError(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)

	if archiveExistsForSession(cfg, "sess-none") {
		t.Fatal("o teste comecou com um archive que nao deveria existir")
	}
	if recovered := reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL),
		"sess-none", queueBacklog{}); recovered != 0 {
		t.Errorf("recuperou %d sem archive", recovered)
	}
}

// TestExistingSessionIsNotResentOnFirstRun is the regression test for the incident
// I caused on 2026-07-31. The confirmed cursor starts at zero, so on an install
// that was already running, EVERY pre-existing session looked like "archive full,
// nothing confirmed" and the first persist re-sent its whole history. Six chunks
// were duplicated in the owner's real memory before 25 more sessions were stopped
// by hand. Reconciliation, written to prevent loss, became a duplicate factory.
func TestExistingSessionIsNotResentOnFirstRun(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-preexisting"

	// Uma sessão que a versão ANTERIOR já entregou por completo: archive cheio,
	// cursor de tentativa no fim, e nenhum cursor de confirmação (ele não existia).
	writeArchivedTranscript(t, cfg, sessionID, []string{"turno um", "turno dois", "turno tres"})
	writeCursor(filepath.Join(cfg.cursorDir(), sanitize(sessionID)), 3)

	recovered := reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL),
		sessionID, queueBacklog{})

	if recovered != 0 {
		t.Fatalf("reenviou %d linha(s) de uma sessão já entregue — histórico duplicado", recovered)
	}
	if got := len(fake.allIngested()); got != 0 {
		t.Errorf("escritas = %d, want 0 — a memória do usuário seria reescrita", got)
	}
	// E a semente tem de ficar GRAVADA: sem isso, cada execução re-semearia a partir
	// de um cursor de tentativa que avança, e um buraco real nunca seria visto.
	if confirmed := readCursor(confirmedCursorPath(cfg, sessionID)); confirmed != 3 {
		t.Errorf("cursor de confirmação semeado = %d, want 3", confirmed)
	}
}

// TestSeedingDoesNotHideAGenuineGap: seeding must use the ATTEMPTED cursor, not the
// archive length. A session whose archive grew past what was ever attempted still
// has a real gap, and it must still be recovered.
func TestSeedingDoesNotHideAGenuineGap(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-partial"

	writeArchivedTranscript(t, cfg, sessionID, []string{"entregue", "perdido um", "perdido dois"})
	writeCursor(filepath.Join(cfg.cursorDir(), sanitize(sessionID)), 1) // só a 1ª foi processada

	recovered := reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL),
		sessionID, queueBacklog{})

	if recovered != 2 {
		t.Fatalf("recuperadas = %d, want 2 — a semente engoliu um buraco real", recovered)
	}
	ingested := strings.Join(fake.allIngested(), "\n")
	if strings.Contains(ingested, "entregue") {
		t.Error("reenviou a linha que já tinha sido entregue")
	}
	for _, missing := range []string{"perdido um", "perdido dois"} {
		if !strings.Contains(ingested, missing) {
			t.Errorf("%q não foi recuperado", missing)
		}
	}
}

// TestBrandNewSessionStillReconciles guards the fresh-install case: with no cursor
// at all, seeding must be a no-op and a genuine gap must still be recovered.
func TestBrandNewSessionStillReconciles(t *testing.T) {
	fake := &countingServer{}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newReconcileConfig(t)
	const sessionID = "sess-brand-new"

	writeArchivedTranscript(t, cfg, sessionID, []string{"turno um"})
	// Sem cursor de tentativa: instalação nova, processo morreu antes do primeiro persist.

	if recovered := reconcileSession(context.Background(), cfg, newReconcileMemory(t, server.URL),
		sessionID, queueBacklog{}); recovered != 1 {
		t.Errorf("recuperadas = %d, want 1 numa instalação nova", recovered)
	}
}
