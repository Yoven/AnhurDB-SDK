package core

// status.go — `<binary> status`: what is held locally, and why, WITHOUT the network.
//
// Domain, in one sentence: read the local queue (queue.db plus the flat-file
// floor) and print a queue verdict that memory-health can quote — including,
// by name and numbers, every session AnhurDB refuses because it is full.
//
// Junior Tip [why a subcommand and not "ls queue/*.txt"]: the memory-health
// skill used to count .txt files. Since 2026-07-31 the queue is a SQLite file,
// so that count reads 0 while queue.db holds chunks — a health check that says
// ALIVE over a stuck queue is the exact failure it exists to catch. Only the
// engine can read its own queue correctly, so the engine answers.
//
// Junior Tip [no key, no network, no log line, no side effects]: status runs
// BEFORE the API-key guard (a missing key must not hide the backlog), it never
// creates queue.db, never migrates or drains, and never writes plugin.log —
// a diagnostic that changes what it inspects, or that feeds the watcher's
// failure stream, is not a diagnostic.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// sessionLimitBlock is one full session as seen from the local queue.
type sessionLimitBlock struct {
	sessionID   string
	chunkCount  int
	limitText   string
	countText   string
	oldestQueue string
}

// cmdStatus prints the queue report to stdout. Read the `verdict:` line; the
// process always exits 0 like every other hook subcommand.
func cmdStatus(cfg config, output io.Writer) {
	fmt.Fprintln(output, "anhur-memory status — local queue only (no network, no API key read)")
	var reasons []string

	databasePath := filepath.Join(cfg.stateDir, "queue.db")
	if _, statErr := os.Stat(databasePath); statErr == nil {
		reasons = append(reasons, reportQueueStore(cfg, databasePath, output)...)
	} else {
		fmt.Fprintln(output, "queue.db: absent (nothing has ever been queued through the state queue)")
	}

	floorCount := countTextFiles(cfg.queueDir())
	floorQuarantined := countTextFiles(cfg.quarantineDir())
	fmt.Fprintf(output, "flat-file floor: %d queued, %d quarantined (%s)\n", floorCount, floorQuarantined, cfg.queueDir())
	if floorCount > 0 {
		reasons = append(reasons, fmt.Sprintf("%d flat-file chunk(s) waiting (retried automatically)", floorCount))
	}
	if floorQuarantined > 0 {
		reasons = append(reasons, fmt.Sprintf("%d flat-file chunk(s) quarantined — need a human", floorQuarantined))
	}

	if len(reasons) == 0 {
		fmt.Fprintln(output, "verdict: OK — nothing is held locally")
		return
	}
	fmt.Fprintln(output, "verdict: DEGRADED — "+strings.Join(reasons, "; "))
}

// reportQueueStore prints the state-queue section and returns the verdict reasons.
func reportQueueStore(cfg config, databasePath string, output io.Writer) []string {
	store, openErr := openQueueStore(cfg.stateDir)
	if openErr != nil {
		fmt.Fprintln(output, "queue.db: UNREADABLE: "+openErr.Error())
		return []string{"queue.db unreadable (" + databasePath + ") — backlog unknown"}
	}
	defer store.Close()

	pendingItems, pendingErr := store.ListPending()
	quarantinedItems, quarantinedErr := store.ListQuarantined()
	if pendingErr != nil || quarantinedErr != nil {
		fmt.Fprintf(output, "queue.db: UNREADABLE: %v %v\n", pendingErr, quarantinedErr)
		return []string{"queue.db unreadable — backlog unknown"}
	}
	fmt.Fprintf(output, "queue.db: %d pending, %d quarantined\n", len(pendingItems), len(quarantinedItems))

	var reasons []string
	limitBlocks := sessionLimitBlocks(pendingItems)
	limitedChunks := 0
	for _, block := range limitBlocks {
		limitedChunks += block.chunkCount
		fmt.Fprintf(output, "%s: session=%s chunks=%d limit=%s count=%s since=%s — AnhurDB refuses new records for this session; the chunks are kept locally and retried with backoff, not lost\n",
			sessionLimitMarker, block.sessionID, block.chunkCount, block.limitText, block.countText, block.oldestQueue)
		reasons = append(reasons, fmt.Sprintf("session record limit: session %s (%d chunk(s) queued, limit %s, count %s)",
			block.sessionID, block.chunkCount, block.limitText, block.countText))
	}
	if otherPending := len(pendingItems) - limitedChunks; otherPending > 0 {
		reasons = append(reasons, fmt.Sprintf("%d chunk(s) queued (retried automatically)", otherPending))
	}
	if len(quarantinedItems) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d chunk(s) quarantined — need a human", len(quarantinedItems)))
	}
	return reasons
}

// sessionLimitBlocks groups pending items whose last failure was a full session.
func sessionLimitBlocks(pendingItems []queueItem) []sessionLimitBlock {
	blocksBySession := map[string]*sessionLimitBlock{}
	for _, item := range pendingItems {
		limitText, countText, isSessionLimit := sessionLimitFromCause(item.LastError)
		if !isSessionLimit {
			continue
		}
		block, found := blocksBySession[item.SessionID]
		if !found {
			// Items arrive oldest first (ListPending orders by created_at).
			block = &sessionLimitBlock{sessionID: item.SessionID, oldestQueue: item.CreatedAt.UTC().Format(time.RFC3339)}
			blocksBySession[item.SessionID] = block
		}
		block.chunkCount++
		// The newest numbers win: the count only grows while the cap holds.
		block.limitText, block.countText = limitText, countText
	}
	limitBlocks := make([]sessionLimitBlock, 0, len(blocksBySession))
	for _, block := range blocksBySession {
		limitBlocks = append(limitBlocks, *block)
	}
	sort.Slice(limitBlocks, func(left int, right int) bool {
		return limitBlocks[left].sessionID < limitBlocks[right].sessionID
	})
	return limitBlocks
}

// countTextFiles counts .txt files directly inside directoryPath (0 if absent).
func countTextFiles(directoryPath string) int {
	directoryEntries, readErr := os.ReadDir(directoryPath)
	if readErr != nil {
		return 0
	}
	textFileCount := 0
	for _, directoryEntry := range directoryEntries {
		if !directoryEntry.IsDir() && strings.HasSuffix(directoryEntry.Name(), ".txt") {
			textFileCount++
		}
	}
	return textFileCount
}
