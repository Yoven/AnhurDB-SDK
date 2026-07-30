package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// TestFormatMemory_BacklogWarning pins the contract that a stuck disk queue is announced INSIDE the
// injected block, above the recalled memory.
//
// Junior Tip [why this test exists, 2026-07-17]: the queue already logged permanent failures to
// plugin.log and that was not enough — a 409 cap rejection sat there while recall kept rendering a
// confident, silently-incomplete memory, and nobody noticed until the user asked directly. The
// regression this guards is not "the code errors"; it is "the code stays quiet where a human looks".
func TestFormatMemory_BacklogWarning(t *testing.T) {
	cfg := config{container: "fable-1", recallLimit: 10}
	profile := &client.ProfileResult{Stats: map[string]interface{}{"total_records": 186.0, "sessions": 18.0}}

	clean := formatMemory(cfg, profile, queueBacklog{})
	if strings.Contains(clean, "Unpersisted backlog") {
		t.Fatal("empty queue must not inject a backlog warning")
	}

	stuck := formatMemory(cfg, profile, queueBacklog{
		chunkCount:   2,
		oldestChunk:  "2026-07-17T03:34:53Z",
		lastFlushErr: errors.New(`AnhurDB API error (HTTP 409): {"error":"session has reached the maximum of 500 records"}`),
	})
	for _, want := range []string{
		"Unpersisted backlog",
		"2 chunk(s)",
		"2026-07-17T03:34:53Z",
		"maximum of 500 records",
		"TELL THE USER",
	} {
		if !strings.Contains(stuck, want) {
			t.Errorf("backlog warning missing %q\n--- block ---\n%s", want, stuck)
		}
	}

	// The caveat is worthless if the model reads it after the memory it qualifies.
	if warnAt, memAt := strings.Index(stuck, "Unpersisted backlog"), strings.Index(stuck, "records across"); warnAt > memAt {
		t.Errorf("warning must precede the recalled memory (warning at %d, stats at %d)", warnAt, memAt)
	}
}

// TestFormatMemory_QuarantineWarning pins that quarantined chunks are announced inside the
// injected block, above the recalled memory, independently of the retryable backlog.
//
// Junior Tip [why this is its own warning, 2026-07-30]: the retryable backlog self-heals when
// AnhurDB recovers; a quarantined chunk never does — it has no provable session and writing it
// into any other session would merge two conversations (inviolable: 1 Claude session =
// 1 AnhurDB session). If this warning disappeared, quarantined turns would be silently missing
// from memory forever, with only a log line nobody tails as evidence.
func TestFormatMemory_QuarantineWarning(t *testing.T) {
	cfg := config{container: "fable-1", recallLimit: 10}
	profile := &client.ProfileResult{Stats: map[string]interface{}{"total_records": 10.0, "sessions": 2.0}}

	clean := formatMemory(cfg, profile, queueBacklog{})
	if strings.Contains(clean, "Quarantined") {
		t.Fatal("empty quarantine must not inject a quarantine warning")
	}

	warned := formatMemory(cfg, profile, queueBacklog{quarantinedChunkCount: 3, oldestQuarantined: "2026-07-30T00:00:00Z"})
	for _, want := range []string{
		"Quarantined chunks",
		"3 chunk(s)",
		"2026-07-30T00:00:00Z",
		"quarantine/",
		"NEVER persisted automatically",
		"TELL THE USER",
	} {
		if !strings.Contains(warned, want) {
			t.Errorf("quarantine warning missing %q\n--- block ---\n%s", want, warned)
		}
	}
	// The caveat is worthless if the model reads it after the memory it qualifies.
	if warnAt, memAt := strings.Index(warned, "Quarantined chunks"), strings.Index(warned, "records across"); warnAt > memAt {
		t.Errorf("quarantine warning must precede the recalled memory (warning at %d, stats at %d)", warnAt, memAt)
	}
}

// TestFormatMemory_NoMCPToolAdvertisement pins that the block never tells the model to call the
// mcp__anhurdb__* tools. They all require api_key (mcp.Required server-side), and the key is kept
// out of the transcript on purpose — the Stop hook persists that transcript into AnhurDB, so the
// advertisement invited leaking the key into the memory it protects.
func TestFormatMemory_NoMCPToolAdvertisement(t *testing.T) {
	cfg := config{container: "fable-1", recallLimit: 10}
	profile := &client.ProfileResult{Stats: map[string]interface{}{"total_records": 1.0, "sessions": 1.0}}

	block := formatMemory(cfg, profile, queueBacklog{})
	if strings.Contains(block, "let you recall/store more") {
		t.Error("block still advertises the mcp__anhurdb__* tools as usable")
	}
	if !strings.Contains(block, "api_key that is deliberately kept out of your context") {
		t.Errorf("block must state WHY the tools are unusable\n--- block ---\n%s", block)
	}
}

// TestChunkQueuedAt covers the filename-stamp parsing, including the fallback that keeps a stray
// file from costing us the warning entirely.
func TestChunkQueuedAt(t *testing.T) {
	cases := []struct{ chunkName, want string }{
		{"20260717T033453.506945398-2012775-1.txt", "2026-07-17T03:34:53Z"},
		{"garbage.txt", "garbage.txt"},
		{"nodash", "nodash"},
	}
	for _, testCase := range cases {
		if got := chunkQueuedAt(testCase.chunkName); got != testCase.want {
			t.Errorf("chunkQueuedAt(%q) = %q, want %q", testCase.chunkName, got, testCase.want)
		}
	}
}
