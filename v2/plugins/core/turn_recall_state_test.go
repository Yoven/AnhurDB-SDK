package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestTurnSeenSetPersistence covers the dedupe file's lifecycle: round trip, capacity
// trim (newest kept), corrupt file read as empty, reset, and the age-based prune.
func TestTurnSeenSetPersistence(testingContext *testing.T) {
	testCases := []struct {
		name  string
		check func(subTest *testing.T, cfg config)
	}{
		{name: "round trip keeps every key", check: func(subTest *testing.T, cfg config) {
			seenSet := newTurnSeenSet()
			seenSet.add("id:1", "text:aa", "id:2")
			if saveError := saveTurnSeenSet(cfg, "sess-1", seenSet); saveError != nil {
				subTest.Fatalf("save: %v", saveError)
			}
			reloaded := loadTurnSeenSet(cfg, "sess-1")
			for _, key := range []string{"id:1", "text:aa", "id:2"} {
				if !reloaded.contains(key) {
					subTest.Errorf("key %q lost on round trip", key)
				}
			}
		}},
		{name: "capacity trim drops the oldest keys", check: func(subTest *testing.T, cfg config) {
			seenSet := newTurnSeenSet()
			for keyIndex := 0; keyIndex < turnSeenCapacity+10; keyIndex++ {
				seenSet.add(fmt.Sprintf("id:%d", keyIndex))
			}
			if saveError := saveTurnSeenSet(cfg, "sess-cap", seenSet); saveError != nil {
				subTest.Fatalf("save: %v", saveError)
			}
			reloaded := loadTurnSeenSet(cfg, "sess-cap")
			if len(reloaded.order) != turnSeenCapacity {
				subTest.Errorf("kept %d keys, want %d", len(reloaded.order), turnSeenCapacity)
			}
			if reloaded.contains("id:0") || !reloaded.contains(fmt.Sprintf("id:%d", turnSeenCapacity+9)) {
				subTest.Errorf("trim kept the wrong end: oldest must go, newest must stay")
			}
		}},
		{name: "a corrupt file reads as empty instead of failing", check: func(subTest *testing.T, cfg config) {
			if mkdirError := os.MkdirAll(cfg.turnRecallDir(), 0o700); mkdirError != nil {
				subTest.Fatal(mkdirError)
			}
			if writeError := os.WriteFile(turnStatePath(cfg, "sess-bad"), []byte("{not json"), 0o600); writeError != nil {
				subTest.Fatal(writeError)
			}
			if reloaded := loadTurnSeenSet(cfg, "sess-bad"); len(reloaded.order) != 0 {
				subTest.Errorf("corrupt state yielded %d keys, want 0", len(reloaded.order))
			}
		}},
		{name: "reset removes the file and tolerates a missing one", check: func(subTest *testing.T, cfg config) {
			seenSet := newTurnSeenSet()
			seenSet.add("id:7")
			if saveError := saveTurnSeenSet(cfg, "sess-reset", seenSet); saveError != nil {
				subTest.Fatal(saveError)
			}
			if resetError := resetTurnSeenSet(cfg, "sess-reset"); resetError != nil {
				subTest.Fatalf("reset: %v", resetError)
			}
			if loadTurnSeenSet(cfg, "sess-reset").contains("id:7") {
				subTest.Errorf("key survived the reset")
			}
			if resetError := resetTurnSeenSet(cfg, "sess-reset"); resetError != nil {
				subTest.Errorf("second reset on a missing file errored: %v", resetError)
			}
		}},
		{name: "prune removes only files older than the max age", check: func(subTest *testing.T, cfg config) {
			if mkdirError := os.MkdirAll(cfg.turnRecallDir(), 0o700); mkdirError != nil {
				subTest.Fatal(mkdirError)
			}
			stalePath := filepath.Join(cfg.turnRecallDir(), "stale.json")
			freshPath := filepath.Join(cfg.turnRecallDir(), "fresh.json")
			for _, filePath := range []string{stalePath, freshPath} {
				if writeError := os.WriteFile(filePath, []byte(`{}`), 0o600); writeError != nil {
					subTest.Fatal(writeError)
				}
			}
			staleTime := time.Now().Add(-turnStateMaxAge - time.Hour)
			if chtimesError := os.Chtimes(stalePath, staleTime, staleTime); chtimesError != nil {
				subTest.Fatal(chtimesError)
			}
			pruneStaleTurnStates(cfg, time.Now())
			if _, statError := os.Stat(stalePath); statError == nil {
				subTest.Errorf("stale file survived the prune")
			}
			if _, statError := os.Stat(freshPath); statError != nil {
				subTest.Errorf("fresh file was pruned: %v", statError)
			}
		}},
	}
	for _, testCase := range testCases {
		testingContext.Run(testCase.name, func(subTest *testing.T) {
			testCase.check(subTest, config{stateDir: subTest.TempDir()})
		})
	}
}

// TestRenderTurnBlockRespectsTokenCap proves the block never crosses blockMaxRunes and
// that renderedCount reports exactly the candidates that made it in.
func TestRenderTurnBlockRespectsTokenCap(testingContext *testing.T) {
	limits := defaultTurnRecallLimits()
	var candidates []turnCandidate
	for candidateIndex := 0; candidateIndex < 20; candidateIndex++ {
		candidates = append(candidates, turnCandidate{
			recordID: candidateIndex + 1, recordType: "fact",
			text: strings.Repeat("ã", 600), // multi-byte, longer than itemMaxRunes
		})
	}
	block, renderedCount := renderTurnBlock("ctr", candidates, limits)
	if blockRunes := utf8.RuneCountInString(block); blockRunes > limits.blockMaxRunes {
		testingContext.Errorf("block has %d runes, cap is %d", blockRunes, limits.blockMaxRunes)
	}
	if renderedCount == 0 || renderedCount >= len(candidates) {
		testingContext.Errorf("renderedCount = %d: the cap should admit some but not all of %d", renderedCount, len(candidates))
	}
	if lineCount := strings.Count(block, "\n- ["); lineCount != renderedCount {
		testingContext.Errorf("rendered lines = %d, renderedCount = %d: they must agree", lineCount, renderedCount)
	}
	if !utf8.ValidString(block) {
		testingContext.Errorf("truncation split a UTF-8 sequence")
	}
}

// TestCmdPreCompactResetsDedupeAndPersistsWithEmptyStdout proves the three PreCompact
// promises: the dedupe is reset, the transcript delta reaches AnhurDB, and NOTHING is
// written to stdout (which Claude Code would feed to the summariser as instructions).
func TestCmdPreCompactResetsDedupeAndPersistsWithEmptyStdout(testingContext *testing.T) {
	fake := newSelectiveFakeServer()
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	cfg := newTestConfig(testingContext)
	cfg.maxChunkChars = 24000
	cfg.includeTools = "calls"
	if mkdirError := os.MkdirAll(cfg.cursorDir(), 0o755); mkdirError != nil {
		testingContext.Fatal(mkdirError)
	}

	seenSet := newTurnSeenSet()
	seenSet.add("id:99")
	if saveError := saveTurnSeenSet(cfg, "sess-compact", seenSet); saveError != nil {
		testingContext.Fatal(saveError)
	}
	transcriptPath := filepath.Join(testingContext.TempDir(), "sess-compact.jsonl")
	transcriptLines := `{"type":"user","message":{"content":"remember the raft snapshot plan"}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Snapshots every 10k entries."}]}}` + "\n"
	if writeError := os.WriteFile(transcriptPath, []byte(transcriptLines), 0o600); writeError != nil {
		testingContext.Fatal(writeError)
	}
	hookPayload := `{"session_id":"sess-compact","transcript_path":"` + transcriptPath +
		`","cwd":"/tmp","hook_event_name":"PreCompact","trigger":"auto","custom_instructions":null}`

	originalStdout := os.Stdout
	pipeReader, pipeWriter, pipeError := os.Pipe()
	if pipeError != nil {
		testingContext.Fatal(pipeError)
	}
	os.Stdout = pipeWriter
	cmdPreCompact(context.Background(), cfg, newTestMemory(testingContext, server.URL), strings.NewReader(hookPayload))
	os.Stdout = originalStdout
	_ = pipeWriter.Close()
	var capturedStdout bytes.Buffer
	_, _ = io.Copy(&capturedStdout, pipeReader)

	if capturedStdout.Len() != 0 {
		testingContext.Errorf("PreCompact wrote to stdout (would steer the compaction summary): %q", capturedStdout.String())
	}
	if loadTurnSeenSet(cfg, "sess-compact").contains("id:99") {
		testingContext.Errorf("per-turn dedupe survived compaction")
	}
	delivered := fake.delivered()
	if len(delivered) != 1 || !strings.Contains(delivered[0], "raft snapshot plan") {
		testingContext.Errorf("delivered = %v, want the transcript delta for sess-compact", delivered)
	}
}
