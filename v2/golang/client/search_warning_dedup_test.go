package client

// search_warning_dedup_test.go — the CADENCE of the ADR-0031 "server ignored
// your knob" warnings, and the FORMAT of the line that carries them.
//
// Domain, in one sentence: how many times does this SDK say it, and does the
// line it says look like the one TypeScript prints?
//
// Junior Tip [why this is its own file]: search_parity_test.go was already at
// 315 lines against this project's ~300-line cut, and house law forbids growing
// a file that is already past it. The seam is honest anyway: every other parity
// test asserts the WORDS of a message, while everything here asserts how often
// and in what shape the words appear.

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRepeatedSearchesWarnOncePerKnob is the measurement that motivated the
// dedup: three identical searches against ONE pre-ADR-0031 server printed 3
// lines in Go, 1 in TypeScript and 1 in Python. An operator running a search
// loop against an old server got one line per query, which is how a real signal
// drowns in its own repetition.
//
// The server here is the "old" generation: it answers 200 with results and no
// retrieval block at all, which is exactly how an AnhurDB that predates
// ADR-0031 replies to a request carrying the three knobs.
func TestRepeatedSearchesWarnOncePerKnob(t *testing.T) {
	server := searchServerWith(t, `{"results":[]}`, nil)
	defer server.Close()

	resetSearchKnobWarnings()
	var logBuffer bytes.Buffer
	previousOutput := sdkWarningLogger.Writer()
	sdkWarningLogger.SetOutput(&logBuffer)
	defer sdkWarningLogger.SetOutput(previousOutput)

	memoryClient := NewMemory("k", WithURL(server.URL))
	const identicalSearchCount = 3
	for searchAttempt := 0; searchAttempt < identicalSearchCount; searchAttempt++ {
		if _, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
			WithSearchMode(SearchModeFast)); searchErr != nil {
			t.Fatalf("search %d returned error: %v", searchAttempt+1, searchErr)
		}
	}

	emittedLines := nonEmptyLines(logBuffer.String())
	if len(emittedLines) != 1 {
		t.Fatalf("%d identical searches produced %d warning lines, want 1:\n%s",
			identicalSearchCount, len(emittedLines), logBuffer.String())
	}
}

// TestEachKnobKeepsItsOwnWarningSlot guards the other half of the contract: a
// dedup that silenced the WHOLE set after the first line would hide two real
// facts. TypeScript keys its set per knob ("mode", "semantic_timeout_ms",
// "debug_signals"), so Go must produce the same three lines from one call and
// then nothing on the next.
func TestEachKnobKeepsItsOwnWarningSlot(t *testing.T) {
	server := searchServerWith(t, `{"results":[]}`, nil)
	defer server.Close()

	resetSearchKnobWarnings()
	var logBuffer bytes.Buffer
	previousOutput := sdkWarningLogger.Writer()
	sdkWarningLogger.SetOutput(&logBuffer)
	defer sdkWarningLogger.SetOutput(previousOutput)

	memoryClient := NewMemory("k", WithURL(server.URL))
	allThreeKnobs := []SearchOption{
		WithSearchMode(SearchModeBalanced),
		WithSemanticTimeoutMs(120),
		WithDebugSignals(),
	}
	if _, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
		allThreeKnobs...); searchErr != nil {
		t.Fatalf("first search returned error: %v", searchErr)
	}
	if firstPassLines := nonEmptyLines(logBuffer.String()); len(firstPassLines) != 3 {
		t.Fatalf("first search produced %d warning lines, want 3 (one per knob):\n%s",
			len(firstPassLines), logBuffer.String())
	}

	// A DIFFERENT budget on the second call must stay silent: the ledger is keyed
	// on the knob NAME, exactly like TypeScript's alreadyWarnedKnobs, so a loop
	// that varies the value cannot reopen the tap.
	logBuffer.Reset()
	if _, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
		WithSearchMode(SearchModeBalanced),
		WithSemanticTimeoutMs(999),
		WithDebugSignals()); searchErr != nil {
		t.Fatalf("second search returned error: %v", searchErr)
	}
	if secondPassLines := nonEmptyLines(logBuffer.String()); len(secondPassLines) != 0 {
		t.Fatalf("second search produced %d warning lines, want 0:\n%s",
			len(secondPassLines), logBuffer.String())
	}
}

// TestWarningLineCarriesNoTimestampPrefix pins the FORMAT against TypeScript.
//
// Junior Tip [why the prefix is part of the contract]: the warning text is
// pinned byte for byte in all three SDKs so an operator grepping a mixed
// application log never has to know which SDK wrote a line. Emitting it through
// the package-level log.Printf put "2026/09/06 00:01:18 " in front of it, which
// is precisely the part a grep sees first — the contract held in the constant
// and broke on the way out. Note this test does NOT set log flags to zero the
// way the older tests had to; not needing to is the proof.
func TestWarningLineCarriesNoTimestampPrefix(t *testing.T) {
	server := searchServerWith(t, `{"results":[]}`, nil)
	defer server.Close()

	resetSearchKnobWarnings()
	var logBuffer bytes.Buffer
	previousOutput := sdkWarningLogger.Writer()
	sdkWarningLogger.SetOutput(&logBuffer)
	defer sdkWarningLogger.SetOutput(previousOutput)

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
		WithDebugSignals()); searchErr != nil {
		t.Fatalf("search returned error: %v", searchErr)
	}

	const expectedLine = `anhurdb-sdk: warning: this AnhurDB server ignored debug_signals (it predates ` +
		`ADR-0031); per-hit signals and leg_scores are absent, not empty.`
	emittedLine := strings.TrimRight(logBuffer.String(), "\n")
	if emittedLine != expectedLine {
		t.Fatalf("log=%q\nwant %q", emittedLine, expectedLine)
	}
}

// nonEmptyLines splits captured log output into the lines that actually carry
// text, so a trailing newline never counts as a fourth warning.
func nonEmptyLines(capturedOutput string) []string {
	lines := make([]string, 0, 4)
	for _, candidateLine := range strings.Split(capturedOutput, "\n") {
		if strings.TrimSpace(candidateLine) != "" {
			lines = append(lines, candidateLine)
		}
	}
	return lines
}
