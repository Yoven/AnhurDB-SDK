package client

// smart_search_decode_test.go — one domain: SmartSearch's typed envelope.
//
// These cases cannot even be written against the pre-3.0.0 SmartSearch, which
// returned raw []byte and left every caller to invent a shape.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSmartSearchDecodesTheEnvelope proves every key of the live envelope, and
// every key of a row, lands on a typed field.
func TestSmartSearchDecodesTheEnvelope(t *testing.T) {
	const responseJSON = `{"results":[{"id":18,"uuid":"chat-1","type":"episodic",` +
		`"summary":"memory notes","metadata":"{\"container_tag\":\"fable-1\"}",` +
		`"score":5,"weight":0.5,"status":"saved","relevance":3.25,"bm25":7.5,` +
		`"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z",` +
		`"provenance":"tenant_shared","scope":"shared_all","leg_relevance":2.5}],` +
		`"count":1,"scope":"sessions","bundle_hash":"abc123",` +
		`"bundle_ordering":"smart_relevance"}`
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			io.WriteString(responseWriter, responseJSON)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	response, searchErr := memoryClient.SmartSearch(context.Background(), "memory", SessionsAll(), 2)
	if searchErr != nil {
		t.Fatalf("SmartSearch returned error: %v", searchErr)
	}
	if response.Count != 1 || response.Scope != "sessions" {
		t.Fatalf("envelope = %+v, want count 1 / scope sessions", response)
	}
	if response.BundleHash != "abc123" || response.BundleOrdering != "smart_relevance" {
		t.Fatalf("bundle fields lost: %+v", response)
	}
	if len(response.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(response.Results))
	}
	hit := response.Results[0]
	if hit.ID != 18 || hit.UUID != "chat-1" || hit.Type != "episodic" {
		t.Fatalf("hit identity lost: %+v", hit)
	}
	if hit.Relevance != 3.25 || hit.BM25 != 7.5 {
		t.Fatalf("hit scores lost: relevance=%v bm25=%v", hit.Relevance, hit.BM25)
	}
	if hit.Metadata == "" {
		t.Fatal("metadata is a RAW JSON STRING on this route and must survive decoding")
	}
	if hit.Provenance != "tenant_shared" || hit.Scope != "shared_all" || hit.LegRelevance != 2.5 {
		t.Fatalf("shared-plane stamps lost: %+v", hit)
	}
}

// TestSmartSearchNullResultsDecodeToNil pins the trap: the handler marshals a Go
// slice, so "no matches" arrives as JSON null, not [].
//
// Junior Tip [why this deserves its own test]: a type promising a non-null array
// compiles fine and then throws on the most ORDINARY answer the endpoint gives.
// Nil here means no matches — it does not mean the key was absent.
func TestSmartSearchNullResultsDecodeToNil(t *testing.T) {
	const responseJSON = `{"results":null,"count":0,"scope":"sessions",` +
		`"bundle_hash":"","bundle_ordering":"smart_relevance"}`
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			io.WriteString(responseWriter, responseJSON)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	response, searchErr := memoryClient.SmartSearch(context.Background(), "nothing", SessionsAll(), 2)
	if searchErr != nil {
		t.Fatalf("a null results array is a normal answer, not an error: %v", searchErr)
	}
	if response.Results != nil {
		t.Fatalf("Results = %v, want nil for a JSON null", response.Results)
	}
	if response.Count != 0 {
		t.Fatalf("Count = %d, want 0", response.Count)
	}
	// Ranging over nil must be safe — that is the contract the doc comment sells.
	for range response.Results {
		t.Fatal("ranging a nil Results yielded an element")
	}
}
