package client

// search_adr0031_test.go — the ADR-0031 knobs: they must reach the wire, an
// unknown mode must be refused locally, the 13 per-hit signals and leg_scores
// must decode, and a server that IGNORED mode=semantic must fail the call loud.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// searchServerWith replies to POST /api/v1/search with the given raw JSON body
// and records the request body it received.
func searchServerWith(t *testing.T, responseJSON string, captured *map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		rawBody, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("reading request body: %v", readErr)
		}
		if captured != nil {
			if unmarshalErr := json.Unmarshal(rawBody, captured); unmarshalErr != nil {
				t.Fatalf("request body is not valid JSON: %v (%s)", unmarshalErr, rawBody)
			}
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, responseJSON)
	}))
}

// TestSearchKnobsReachTheWire pins that each of the three controls is sent when
// set — and, just as important, OMITTED when not. A knob that is always present
// is a knob the server can never default.
func TestSearchKnobsReachTheWire(t *testing.T) {
	testCases := []struct {
		name          string
		options       []SearchOption
		expectPresent map[string]interface{}
		expectAbsent  []string
	}{
		{
			name:         "no knobs set omits all three",
			options:      nil,
			expectAbsent: []string{"mode", "semantic_timeout_ms", "debug_signals"},
		},
		{
			name:          "mode fast is sent",
			options:       []SearchOption{WithSearchMode(SearchModeFast)},
			expectPresent: map[string]interface{}{"mode": SearchModeFast},
			expectAbsent:  []string{"semantic_timeout_ms", "debug_signals"},
		},
		{
			name:          "semantic timeout is sent",
			options:       []SearchOption{WithSemanticTimeoutMs(120)},
			expectPresent: map[string]interface{}{"semantic_timeout_ms": float64(120)},
			expectAbsent:  []string{"mode", "debug_signals"},
		},
		{
			name:          "debug signals is sent",
			options:       []SearchOption{WithDebugSignals()},
			expectPresent: map[string]interface{}{"debug_signals": true},
			expectAbsent:  []string{"mode", "semantic_timeout_ms"},
		},
		{
			name:          "a zero timeout is treated as unset",
			options:       []SearchOption{WithSemanticTimeoutMs(0)},
			expectAbsent:  []string{"semantic_timeout_ms"},
			expectPresent: map[string]interface{}{},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			var capturedBody map[string]interface{}
			// retrieval.mode is filled so the cross-version guard stays quiet.
			server := searchServerWith(subTest, `{"results":[],"retrieval":{"mode":"balanced"}}`, &capturedBody)
			defer server.Close()

			memoryClient := NewMemory("k", WithURL(server.URL))
			if _, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(), testCase.options...); searchErr != nil {
				subTest.Fatalf("Search returned error: %v", searchErr)
			}
			for key, expectedValue := range testCase.expectPresent {
				if capturedBody[key] != expectedValue {
					subTest.Fatalf("payload[%q]=%#v want %#v", key, capturedBody[key], expectedValue)
				}
			}
			for _, key := range testCase.expectAbsent {
				if _, present := capturedBody[key]; present {
					subTest.Fatalf("payload carries %q=%#v; an unset knob must be omitted", key, capturedBody[key])
				}
			}
		})
	}
}

// TestUnknownSearchModeIsRefusedLocally proves the SDK is stricter than the
// server on purpose: the server would normalise "semantik" to balanced and
// answer 200, so only a client-side check can tell the caller about the typo.
func TestUnknownSearchModeIsRefusedLocally(t *testing.T) {
	requestReached := false
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestReached = true
		io.WriteString(responseWriter, `{"results":[]}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(), WithSearchMode("semantik"))
	if searchErr == nil {
		t.Fatal("Search accepted an unknown mode; it must be refused before the request leaves")
	}
	if requestReached {
		t.Fatal("the request reached the server; validation must happen client-side")
	}

	expectedMessage := `INVALID_PARAM: 'mode' "semantik" is not supported; use "fast", "balanced" or "semantic"`
	if searchErr.Error() != expectedMessage {
		t.Fatalf("error=%q want %q", searchErr.Error(), expectedMessage)
	}

	var apiError *APIError
	if !errors.As(searchErr, &apiError) {
		t.Fatal("a client-side rejection must still be a *APIError so callers can branch on Kind()")
	}
	if apiError.StatusCode != 400 || apiError.Kind() != KindInvalidRequest || apiError.Retryable() {
		t.Fatalf("status=%d kind=%q retryable=%v want 400/invalid_request/false",
			apiError.StatusCode, apiError.Kind(), apiError.Retryable())
	}
}

// TestSemanticModeFailsLoudWhenServerIgnoredIt is the cross-VERSION guard from
// ADR-0031's 2026-09-05 amendment. An old server drops `mode` into unknownFields
// and answers 200 with balanced results; the ONLY honest detector is that it did
// not echo retrieval.mode=semantic back.
func TestSemanticModeFailsLoudWhenServerIgnoredIt(t *testing.T) {
	testCases := []struct {
		name         string
		responseJSON string
		wantError    bool
	}{
		{
			name:         "old server: no retrieval block at all",
			responseJSON: `{"results":[{"record":{"id":1},"similarity":0.9}]}`,
			wantError:    true,
		},
		{
			name:         "old server: retrieval present but mode empty",
			responseJSON: `{"results":[],"retrieval":{"mode":""}}`,
			wantError:    true,
		},
		{
			name:         "old server: silently ran balanced",
			responseJSON: `{"results":[],"retrieval":{"mode":"balanced"}}`,
			wantError:    true,
		},
		{
			name:         "current server: honoured semantic",
			responseJSON: `{"results":[],"retrieval":{"mode":"semantic"}}`,
			wantError:    false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			server := searchServerWith(subTest, testCase.responseJSON, nil)
			defer server.Close()

			memoryClient := NewMemory("k", WithURL(server.URL))
			_, searchErr := memoryClient.Search(context.Background(), "q", SessionsAll(),
				WithSearchMode(SearchModeSemantic))

			if testCase.wantError && searchErr == nil {
				subTest.Fatal("mode=semantic was ignored by the server and the SDK stayed silent")
			}
			if !testCase.wantError && searchErr != nil {
				subTest.Fatalf("a server that honoured semantic must not error: %v", searchErr)
			}
			if testCase.wantError && !strings.Contains(searchErr.Error(), "SERVER_TOO_OLD") {
				subTest.Fatalf("error=%q must name the server as too old", searchErr.Error())
			}
		})
	}
}

// TestSemanticModeOnSharedAllWarnsInsteadOfFailing pins the exception measured
// in the server: handler/record_search_shared_all.go builds its RetrievalMeta by
// hand and leaves Mode empty on purpose (two legs, no single honest mode). A
// blanket fail-loud would reject every shared_all query against a HEALTHY
// current server, which is worse than the hazard it guards.
func TestSemanticModeOnSharedAllWarnsInsteadOfFailing(t *testing.T) {
	server := searchServerWith(t, `{"results":[],"retrieval":{"astar_weight":0.2}}`, nil)
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.SearchShared(context.Background(), "q", SessionsAll(),
		WithSearchMode(SearchModeSemantic))
	if searchErr != nil {
		t.Fatalf("shared_all must not fail loud on an empty retrieval.mode: %v", searchErr)
	}
}

// TestThirteenHitSignalsAndLegScoresDecode is the VALUE trap next to the name
// trap: every one of the 13 signals is driven with a DISTINCT non-zero sentinel,
// so a field wired to the wrong JSON key comes back zero and fails BY NAME. The
// same response carries leg_scores at the TOP level, where the server puts it.
func TestThirteenHitSignalsAndLegScoresDecode(t *testing.T) {
	responseJSON := `{
	  "results":[{"record":{"id":7},"similarity":0.5,"signals":{
	    "fts_rank":1,"semantic_rank":2,"simhash_rank":3,"simhash_hamming":4,
	    "rrf_score":5.5,"semantic_cosine":6.5,
	    "hnsw_rank":8,"bsq_rank":9,"parquet_rank":10,"fts5_rank":11,
	    "astar_rank":12,"entity_jaccard_rank":13,"active_leg_weight_sum":14.5}}],
	  "retrieval":{"mode":"balanced"},
	  "leg_scores":[{"leg":"hnsw","candidates":42,"top_scores":[0.9,0.8],"mean":0.85,"stddev":0.05}]
	}`
	server := searchServerWith(t, responseJSON, nil)
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	outcome, searchErr := memoryClient.SearchWithSignals(context.Background(), "q", SessionsAll(), WithDebugSignals())
	if searchErr != nil {
		t.Fatalf("SearchWithSignals returned error: %v", searchErr)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Signals == nil {
		t.Fatalf("expected one hit carrying signals, got %#v", outcome.Results)
	}

	signals := outcome.Results[0].Signals
	intFields := map[string]int{
		"fts_rank": signals.FTSRank, "semantic_rank": signals.SemanticRank,
		"simhash_rank": signals.SimHashRank, "simhash_hamming": signals.SimHashHamming,
		"hnsw_rank": signals.HNSWRank, "bsq_rank": signals.BSQRank,
		"parquet_rank": signals.ParquetRank, "fts5_rank": signals.FTS5Rank,
		"astar_rank": signals.AStarRank, "entity_jaccard_rank": signals.EntityJaccardRank,
	}
	expectedInts := map[string]int{
		"fts_rank": 1, "semantic_rank": 2, "simhash_rank": 3, "simhash_hamming": 4,
		"hnsw_rank": 8, "bsq_rank": 9, "parquet_rank": 10, "fts5_rank": 11,
		"astar_rank": 12, "entity_jaccard_rank": 13,
	}
	for fieldName, gotValue := range intFields {
		if gotValue != expectedInts[fieldName] {
			t.Fatalf("signal %q=%d want %d", fieldName, gotValue, expectedInts[fieldName])
		}
	}
	if signals.RRFScore != 5.5 || signals.SemanticCosine != 6.5 || signals.ActiveLegWeightSum != 14.5 {
		t.Fatalf("float signals rrf=%v cosine=%v weightSum=%v want 5.5/6.5/14.5",
			signals.RRFScore, signals.SemanticCosine, signals.ActiveLegWeightSum)
	}

	if len(outcome.LegScores) != 1 {
		t.Fatalf("leg_scores=%#v want exactly one summary decoded from the TOP level", outcome.LegScores)
	}
	legScore := outcome.LegScores[0]
	if legScore.Leg != "hnsw" || legScore.Candidates != 42 || legScore.Mean != 0.85 || legScore.StdDev != 0.05 {
		t.Fatalf("leg score=%#v want hnsw/42/0.85/0.05", legScore)
	}
	if len(legScore.TopScores) != 2 {
		t.Fatalf("top_scores=%#v want two values", legScore.TopScores)
	}
}
