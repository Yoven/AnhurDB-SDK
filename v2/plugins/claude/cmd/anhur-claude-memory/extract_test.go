package main

import (
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
