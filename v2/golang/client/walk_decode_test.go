package client

// walk_decode_test.go — one domain: what a walk response decodes into.
//
// These cases fail against the pre-3.0.0 WalkResult, whose Nodes were a
// three-field WalkNode projection (so eleven of the record's fourteen keys were
// silently dropped) and which had no Truncated field at all.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWalkDecodesFullRecordsNotAProjection proves nodes[] are full records.
//
// Junior Tip [why a projection is worse than it looks]: WalkNode kept id, type
// and summary. A caller who walked the graph to inspect weight, status or
// related_ids got zeros back and concluded the records were unscored and
// unlinked — a wrong answer with no error anywhere.
func TestWalkDecodesFullRecordsNotAProjection(t *testing.T) {
	const responseJSON = `{"nodes":[{"id":18,"uuid":"chat-1","type":"episodic",` +
		`"summary":"seed","status":"saved","weight":0.75,"score":9,` +
		`"related_ids":[19,20],"main_ids":[1],"consolidated":false,"archived":false,` +
		`"metadata":"{}","created_at":"2026-09-01T00:00:00Z",` +
		`"updated_at":"2026-09-02T00:00:00Z"}],` +
		`"edges":[{"source":18,"target":19}],"truncated":true}`
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			io.WriteString(responseWriter, responseJSON)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	result, walkErr := memoryClient.Walk(context.Background(), 18, 2)
	if walkErr != nil {
		t.Fatalf("Walk returned error: %v", walkErr)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(result.Nodes))
	}
	node := result.Nodes[0]
	if node.ID != 18 || node.UUID != "chat-1" || string(node.Type) != "episodic" {
		t.Fatalf("node identity lost: %+v", node)
	}
	if node.Weight != 0.75 || node.Score != 9 {
		t.Fatalf("node ranking signal lost: weight=%v score=%v", node.Weight, node.Score)
	}
	if len(node.RelatedIDs) != 2 || len(node.MainIDs) != 1 {
		t.Fatalf("node graph edges lost: related=%v main=%v", node.RelatedIDs, node.MainIDs)
	}
	if len(result.Edges) != 1 || result.Edges[0].Source != 18 || result.Edges[0].Target != 19 {
		t.Fatalf("edges lost: %+v", result.Edges)
	}
}

// TestWalkReportsTruncation proves a capped traversal is distinguishable from a
// genuinely small subgraph. Without this field they decode identically, and
// "the graph is sparse here" is exactly the wrong conclusion to reach silently.
func TestWalkReportsTruncation(t *testing.T) {
	testCases := []struct {
		name          string
		responseJSON  string
		wantTruncated bool
	}{
		{
			name:          "capped traversal",
			responseJSON:  `{"nodes":[],"edges":[],"truncated":true}`,
			wantTruncated: true,
		},
		{
			name:          "complete traversal",
			responseJSON:  `{"nodes":[],"edges":[],"truncated":false}`,
			wantTruncated: false,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(responseWriter http.ResponseWriter, request *http.Request) {
					io.WriteString(responseWriter, testCase.responseJSON)
				}))
			defer server.Close()

			memoryClient := NewMemory("k", WithURL(server.URL))
			result, walkErr := memoryClient.Walk(context.Background(), 18, 2)
			if walkErr != nil {
				subTest.Fatalf("Walk returned error: %v", walkErr)
			}
			if result.Truncated != testCase.wantTruncated {
				subTest.Fatalf("Truncated = %v, want %v", result.Truncated, testCase.wantTruncated)
			}
		})
	}
}
