package client

// search_outcome_test.go — one domain: SearchWithRetrieval's 3.0.0 envelope.
//
// Split out of search_parity_test.go on 2026-09-14: that file had reached the
// ~300-line cut, and this case belongs to the return-shape change, not to the
// cross-SDK parity table.

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// ── L5: SearchWithRetrieval returns the WHOLE envelope ─────────────────────

// TestSearchWithRetrievalReturnsLegScores proves the 3.0.0 shape: one method,
// one envelope, nothing dropped. It replaces
// TestSearchWithRetrievalAnnouncesDroppedLegScores, which asserted a warning
// that Go was losing leg_scores — once nothing is lost, a warning about loss is
// a second lie.
//
// This is the test that would have caught the OLD behaviour: the old
// ([]SearchResult, *RetrievalMeta, error) tuple has no third slot, so this file
// does not compile against it.
func TestSearchWithRetrievalReturnsLegScores(t *testing.T) {
	const responseJSON = `{"results":[],"retrieval":{"mode":"balanced"},` +
		`"leg_scores":[{"leg":"fts5","candidates":7},{"leg":"vector","candidates":9}]}`
	server := searchServerWith(t, responseJSON, nil)
	defer server.Close()

	// The old form ALSO logged a warning. Capture the log and prove it stays
	// empty: a silent success is the whole point of widening the return.
	var logBuffer bytes.Buffer
	previousOutput := sdkWarningLogger.Writer()
	sdkWarningLogger.SetOutput(&logBuffer)
	defer sdkWarningLogger.SetOutput(previousOutput)

	memoryClient := NewMemory("k", WithURL(server.URL))
	outcome, searchErr := memoryClient.SearchWithRetrieval(context.Background(), "q", SessionsAll(),
		WithDebugSignals())
	if searchErr != nil {
		t.Fatalf("SearchWithRetrieval returned error: %v", searchErr)
	}
	if len(outcome.LegScores) != 2 {
		t.Fatalf("SearchWithRetrieval LegScores=%d want 2 — the envelope must carry them",
			len(outcome.LegScores))
	}
	if outcome.Retrieval == nil || outcome.Retrieval.Mode != "balanced" {
		t.Fatalf("Retrieval = %+v, want mode balanced", outcome.Retrieval)
	}
	if strings.TrimSpace(logBuffer.String()) != "" {
		t.Fatalf("a warning was logged for a call that dropped nothing: %q", logBuffer.String())
	}

	// The deprecated alias must be the SAME envelope, not a second code path.
	aliasOutcome, aliasErr := memoryClient.SearchWithSignals(context.Background(), "q", SessionsAll(),
		WithDebugSignals())
	if aliasErr != nil {
		t.Fatalf("SearchWithSignals returned error: %v", aliasErr)
	}
	if len(aliasOutcome.LegScores) != len(outcome.LegScores) {
		t.Fatalf("alias LegScores=%d want %d", len(aliasOutcome.LegScores), len(outcome.LegScores))
	}
}
