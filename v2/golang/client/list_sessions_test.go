package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListSessionsEmptyEnvelope reproduces the empty-sessions crash discovered
// 2026-06-11 by monitoring the live pipeline: a tenant with ZERO sessions is
// returned by the server as {"sessions": []} (an OBJECT). The old code branched
// on len(wrapped.Sessions) > 0, fell through to a bare-array Unmarshal, and
// errored with "cannot unmarshal object into []SessionStats" — silently
// breaking the RunCycle of every pipeline agent on empty/new tenants. It must
// now yield an empty slice, no error.
func TestListSessionsEmptyEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, `{"count":0,"has_more":false,"limit":50,"next_offset":0,"offset":0,"sessions":[]}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	sessions, err := mem.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("empty sessions envelope must not error, got: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

// TestListSessionsPopulatedEnvelope verifies the envelope sessions are returned.
func TestListSessionsPopulatedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, `{"count":1,"sessions":[{"uuid":"s1","record_count":3}]}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	sessions, err := mem.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("populated envelope errored: %v", err)
	}
	if len(sessions) != 1 || sessions[0].UUID != "s1" {
		t.Fatalf("expected 1 session uuid=s1, got %+v", sessions)
	}
}

// TestListSessionsBareArray verifies the legacy bare-array shape still parses.
func TestListSessionsBareArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		io.WriteString(responseWriter, `[{"uuid":"s2","record_count":1}]`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	sessions, err := mem.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("bare array errored: %v", err)
	}
	if len(sessions) != 1 || sessions[0].UUID != "s2" {
		t.Fatalf("expected 1 session uuid=s2, got %+v", sessions)
	}
}

// TestListSessionsPaginatesPastDefaultLimit50 proves agents see the full tenant.
// Server default without query params is limit=50; ListSessions must page with
// limit=500 until has_more=false so consolidation/planner/judge do not silently
// stall after the first page (HEL1 bench-1: 356 sessions, 64 still needing work).
func TestListSessionsPaginatesPastDefaultLimit50(testState *testing.T) {
	requestOffsets := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestOffsets = append(requestOffsets, request.URL.Query().Get("offset"))
		limitParam := request.URL.Query().Get("limit")
		if limitParam != "500" {
			testState.Errorf("expected limit=500, got %q", limitParam)
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		switch request.URL.Query().Get("offset") {
		case "", "0":
			io.WriteString(responseWriter, `{"count":500,"limit":500,"offset":0,"has_more":true,"next_offset":500,"sessions":[{"uuid":"page0","record_count":1}]}`)
		case "500":
			io.WriteString(responseWriter, `{"count":1,"limit":500,"offset":500,"has_more":false,"next_offset":501,"sessions":[{"uuid":"page1","record_count":2}]}`)
		default:
			testState.Errorf("unexpected offset %q", request.URL.Query().Get("offset"))
			io.WriteString(responseWriter, `{"sessions":[],"has_more":false}`)
		}
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	sessions, listErr := mem.ListSessions(context.Background())
	if listErr != nil {
		testState.Fatalf("paginated ListSessions errored: %v", listErr)
	}
	if len(sessions) != 2 {
		testState.Fatalf("expected 2 sessions across pages, got %d (%+v)", len(sessions), sessions)
	}
	if sessions[0].UUID != "page0" || sessions[1].UUID != "page1" {
		testState.Fatalf("unexpected session order: %+v", sessions)
	}
	if len(requestOffsets) != 2 {
		testState.Fatalf("expected 2 page fetches, got offsets=%v", requestOffsets)
	}
}
