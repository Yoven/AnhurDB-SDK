package core

// queue_files.go — o PISO de arquivos achatados da fila: um chunk, um .txt, zero
// dependências. Roda apenas quando o SQLite da fila (queue_store.go) não abre —
// a rede de segurança da rede de segurança. Um domínio: "guardar no disco quando
// nada mais funciona". Regra do corte (~300, CLAUDE.md).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// queueSeq gives every queued chunk a process-unique suffix so multiple chunks
// persisted within the same clock tick never collide onto one filename.
var queueSeq uint64

// queueChunkToFile is the FLOOR beneath the state queue: one chunk, one flat file,
// no schema, no dependency. queueChunk (queue_bridge.go) calls it whenever the
// SQLite queue cannot be opened.
//
// Junior Tip [why this stayed instead of being deleted, 2026-07-31]: the state
// queue is better at everything except one thing — it can fail to open. Disk full,
// a permission change, a corrupted page. On that day the only acceptable outcome is
// still "the memory is on disk", and os.WriteFile is the shortest path to it. A
// safety net that shares a failure mode with the thing it protects is not a net;
// that is precisely how the lossless archive was lost for 12.8 days in July 2026.
func queueChunkToFile(cfg config, content string) {
	// Junior Tip [collision-proof + fail-loud, 2026-07-04]: the old name was
	// second-granular time + pid, so two chunks queued in the same second (a NORMAL
	// multi-chunk persist — cmdPersist loops over chunks) produced the SAME path and
	// os.WriteFile silently overwrote all-but-the-last = silent memory loss, violating
	// this file's own "#1 rule — no silent loss" (header). And a WriteFile error was
	// swallowed (only the success path logged), so a failed queue write vanished with no
	// trace while the transcript moved on. Now the name is unique per write (nanosecond +
	// pid + atomic seq) and a write failure fails LOUD (logged, not silent).
	seq := atomic.AddUint64(&queueSeq, 1)
	name := fmt.Sprintf("%s-%d-%d.txt", time.Now().UTC().Format("20060102T150405.000000000"), os.Getpid(), seq)
	path := filepath.Join(cfg.queueDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		logLine(cfg, fmt.Sprintf("queue write FAILED for chunk (data at risk): %v", err))
		return
	}
	logLine(cfg, "queued chunk -> "+path)
}

// queueBacklog summarises the chunks still sitting on disk after a flush attempt: how many
// remain, when the oldest was queued, and why the last retry failed.
//
// Junior Tip [why the backlog is RETURNED and not just logged, 2026-07-17]: flushQueue already
// fails LOUD into plugin.log, and that was still not enough. A permanent cap rejection (HTTP 409)
// sat in the log while the session's memory stayed incomplete, and nobody noticed until the user
// happened to ask — because no human tails a log file. A log records; it does not reach anyone.
// "Fail loud" only counts if it lands in a channel a human actually reads, and the ONLY such
// channel this plugin owns is the <anhur-memory> block injected at session start. So flushQueue
// reports the backlog upward and cmdRecall surfaces it there. Chunks are still never dropped —
// that invariant is untouched; this only makes the failure visible while it persists.
type queueBacklog struct {
	chunkCount   int
	oldestChunk  string
	lastFlushErr error
	// quarantinedChunkCount / oldestQuarantined describe chunks parked in quarantineDir()
	// because no session marker could be recovered from their content. They are reported on
	// EVERY flush — not only at the moment a chunk is moved — because a quarantined chunk
	// never drains by itself, so its warning must keep reappearing in recall until a human
	// resolves it. See quarantineChunk for why these are never retried.
	quarantinedChunkCount int
	oldestQuarantined     string
}

// chunkQueuedAt recovers the UTC instant encoded in a queued chunk's filename (see queueChunk's
// naming scheme). Falls back to the raw name when the format does not match, so a stray file in
// the queue directory can never cost us the warning itself.
func chunkQueuedAt(chunkName string) string {
	stampEnd := strings.Index(chunkName, "-")
	if stampEnd <= 0 {
		return chunkName
	}
	queuedAt, parseErr := time.Parse("20060102T150405.000000000", chunkName[:stampEnd])
	if parseErr != nil {
		return chunkName
	}
	return queuedAt.UTC().Format(time.RFC3339)
}

// flushQueueFiles retries every chunk sitting in the flat-file queue; successful ones are
// removed, the rest stay for next time. It returns what is still stuck — see queueBacklog.
//
// Junior Tip [when this runs, 2026-07-31]: only when the SQLite queue cannot be opened.
// Normal operation migrates these files into the state queue at startup
// (migrateLegacyQueueFiles) and drains from there. This path is the floor — see
// queueChunkToFile — and it is kept working, not kept around: a fallback nobody
// exercises is a fallback nobody can trust.
func flushQueueFiles(ctx context.Context, cfg config, mem *client.Memory) queueBacklog {
	var backlog queueBacklog
	entries, err := os.ReadDir(cfg.queueDir())
	if err != nil {
		return backlog
	}
	// ReadDir sorts by filename and queueChunk prefixes every name with a lexicographically
	// sortable UTC stamp, so the first chunk left behind is always the oldest one.
	noteStuck := func(chunkName string, cause error) {
		backlog.chunkCount++
		if backlog.oldestChunk == "" {
			backlog.oldestChunk = chunkQueuedAt(chunkName)
		}
		if cause != nil {
			backlog.lastFlushErr = cause
		}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		path := filepath.Join(cfg.queueDir(), entry.Name())
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			// The chunk is still on disk and still unpersisted — count it, or the warning
			// would under-report exactly the chunks we cannot even read.
			noteStuck(entry.Name(), readErr)
			continue
		}
		// Recover into the ORIGINAL conversation's session: the session id is
		// embedded in the chunk header ("Claude Code session <id> — ..."), so a
		// queued chunk drains back into the same session it came from instead of
		// collapsing into the container. Register first (session-first servers).
		queuedSessionID := sessionFromChunk(string(content))
		if queuedSessionID == "" {
			// Junior Tip [quarantine, NEVER guess a session, 2026-07-30]: this used to fall
			// through to mem.Add with NO session option. The SDK's resolveWriteSessionID then
			// falls back to the client's REGISTERED session — and this ONE client.Memory is
			// reused for every chunk in this loop, so whichever session the PREVIOUS chunk's
			// CreateSession registered silently absorbed this chunk: two conversations merged
			// into one AnhurDB session, violating the inviolable rule "1 Claude session =
			// 1 AnhurDB session" (and with no session registered yet, the Add failed with
			// "session_id is required" and the chunk retried forever). A chunk whose owner
			// cannot be proven must not be written ANYWHERE: park it in quarantine/, fail
			// LOUD (ERROR log + recall backlog warning), and leave the decision to a human.
			if quarantineErr := quarantineChunk(cfg, path, entry.Name()); quarantineErr != nil {
				// The chunk is still in the queue: keep it visible as stuck and retry the
				// move on the next flush — a failed quarantine must not go silent either.
				noteStuck(entry.Name(), quarantineErr)
			}
			continue
		}
		if _, createSessionErr := mem.CreateSession(ctx, client.WithCreateSessionID(queuedSessionID)); createSessionErr != nil {
			logLine(cfg, fmt.Sprintf("flush CreateSession failed for %s: %v", path, createSessionErr))
			noteStuck(entry.Name(), createSessionErr)
			continue
		}
		// Junior Tip [session pinned EXPLICITLY on every Add, 2026-07-30]: CreateSession above
		// mutates the shared client's registered session as a side effect, and chunks from
		// DIFFERENT conversations flow through this loop back to back. WithSessionID makes the
		// write independent of that shared state (an explicit id always wins inside the SDK's
		// resolveWriteSessionID), so no chunk can ever inherit a neighbour's session.
		if _, addErr := mem.Add(ctx, string(content), client.WithSessionID(queuedSessionID)); addErr == nil {
			_ = os.Remove(path)
			logLine(cfg, "flushed queued chunk "+path)
		} else {
			// Junior Tip [log the addErr, 2026-07-03]: this line used to swallow the
			// error, so a PERMANENT rejection (e.g. HTTP 409 "session has reached the
			// maximum of 500 records") looked identical to a transient DB-down retry —
			// the queue sat "still failing" for 10 days before anyone saw the 409.
			// The queue must never drop chunks, but it must FAIL LOUD about why.
			logLine(cfg, fmt.Sprintf("flush still failing for %s (retried on every persist): %v", path, addErr))
			noteStuck(entry.Name(), addErr)
		}
	}
	// Count what sits in quarantine on EVERY flush: those chunks never drain by themselves,
	// so their warning must keep reappearing in recall until a human resolves them. A read
	// error on an EXISTING quarantine dir fails loud — hiding quarantined chunks would be
	// exactly the silent loss the quarantine exists to prevent.
	quarantineEntries, quarantineReadErr := os.ReadDir(cfg.quarantineDir())
	if quarantineReadErr != nil && !os.IsNotExist(quarantineReadErr) {
		logLine(cfg, "ERROR: cannot read quarantine dir (quarantined chunks may be invisible): "+quarantineReadErr.Error())
	}
	for _, quarantineEntry := range quarantineEntries {
		if quarantineEntry.IsDir() || !strings.HasSuffix(quarantineEntry.Name(), ".txt") {
			continue
		}
		backlog.quarantinedChunkCount++
		if backlog.oldestQuarantined == "" {
			// ReadDir is name-sorted and names start with the sortable queue stamp,
			// so the first .txt entry is always the oldest quarantined chunk.
			backlog.oldestQuarantined = chunkQueuedAt(quarantineEntry.Name())
		}
	}
	return backlog
}

// quarantineChunk moves a queued chunk whose conversation session cannot be identified into
// quarantineDir(), where it is kept intact but NEVER retried. It returns an error when the move
// fails; the chunk then stays in the queue and the caller must surface it as stuck.
//
// Junior Tip [why quarantine exists at all, 2026-07-30]: the server made session_id mandatory
// on every write (ADR-0014), and the only session this plugin can PROVE a chunk belongs to is
// the one in its own "Claude Code session <id>" header. Writing a headerless chunk under any
// other id — the container, the current conversation, the client's last registered session —
// would merge two conversations' memories, breaking the inviolable "1 Claude session =
// 1 AnhurDB session" rule. Losing availability (the chunk waits for a human) is acceptable;
// losing attribution (the chunk lands in the wrong session) is not.
func quarantineChunk(cfg config, queuedPath string, chunkName string) error {
	// 0700 (tighter than the 0755 queue dir): quarantined chunks are verbatim conversation
	// excerpts that may sit on disk indefinitely, so keep them owner-only like the archive.
	if mkdirErr := os.MkdirAll(cfg.quarantineDir(), 0o700); mkdirErr != nil {
		logLine(cfg, fmt.Sprintf("ERROR: cannot create quarantine dir for %s: %v", queuedPath, mkdirErr))
		return mkdirErr
	}
	quarantinedPath := filepath.Join(cfg.quarantineDir(), chunkName)
	if renameErr := os.Rename(queuedPath, quarantinedPath); renameErr != nil {
		logLine(cfg, fmt.Sprintf("ERROR: quarantine move failed for %s: %v", queuedPath, renameErr))
		return renameErr
	}
	logLine(cfg, fmt.Sprintf("ERROR: chunk has no session marker — quarantined %s -> %s (will NOT be persisted; needs manual review)", queuedPath, quarantinedPath))
	return nil
}

// ── small helpers ────────────────────────────────────────────────────────────
