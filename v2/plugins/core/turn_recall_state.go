package core

// turn_recall_state.go — a MEMÓRIA DO QUE JÁ FOI MOSTRADO nesta sessão. Um
// domínio: "quais registros o recall por turno já injetou, e quais são novos".
// Um arquivo JSON pequeno por sessão em <stateDir>/turn-recall/. Regra do corte.

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/client"
)

const (
	// turnSeenStateVersion tags the file format so a future change can be detected
	// instead of misread.
	turnSeenStateVersion = 1
	// turnSeenCapacity bounds one session's file. Each injected record adds at most
	// two keys, so 400 keys is ~200 records — far beyond what fits in one context.
	turnSeenCapacity = 400
	// turnStateMaxAge is how long an untouched session file survives the prune.
	turnStateMaxAge = 30 * 24 * time.Hour
)

// turnRecallDir is where the per-session dedupe files live.
func (cfg config) turnRecallDir() string { return filepath.Join(cfg.stateDir, "turn-recall") }

// turnSeenSet is the ordered set of dedupe keys already injected in one session.
// order keeps insertion order so trimming to capacity drops the OLDEST keys.
type turnSeenSet struct {
	order   []string
	members map[string]struct{}
}

// turnSeenFile is the on-disk shape of a turnSeenSet.
type turnSeenFile struct {
	Version int      `json:"version"`
	Seen    []string `json:"seen"`
}

func newTurnSeenSet() *turnSeenSet {
	return &turnSeenSet{members: map[string]struct{}{}}
}

// contains reports whether any of the keys was already injected.
func (seenSet *turnSeenSet) contains(keys ...string) bool {
	for _, key := range keys {
		if _, present := seenSet.members[key]; present {
			return true
		}
	}
	return false
}

// add records keys as injected, ignoring the ones already present.
func (seenSet *turnSeenSet) add(keys ...string) {
	for _, key := range keys {
		if _, present := seenSet.members[key]; present {
			continue
		}
		seenSet.members[key] = struct{}{}
		seenSet.order = append(seenSet.order, key)
	}
}

// turnStatePath is the dedupe file for one session. sanitize keeps the id a safe filename.
func turnStatePath(cfg config, sessionID string) string {
	return filepath.Join(cfg.turnRecallDir(), sanitize(sessionID)+".json")
}

// loadTurnSeenSet reads a session's dedupe state. A missing, unreadable or corrupt
// file yields an EMPTY set: the cost is re-showing records, never blocking a prompt.
func loadTurnSeenSet(cfg config, sessionID string) *turnSeenSet {
	seenSet := newTurnSeenSet()
	if sessionID == "" {
		return seenSet
	}
	rawState, readError := os.ReadFile(turnStatePath(cfg, sessionID))
	if readError != nil {
		if !errors.Is(readError, fs.ErrNotExist) {
			logLine(cfg, "turn-recall: dedupe state unreadable, starting empty: "+readError.Error())
		}
		return seenSet
	}
	var stateFile turnSeenFile
	if parseError := json.Unmarshal(rawState, &stateFile); parseError != nil || stateFile.Version != turnSeenStateVersion {
		logLine(cfg, "turn-recall: dedupe state corrupt or from another version, starting empty")
		return seenSet
	}
	seenSet.add(stateFile.Seen...)
	return seenSet
}

// saveTurnSeenSet atomically replaces a session's dedupe file with seenSet, trimmed to
// turnSeenCapacity. A session with no id has nowhere to be keyed and is not saved.
//
// Junior Tip [temp file + rename, unique temp name]: rename is atomic on one filesystem,
// so a reader never sees half a file. The temp name comes from os.CreateTemp, NOT a
// fixed "<file>.tmp": a background subagent reporting back fires UserPromptSubmit too,
// so two hook processes for one session can overlap, and a shared temp name would let
// one truncate the other's bytes mid-write. With unique temps the last rename wins and
// the loser's keys are merely re-shown later — duplicate, never corruption.
func saveTurnSeenSet(cfg config, sessionID string, seenSet *turnSeenSet) error {
	if sessionID == "" {
		return errors.New("hook input carried no session_id; dedupe disabled for this prompt")
	}
	if mkdirError := os.MkdirAll(cfg.turnRecallDir(), 0o700); mkdirError != nil {
		return mkdirError
	}
	statePath := turnStatePath(cfg, sessionID)
	if _, statError := os.Stat(statePath); errors.Is(statError, fs.ErrNotExist) {
		// First write of a new session: the one moment a prune is cheap and bounded.
		pruneStaleTurnStates(cfg, time.Now())
	}

	keptKeys := seenSet.order
	if len(keptKeys) > turnSeenCapacity {
		keptKeys = keptKeys[len(keptKeys)-turnSeenCapacity:]
	}
	encodedState, marshalError := json.Marshal(turnSeenFile{Version: turnSeenStateVersion, Seen: keptKeys})
	if marshalError != nil {
		return marshalError
	}
	tempFile, createError := os.CreateTemp(cfg.turnRecallDir(), ".seen-*.tmp")
	if createError != nil {
		return createError
	}
	tempPath := tempFile.Name()
	if _, writeError := tempFile.Write(encodedState); writeError != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return writeError
	}
	if closeError := tempFile.Close(); closeError != nil {
		_ = os.Remove(tempPath)
		return closeError
	}
	if renameError := os.Rename(tempPath, statePath); renameError != nil {
		_ = os.Remove(tempPath)
		return renameError
	}
	return nil
}

// resetTurnSeenSet forgets everything injected in a session. PreCompact calls it.
//
// Junior Tip [why compaction resets the dedupe]: the dedupe exists because a record
// shown earlier is still in the model's context. Compaction replaces that context with
// a summary that may have dropped it — keeping the dedupe would then HIDE a record the
// model no longer has. After the reset, the next prompt may surface it again.
func resetTurnSeenSet(cfg config, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	removeError := os.Remove(turnStatePath(cfg, sessionID))
	if removeError != nil && !errors.Is(removeError, fs.ErrNotExist) {
		return removeError
	}
	return nil
}

// pruneStaleTurnStates deletes session files (and orphaned temps) untouched for
// turnStateMaxAge. Best effort: a failure only leaves a small file behind.
func pruneStaleTurnStates(cfg config, now time.Time) {
	directoryEntries, readError := os.ReadDir(cfg.turnRecallDir())
	if readError != nil {
		return
	}
	for _, directoryEntry := range directoryEntries {
		if !directoryEntry.Type().IsRegular() {
			continue
		}
		entryInfo, infoError := directoryEntry.Info()
		if infoError != nil || now.Sub(entryInfo.ModTime()) < turnStateMaxAge {
			continue
		}
		_ = os.Remove(filepath.Join(cfg.turnRecallDir(), directoryEntry.Name()))
	}
}

// turnCandidate is one search hit that survived the dedupe, ready to render.
type turnCandidate struct {
	recordID   int
	recordType string
	createdAt  *time.Time
	text       string
	dedupeKeys []string
}

// selectNewTurnRecords walks hits in rank order and keeps at most maxInjected that are
// displayable, not superseded or archived, and not already injected this session.
//
// Junior Tip [two dedupe keys per record]: the record id catches the same record seen
// again. The text fingerprint catches DIFFERENT ids with the same sentence — the live
// tenant holds a fact and a risk that share one summary verbatim (ids 39308 and 39317,
// observed 2026-11-22). Showing both would spend the token budget saying one thing twice.
func selectNewTurnRecords(searchResults []client.SearchResult, seenSet *turnSeenSet, maxInjected int) []turnCandidate {
	batchSeen := newTurnSeenSet()
	candidates := make([]turnCandidate, 0, maxInjected)
	for _, searchResult := range searchResults {
		if len(candidates) >= maxInjected {
			break
		}
		record := searchResult.Record
		if record.SupersededBy != nil || record.Archived {
			continue
		}
		displayText := recordDisplayText(record.Summary, record.Content)
		if displayText == "" {
			continue
		}
		dedupeKeys := []string{"id:" + strconv.Itoa(record.ID), "text:" + textFingerprint(displayText)}
		if record.ID == 0 {
			dedupeKeys = dedupeKeys[1:] // id 0 means "absent on the wire", not a real id
		}
		if seenSet.contains(dedupeKeys...) || batchSeen.contains(dedupeKeys...) {
			continue
		}
		batchSeen.add(dedupeKeys...)
		candidates = append(candidates, turnCandidate{
			recordID:   record.ID,
			recordType: string(record.Type),
			createdAt:  record.CreatedAt,
			text:       displayText,
			dedupeKeys: dedupeKeys,
		})
	}
	return candidates
}

// recordDisplayText prefers the summary; falls back to string content; collapses whitespace.
func recordDisplayText(summary string, content any) string {
	displayText := strings.Join(strings.Fields(summary), " ")
	if displayText == "" {
		if contentText, isText := content.(string); isText {
			displayText = strings.Join(strings.Fields(contentText), " ")
		}
	}
	return displayText
}

// textFingerprint is a case-insensitive FNV-64a of already-collapsed text, in hex.
func textFingerprint(displayText string) string {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(strings.ToLower(displayText)))
	return strconv.FormatUint(hasher.Sum64(), 16)
}
