package client

// session_clusters_test.go — one domain: the two clustering knobs the server
// has always honoured and Go could not reach.
//
// These cases fail against the pre-3.0.0 GetSessionClusters, whose signature was
// (ctx, sessionUUID, opts ...ReadOption) with `_ = opts`: every Go caller got
// exactly one clustering, at the server's defaults, no matter what they asked.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestGetSessionClustersSendsEpsAndMin proves both knobs reach the query string
// with the names the handler parses (record_clustering.go:31-42).
func TestGetSessionClustersSendsEpsAndMin(t *testing.T) {
	var seenQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			seenQuery = request.URL.Query()
			io.WriteString(responseWriter, `{"clusters":[],"count":0}`)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, clusterErr := memoryClient.GetSessionClusters(context.Background(), "chat-1",
		0.6, 5); clusterErr != nil {
		t.Fatalf("GetSessionClusters returned error: %v", clusterErr)
	}
	if got := seenQuery.Get("eps"); got != "0.6" {
		t.Fatalf("eps=%q, want 0.6", got)
	}
	if got := seenQuery.Get("min"); got != "5" {
		t.Fatalf("min=%q, want 5", got)
	}
}

// TestGetSessionClustersOmitsUnsetKnobs keeps the server's tuned defaults
// (0.45 / 3) the single source of truth instead of restating them here.
func TestGetSessionClustersOmitsUnsetKnobs(t *testing.T) {
	var seenRawQuery string
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			seenRawQuery = request.URL.RawQuery
			io.WriteString(responseWriter, `{"clusters":[],"count":0}`)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, clusterErr := memoryClient.GetSessionClusters(context.Background(), "chat-1",
		0, 0); clusterErr != nil {
		t.Fatalf("GetSessionClusters returned error: %v", clusterErr)
	}
	if seenRawQuery != "" {
		t.Fatalf("query=%q, want empty so the server applies its own defaults", seenRawQuery)
	}
}
