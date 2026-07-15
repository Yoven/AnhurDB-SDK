package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestExtractConversation_ToolModes verifies ANHUR_INCLUDE_TOOLS none|calls|all and
// that thinking blocks never leak.
func TestExtractConversation_ToolModes(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"content":"please run the tests"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Running them now."},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok  anhurdb/service  0.02s"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"SECRET-INTERNAL"},{"type":"text","text":"All green."}]}}`,
	}
	cases := map[string]struct{ wantTool, wantResult bool }{
		"none":  {false, false},
		"calls": {true, false},
		"all":   {true, true},
	}
	for mode, want := range cases {
		got := extractConversation(config{includeTools: mode}, lines)
		if has := strings.Contains(got, "[tool: Bash"); has != want.wantTool {
			t.Errorf("mode %q: tool_use present=%v want=%v\n%s", mode, has, want.wantTool, got)
		}
		if has := strings.Contains(got, "[result:"); has != want.wantResult {
			t.Errorf("mode %q: tool_result present=%v want=%v\n%s", mode, has, want.wantResult, got)
		}
		if !strings.Contains(got, "please run the tests") || !strings.Contains(got, "All green") {
			t.Errorf("mode %q: missing conversation text\n%s", mode, got)
		}
		if strings.Contains(got, "SECRET-INTERNAL") {
			t.Errorf("mode %q: thinking block leaked into memory\n%s", mode, got)
		}
	}
}

// TestSplitIntoChunks verifies no-silent-loss chunking + UTF-8 rune safety.
func TestSplitIntoChunks(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("line-of-some-length\n") // 20 bytes each
	}
	text := b.String()
	chunks := splitIntoChunks(text, 80)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	if strings.Join(chunks, "") != text {
		t.Errorf("chunks do not reassemble to the original (silent loss)")
	}
	for i, c := range chunks {
		if len(c) > 80 {
			t.Errorf("chunk %d exceeds maxChars: %d bytes", i, len(c))
		}
	}

	// A single oversized line of multibyte runes must hard-split on rune boundaries.
	long := strings.Repeat("é", 100) // 200 bytes, no newline
	rc := splitIntoChunks(long, 50)
	if strings.Join(rc, "") != long {
		t.Errorf("rune hard-split lost data")
	}
	for i, c := range rc {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8 (split mid-rune)", i)
		}
		if len(c) > 50 {
			t.Errorf("rune chunk %d exceeds maxBytes: %d", i, len(c))
		}
	}

	// Short text returns a single chunk unchanged.
	if got := splitIntoChunks("hi", 80); len(got) != 1 || got[0] != "hi" {
		t.Errorf("short text should be one unchanged chunk, got %v", got)
	}
}

// TestArchiveTranscript verifies the lossless archive: byte-identical copy that KEEPS the
// thinking + tool_result the episodic feed drops, atomic idempotent overwrite, 0600 mode,
// and that it honours the archive=false toggle.
func TestArchiveTranscript(t *testing.T) {
	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "transcript.jsonl")
	archiveDir := filepath.Join(tempDir, "archive")

	// A transcript whose fidelity matters: it contains a thinking block and a tool_result,
	// both of which the episodic extractor drops but the archive MUST keep verbatim.
	transcript := `{"type":"user","message":{"content":"hi"}}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"SECRET-INTERNAL"},{"type":"text","text":"ok"},{"type":"tool_use","name":"Bash","input":{"command":"ls -la /very/long/path/that/would/be/truncated/in/the/episodic/feed"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"total 0 — full untruncated output"}]}}
`
	if writeErr := os.WriteFile(sourcePath, []byte(transcript), 0o600); writeErr != nil {
		t.Fatalf("seeding transcript: %v", writeErr)
	}

	enabled := config{archive: true, archiveDir: archiveDir}
	archiveTranscript(enabled, "sess-1", sourcePath)

	destPath := filepath.Join(archiveDir, "sess-1.jsonl")
	archived, readErr := os.ReadFile(destPath)
	if readErr != nil {
		t.Fatalf("archive not written: %v", readErr)
	}
	if string(archived) != transcript {
		t.Errorf("archive is not byte-identical to the source transcript")
	}
	if !strings.Contains(string(archived), "SECRET-INTERNAL") || !strings.Contains(string(archived), "tool_result") {
		t.Errorf("archive dropped thinking/tool_result — it must be verbatim, not filtered")
	}
	if info, statErr := os.Stat(destPath); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("archive mode = %v (err %v), want 0600", info.Mode().Perm(), statErr)
	}

	// Idempotent overwrite: re-archiving the same session leaves an identical file (no
	// duplicate, no leftover .tmp).
	archiveTranscript(enabled, "sess-1", sourcePath)
	if again, _ := os.ReadFile(destPath); string(again) != transcript {
		t.Errorf("re-archiving changed the file")
	}
	if _, tmpErr := os.Stat(destPath + ".tmp"); !os.IsNotExist(tmpErr) {
		t.Errorf("temp file left behind after atomic rename")
	}

	// archive=false disables the feature entirely.
	disabled := config{archive: false, archiveDir: filepath.Join(tempDir, "off")}
	archiveTranscript(disabled, "sess-2", sourcePath)
	if _, offErr := os.Stat(filepath.Join(tempDir, "off", "sess-2.jsonl")); !os.IsNotExist(offErr) {
		t.Errorf("archive written despite archive=false")
	}
}
