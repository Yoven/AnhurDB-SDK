package client

// smart_search_types.go — one domain: the response envelope of
// GET /api/v1/search/smart.
//
// New file on 2026-09-14 because types.go is past the ~300-line house cut, and
// because this envelope is NOT the hybrid-search envelope: smart search is
// purely lexical (DuckDB/Parquet FTS fused with SQLite FTS5 by RRF, k=60) and
// its rows are a flat projection, not {record, similarity} pairs.

// SmartSearchResponse is the full envelope of GET /api/v1/search/smart
// (server/handler/search_smart.go:216-224).
//
// Junior Tip [Results is genuinely nullable, 2026-09-14]: the handler marshals
// a Go slice, and a nil slice serialises as JSON `null`, not `[]`. So the
// ordinary "no matches" answer is `"results": null`. A nil Results here means
// NO MATCHES — it does not mean the key was absent and it is not an error.
// Range over it directly; ranging over a nil slice is legal Go and yields
// nothing, which is exactly right.
//
// Junior Tip [why this replaced a []byte return]: SmartSearch used to hand the
// caller raw bytes, so every caller wrote their own decoder against a shape
// they had to guess. Three callers, three guesses, and none of them learned
// about bundle_hash. The SDK owns the shape now.
type SmartSearchResponse struct {
	// Results is nil when nothing matched. See the Junior Tip above.
	Results []SmartSearchHit `json:"results"`
	// Count is the number of hits the server returned.
	Count int `json:"count"`
	// Scope is the memory plane that actually answered (sessions,
	// tenant_shared, client_shared, shared_all).
	Scope string `json:"scope"`
	// BundleHash identifies the exact set of record ids this answer contains;
	// two identical hashes mean two identical bundles.
	BundleHash string `json:"bundle_hash"`
	// BundleOrdering is always "smart_relevance" on this route.
	BundleOrdering string `json:"bundle_ordering"`
}

// SmartSearchHit is one lexical hit. The fields are a FLAT projection of the
// record (server/duckdb/engine_lifecycle.go:34), not a nested record object.
//
// Junior Tip [Relevance is NOT comparable with SearchResult.Similarity]:
// Relevance is BM25 multiplied by cognitive decay — a lexical score on an
// unbounded scale that also folds in the record's age. Similarity on a hybrid
// SearchResult is a cosine in [-1,1]. Sorting a merged list by "whichever
// number the hit happens to carry" silently reorders one leg against the other;
// that is the same defect the cross-store merge rule exists to prevent. Keep
// the two rankings apart.
type SmartSearchHit struct {
	ID      int64  `json:"id"`
	UUID    string `json:"uuid"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	// Metadata is the record's metadata column as a RAW JSON STRING, exactly
	// as models.Record carries it — not a decoded object. Unmarshal it yourself
	// when you need a field out of it.
	Metadata  string  `json:"metadata"`
	Score     float64 `json:"score"`
	Weight    float64 `json:"weight"`
	Status    string  `json:"status"`
	Relevance float64 `json:"relevance"`
	// BM25 is the UNDECAYED lexical score, present only on the FTS leg
	// (omitempty on the wire). Zero here means "this leg produces no BM25",
	// never "the text matched badly".
	BM25      float64 `json:"bm25,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`

	// The three fields below are stamped only on the shared planes
	// (server/handler/search_scope_smart_merge.go:17-32) and are absent on the
	// sessions plane.

	// Provenance names which shared leg produced this row (tenant_shared or
	// client_shared).
	Provenance string `json:"provenance,omitempty"`
	// Scope is the plane stamp carried by the row itself.
	Scope string `json:"scope,omitempty"`
	// LegRelevance is the note the row carried OUT OF ITS OWN LEG, before the
	// cross-leg RRF fusion overwrote Relevance. Without Provenance next to it
	// this number means nothing: the two legs are different rulers.
	LegRelevance float64 `json:"leg_relevance,omitempty"`
}
