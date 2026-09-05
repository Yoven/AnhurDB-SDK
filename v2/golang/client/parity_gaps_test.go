package client

// parity_gaps_test.go — the three-SDK divergences closed on 2026-09-05:
// the walk depth default, the fluent query builder, and the version constant
// the User-Agent is derived from.

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

// captureWalkPayload records the JSON body of one walk request.
func captureWalkPayload(t *testing.T, path string, invoke func(*Memory) error) map[string]interface{} {
	t.Helper()
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != path {
			t.Fatalf("request hit %q want %q", request.URL.Path, path)
		}
		rawBody, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("reading request body: %v", readErr)
		}
		if unmarshalErr := json.Unmarshal(rawBody, &capturedBody); unmarshalErr != nil {
			t.Fatalf("request body is not valid JSON: %v", unmarshalErr)
		}
		io.WriteString(responseWriter, `{"nodes":[],"edges":[]}`)
	}))
	defer server.Close()

	if invokeErr := invoke(NewMemory("k", WithURL(server.URL))); invokeErr != nil {
		t.Fatalf("walk returned error: %v", invokeErr)
	}
	return capturedBody
}

// TestWalkDepthDefaultsToThree pins the cross-SDK default. Go used to forward
// depth:0 verbatim while TypeScript sent `depth ?? 3` and Python defaulted to 3 —
// the same call in three languages produced three different requests, and the Go
// one came back looking like an empty graph.
func TestWalkDepthDefaultsToThree(t *testing.T) {
	testCases := []struct {
		name          string
		requestDepth  int
		expectedDepth float64
	}{
		{name: "zero falls back", requestDepth: 0, expectedDepth: 3},
		{name: "negative falls back", requestDepth: -1, expectedDepth: 3},
		{name: "explicit depth is honoured", requestDepth: 5, expectedDepth: 5},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name+"/walk", func(subTest *testing.T) {
			payload := captureWalkPayload(subTest, "/api/v1/walk", func(memoryClient *Memory) error {
				_, walkErr := memoryClient.Walk(context.Background(), 1, testCase.requestDepth)
				return walkErr
			})
			if payload["depth"] != testCase.expectedDepth {
				subTest.Fatalf("depth=%#v want %v", payload["depth"], testCase.expectedDepth)
			}
		})
		t.Run(testCase.name+"/walk_semantic", func(subTest *testing.T) {
			payload := captureWalkPayload(subTest, "/api/v1/walk/semantic", func(memoryClient *Memory) error {
				_, walkErr := memoryClient.WalkSemantic(context.Background(), 1, testCase.requestDepth)
				return walkErr
			})
			if payload["depth"] != testCase.expectedDepth {
				subTest.Fatalf("depth=%#v want %v", payload["depth"], testCase.expectedDepth)
			}
		})
	}
}

// TestQueryBuilderParity covers the three methods Go was missing next to the
// TypeScript and Python builders: SelectFields, WhereEquals and Build.
func TestQueryBuilderParity(t *testing.T) {
	built, buildErr := NewQuery().
		SelectFields("id", "type", "id").
		WhereEquals("type", "fact").
		OrderBy("created_at", "desc").
		Limit(25).
		Offset(5).
		Build()
	if buildErr != nil {
		t.Fatalf("Build returned error: %v", buildErr)
	}

	if len(built.Select) != 2 || built.Select[0] != "id" || built.Select[1] != "type" {
		t.Fatalf("Select=%#v want deduped [id type] in first-seen order", built.Select)
	}
	if built.Filters["type"].Eq != "fact" {
		t.Fatalf("Filters[type].Eq=%#v want \"fact\"", built.Filters["type"].Eq)
	}
	if built.Pagination["limit"] != 25 || built.Pagination["offset"] != 5 {
		t.Fatalf("Pagination=%#v want limit 25 / offset 5", built.Pagination)
	}
}

// TestQueryBuilderBuildRefusesIllegalChain proves Build is a real gate and not
// decoration: the fluent chain swallows its complaints into buildErrors, so if
// Build did not surface them the query would only fail at the server.
func TestQueryBuilderBuildRefusesIllegalChain(t *testing.T) {
	_, buildErr := NewQuery().WhereEquals("not_a_column", 1).Build()
	if buildErr == nil {
		t.Fatal("Build accepted a column outside the whitelist")
	}
	if !strings.Contains(buildErr.Error(), "not allowed in filters") {
		t.Fatalf("error=%q must name the rejected column", buildErr.Error())
	}
}

// TestQueryBuilderExecuteRunsAgainstMemory pins that the builder stays ignorant
// of HTTP: it delegates to Memory.Query rather than learning a URL of its own.
func TestQueryBuilderExecuteRunsAgainstMemory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/query" {
			t.Fatalf("builder hit %q want /api/v1/query", request.URL.Path)
		}
		io.WriteString(responseWriter, `{"records":[{"id":9}],"count":1}`)
	}))
	defer server.Close()

	records, executeErr := NewQuery().WhereEquals("type", "fact").
		Execute(context.Background(), NewMemory("k", WithURL(server.URL)))
	if executeErr != nil {
		t.Fatalf("Execute returned error: %v", executeErr)
	}
	if len(records) != 1 || records[0].ID != 9 {
		t.Fatalf("records=%#v want one record with id 9", records)
	}

	if _, nilMemoryErr := NewQuery().Execute(context.Background(), nil); nilMemoryErr == nil {
		t.Fatal("Execute against a nil *Memory must fail loud, not panic later")
	}
}

// TestUserAgentIsDerivedFromVersion is the anti-drift trap. Before version.go
// the User-Agent was a literal that no manifest, tag or changelog agreed with.
func TestUserAgentIsDerivedFromVersion(t *testing.T) {
	if Version != "2.1.0" {
		t.Fatalf("Version=%q want 2.1.0 (converged with the TypeScript and Python SDKs)", Version)
	}
	if UserAgent != "AnhurSDK-Golang/"+Version {
		t.Fatalf("UserAgent=%q must be derived from Version, never typed again", UserAgent)
	}

	var observedUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedUserAgent = request.Header.Get("User-Agent")
		io.WriteString(responseWriter, `{"status":"ok"}`)
	}))
	defer server.Close()

	if _, healthErr := NewMemory("k", WithURL(server.URL)).Health(context.Background()); healthErr != nil {
		t.Fatalf("Health returned error: %v", healthErr)
	}
	if observedUserAgent != UserAgent {
		t.Fatalf("wire User-Agent=%q want %q", observedUserAgent, UserAgent)
	}
}

// TestSessionFilterRejectionsAreTypedErrors proves the session-filter rejections
// became *APIError WITHOUT their message changing — the message is pinned
// byte-for-byte against the Python and TypeScript SDKs.
func TestSessionFilterRejectionsAreTypedErrors(t *testing.T) {
	_, normalizeErr := normalizeSessionFilter(nil)
	if normalizeErr == nil {
		t.Fatal("a nil session filter must be rejected")
	}

	expectedMessage := `INVALID_PARAM: 'sessions' is required; use ["*"] for every session in scope`
	if normalizeErr.Error() != expectedMessage {
		t.Fatalf("error=%q want the pinned cross-SDK message %q", normalizeErr.Error(), expectedMessage)
	}

	var apiError *APIError
	if !errors.As(normalizeErr, &apiError) {
		t.Fatal("session-filter rejections must be *APIError so callers stop matching on text")
	}
	if apiError.StatusCode != 400 || apiError.Retryable() {
		t.Fatalf("status=%d retryable=%v want 400/false", apiError.StatusCode, apiError.Retryable())
	}
}
