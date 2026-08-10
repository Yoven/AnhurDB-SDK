package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureSearchBody stands up an httptest server that records the JSON body of
// the single POST /api/v1/search request and replies with a minimal valid
// search envelope. It returns the decoded body captured during the call.
//
// Mirrors captureWalkBody's shape (walk_semantic_test.go) so the two mutation
// guards read the same way.
func captureSearchBody(t *testing.T, query string, opts ...SearchOption) map[string]interface{} {
	t.Helper()

	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		rawBody, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("reading request body: %v", readErr)
		}
		if unmarshalErr := json.Unmarshal(rawBody, &capturedBody); unmarshalErr != nil {
			t.Fatalf("request body is not valid JSON: %v (%s)", unmarshalErr, rawBody)
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, `{"results":[]}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, searchErr := memoryClient.Search(context.Background(), query, SessionsAll(), opts...); searchErr != nil {
		t.Fatalf("Search returned error: %v", searchErr)
	}
	return capturedBody
}

// TestSearchExpandRelatedRequestSerialization is a table-driven mutation guard
// proving the 3 ADR-0021 request fields (expand_related, astar_weight,
// entity_jaccard_weight) only land in the wire payload when the caller
// explicitly sets them — same omit-unless-set discipline already proven for
// skip_query_embed/skip_cognitive_rerank and for WalkSemantic's goal options.
func TestSearchExpandRelatedRequestSerialization(t *testing.T) {
	testCases := []struct {
		name        string
		options     []SearchOption
		wantPresent map[string]interface{}
		wantAbsent  []string
	}{
		{
			name:       "no options omits all 3 new fields",
			options:    nil,
			wantAbsent: []string{"expand_related", "astar_weight", "entity_jaccard_weight"},
		},
		{
			name: "WithExpandRelated sends expand_related=true",
			options: []SearchOption{
				WithExpandRelated(),
			},
			wantPresent: map[string]interface{}{
				"expand_related": true,
			},
			wantAbsent: []string{"astar_weight", "entity_jaccard_weight"},
		},
		{
			name: "WithAstarWeight sends the exact value, including a legal zero",
			options: []SearchOption{
				WithAstarWeight(0),
			},
			wantPresent: map[string]interface{}{
				// A caller-supplied 0 must reach the wire — it means "zero this
				// leg for this query only", distinct from never calling the
				// option at all (which omits the key entirely, see the first
				// test case above).
				"astar_weight": float64(0),
			},
			wantAbsent: []string{"expand_related", "entity_jaccard_weight"},
		},
		{
			name: "WithAstarWeight sends a non-zero override",
			options: []SearchOption{
				WithAstarWeight(0.35),
			},
			wantPresent: map[string]interface{}{
				"astar_weight": float64(0.35),
			},
			wantAbsent: []string{"expand_related", "entity_jaccard_weight"},
		},
		{
			name: "WithEntityJaccardWeight sends the exact value",
			options: []SearchOption{
				WithEntityJaccardWeight(0.7),
			},
			wantPresent: map[string]interface{}{
				"entity_jaccard_weight": float64(0.7),
			},
			wantAbsent: []string{"expand_related", "astar_weight"},
		},
		{
			name: "all 3 options compose on the same call",
			options: []SearchOption{
				WithExpandRelated(),
				WithAstarWeight(0.2),
				WithEntityJaccardWeight(0.4),
			},
			wantPresent: map[string]interface{}{
				"expand_related":        true,
				"astar_weight":          float64(0.2),
				"entity_jaccard_weight": float64(0.4),
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			body := captureSearchBody(t, "hello", testCase.options...)

			for wantKey, wantValue := range testCase.wantPresent {
				gotValue, present := body[wantKey]
				if !present {
					t.Fatalf("body missing key %q; got %#v", wantKey, body)
				}
				if gotValue != wantValue {
					t.Fatalf("body[%q] = %#v, want %#v", wantKey, gotValue, wantValue)
				}
			}
			for _, absentKey := range testCase.wantAbsent {
				if _, present := body[absentKey]; present {
					t.Fatalf("body should not contain key %q; got %#v", absentKey, body)
				}
			}
		})
	}
}

// searchResponseFixtureJSON is a representative POST /api/v1/search response
// carrying every ADR-0021 addition at once: provenance, scope, signals,
// related_nodes on a hit, plus the top-level retrieval block. Field names and
// types mirror server/model.SearchResult / RelatedNode / RetrievalMeta and
// the gRPC anhurpb.v1 counterparts verified against
// server/proto/anhurpb/v1/common.pb.go (RelatedNode.Id is int64, Weight is
// float64).
const searchResponseFixtureJSON = `{
  "results": [
    {
      "record": {"id": 42, "type": "fact", "summary": "acme uses postgres"},
      "similarity": 0.87,
      "provenance": "tenant_shared",
      "scope": "shared_all",
      "signals": {
        "fts_rank": 1,
        "semantic_rank": 2,
        "simhash_rank": 3,
        "simhash_hamming": 5,
        "rrf_score": 0.041,
        "semantic_cosine": 0.912
      },
      "related_nodes": [
        {"id": 123, "type": "fact", "summary": "acme migrated off mysql in 2025", "weight": 0.7},
        {"id": 124, "type": "decision", "summary": "chose postgres for jsonb support", "weight": 0.55}
      ]
    }
  ],
  "retrieval": {
    "mode": "balanced",
    "signals_used": ["fts", "semantic"],
    "semantic_attempted": true,
    "semantic_used": true,
    "degraded": false,
    "elapsed_ms": 37,
    "content_simhash_enabled": true,
    "content_simhash_weight": 0.15,
    "astar_enabled": true,
    "astar_weight": 0.2,
    "entity_jaccard_enabled": true,
    "entity_jaccard_weight": 0.4
  }
}`

// TestSearchDecodesExpandRelatedResponseFields proves SearchWithRetrieval
// decodes provenance/scope/signals/related_nodes on the hit and the top-level
// retrieval block, using the exact server wire shape (ADR-0021).
func TestSearchDecodesExpandRelatedResponseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, searchResponseFixtureJSON)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	results, retrieval, searchErr := memoryClient.SearchWithRetrieval(
		context.Background(), "postgres", SessionsAll(), WithExpandRelated(),
	)
	if searchErr != nil {
		t.Fatalf("SearchWithRetrieval returned error: %v", searchErr)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	hit := results[0]
	if hit.Provenance != "tenant_shared" {
		t.Fatalf("Provenance = %q, want tenant_shared", hit.Provenance)
	}
	if hit.Scope != "shared_all" {
		t.Fatalf("Scope = %q, want shared_all", hit.Scope)
	}
	if hit.Signals == nil {
		t.Fatal("Signals is nil, want populated")
	}
	if hit.Signals.FTSRank != 1 || hit.Signals.SemanticRank != 2 || hit.Signals.SimHashRank != 3 {
		t.Fatalf("Signals ranks = %+v, want fts=1 semantic=2 simhash=3", hit.Signals)
	}
	if hit.Signals.SimHashHamming != 5 {
		t.Fatalf("Signals.SimHashHamming = %d, want 5", hit.Signals.SimHashHamming)
	}
	if hit.Signals.RRFScore != 0.041 {
		t.Fatalf("Signals.RRFScore = %v, want 0.041", hit.Signals.RRFScore)
	}
	if hit.Signals.SemanticCosine != 0.912 {
		t.Fatalf("Signals.SemanticCosine = %v, want 0.912", hit.Signals.SemanticCosine)
	}

	if len(hit.RelatedNodes) != 2 {
		t.Fatalf("got %d related nodes, want 2", len(hit.RelatedNodes))
	}
	firstRelated := hit.RelatedNodes[0]
	if firstRelated.ID != 123 {
		t.Fatalf("RelatedNodes[0].ID = %d, want 123", firstRelated.ID)
	}
	if firstRelated.Type != "fact" {
		t.Fatalf("RelatedNodes[0].Type = %q, want fact", firstRelated.Type)
	}
	if firstRelated.Summary != "acme migrated off mysql in 2025" {
		t.Fatalf("RelatedNodes[0].Summary = %q", firstRelated.Summary)
	}
	if firstRelated.Weight != 0.7 {
		t.Fatalf("RelatedNodes[0].Weight = %v, want 0.7", firstRelated.Weight)
	}

	if retrieval == nil {
		t.Fatal("retrieval is nil, want populated")
	}
	if retrieval.Mode != "balanced" {
		t.Fatalf("retrieval.Mode = %q, want balanced", retrieval.Mode)
	}
	if len(retrieval.SignalsUsed) != 2 || retrieval.SignalsUsed[0] != "fts" || retrieval.SignalsUsed[1] != "semantic" {
		t.Fatalf("retrieval.SignalsUsed = %v, want [fts semantic]", retrieval.SignalsUsed)
	}
	if !retrieval.SemanticAttempted || !retrieval.SemanticUsed {
		t.Fatalf("retrieval semantic flags = attempted=%v used=%v, want both true", retrieval.SemanticAttempted, retrieval.SemanticUsed)
	}
	if retrieval.Degraded {
		t.Fatal("retrieval.Degraded = true, want false")
	}
	if retrieval.ElapsedMs != 37 {
		t.Fatalf("retrieval.ElapsedMs = %d, want 37", retrieval.ElapsedMs)
	}
	if retrieval.AStarWeight != 0.2 {
		t.Fatalf("retrieval.AStarWeight = %v, want 0.2", retrieval.AStarWeight)
	}
	if retrieval.EntityJaccardWeight != 0.4 {
		t.Fatalf("retrieval.EntityJaccardWeight = %v, want 0.4", retrieval.EntityJaccardWeight)
	}
}

// TestSearchIgnoresRetrievalWhenServerOmitsIt proves the plain Search method
// (unchanged public signature) keeps working when the server response carries
// none of the ADR-0021 fields, and that SearchWithRetrieval reports a nil
// RetrievalMeta rather than a zero-valued one when "retrieval" is absent from
// the wire — nil vs. zero-value here means "the server built no retrieval
// meta for this query" vs. "the server built one and every field happens to
// be zero", the same nil-is-not-zero discipline used throughout this SDK.
func TestSearchIgnoresRetrievalWhenServerOmitsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, `{"results":[{"record":{"id":1,"type":"fact","summary":"x"},"similarity":0.5}]}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))

	results, searchErr := memoryClient.Search(context.Background(), "hello", SessionsAll())
	if searchErr != nil {
		t.Fatalf("Search returned error: %v", searchErr)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Provenance != "" || results[0].Scope != "" || results[0].Signals != nil || results[0].RelatedNodes != nil {
		t.Fatalf("expected all ADR-0021 fields empty, got %+v", results[0])
	}

	_, retrieval, searchWithRetrievalErr := memoryClient.SearchWithRetrieval(context.Background(), "hello", SessionsAll())
	if searchWithRetrievalErr != nil {
		t.Fatalf("SearchWithRetrieval returned error: %v", searchWithRetrievalErr)
	}
	if retrieval != nil {
		t.Fatalf("retrieval = %+v, want nil", retrieval)
	}
}
