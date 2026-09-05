package client

// search_parity_test.go — the five cross-SDK divergences closed on 2026-09-05.
//
// Domain: proof that the Go SDK behaves like the TypeScript and Python SDKs on
// the ADR-0031 knobs. Each test below corresponds to one divergence an
// independent probe measured by capturing the ACTUAL request body from every
// entry point in every SDK.
//
// Junior Tip [why these assert with EXACT equality]: the previous round pinned
// the INVALID_PARAM strings and asserted the SERVER_TOO_OLD one by substring.
// The substring assertion is precisely what let the three wordings drift apart
// again — `strings.Contains(err, "SERVER_TOO_OLD")` is true for any wording at
// all. A cross-SDK contract has to be compared the way a contract is compared.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── L1: SearchSession is on the ADR-0031 path ───────────────────────────────

// TestSearchSessionForwardsTheADR0031Knobs is the regression for the defect
// this file exists for: SearchSession built its own payload and dropped mode,
// semantic_timeout_ms and debug_signals on the floor. It compiled because
// SearchOption is a type alias of ReadOption, so the options were accepted and
// then never read.
func TestSearchSessionForwardsTheADR0031Knobs(t *testing.T) {
	var capturedBody map[string]interface{}
	server := searchServerWith(t, `{"results":[],"retrieval":{"mode":"semantic"}}`, &capturedBody)
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.SearchSession(context.Background(), "sess-1", "q",
		WithSearchMode(SearchModeSemantic),
		WithSemanticTimeoutMs(1500),
		WithDebugSignals())
	if searchErr != nil {
		t.Fatalf("SearchSession returned error: %v", searchErr)
	}

	if capturedBody["mode"] != SearchModeSemantic {
		t.Fatalf("payload[\"mode\"]=%#v want %q — the knob was dropped again", capturedBody["mode"], SearchModeSemantic)
	}
	if capturedBody["semantic_timeout_ms"] != float64(1500) {
		t.Fatalf("payload[\"semantic_timeout_ms\"]=%#v want 1500", capturedBody["semantic_timeout_ms"])
	}
	if capturedBody["debug_signals"] != true {
		t.Fatalf("payload[\"debug_signals\"]=%#v want true", capturedBody["debug_signals"])
	}
	if capturedBody["scope"] != searchScopeSessions {
		t.Fatalf("payload[\"scope\"]=%#v want %q", capturedBody["scope"], searchScopeSessions)
	}
}

// TestSearchSessionRefusesANegativeSemanticBudget proves the request-side
// validation now runs for this entry point too. Before the fix the same SDK
// refused -1 on Search and accepted it on SearchSession.
func TestSearchSessionRefusesANegativeSemanticBudget(t *testing.T) {
	requestReached := false
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestReached = true
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"results":[]}`))
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.SearchSession(context.Background(), "sess-1", "q",
		WithSemanticTimeoutMs(-1))
	if searchErr == nil {
		t.Fatal("SearchSession accepted a negative semantic budget; Search refuses it, so this must too")
	}
	if requestReached {
		t.Fatal("the request reached the server; a negative budget must be refused client-side")
	}
	const expectedMessage = "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0"
	if searchErr.Error() != expectedMessage {
		t.Fatalf("error=%q want %q", searchErr.Error(), expectedMessage)
	}
}

// TestSearchSessionFailsLoudWhenTheServerIgnoredSemanticMode proves the
// cross-VERSION guard now runs on this path. It never ran before, because the
// method never called runSearch.
func TestSearchSessionFailsLoudWhenTheServerIgnoredSemanticMode(t *testing.T) {
	server := searchServerWith(t, `{"results":[{"record":{"id":1},"similarity":0.9}]}`, nil)
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	results, searchErr := memoryClient.SearchSession(context.Background(), "sess-1", "q",
		WithSearchMode(SearchModeSemantic))
	if searchErr == nil {
		t.Fatal("an ignored mode=semantic must fail loud, not return lexical hits as semantic ones")
	}
	if results != nil {
		t.Fatalf("results=%#v want nil — degraded hits must never be handed back", results)
	}
	if !strings.HasPrefix(searchErr.Error(), "SERVER_TOO_OLD:") {
		t.Fatalf("error=%q want a SERVER_TOO_OLD failure", searchErr.Error())
	}
}

// TestSearchSessionBodyIsUnchangedForAKnoblessCaller is the other half of the
// L1 fix: routing through runSearch may not change one byte for a caller who
// passes no ADR-0031 knob. An "improvement" that rewrites the wire for existing
// callers is a breaking change wearing a fix's clothes.
func TestSearchSessionBodyIsUnchangedForAKnoblessCaller(t *testing.T) {
	testCases := []struct {
		name        string
		options     []ReadOption
		expectedRaw string
	}{
		{
			name:        "no options at all",
			options:     nil,
			expectedRaw: `{"limit":10,"scope":"sessions","sessions":["sess-1"],"text":"q"}`,
		},
		{
			name:        "an explicit non-positive limit still coerces to 10",
			options:     []ReadOption{WithLimit(0)},
			expectedRaw: `{"limit":10,"scope":"sessions","sessions":["sess-1"],"text":"q"}`,
		},
		{
			name:        "limit and type filter",
			options:     []ReadOption{WithLimit(3), WithTypeFilter("fact")},
			expectedRaw: `{"limit":3,"scope":"sessions","sessions":["sess-1"],"text":"q","type_filter":"fact"}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			var capturedBody map[string]interface{}
			server := searchServerWith(subTest, `{"results":[]}`, &capturedBody)
			defer server.Close()

			memoryClient := NewMemory("k", WithURL(server.URL))
			if _, searchErr := memoryClient.SearchSession(context.Background(), "sess-1", "q", testCase.options...); searchErr != nil {
				subTest.Fatalf("SearchSession returned error: %v", searchErr)
			}
			// Re-marshal the decoded map: encoding/json sorts object keys, so this
			// compares the SET of keys and their values, not the transport's
			// incidental ordering.
			reMarshalled, marshalErr := json.Marshal(capturedBody)
			if marshalErr != nil {
				subTest.Fatalf("re-marshalling the captured body: %v", marshalErr)
			}
			if string(reMarshalled) != testCase.expectedRaw {
				subTest.Fatalf("body=%s want %s", reMarshalled, testCase.expectedRaw)
			}
		})
	}
}

// ── L2: the SERVER_TOO_OLD text is a pinned cross-SDK contract ──────────────

// TestServerTooOldMessageIsThePinnedCrossSDKText compares the WHOLE message
// with equality. The TypeScript and Python suites pin the identical string.
func TestServerTooOldMessageIsThePinnedCrossSDKText(t *testing.T) {
	server := searchServerWith(t, `{"results":[]}`, nil)
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
		WithSearchMode(SearchModeSemantic))
	if searchErr == nil {
		t.Fatal("an ignored mode=semantic must fail loud")
	}
	const expectedMessage = `SERVER_TOO_OLD: requested mode="semantic" but the server answered ` +
		`retrieval.mode="" — this server predates ADR-0031 and IGNORED the mode field, so these ` +
		`results came from the balanced budget and may be purely lexical. Upgrade the server, or ` +
		`drop to mode="balanced" and read retrieval.degraded yourself.`
	if searchErr.Error() != expectedMessage {
		t.Fatalf("error=%q\nwant     %q", searchErr.Error(), expectedMessage)
	}
}

// TestSharedAllWarningIsThePinnedCrossSDKText pins the OTHER string of the
// pair. shared_all cannot be verified, so the SDK warns instead of throwing —
// and the words it warns with are part of the same contract.
func TestSharedAllWarningIsThePinnedCrossSDKText(t *testing.T) {
	server := searchServerWith(t, `{"results":[]}`, nil)
	defer server.Close()

	// The dedup ledger is per PROCESS, so an earlier test in this binary may
	// already have consumed the one "mode_shared_all" line this test waits for.
	resetSearchKnobWarnings()
	var logBuffer bytes.Buffer
	previousOutput := sdkWarningLogger.Writer()
	sdkWarningLogger.SetOutput(&logBuffer)
	defer sdkWarningLogger.SetOutput(previousOutput)

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
		WithScope(searchScopeSharedAll), WithSearchMode(SearchModeSemantic)); searchErr != nil {
		t.Fatalf("shared_all must warn, never fail: %v", searchErr)
	}

	const expectedLine = `anhurdb-sdk: warning: mode="semantic" cannot be CONFIRMED on scope="shared_all" — the ` +
		`server never echoes retrieval.mode for a two-leg merge, so a server too old for ADR-0031 ` +
		`looks identical to a current one here. Use a single scope to get the strict-semantic ` +
		`guarantee verified.`
	if strings.TrimRight(logBuffer.String(), "\n") != expectedLine {
		t.Fatalf("log=%q\nwant %q", strings.TrimRight(logBuffer.String(), "\n"), expectedLine)
	}
}

// ── L4: mode is trimmed and lowercased before it is validated ───────────────

// TestSearchModeIsNormalisedBeforeValidation covers the three spellings the
// probe measured: Python accepted them, Go and TypeScript rejected them. The
// direction of the fix is leniency, because it can only accept MORE.
func TestSearchModeIsNormalisedBeforeValidation(t *testing.T) {
	testCases := []struct {
		name         string
		requested    string
		expectedWire string
	}{
		{name: "uppercase", requested: "SEMANTIC", expectedWire: SearchModeSemantic},
		{name: "surrounding whitespace", requested: " semantic ", expectedWire: SearchModeSemantic},
		{name: "title case", requested: "Balanced", expectedWire: SearchModeBalanced},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			var capturedBody map[string]interface{}
			// Echo the NORMALISED mode, which is what a real server would echo —
			// this also proves the read-back comparison uses the normalised form
			// and does not throw SERVER_TOO_OLD at a healthy server.
			server := searchServerWith(subTest,
				`{"results":[],"retrieval":{"mode":"`+testCase.expectedWire+`"}}`, &capturedBody)
			defer server.Close()

			memoryClient := NewMemory("k", WithURL(server.URL))
			if _, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
				WithSearchMode(testCase.requested)); searchErr != nil {
				subTest.Fatalf("mode %q must be accepted: %v", testCase.requested, searchErr)
			}
			if capturedBody["mode"] != testCase.expectedWire {
				subTest.Fatalf("payload[\"mode\"]=%#v want %q — the wire must carry the normalised form",
					capturedBody["mode"], testCase.expectedWire)
			}
		})
	}
}

// TestAnUnknownModeStillEchoesWhatTheCallerTyped guards the other side of
// normalisation: leniency must not make the error message lie about the typo.
func TestAnUnknownModeStillEchoesWhatTheCallerTyped(t *testing.T) {
	memoryClient := NewMemory("k", WithURL("http://127.0.0.1:1"))
	_, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
		WithSearchMode(" SEMANITC "))
	if searchErr == nil {
		t.Fatal("an unknown mode must still be refused after trimming and lowering")
	}
	const expectedMessage = `INVALID_PARAM: 'mode' " SEMANITC " is not supported; use "fast", "balanced" or "semantic"`
	if searchErr.Error() != expectedMessage {
		t.Fatalf("error=%q want %q", searchErr.Error(), expectedMessage)
	}
}

// ── L5: SearchWithRetrieval never drops leg_scores in silence ───────────────

// TestSearchWithRetrievalAnnouncesDroppedLegScores proves the Go-only shape
// difference is reported rather than swallowed. TypeScript and Python return
// legScores from their searchWithRetrieval; Go's tuple cannot, so it says so.
func TestSearchWithRetrievalAnnouncesDroppedLegScores(t *testing.T) {
	const responseJSON = `{"results":[],"retrieval":{"mode":"balanced"},` +
		`"leg_scores":[{"leg":"fts5","candidates":7},{"leg":"vector","candidates":9}]}`
	server := searchServerWith(t, responseJSON, nil)
	defer server.Close()

	var logBuffer bytes.Buffer
	previousOutput := sdkWarningLogger.Writer()
	sdkWarningLogger.SetOutput(&logBuffer)
	defer sdkWarningLogger.SetOutput(previousOutput)

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, _, searchErr := memoryClient.SearchWithRetrieval(context.Background(), "q", SessionsAll(),
		WithDebugSignals())
	if searchErr != nil {
		t.Fatalf("SearchWithRetrieval returned error: %v", searchErr)
	}

	const expectedLine = `anhurdb-sdk: warning: debug_signals was set and the server returned 2 leg_scores ` +
		`entries, but SearchWithRetrieval's ([]SearchResult, *RetrievalMeta, error) tuple has no ` +
		`room for them. Call SearchWithSignals instead — it returns the same single envelope that ` +
		`TypeScript searchWithRetrieval and Python search_with_retrieval return.`
	if strings.TrimRight(logBuffer.String(), "\n") != expectedLine {
		t.Fatalf("log=%q\nwant %q", strings.TrimRight(logBuffer.String(), "\n"), expectedLine)
	}

	// And the rich form really does carry them, so the warning points somewhere.
	outcome, signalsErr := memoryClient.SearchWithSignals(context.Background(), "q", SessionsAll(),
		WithDebugSignals())
	if signalsErr != nil {
		t.Fatalf("SearchWithSignals returned error: %v", signalsErr)
	}
	if len(outcome.LegScores) != 2 {
		t.Fatalf("SearchWithSignals LegScores=%d want 2", len(outcome.LegScores))
	}
}
