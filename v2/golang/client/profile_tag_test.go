package client

// profile_tag_test.go — one domain: GET /api/v1/profile and its single query
// parameter.
//
// Every case here fails against the pre-3.0.0 Profile, which took
// `opts ...ReadOption`, discarded them, and could only ever read the client's
// own derived tag.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const profileEnvelopeJSON = `{"static":{"facts":["f1"],"preferences":[],"decisions":[],` +
	`"risks":[],"emotions":[],"highlight":["h1"]},"dynamic":{"recent_tasks":["t1"],` +
	`"recent_topics":["topic"]},"stats":{"total_records":3577,"sessions":31,` +
	`"last_active":"2026-09-14T10:00:00Z"}}`

// TestProfileSendsTheRequestedTag proves WithProfileTag reaches the wire. Before
// 3.0.0 a Go caller had no way to express a tag at all.
func TestProfileSendsTheRequestedTag(t *testing.T) {
	var seenTag string
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			seenTag = request.URL.Query().Get("tag")
			io.WriteString(responseWriter, profileEnvelopeJSON)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, profileErr := memoryClient.Profile(context.Background(),
		WithProfileTag("hermes-1")); profileErr != nil {
		t.Fatalf("Profile returned error: %v", profileErr)
	}
	if seenTag != "hermes-1" {
		t.Fatalf("tag=%q, want hermes-1", seenTag)
	}
}

// TestProfileDefaultsToTheClientsOwnTag keeps the historical behaviour for a
// caller who passes no option.
func TestProfileDefaultsToTheClientsOwnTag(t *testing.T) {
	var seenTag string
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			seenTag = request.URL.Query().Get("tag")
			io.WriteString(responseWriter, profileEnvelopeJSON)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, profileErr := memoryClient.Profile(context.Background()); profileErr != nil {
		t.Fatalf("Profile returned error: %v", profileErr)
	}
	if seenTag == "" {
		t.Fatal("Profile sent an empty tag — the server answers HTTP 400 for that")
	}
	if seenTag != memoryClient.ContainerTag() {
		t.Fatalf("tag=%q, want the client's own %q", seenTag, memoryClient.ContainerTag())
	}
}

// TestProfileRefusesAnEmptyTagBeforeTheRequest proves the SDK does not spend a
// round trip to learn what it already knows: an absent tag is a guaranteed 400
// ("tag: tag is required", live-verified 2026-09-14).
func TestProfileRefusesAnEmptyTagBeforeTheRequest(t *testing.T) {
	requestsSeen := 0
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			requestsSeen++
			io.WriteString(responseWriter, profileEnvelopeJSON)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, profileErr := memoryClient.Profile(context.Background(),
		WithProfileTag("")); profileErr != nil {
		// An explicitly empty tag falls back to the client's own tag, which is
		// non-empty here, so this must SUCCEED rather than send tag="".
		t.Fatalf("Profile returned error: %v", profileErr)
	}
	if requestsSeen != 1 {
		t.Fatalf("requestsSeen=%d, want 1", requestsSeen)
	}

	// With no derivable tag at all there is nothing to send, so the call is
	// refused locally instead of collecting a 400.
	taglessClient := NewMemory("k", WithURL(server.URL))
	// Same-package test, so the unexported field is reachable: this reproduces
	// the one state where there is nothing to send — a live client whose tag
	// could not be derived.
	taglessClient.containerTag = ""
	_, taglessErr := taglessClient.Profile(context.Background())
	if taglessErr == nil {
		t.Fatal("Profile with no tag must be refused locally, not sent as tag=")
	}
	if !strings.Contains(taglessErr.Error(), "tag is required") {
		t.Fatalf("error %q must name the missing tag", taglessErr.Error())
	}
	if requestsSeen != 1 {
		t.Fatalf("the tagless call reached the server (%d requests) — refuse before the wire",
			requestsSeen)
	}
}

// TestProfileDecodesTheConcreteBlocks proves the three blocks are typed, not
// maps. Before 3.0.0 a caller had to string-key their way into interface{}.
func TestProfileDecodesTheConcreteBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			io.WriteString(responseWriter, profileEnvelopeJSON)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	profile, profileErr := memoryClient.Profile(context.Background())
	if profileErr != nil {
		t.Fatalf("Profile returned error: %v", profileErr)
	}
	if len(profile.Static.Facts) != 1 || profile.Static.Facts[0] != "f1" {
		t.Fatalf("Static.Facts = %v, want [f1]", profile.Static.Facts)
	}
	if len(profile.Dynamic.RecentTopics) != 1 {
		t.Fatalf("Dynamic.RecentTopics = %v, want one entry", profile.Dynamic.RecentTopics)
	}
	if profile.Stats.TotalRecords != 3577 || profile.Stats.Sessions != 31 {
		t.Fatalf("Stats = %+v, want 3577/31", profile.Stats)
	}
	if profile.Stats.LastActive == "" {
		t.Fatal("Stats.LastActive decoded empty — the key is last_active on THIS object")
	}
}

// TestProfileTreatsAnUnknownTagAsAnEmptyProfile pins the live behaviour: an
// unknown tag answers HTTP 200 with all zeros, NOT a 404. Turning that into an
// error would dress a caller's typo as a server failure.
func TestProfileTreatsAnUnknownTagAsAnEmptyProfile(t *testing.T) {
	const emptyProfileJSON = `{"static":{"facts":[],"preferences":[],"decisions":[],` +
		`"risks":[],"emotions":[],"highlight":[]},"dynamic":{"recent_tasks":[],` +
		`"recent_topics":[]},"stats":{"total_records":0,"sessions":0,"last_active":""}}`
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			io.WriteString(responseWriter, emptyProfileJSON)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	profile, profileErr := memoryClient.Profile(context.Background(),
		WithProfileTag("totally-not-a-real-tag-xyz"))
	if profileErr != nil {
		t.Fatalf("an unknown tag must not be an error: %v", profileErr)
	}
	if profile.Stats.TotalRecords != 0 || profile.Stats.LastActive != "" {
		t.Fatalf("expected an empty profile, got %+v", profile.Stats)
	}
}
