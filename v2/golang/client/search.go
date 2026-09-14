package client

// search.go — every method that reads the search endpoints.
//
// Domain, in one sentence: the MECHANISM of POST /api/v1/search — build the
// body, validate the knobs before they leave, send it, read the response, and
// verify the server actually honoured what was asked.
//
// Split out of client.go on 2026-09-05 to carry the ADR-0031 knobs (client.go
// was 1679 lines, far past the ~300-line house cut). It was split AGAIN the
// same day, at 330 lines: the scope shortcuts and the sibling endpoints moved
// to search_scopes.go, mirroring the split the Python SDK already had
// (search.py vs search_scopes.py). What stays here is one responsibility a
// junior can name in one sentence; what left was "which plane, which endpoint",
// which is a different sentence.

import (
	"context"
	"encoding/json"
	"fmt"
)

// searchScopeSessions is the default plane for a search: the tenant's chat
// sessions, excluding the shared libraries.
const searchScopeSessions = "sessions"

// searchScopeSharedAll is the two-leg merge (tenant_shared + client_shared).
// It is called out by name because the server cannot report a single honest
// retrieval.mode for two legs — see verifySearchKnobsHonoured.
const searchScopeSharedAll = "shared_all"

// Search finds relevant memories using hybrid plane search.
//
// Uses POST /api/v1/search with default scope "sessions" (all chat sessions
// for the tenant, excluding shared-library uuids).
//
// sessions is MANDATORY (ADR-0014): pass client.SessionsAll() for every session
// inside the scope, or the explicit uuids to confine the query to those chats.
// nil and an empty slice are errors, never "all".
//
// Junior Tip [scope vs sessions]: the two are orthogonal. WithScope picks the
// BOUNDARY (which store/plane is reachable at all); sessions picks the SUBSET
// inside that boundary. ["*"] means "everything in this boundary" — it is not a
// way to cross into a shared plane.
//
// Agent UX — text is not semantic: query is sent as body "text" (FTS5
// exact-word matching), not an embedding. For conceptual RAG without a
// vector, prefer SmartSearch (or MCP recall).
func (m *Memory) Search(ctx context.Context, query string, sessions []string, opts ...SearchOption) ([]SearchResult, error) {
	outcome, searchErr := m.runSearch(ctx, query, sessions, opts...)
	if searchErr != nil {
		return nil, searchErr
	}
	return outcome.Results, nil
}

// SearchWithRetrieval is Search plus the server's RetrievalMeta block (ADR-0012,
// wire-extended by ADR-0021): which search arms actually ran, whether the
// query degraded, and the RESOLVED astar/entity-jaccard weights after any
// per-request override.
//
// Junior Tip [why a new method instead of changing Search's return type]:
// Search's signature ([]SearchResult, error) is public API that existing
// callers already depend on — widening it to a 3-tuple would be a breaking
// change for every caller in every repo that imports this SDK. Retrieval meta
// is additive and opt-in-by-relevance (most callers never look at it), so it
// gets its own method instead of forcing every caller to accept and discard a
// third return value. Returns retrieval=nil when the server did not attach a
// "retrieval" key to the response — nil is not an error, it just means the
// server built no RetrievalMeta for this query.
//
// It returns the WHOLE envelope the server sent — Results, Retrieval and
// LegScores — in one *SearchOutcome.
//
// Junior Tip [why this stopped being a 3-tuple, 2026-09-14]: the old signature
// was ([]SearchResult, *RetrievalMeta, error). leg_scores is a TOP-LEVEL key of
// the search response, a sibling of retrieval, so the tuple structurally could
// not carry it — and the SDK compensated by logging a warning that leg scores
// were being dropped, then by growing a FOURTH search method to return them.
// A method that cannot express its own endpoint's answer is not an API, it is a
// migration waiting to happen. SearchOutcome{Results, Retrieval, LegScores} is
// field-for-field the TypeScript SearchWithRetrievalResult and the Python
// SearchResponse, so all three SDKs now return one envelope from one method.
//
// Migration from the tuple form is two lines:
//
//	results, meta, err := mem.SearchWithRetrieval(ctx, q, sessions)   // before
//	outcome, err := mem.SearchWithRetrieval(ctx, q, sessions)         // after
//	// then outcome.Results / outcome.Retrieval / outcome.LegScores
//
// Retrieval is nil when the server attached no "retrieval" key to the response.
// nil is not an error: it means the server built no RetrievalMeta for this
// query.
func (m *Memory) SearchWithRetrieval(ctx context.Context, query string, sessions []string, opts ...SearchOption) (*SearchOutcome, error) {
	return m.runSearch(ctx, query, sessions, opts...)
}

// SearchWithSignals is the pre-3.0.0 name for SearchWithRetrieval.
//
// Deprecated: use SearchWithRetrieval, which now returns the same
// *SearchOutcome. This alias exists so 2.x callers survive the 3.0.0 rename by
// one release; it is removed in 4.0.0. One break, not two.
func (m *Memory) SearchWithSignals(ctx context.Context, query string, sessions []string, opts ...SearchOption) (*SearchOutcome, error) {
	return m.SearchWithRetrieval(ctx, query, sessions, opts...)
}

// runSearch is the shared implementation behind Search, SearchWithRetrieval and
// SearchWithSignals — centralised so the three public entry points can never
// drift in payload shape, validation, cross-version checking or response
// decoding. Every one of them is a projection of the single SearchOutcome this
// builds.
func (m *Memory) runSearch(ctx context.Context, query string, sessions []string, opts ...SearchOption) (*SearchOutcome, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if query == "" {
		return nil, ErrEmptyInput
	}

	resolvedSessions, sessionsErr := normalizeSessionFilter(sessions)
	if sessionsErr != nil {
		return nil, sessionsErr
	}

	cfg := &searchConfig{limit: 10, scope: searchScopeSessions}
	for _, opt := range opts {
		opt(cfg)
	}

	// Validate BEFORE the request leaves: an unknown mode is a caller typo, and
	// the server would silently fall back to balanced instead of complaining.
	// The NORMALISED form (trimmed, lowercased) is written back into cfg so that
	// the payload, the response comparison and the warnings all speak one
	// alphabet — see normalizeSearchMode for why leniency was the right
	// direction to converge the three SDKs on.
	normalizedMode, modeErr := validateSearchMode(cfg.searchMode)
	if modeErr != nil {
		return nil, modeErr
	}
	cfg.searchMode = normalizedMode
	// Same reason, same moment: a negative budget would be swallowed by the
	// omit-unless-set gate below and never reach the server. See
	// validateSemanticTimeoutMs.
	if timeoutErr := validateSemanticTimeoutMs(cfg.semanticTimeoutMs); timeoutErr != nil {
		return nil, timeoutErr
	}

	payload := map[string]interface{}{
		"text":     query,
		"limit":    cfg.limit,
		"scope":    cfg.scope,
		"sessions": resolvedSessions,
	}
	if cfg.typeFilter != "" {
		payload["type_filter"] = cfg.typeFilter
	}
	if cfg.skipQueryEmbed {
		payload["skip_query_embed"] = true
	}
	if cfg.skipCognitiveRerank {
		payload["skip_cognitive_rerank"] = true
	}
	// ADR-0021 (2026-08-10): same omit-unless-set discipline as the two flags
	// above. astarWeight/entityJaccardWeight are pointers specifically so a
	// caller-supplied 0.0 ("zero this leg for this query only") can be told
	// apart from "never called the option" ("leave the server default alone")
	// — dereference only after the nil check, never collapse the two.
	if cfg.expandRelated {
		payload["expand_related"] = true
	}
	if cfg.astarWeight != nil {
		payload["astar_weight"] = *cfg.astarWeight
	}
	if cfg.entityJaccardWeight != nil {
		payload["entity_jaccard_weight"] = *cfg.entityJaccardWeight
	}
	// ADR-0031 Stage 2 (2026-09-05): the same omit-unless-set discipline once
	// more. Sending mode:"" would be harmless (the server normalises it to
	// balanced) but sending semantic_timeout_ms:0 would NOT be — 0 is the
	// server's own sentinel for "use the default", so writing the key at all
	// only adds noise. Omit means omit, for all three.
	if cfg.searchMode != "" {
		payload["mode"] = cfg.searchMode
	}
	if cfg.semanticTimeoutMs > 0 {
		payload["semantic_timeout_ms"] = cfg.semanticTimeoutMs
	}
	if cfg.debugSignals {
		payload["debug_signals"] = true
	}

	respBytes, err := m.conn.PostRead(ctx, "/api/v1/search", payload)
	if err != nil {
		return nil, err
	}

	var resp searchResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parsing search response: %w", err)
	}

	// Cross-VERSION guard: prove the server honoured what we asked for before
	// handing the caller a result set that may not mean what they think.
	if honourErr := verifySearchKnobsHonoured(cfg, resp.Retrieval, cfg.scope); honourErr != nil {
		return nil, honourErr
	}

	// The wire envelope already IS the public SearchResult shape ({record,
	// similarity}), so return the decoded slice directly — no flatten step, and
	// the FULL nested models.Record survives (the old flatten kept only
	// id/type/summary/metadata/content). Preserve the historical non-nil
	// empty-slice contract when the server omits "results".
	outcome := &SearchOutcome{
		Results:   resp.Results,
		Retrieval: resp.Retrieval,
		LegScores: resp.LegScores,
	}
	if outcome.Results == nil {
		outcome.Results = []SearchResult{}
	}
	return outcome, nil
}
