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
