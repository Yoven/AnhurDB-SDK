package core

// archive_health.go — a redundância local só conta se alguém souber que ela existe.
//
// Junior Tip [por que este arquivo existe, 2026-07-31]: o archive verbatim é a
// ÚNICA cópia local completa da conversa — é o chão sobre o qual a reconciliação
// (e o índice local) vão rodar. Ele grava sempre, antes de qualquer rede, e é
// deliberadamente best-effort: nenhuma falha dele pode afetar o feed episódico
// (ver archiveTranscript). Mas "best-effort + só loga" significa que ele pode
// estar MORTO há semanas sem ninguém saber, e aí a redundância que a arquitetura
// pressupõe simplesmente não existe. É a forma exata do apagão de julho de 2026:
// não uma falha, um silêncio. Log registra; log não alcança ninguém.
//
// Junior Tip [por que um MARCADOR em disco, e não uma variável]: persist e recall
// são PROCESSOS DIFERENTES. O archive é escrito durante o persist; o bloco
// <anhur-memory> é montado no recall, numa invocação posterior do binário. Nada
// sobrevive entre as duas exceto o disco. Sem o marcador, uma falha de archive
// morre junto com o processo que a viu.
//
// Junior Tip [a sonda VIVA é a defesa principal, o marcador é o complemento]: o
// marcador tem o mesmo problema de tudo o mais aqui — a gravação dele pode falhar
// exatamente pelo motivo que ele deveria registrar (disco cheio, permissão). Por
// isso a verificação principal é uma sonda executada no recall, que reproduz em
// miniatura o que archiveTranscript faz. Um disco cheio reprova a sonda mesmo que
// o marcador nunca tenha chegado ao disco. A rede de segurança não pode depender
// do que ela protege.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// archiveFailureMarkerName é o arquivo que carrega a última falha de archive
// entre um persist e o recall seguinte.
const archiveFailureMarkerName = "archive_failure"

// archiveProbeName é o nome usado pela sonda de escrita. O ponto inicial e o
// sufixo o mantêm fora do padrão <sessionID>.jsonl, então ele nunca pode ser
// confundido com uma sessão arquivada por quem lê o diretório.
const archiveProbeName = ".archive-probe"

// archiveHealth é o retrato da redundância local no instante do recall.
type archiveHealth struct {
	// enabled reflete ANHUR_ARCHIVE. Desligado é um estado LEGÍTIMO e perigoso:
	// não há defeito nenhum, e mesmo assim não existe redundância.
	enabled bool

	directory string

	// writable é o resultado da sonda viva. Falso significa que o próximo persist
	// NÃO vai conseguir arquivar — a redundância está quebrada agora, não no passado.
	writable   bool
	probeError string

	// lastFailure descreve a falha registrada pelo persist anterior, se houver.
	lastFailureAt     string
	lastFailureReason string

	// archivedSessionCount / archivedBytes medem o que já está protegido. Zero
	// sessões com o archive ligado é suspeito: ou é a primeira sessão, ou nada
	// nunca foi gravado.
	archivedSessionCount int
	archivedBytes        int64
}

// degraded responde a única pergunta que interessa: a redundância local está
// funcionando?
func (health archiveHealth) degraded() bool {
	return !health.enabled || !health.writable || health.lastFailureReason != ""
}

// inspectArchiveHealth executa a sonda viva e lê o marcador. Nunca retorna erro:
// toda falha VIRA o diagnóstico, em vez de impedi-lo.
func inspectArchiveHealth(cfg config) archiveHealth {
	health := archiveHealth{enabled: cfg.archive, directory: cfg.archiveDir}
	if !cfg.archive {
		return health // desligado: não há o que sondar, e o aviso já está decidido
	}

	health.lastFailureAt, health.lastFailureReason = readArchiveFailureMarker(cfg)
	health.writable, health.probeError = probeArchiveWritableWithDeadline(cfg.archiveDir, archiveProbeDeadline)
	health.archivedSessionCount, health.archivedBytes = measureArchivedSessions(cfg.archiveDir)
	return health
}

// archiveProbeDeadline limita quanto tempo o diagnóstico pode custar.
//
// Junior Tip [o diagnóstico não pode virar a falha, 2026-07-31]: esta sonda roda
// dentro do hook de SessionStart, e ANHUR_ARCHIVE_DIR pode apontar para um NFS.
// Uma montagem pendurada bloqueia qualquer syscall de arquivo INDEFINIDAMENTE —
// então, sem prazo, a verificação da rede de segurança seria ela mesma capaz de
// matar o hook, e o Claude Code mata hook lento. Resultado: nenhum bloco de
// memória, por causa do código escrito para avisar que a memória está em risco.
// Dois segundos são generosos para 4 syscalls locais e curtos o bastante para
// nunca competir com o orçamento do hook.
const archiveProbeDeadline = 2 * time.Second

// probeArchiveWritableWithDeadline executa a sonda com prazo.
//
// Junior Tip [a goroutine pode vazar, e tudo bem]: num syscall pendurado a
// goroutine não morre — não existe cancelamento de I/O de arquivo em Go. Isso é
// aceitável AQUI e só aqui: o binário é um hook de vida curtíssima que executa um
// comando e termina, então o processo inteiro leva a goroutine junto. Num serviço
// de vida longa este mesmo código seria um vazamento a cada chamada.
func probeArchiveWritableWithDeadline(archiveDir string, deadline time.Duration) (writable bool, probeError string) {
	type probeResult struct {
		writable   bool
		probeError string
	}
	resultChannel := make(chan probeResult, 1) // buffer 1: a goroutine nunca fica presa no envio
	go func() {
		succeeded, failureReason := probeArchiveWritable(archiveDir)
		resultChannel <- probeResult{writable: succeeded, probeError: failureReason}
	}()

	select {
	case result := <-resultChannel:
		return result.writable, result.probeError
	case <-time.After(deadline):
		// Não saber é o mesmo que estar quebrado, para efeito de aviso: um sistema de
		// arquivos que não responde em 2s não vai arquivar o próximo turno.
		return false, fmt.Sprintf("the archive filesystem did not respond within %s (hung mount?)", deadline)
	}
}

// probeArchiveWritable reproduz em miniatura o que archiveTranscript faz —
// MkdirAll, criar temporário, escrever, renomear, remover.
//
// Junior Tip [a sonda tem de repetir os MESMOS passos]: bastasse um os.Stat no
// diretório, um sistema de arquivos cheio ou montado somente-leitura passaria
// (o diretório existe!) e o persist seguinte falharia mesmo assim. Uma sonda que
// verifica algo mais fácil do que a operação real produz um "ok" que não vale nada.
func probeArchiveWritable(archiveDir string) (writable bool, probeError string) {
	if mkdirErr := os.MkdirAll(archiveDir, 0o700); mkdirErr != nil {
		return false, "cannot create the archive directory: " + mkdirErr.Error()
	}
	probeTemporaryPath := filepath.Join(archiveDir, archiveProbeName+".tmp")
	probeFinalPath := filepath.Join(archiveDir, archiveProbeName)

	if writeErr := os.WriteFile(probeTemporaryPath, []byte("probe"), 0o600); writeErr != nil {
		return false, "cannot write into the archive directory: " + writeErr.Error()
	}
	if renameErr := os.Rename(probeTemporaryPath, probeFinalPath); renameErr != nil {
		_ = os.Remove(probeTemporaryPath)
		return false, "cannot publish into the archive directory: " + renameErr.Error()
	}
	// Não deixar rastro: um probe esquecido confundiria quem lista o diretório
	// procurando sessões. Falhar ao remover não invalida a prova de escrita.
	_ = os.Remove(probeFinalPath)
	return true, ""
}

// measureArchivedSessions conta os transcripts já protegidos e quantos bytes eles
// somam. Só metadados — o conteúdo nunca é lido aqui.
func measureArchivedSessions(archiveDir string) (sessionCount int, totalBytes int64) {
	archiveEntries, readErr := os.ReadDir(archiveDir)
	if readErr != nil {
		return 0, 0
	}
	for _, archiveEntry := range archiveEntries {
		if archiveEntry.IsDir() || !strings.HasSuffix(archiveEntry.Name(), ".jsonl") {
			continue
		}
		sessionCount++
		if entryInfo, infoErr := archiveEntry.Info(); infoErr == nil {
			totalBytes += entryInfo.Size()
		}
	}
	return sessionCount, totalBytes
}

// recordArchiveFailure grava o marcador lido pelo recall seguinte. Best-effort
// por construção: se ele não puder ser gravado, a sonda viva ainda vai reprovar.
func recordArchiveFailure(cfg config, reason string) {
	markerPath := filepath.Join(cfg.stateDir, archiveFailureMarkerName)
	markerBody := time.Now().UTC().Format(time.RFC3339) + "\n" + truncateRunes(reason, 300) + "\n"
	_ = os.WriteFile(markerPath, []byte(markerBody), 0o600)
}

// clearArchiveFailure apaga o marcador depois de um archive bem-sucedido.
//
// Junior Tip [limpar é obrigatório, não cosmético]: um marcador que nunca some
// transforma um problema resolvido num aviso permanente. Aviso permanente é
// aviso ignorado — e no dia em que o archive quebrar de verdade, ninguém vai
// olhar. Um alarme que grita sempre não é um alarme.
func clearArchiveFailure(cfg config) {
	_ = os.Remove(filepath.Join(cfg.stateDir, archiveFailureMarkerName))
}

// readArchiveFailureMarker devolve (instante, motivo) da última falha registrada.
func readArchiveFailureMarker(cfg config) (failedAt string, reason string) {
	markerBytes, readErr := os.ReadFile(filepath.Join(cfg.stateDir, archiveFailureMarkerName))
	if readErr != nil {
		return "", ""
	}
	markerLines := strings.SplitN(strings.TrimSpace(string(markerBytes)), "\n", 2)
	if len(markerLines) < 2 {
		// Marcador truncado (crash no meio da escrita): ainda é a prova de que algo
		// falhou. Reportar sem o motivo é infinitamente melhor que não reportar.
		return strings.TrimSpace(string(markerBytes)), "reason not recorded (marker truncated)"
	}
	return strings.TrimSpace(markerLines[0]), strings.TrimSpace(markerLines[1])
}

// renderArchiveWarning escreve a seção do bloco <anhur-memory> quando a
// redundância local está degradada — e NADA quando ela está sadia.
//
// Junior Tip [silêncio quando está tudo bem, 2026-07-31]: este bloco é injetado
// em TODA sessão. Uma linha de "archive ok" a cada início treinaria o leitor a
// pular a região inteira, e junto com ela os avisos de backlog e quarentena que
// vivem ali. O canal só continua alto porque é usado com parcimônia. Para
// confirmar saúde de propósito existe o verificador (scripts/anhur-memory-verify.sh).
func renderArchiveWarning(builder *strings.Builder, health archiveHealth) {
	if !health.degraded() {
		return
	}
	builder.WriteString("\n## 🚨 Local redundancy is not working\n")

	if !health.enabled {
		builder.WriteString("- The verbatim transcript archive is DISABLED (ANHUR_ARCHIVE), so this conversation exists in exactly one place: AnhurDB.\n")
		builder.WriteString("- Nothing is wrong right now — but there is no second copy, so a failed write is a lost turn with no way back.\n")
		builder.WriteString("- TELL THE USER about this now, in your first reply.\n")
		return
	}

	if health.lastFailureReason != "" {
		builder.WriteString(fmt.Sprintf("- The last attempt to archive a transcript FAILED at %s: %s\n",
			health.lastFailureAt, health.lastFailureReason))
	}
	if !health.writable {
		builder.WriteString(fmt.Sprintf("- The archive directory (%s) cannot be written RIGHT NOW: %s\n",
			health.directory, health.probeError))
		builder.WriteString("- This means the next turn will NOT get a local copy — the safety net is off while this lasts.\n")
	}
	builder.WriteString(fmt.Sprintf("- Sessions protected so far: %d (%s).\n",
		health.archivedSessionCount, humanBytes(health.archivedBytes)))
	builder.WriteString("- TELL THE USER about this now, in your first reply: a broken archive is silent, and it is the copy that survives when AnhurDB does not.\n")
}

// humanBytes formata um tamanho para leitura humana. Aproximado de propósito: o
// número existe para dar ordem de grandeza a quem lê o aviso, não para auditoria.
func humanBytes(byteCount int64) string {
	const unit = 1024
	if byteCount < unit {
		return fmt.Sprintf("%d B", byteCount)
	}
	divisor, exponent := int64(unit), 0
	for remaining := byteCount / unit; remaining >= unit; remaining /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %cB", float64(byteCount)/float64(divisor), "KMGTPE"[exponent])
}
