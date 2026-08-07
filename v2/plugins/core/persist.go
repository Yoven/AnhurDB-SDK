package core

// persist.go — o caminho de ESCRITA: do hook Stop/SessionEnd até o AnhurDB (ou a
// fila). Um domínio: "persistir o delta desta conversa sem nunca perder um turno".
// A leitura/render do recall mora em core.go+format_memory.go; a fila com estado
// em queue_store.go/queue_bridge.go. Regra do corte (~300, CLAUDE.md).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// hookInput is the JSON Claude Code pipes to a hook on stdin. Stop hooks include transcript_path;
// SessionEnd may not, so we fall back to the documented transcript location using session_id+cwd.
type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
}

// cmdPersist ingests the transcript lines added since the last run (cursor-based), so each Stop
// persists only the new turn. On a write failure the chunk is queued to disk for retry.
func cmdPersist(ctx context.Context, cfg config, mem *client.Memory) {
	var input hookInput
	_ = json.NewDecoder(os.Stdin).Decode(&input)

	// Junior Tip [drain the backlog on EVERY persist, 2026-07-08]: flushQueue used to
	// run ONLY on cmdRecall (SessionStart). In a long-running session that queued a
	// chunk during a transient DB-down (e.g. a stack restart), the backlog then sat
	// on disk until the NEXT session start — could be hours. Persist runs every few
	// minutes, so flushing here first drains queued chunks opportunistically the
	// moment the DB is reachable again. Order matters: drain the OLD backlog before
	// adding this turn's chunk, so recovered memories keep their original ordering.
	backlog := flushQueue(ctx, cfg, mem)

	// Junior Tip [reconciliar DEPOIS de drenar, 2026-07-31]: a reconciliação só age
	// com a fila vazia (ver reconcileSession), e drenar primeiro é o que dá a ela a
	// chance de estar vazia. Invertida a ordem, um chunk que a fila entregaria neste
	// mesmo persist ainda estaria lá e a reconciliação se recusaria a rodar por
	// causa dele. Roda a cada persist, não só no SessionEnd: um SessionEnd pode
	// nunca acontecer — máquina desligada, processo morto — e é justamente esse o
	// cenário que produz o buraco.
	if recovered := reconcileSession(ctx, cfg, mem, input.SessionID, backlog); recovered > 0 {
		logLine(cfg, fmt.Sprintf("persist: reconciliation recovered %d archived line(s) that never reached AnhurDB", recovered))
	}

	path := resolveTranscript(input)
	if path == "" {
		logLine(cfg, "persist: transcript not found (session="+input.SessionID+")")
		return
	}
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = "anon-" + filepath.Base(path)
	}

	// Archive the FULL verbatim transcript (thinking + tools included) on EVERY persist,
	// independent of the filtered episodic delta below. A session with no new dialogue lines
	// still refreshes the archive. Best-effort and isolated — see archiveTranscript.
	archiveTranscript(cfg, sessionID, path)

	lines, err := readLines(path)
	if err != nil {
		logLine(cfg, "persist: cannot read transcript: "+err.Error())
		return
	}
	cursorFile := filepath.Join(cfg.cursorDir(), sanitize(sessionID))
	last := readCursor(cursorFile)
	if len(lines) <= last {
		return // nothing new since the last persist
	}

	text := extractConversation(cfg, lines[last:])
	// Advance the cursor unconditionally: these lines are now processed (every chunk
	// below is either sent or queued — never dropped).
	writeCursor(cursorFile, len(lines))
	if strings.TrimSpace(text) == "" {
		return // the new lines were only tool noise we don't persist (or empty)
	}

	// Junior Tip [no-silent-loss chunking, 2026-06-20]: a long delta is split into
	// maxChunkChars-sized pieces and EACH is persisted (queued on failure), instead of
	// truncating to the last maxChunkChars and dropping the rest. Every chunk stays a
	// search-/extraction-friendly size and nothing is lost.
	if deliverSessionText(ctx, cfg, mem, sessionID, text) {
		// Todos os chunks aceitos: o AnhurDB confirmou até aqui. Ver reconcile.go
		// para por que este cursor é separado do de tentativa.
		markDeliveryConfirmed(cfg, sessionID, len(lines))
	}
	logLine(cfg, fmt.Sprintf("persist: lines %d-%d (session=%s)", last+1, len(lines), sessionID))
}

// deliverSessionText fatia o texto e entrega cada pedaço, enfileirando o que não
// passar. Devolve true quando TODOS os chunks foram aceitos pelo AnhurDB.
//
// Junior Tip [uma regra, duas portas, 2026-07-31]: esta função era o corpo do
// cmdPersist. Saiu de lá porque a reconciliação (reconcile.go) precisa entregar
// exatamente do mesmo jeito — mesmo fatiamento, mesmo cabeçalho, mesma política
// de retentativa, mesma fila. Uma segunda cópia do laço divergiria no primeiro
// ajuste que alguém fizesse num dos lados, e a divergência apareceria como
// memória gravada com formato diferente conforme o caminho que a trouxe.
func deliverSessionText(ctx context.Context, cfg config, mem *client.Memory,
	sessionID string, text string) (allDelivered bool) {
	// Junior Tip [no-silent-loss chunking, 2026-06-20]: a long delta is split into
	// maxChunkChars-sized pieces and EACH is persisted (queued on failure), instead of
	// truncating to the last maxChunkChars and dropping the rest. Every chunk stays a
	// search-/extraction-friendly size and nothing is lost.
	chunks := splitIntoChunks(text, cfg.maxChunkChars)
	if len(chunks) == 0 {
		return false
	}
	buildChunk := func(chunkIndex int, body string) string {
		label := ""
		if len(chunks) > 1 {
			label = fmt.Sprintf(" [part %d/%d]", chunkIndex+1, len(chunks))
		}
		return fmt.Sprintf("Claude Code session %s — conversation excerpt%s (%s):\n%s",
			sessionID, label, time.Now().UTC().Format(time.RFC3339), body)
	}

	// Junior Tip [session-first 2026-07-18]: register the Claude conversation
	// uuid before any ingest. Idempotent upsert — safe to call every persist.
	sessionOutcome := deliverWithRetry(ctx, func() error {
		_, createSessionErr := mem.CreateSession(ctx, client.WithCreateSessionID(sessionID))
		return createSessionErr
	})
	if sessionOutcome.err != nil {
		logLine(cfg, fmt.Sprintf("deliver: CreateSession failed after %d attempt(s) (%s) — queueing all %d chunk(s): %v",
			sessionOutcome.attemptsMade, terminalityLabel(sessionOutcome.terminal), len(chunks), sessionOutcome.err))
		for chunkIndex, body := range chunks {
			queueChunk(cfg, buildChunk(chunkIndex, body))
		}
		return false
	}

	sent, queued := 0, 0
	for chunkIndex, body := range chunks {
		chunk := buildChunk(chunkIndex, body)
		// Junior Tip [tenant + session, 2026-07-08]: pin the record to THIS
		// conversation's session (sessionID), NOT the container. The tenant comes
		// from the API key; each Claude Code conversation is its own session, so
		// consolidation produces one consolidated per conversation anchored at its
		// first episodic. Recall still scopes to the whole tenant.
		// Junior Tip [3 tentativas ANTES do disco, 2026-07-31]: escrever local é último
		// recurso. Um blip — eleição de líder, reconexão, 503 de deploy — não pode criar
		// uma segunda cópia da memória do usuário. Só depois de o banco não responder
		// maxDeliveryAttempts vezes (ou responder um "não" definitivo) o chunk desce para
		// a fila. Ele nunca é perdido nos dois caminhos; o que muda é quando o disco entra.
		outcome := deliverWithRetry(ctx, func() error {
			_, addErr := mem.Add(ctx, chunk, client.WithSessionID(sessionID))
			return addErr
		})
		if outcome.err != nil {
			logLine(cfg, fmt.Sprintf("deliver: chunk not delivered after %d attempt(s) (%s) — queueing: %v",
				outcome.attemptsMade, terminalityLabel(outcome.terminal), outcome.err))
			queueChunk(cfg, chunk) // fila; o próximo persist ou recall drena (sem perda silenciosa)
			queued++
		} else {
			sent++
		}
	}
	logLine(cfg, fmt.Sprintf("deliver: session=%s chunks=%d sent=%d queued=%d", sessionID, len(chunks), sent, queued))
	return queued == 0
}

// ── lossless transcript archive ──────────────────────────────────────────────

// archiveTranscript copies the complete session transcript — Claude Code's verbatim ground
// truth, including thinking blocks and full untruncated tool I/O that the episodic feed
// deliberately drops — to a durable archive directory. It is the LOSSLESS counterpart to
// the filtered cortex feed: the extraction pipeline keeps seeing clean dialogue, while the
// archive guarantees nothing from the session is ever lost.
//
// Junior Tip [whole-file overwrite, keyed by session, 2026-07-14]: the transcript is copied
// in FULL each persist to <archiveDir>/<sessionID>.jsonl, overwriting the prior copy. So the
// archive always holds the latest complete transcript with ZERO delta/cursor bookkeeping —
// no chance of a gap, duplicate, or reordering (the same failure modes the episodic path
// works hard to avoid). At session end the file is final.
//
// Junior Tip [best-effort + isolated, 2026-07-14]: archiving must never affect the durable
// cortex feed. Every failure path only logs and returns; it never touches the episodic
// persist, the queue, or the exit code. Mode 0600 (dir 0700) because the transcript holds
// verbatim secrets — same restriction the rest of the plugin uses.
func archiveTranscript(cfg config, sessionID, transcriptPath string) {
	if !cfg.archive {
		return
	}
	if err := os.MkdirAll(cfg.archiveDir, 0o700); err != nil {
		logLine(cfg, "archive: cannot create archive dir: "+err.Error())
		recordArchiveFailure(cfg, "cannot create archive dir: "+err.Error())
		return
	}
	source, openErr := os.Open(transcriptPath)
	if openErr != nil {
		logLine(cfg, "archive: cannot read transcript: "+openErr.Error())
		recordArchiveFailure(cfg, "cannot read transcript: "+openErr.Error())
		return
	}
	defer source.Close()

	// Atomic publish: stream into a sibling temp file, then rename (atomic on one
	// filesystem). Streaming via io.Copy keeps a large transcript off the heap.
	dest := filepath.Join(cfg.archiveDir, sanitize(sessionID)+".jsonl")
	tmp := dest + ".tmp"
	tmpFile, createErr := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if createErr != nil {
		logLine(cfg, "archive: cannot create temp file: "+createErr.Error())
		recordArchiveFailure(cfg, "cannot create temp file: "+createErr.Error())
		return
	}
	if _, copyErr := io.Copy(tmpFile, source); copyErr != nil {
		tmpFile.Close()
		_ = os.Remove(tmp)
		logLine(cfg, "archive: copy failed: "+copyErr.Error())
		recordArchiveFailure(cfg, "copy failed: "+copyErr.Error())
		return
	}
	if closeErr := tmpFile.Close(); closeErr != nil {
		_ = os.Remove(tmp)
		logLine(cfg, "archive: temp close failed: "+closeErr.Error())
		recordArchiveFailure(cfg, "temp close failed: "+closeErr.Error())
		return
	}
	if renameErr := os.Rename(tmp, dest); renameErr != nil {
		_ = os.Remove(tmp)
		logLine(cfg, "archive: rename failed: "+renameErr.Error())
		recordArchiveFailure(cfg, "rename failed: "+renameErr.Error())
		return
	}
	// Chegou aqui: existe uma cópia local completa e fresca desta sessão. Limpar o
	// marcador é obrigatório — ver a Junior Tip de clearArchiveFailure.
	clearArchiveFailure(cfg)
}

// resolveTranscript returns the transcript path, preferring the one the hook provided and falling
// back to the documented location built from cwd + session_id.
func resolveTranscript(input hookInput) string {
	if input.TranscriptPath != "" && fileExists(input.TranscriptPath) {
		return input.TranscriptPath
	}
	if input.SessionID != "" && input.Cwd != "" {
		munged := strings.ReplaceAll(input.Cwd, "/", "-")
		guess := filepath.Join(homeDir(), ".claude", "projects", munged, input.SessionID+".jsonl")
		if fileExists(guess) {
			return guess
		}
	}
	return ""
}
