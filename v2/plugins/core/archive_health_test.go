package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// archive_health_test.go — a redundância local só conta se alguém souber que ela
// existe. Cada teste aqui é um estado em que a rede de segurança está morta e o
// sistema, hoje, parecia saudável.

func newArchiveTestConfig(t *testing.T) config {
	t.Helper()
	stateDir := t.TempDir()
	return config{
		stateDir:    stateDir,
		archive:     true,
		archiveDir:  filepath.Join(stateDir, "archive"),
		container:   "test-container",
		recallLimit: 10,
	}
}

// TestDisabledArchiveIsAnnounced: turning the archive off is a LEGITIMATE state
// with no error anywhere — and it means the conversation exists in exactly one
// place. The absence of a defect is precisely why nobody would ever notice.
func TestDisabledArchiveIsAnnounced(t *testing.T) {
	cfg := newArchiveTestConfig(t)
	cfg.archive = false

	health := inspectArchiveHealth(cfg)
	if !health.degraded() {
		t.Fatal("archive desligado nao foi tratado como degradado — nao existe redundancia nenhuma")
	}

	var builder strings.Builder
	renderArchiveWarning(&builder, health)
	warning := builder.String()
	if !strings.Contains(warning, "DISABLED") {
		t.Errorf("o aviso nao diz que o archive esta desligado: %q", warning)
	}
	if !strings.Contains(warning, "TELL THE USER") {
		t.Error("sem a instrucao de contar ao usuario, o aviso morre no contexto do modelo")
	}
}

// TestUnwritableArchiveIsCaughtByTheLiveProbe: a stat() on the directory would
// pass here (it exists!) and the next persist would still fail. The probe must
// repeat the SAME steps archiveTranscript takes, or its "ok" is worthless.
func TestUnwritableArchiveIsCaughtByTheLiveProbe(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not restrict writes")
	}
	cfg := newArchiveTestConfig(t)
	if mkdirErr := os.MkdirAll(cfg.archiveDir, 0o500); mkdirErr != nil { // r-x: existe, nao escreve
		t.Fatalf("preparando o diretorio somente-leitura: %v", mkdirErr)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg.archiveDir, 0o700) })

	health := inspectArchiveHealth(cfg)
	if health.writable {
		t.Fatal("a sonda aprovou um diretorio onde nao da para escrever — o proximo persist falharia calado")
	}
	if !health.degraded() || health.probeError == "" {
		t.Errorf("estado degradado sem motivo registrado: %+v", health)
	}
}

// TestFailureMarkerSurvivesTheProcess pins the reason the marker exists at all:
// the archive is written during `persist` and the block is built during `recall`,
// in a LATER process. Nothing survives between them except the disk.
func TestFailureMarkerSurvivesTheProcess(t *testing.T) {
	cfg := newArchiveTestConfig(t)

	recordArchiveFailure(cfg, "copy failed: no space left on device")

	// Um processo novo, lendo o mesmo stateDir: exatamente o que o recall faz.
	health := inspectArchiveHealth(cfg)
	if health.lastFailureReason == "" {
		t.Fatal("a falha registrada pelo persist nao chegou ao recall — morreria com o processo")
	}
	if !strings.Contains(health.lastFailureReason, "no space left") {
		t.Errorf("motivo perdido na travessia: %q", health.lastFailureReason)
	}
	if health.lastFailureAt == "" {
		t.Error("sem carimbo de tempo nao da para saber se a falha e de agora ou de semanas atras")
	}
}

// TestSuccessClearsTheMarker: a warning that never goes away is a warning nobody
// reads, and on the day the archive really breaks, nobody will look.
func TestSuccessClearsTheMarker(t *testing.T) {
	cfg := newArchiveTestConfig(t)
	recordArchiveFailure(cfg, "transient failure")
	clearArchiveFailure(cfg)

	health := inspectArchiveHealth(cfg)
	if health.lastFailureReason != "" {
		t.Errorf("marcador sobreviveu ao sucesso: %q — viraria alarme permanente", health.lastFailureReason)
	}
	if health.degraded() {
		t.Error("archive sadio reportado como degradado")
	}
}

// TestArchiveTranscriptRecordsAndClears exercises the REAL function end to end,
// rather than the marker helpers in isolation.
func TestArchiveTranscriptRecordsAndClears(t *testing.T) {
	cfg := newArchiveTestConfig(t)

	// Fracasso: o transcript nao existe.
	archiveTranscript(cfg, "sess-A", filepath.Join(t.TempDir(), "missing.jsonl"))
	if health := inspectArchiveHealth(cfg); health.lastFailureReason == "" {
		t.Fatal("archiveTranscript falhou sem deixar rastro duravel")
	}

	// Sucesso: o transcript existe e e copiado.
	transcriptPath := filepath.Join(t.TempDir(), "real.jsonl")
	if writeErr := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`+"\n"), 0o600); writeErr != nil {
		t.Fatalf("escrevendo o transcript: %v", writeErr)
	}
	archiveTranscript(cfg, "sess-A", transcriptPath)

	health := inspectArchiveHealth(cfg)
	if health.lastFailureReason != "" {
		t.Errorf("archive bem-sucedido nao limpou o marcador: %q", health.lastFailureReason)
	}
	if health.archivedSessionCount != 1 {
		t.Errorf("sessoes arquivadas = %d, want 1", health.archivedSessionCount)
	}
}

// TestHealthyArchiveStaysSilent: this block is injected EVERY session. A cheerful
// "archive ok" line each time trains the reader to skip the whole region — and
// with it the backlog and quarantine warnings that live there.
func TestHealthyArchiveStaysSilent(t *testing.T) {
	cfg := newArchiveTestConfig(t)
	if mkdirErr := os.MkdirAll(cfg.archiveDir, 0o700); mkdirErr != nil {
		t.Fatalf("preparando o diretorio: %v", mkdirErr)
	}

	var builder strings.Builder
	renderArchiveWarning(&builder, inspectArchiveHealth(cfg))
	if builder.Len() != 0 {
		t.Errorf("archive sadio poluiu o bloco: %q", builder.String())
	}
}

// TestProbeLeavesNoTrace: a forgotten probe file would confuse anyone listing the
// directory looking for archived sessions, and would inflate the session count.
func TestProbeLeavesNoTrace(t *testing.T) {
	cfg := newArchiveTestConfig(t)
	inspectArchiveHealth(cfg)

	archiveEntries, readErr := os.ReadDir(cfg.archiveDir)
	if readErr != nil {
		t.Fatalf("lendo o diretorio do archive: %v", readErr)
	}
	if len(archiveEntries) != 0 {
		t.Errorf("a sonda deixou rastro: %v", archiveEntries)
	}
}

// TestWarningsSurviveAnUnreachableAnhurDB is the third occurrence of one shape in
// this codebase: a safety net placed BEHIND the very gate it protects. `recall`
// used to return early when the profile failed, suppressing the whole block — so
// the backlog warning vanished at exactly the moment there WAS a backlog.
func TestWarningsSurviveAnUnreachableAnhurDB(t *testing.T) {
	cfg := newArchiveTestConfig(t)
	cfg.archive = false // redundancia desligada: algo a avisar

	block := formatMemory(cfg, nil, queueBacklog{chunkCount: 3, oldestChunk: "2026-07-31T10:00:00Z"}, inspectArchiveHealth(cfg))

	if block == "" {
		t.Fatal("AnhurDB fora do ar apagou TODOS os avisos — o unico canal que alcanca o usuario")
	}
	if !strings.Contains(block, "Unpersisted backlog") {
		t.Error("o aviso de backlog sumiu justamente quando existe backlog")
	}
	if !strings.Contains(block, "DISABLED") {
		t.Error("o aviso de redundancia sumiu junto")
	}
	if !strings.Contains(block, "NONE of your long-term memory was recalled") {
		t.Error("o bloco nao deixa claro que nada foi recuperado — o modelo agiria como se soubesse tudo")
	}
}

// TestNothingToSayInjectsNothing guards the other direction: an offline session
// with a clean queue and a healthy archive must not inject a block that only says
// "I could not reach the server". That is noise at every offline start.
func TestNothingToSayInjectsNothing(t *testing.T) {
	cfg := newArchiveTestConfig(t)
	if mkdirErr := os.MkdirAll(cfg.archiveDir, 0o700); mkdirErr != nil {
		t.Fatalf("preparando o diretorio: %v", mkdirErr)
	}

	if block := formatMemory(cfg, nil, queueBacklog{}, inspectArchiveHealth(cfg)); block != "" {
		t.Errorf("bloco injetado sem nada a dizer: %q", block)
	}
}

// TestProfileStillRendersWhenArchiveIsHealthy is the regression guard for the
// normal path: adding the archive parameter must not change a healthy session.
func TestProfileStillRendersWhenArchiveIsHealthy(t *testing.T) {
	cfg := newArchiveTestConfig(t)
	if mkdirErr := os.MkdirAll(cfg.archiveDir, 0o700); mkdirErr != nil {
		t.Fatalf("preparando o diretorio: %v", mkdirErr)
	}
	profile := &client.ProfileResult{
		Static: map[string]interface{}{"facts": []interface{}{"AnhurDB roda multi-raft"}},
		Stats:  map[string]interface{}{"total_records": 10.0, "sessions": 2.0},
	}

	block := formatMemory(cfg, profile, queueBacklog{}, inspectArchiveHealth(cfg))
	if !strings.Contains(block, "AnhurDB roda multi-raft") {
		t.Error("o perfil deixou de ser renderizado no caminho sadio")
	}
	if strings.Contains(block, "Local redundancy is not working") {
		t.Error("aviso espurio numa sessao sadia")
	}
}

// TestProbeGivesUpInsteadOfHangingTheHook: the diagnostic must never become the
// failure. ANHUR_ARCHIVE_DIR can point at NFS, a hung mount blocks file syscalls
// indefinitely, and Claude Code kills a slow hook — so an unbounded probe could
// cost the whole memory block while checking whether memory is at risk.
func TestProbeGivesUpInsteadOfHangingTheHook(t *testing.T) {
	startedAt := time.Now()
	writable, probeError := probeArchiveWritableWithDeadline(t.TempDir(), time.Nanosecond)
	elapsed := time.Since(startedAt)

	if elapsed > time.Second {
		t.Errorf("a sonda demorou %v mesmo com prazo de 1ns — penduraria o hook", elapsed)
	}
	// Com prazo de 1ns o resultado é uma corrida legítima: ou a sonda local ganhou,
	// ou o prazo estourou. O que NÃO pode acontecer é bloquear, e é isso que o tempo
	// acima prova. Se o prazo venceu, o motivo tem de estar registrado.
	if !writable && probeError == "" {
		t.Error("sonda reprovada sem motivo — o aviso nao teria o que dizer")
	}
}

// TestDeadlineExpiryCountsAsDegraded: not knowing is the same as broken, for the
// purpose of warning. A filesystem that cannot answer in two seconds will not
// archive the next turn either.
func TestDeadlineExpiryCountsAsDegraded(t *testing.T) {
	health := archiveHealth{
		enabled:    true,
		directory:  "/mnt/hung",
		writable:   false,
		probeError: "the archive filesystem did not respond within 2s (hung mount?)",
	}
	if !health.degraded() {
		t.Fatal("sonda que estourou o prazo nao foi tratada como degradada")
	}
	var builder strings.Builder
	renderArchiveWarning(&builder, health)
	if !strings.Contains(builder.String(), "hung mount") {
		t.Errorf("o motivo do timeout nao chegou ao aviso: %q", builder.String())
	}
}
