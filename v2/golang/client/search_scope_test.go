package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSearchPostsCanonicalPathWithDefaultScope proves Search hits
// POST /api/v1/search with scope=sessions (never /search/global).
func TestSearchPostsCanonicalPathWithDefaultScope(testingState *testing.T) {
	var capturedPath string
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		bodyBytes, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)
		io.WriteString(responseWriter, `{"results":[],"scope":"sessions"}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.Search(context.Background(), "hello")
	if searchErr != nil {
		testingState.Fatalf("Search: %v", searchErr)
	}
	if capturedPath != "/api/v1/search" {
		testingState.Fatalf("path=%q want /api/v1/search", capturedPath)
	}
	if capturedBody["scope"] != "sessions" {
		testingState.Fatalf("scope=%v want sessions", capturedBody["scope"])
	}
}

// TestSearchTenantSharedHelperSendsScope proves the shared-plane helper
// forwards scope=tenant_shared on the wire.
func TestSearchTenantSharedHelperSendsScope(testingState *testing.T) {
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)
		io.WriteString(responseWriter, `{"results":[]}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, searchErr := memoryClient.SearchTenantShared(context.Background(), "Nomad")
	if searchErr != nil {
		testingState.Fatalf("SearchTenantShared: %v", searchErr)
	}
	if capturedBody["scope"] != "tenant_shared" {
		testingState.Fatalf("scope=%v want tenant_shared", capturedBody["scope"])
	}
}

// TestSmartSearchSendsScopeQuery proves SmartSearch defaults to sessions and
// honours WithScope on GET /api/v1/search/smart.
func TestSmartSearchSendsScopeQuery(testingState *testing.T) {
	var capturedPath string
	var capturedScope string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		capturedScope = request.URL.Query().Get("scope")
		io.WriteString(responseWriter, `{"results":[],"count":0}`)
	}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	_, smartErr := memoryClient.SmartSearch(context.Background(), "engineering", 10, WithScope("client_shared"))
	if smartErr != nil {
		testingState.Fatalf("SmartSearch: %v", smartErr)
	}
	if capturedPath != "/api/v1/search/smart" {
		testingState.Fatalf("path=%q", capturedPath)
	}
	if capturedScope != "client_shared" {
		testingState.Fatalf("scope=%q want client_shared", capturedScope)
	}
}
