package client

// search_types.go — the response side of POST /api/v1/search.
//
// Domain: every type the server sends back for a search, and nothing else.
// Split out of types.go on 2026-09-05 (ADR-0031 carry-over) because types.go
// was already 1042 lines — far past the ~300-line house cut — and house law
// forbids growing a file that is already over it. The search response is one
// responsibility a junior can name in one sentence: "what a hit looks like on
// the wire, and what the server tells you about how it found it."

import "github.com/Yoven/AnhurDB-SDK/v2/golang/v2/models"

// SearchResult represents a single search hit from the server: the COMPLETE
// record nested under "record" plus its relevance score under "similarity".
//
// Provenance/Scope/Signals/RelatedNodes were added 2026-08-10 (ADR-0021) to
// close a pre-existing gap: the server has sent these fields for a while, but
// the Go SDK silently dropped them (Go's json.Unmarshal ignores unknown
// fields by default — no error, no signal, the caller just never saw them).
// An agent using this SDK could not tell which plane answered a shared_all
// hit, could not read ablation signals, and could not see expand_related's
// output even after opting in with WithExpandRelated. All four are pointers
// or omitempty-tagged because the server only populates them conditionally
// (Signals needs debug_signals=true upstream of this SDK; RelatedNodes needs
// WithExpandRelated); their absence is not an error.
type SearchResult struct {
	Record     models.Record `json:"record"`
	Similarity float64       `json:"similarity"`
	// Provenance is the plane that actually produced this hit (sessions |
	// tenant_shared | client_shared). Meaningful mainly under scope=shared_all,
	// where Scope echoes the merged request but Provenance names the leg.
	Provenance string `json:"provenance,omitempty"`
	// Scope echoes the request's scope. May read shared_all while Provenance
	// names the specific leg that produced this particular hit.
	Scope string `json:"scope,omitempty"`
	// Signals is optional per-hit ablation debug (fts/semantic/simhash ranks,
	// RRF score, semantic cosine). nil unless the server decided to attach it
	// for this query (e.g. shared_all merge always attaches it internally).
	Signals *SearchHitSignals `json:"signals,omitempty"`
	// RelatedNodes is a bounded summary of graph-connected nodes (ADR-0021),
	// populated ONLY when the request set WithExpandRelated(). Depth 1,
	// budget-bounded, reusing the same A*/WalkAdmission the ranking already
	// built — never the full Record, never content. An empty/nil slice is not
	// an error: it means expand_related was false, or the walk found nothing
	// within budget.
	RelatedNodes []RelatedNode `json:"related_nodes,omitempty"`
}

// SearchHitSignals is optional per-hit ablation debug mirroring server
// model.SearchHitSignals field-for-field — all THIRTEEN fields since ADR-0031
// Stage 2 (2026-09-05). Never calibrated similarity: for measurement only, and
// populated only when the request set WithDebugSignals().
//
// Junior Tip [why the first two ranks are not the whole story, 2026-09-05]:
// FTSRank and SemanticRank are ALREADY-FUSED ranks — HNSW and BSQ collapse into
// SemanticRank, FTS5 and Parquet collapse into FTSRank. A caller debugging "why
// did this record win?" reading only those two sees a bimodal RRFScore with no
// explanation, because the four base legs were folded into two and A*/Jaccard
// had no field at all. The seven fields below are the un-folded view, and
// ActiveLegWeightSum is the RRF denominator (the summed weight of the legs that
// actually produced a rank for THIS hit) — the number that finally answers the
// question. Dropping them, as this SDK did until now, is not a missing feature:
// it is an ablation you cannot debug.
type SearchHitSignals struct {
	FTSRank        int     `json:"fts_rank,omitempty"`
	SemanticRank   int     `json:"semantic_rank,omitempty"`
	SimHashRank    int     `json:"simhash_rank,omitempty"`
	SimHashHamming int     `json:"simhash_hamming,omitempty"`
	RRFScore       float64 `json:"rrf_score,omitempty"`
	SemanticCosine float64 `json:"semantic_cosine,omitempty"`

	// The seven un-folded per-leg signals (ADR-0031 Stage 2, proto fields 7..13).
	// Every one is omitempty on the server too, so a zero here means "this leg
	// produced no rank for this hit", not "the server forgot".
	HNSWRank           int     `json:"hnsw_rank,omitempty"`
	BSQRank            int     `json:"bsq_rank,omitempty"`
	ParquetRank        int     `json:"parquet_rank,omitempty"`
	FTS5Rank           int     `json:"fts5_rank,omitempty"`
	AStarRank          int     `json:"astar_rank,omitempty"`
	EntityJaccardRank  int     `json:"entity_jaccard_rank,omitempty"`
	ActiveLegWeightSum float64 `json:"active_leg_weight_sum,omitempty"`
}

// LegScoreSummary is the pre-fusion raw-score distribution of ONE retrieval leg
// (ADR-0031 Stage 2). Mirrors server model.LegScoreSummary field-for-field.
//
// Junior Tip [why this is a sibling of retrieval and not a field inside it,
// 2026-09-05]: the server deliberately keeps leg_scores OUT of RetrievalMeta —
// see the dated Junior Tip on handler.attachLegScores. RetrievalMeta is mirrored
// field-for-field in the proto and in all three SDKs and that parity is a project
// invariant; leg_scores is research instrumentation that ships under
// debug_signals and must not drag a proto regeneration behind it. So on the REST
// wire it is a TOP-LEVEL key of the search response, exactly as gRPC puts it at
// SearchResponse.leg_scores (field 4) rather than inside the retrieval message.
// This SDK reads it from where the server actually puts it; do not "tidy" it
// into RetrievalMeta, or Go stops agreeing with both ports at once.
type LegScoreSummary struct {
	// Leg names the retrieval arm (e.g. "hnsw", "bsq", "fts5", "parquet").
	Leg string `json:"leg"`
	// Candidates is how many records this leg returned before fusion.
	Candidates int `json:"candidates"`
	// TopScores is the head of this leg's raw score distribution.
	TopScores []float64 `json:"top_scores,omitempty"`
	// Mean and StdDev summarise the same distribution (query-performance
	// prediction input — never a cross-query ranking signal).
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
}

// SearchOutcome is everything one search call produced: the hits, the retrieval
// meta, and (under WithDebugSignals) the per-leg score distributions.
//
// Junior Tip [why a struct instead of a fourth return value, 2026-09-05]: the
// server's own shared contract is service.HybridSearchOutcome{Results,
// Retrieval, LegScores} — ADR-0031 Stage 2 explicitly WIDENED that one object
// rather than opening a second channel, so that both ports read the same thing
// and no port can recompute a signal differently. This mirror keeps the SDK on
// the same shape: the next signal the server adds becomes a field here, not a
// fifth return value that breaks every caller's assignment.
type SearchOutcome struct {
	// Results are the hits, in server rank order. Never nil on success — an
	// empty search returns an empty slice, preserving the historical contract.
	Results []SearchResult
	// Retrieval is which arms ran and whether the query degraded. nil when the
	// server attached no "retrieval" key (see RetrievalMeta).
	Retrieval *RetrievalMeta
	// LegScores is the pre-fusion distribution per leg. Empty unless the request
	// set WithDebugSignals() AND the server is new enough to honour it — see
	// verifySearchKnobsHonoured, which warns when it was silently ignored.
	LegScores []LegScoreSummary
}

// RelatedNode is a bounded summary of one graph-connected node attached to a
// search hit (ADR-0021). Deliberately NOT a full models.Record — no content,
// no internal fields — so expand_related cannot multiply the response
// payload by N. Mirrors server anhurpb.v1.RelatedNode field-for-field (Id
// int64, Type/Summary string, Weight float64).
type RelatedNode struct {
	ID      int64   `json:"id"`
	Type    string  `json:"type"`
	Summary string  `json:"summary"`
	Weight  float64 `json:"weight"`
}

// RetrievalMeta describes which search arms actually ran for a query
// (ADR-0012, wire-extended by ADR-0021's astar/entity-jaccard resolved
// values). Mirrors server model.RetrievalMeta field-for-field. Score is
// intra-response only — not a cross-query ranking signal.
type RetrievalMeta struct {
	Mode                  string   `json:"mode"`
	SignalsUsed           []string `json:"signals_used"`
	SemanticAttempted     bool     `json:"semantic_attempted"`
	SemanticUsed          bool     `json:"semantic_used"`
	Degraded              bool     `json:"degraded"`
	Reason                string   `json:"reason,omitempty"`
	ElapsedMs             int64    `json:"elapsed_ms"`
	ContentSimHashEnabled bool     `json:"content_simhash_enabled"`
	ContentSimHashWeight  float64  `json:"content_simhash_weight"`
	// AStarEnabled/Weight and EntityJaccardEnabled/Weight report the RESOLVED
	// values actually used for this query — after any per-request override
	// (WithAstarWeight / WithEntityJaccardWeight) has already been applied. A
	// caller running an ablation sweep can read these back to confirm its
	// override took effect instead of trusting the request it sent.
	AStarEnabled         bool    `json:"astar_enabled"`
	AStarWeight          float64 `json:"astar_weight"`
	EntityJaccardEnabled bool    `json:"entity_jaccard_enabled"`
	EntityJaccardWeight  float64 `json:"entity_jaccard_weight"`
}

// searchResponse is the wire format returned by POST /api/v1/search:
// {"results":[{"record":{...},"similarity":...}, ...], "retrieval":{...}}.
//
// Retrieval is a pointer because the server only attaches it when it built a
// RetrievalMeta for the query (ADR-0012) — nil here means the response simply
// did not carry a "retrieval" key, not that retrieval was empty.
type searchResponse struct {
	Results   []SearchResult `json:"results"`
	Retrieval *RetrievalMeta `json:"retrieval,omitempty"`
	// LegScores is a TOP-LEVEL sibling of "retrieval", not a field inside it —
	// see the Junior Tip on LegScoreSummary for why the server puts it there.
	LegScores []LegScoreSummary `json:"leg_scores,omitempty"`
}
