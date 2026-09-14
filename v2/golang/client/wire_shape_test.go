package client

// wire_shape_test.go — one domain: pinning each response struct's JSON field
// set against the EXACT key set the handler emits.
//
// Junior Tip [why both directions are asserted, 2026-09-14]: a test that only
// checks the fields it already knows about cannot catch a phantom. Every case
// below asserts two things at once —
//   - no server key is missing from the struct (a dropped field decodes to the
//     zero value, and a zero timestamp reads as "never", not as "I lost it");
//   - no struct field is absent from the server (a phantom field makes a dead
//     branch look like a safety net — see UploadStatusResult.Error, which the
//     wait loop treated as a terminal condition for months).
//
// These cases fail against the pre-3.0.0 structs: UploadResult declared a
// phantom id and missed five keys, UploadStatusResult declared id/filename/error
// and missed uuid/type, ProfileResult declared tag/status, and WalkResult
// declared start_id/depth while missing truncated.

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// jsonFieldNames returns the wire key of every exported field of a struct,
// with the ",omitempty" suffix stripped. Fields tagged `json:"-"` are skipped:
// they are deliberately off the wire.
func jsonFieldNames(structValue interface{}) []string {
	structType := reflect.TypeOf(structValue)
	names := make([]string, 0, structType.NumField())
	for fieldIndex := 0; fieldIndex < structType.NumField(); fieldIndex++ {
		tag := structType.Field(fieldIndex).Tag.Get("json")
		if tag == "-" {
			continue
		}
		key, _, _ := strings.Cut(tag, ",")
		if key == "" {
			key = structType.Field(fieldIndex).Name
		}
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}

// assertExactWireShape fails when the struct and the server disagree in either
// direction, naming which side is wrong so the reader does not have to diff.
func assertExactWireShape(t *testing.T, label string, structValue interface{}, serverKeys []string) {
	t.Helper()
	declared := map[string]bool{}
	for _, key := range jsonFieldNames(structValue) {
		declared[key] = true
	}
	sent := map[string]bool{}
	for _, key := range serverKeys {
		sent[key] = true
	}
	for key := range sent {
		if !declared[key] {
			t.Errorf("%s: the server sends %q and the struct does not declare it — "+
				"that field decodes to its zero value on every response", label, key)
		}
	}
	for key := range declared {
		if !sent[key] {
			t.Errorf("%s: the struct declares %q and the server never sends it — "+
				"phantom field, delete it", label, key)
		}
	}
}

// TestUploadResultMatchesTheAcceptEnvelope pins POST /api/v1/upload's HTTP 202
// map (server/handler/upload.go:109-119). There is no `id` key on this route.
func TestUploadResultMatchesTheAcceptEnvelope(t *testing.T) {
	assertExactWireShape(t, "UploadResult", UploadResult{}, []string{
		"message", "record_id", "uuid", "filename", "mime", "mime_detected",
		"extension", "size_bytes", "status",
	})
}

// TestUploadIDHasNoPhantomFallback proves UploadID() reads record_id and only
// record_id. The old fallback turned a failed decode into a plausible-looking
// id of 0, and the caller then polled /upload/0/status until the budget ran out.
func TestUploadIDHasNoPhantomFallback(t *testing.T) {
	if got := (UploadResult{RecordID: 42}).UploadID(); got != 42 {
		t.Fatalf("UploadID() = %d, want 42", got)
	}
	if got := (UploadResult{}).UploadID(); got != 0 {
		t.Fatalf("UploadID() on an empty result = %d, want 0 so the caller can see the failure", got)
	}
}

// TestUploadStatusResultMatchesTheStatusEnvelope pins the fixed 7-key status map
// (server/handler/upload.go:220-236).
func TestUploadStatusResultMatchesTheStatusEnvelope(t *testing.T) {
	assertExactWireShape(t, "UploadStatusResult", UploadStatusResult{}, []string{
		"record_id", "uuid", "status", "type", "summary", "metadata", "completed",
	})
}

// TestProfileResultMatchesTheProfileEnvelope pins the three-key envelope and
// each of its blocks (server/handler/profile.go:27-51).
func TestProfileResultMatchesTheProfileEnvelope(t *testing.T) {
	assertExactWireShape(t, "ProfileResult", ProfileResult{}, []string{"static", "dynamic", "stats"})
	assertExactWireShape(t, "ProfileStatic", ProfileStatic{}, []string{
		"facts", "preferences", "decisions", "risks", "emotions", "highlight",
	})
	assertExactWireShape(t, "ProfileDynamic", ProfileDynamic{}, []string{"recent_tasks", "recent_topics"})
	// last_active here, last_activity on a session row. Two handlers, two
	// spellings, both real. See ProfileStats.
	assertExactWireShape(t, "ProfileStats", ProfileStats{}, []string{"total_records", "sessions", "last_active"})
}

// TestWalkResultMatchesTheWalkEnvelope pins {nodes, edges, truncated}
// (server/handler/record_search_graph.go:219-223).
func TestWalkResultMatchesTheWalkEnvelope(t *testing.T) {
	assertExactWireShape(t, "WalkResult", WalkResult{}, []string{"nodes", "edges", "truncated"})
	assertExactWireShape(t, "WalkEdge", WalkEdge{}, []string{"source", "target"})
}

// TestSmartSearchResponseMatchesTheSmartEnvelope pins the smart-search envelope
// (server/handler/search_smart.go:216-224).
func TestSmartSearchResponseMatchesTheSmartEnvelope(t *testing.T) {
	assertExactWireShape(t, "SmartSearchResponse", SmartSearchResponse{}, []string{
		"results", "count", "scope", "bundle_hash", "bundle_ordering",
	})
	// Row keys: duckdb.SearchResult plus the three shared-plane stamps from
	// server/handler/search_scope_smart_merge.go:17-32.
	assertExactWireShape(t, "SmartSearchHit", SmartSearchHit{}, []string{
		"id", "uuid", "type", "summary", "metadata", "score", "weight", "status",
		"relevance", "bm25", "created_at", "updated_at",
		"provenance", "scope", "leg_relevance",
	})
}

// TestSessionStatsMatchesTheSessionRow pins the session row; the key really is
// last_activity here, unlike the profile stats block.
func TestSessionStatsMatchesTheSessionRow(t *testing.T) {
	assertExactWireShape(t, "SessionStats", SessionStats{}, []string{
		"uuid", "record_count", "types", "last_activity", "summary",
	})
}
