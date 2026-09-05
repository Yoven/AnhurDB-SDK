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
// It does NOT return leg_scores: the tuple has no room for a third block, and
// widening it would break the same callers the tuple exists to protect. When
// WithDebugSignals() was set and the server actually sent leg scores, this
// method LOGS that they are being dropped and names the method that carries
// them. Use SearchWithSignals for the same single envelope that TypeScript
// searchWithRetrieval and Python search_with_retrieval return.
func (m *Memory) SearchWithRetrieval(ctx context.Context, query string, sessions []string, opts ...SearchOption) ([]SearchResult, *RetrievalMeta, error) {
	outcome, searchErr := m.runSearch(ctx, query, sessions, opts...)
	if searchErr != nil {
		return nil, nil, searchErr
	}
	// Junior Tip [why this warns instead of silently dropping, 2026-09-05]: this
	// method's tuple has no room for leg_scores, while TypeScript
	// searchWithRetrieval and Python search_with_retrieval both return them from
	// their single envelope. Go cannot add a fourth return value without breaking
	// every existing caller, so the tuple stays and the LOSS is announced — a
	// caller who set WithDebugSignals() and got nothing back would otherwise
	// conclude the server produced no leg scores, which is the same
	// "absence read as zero" defect the ADR-0031 guard exists to prevent.
	// The rich form is SearchWithSignals; see its doc comment.
	if requestConfig := applyReadOptions(opts); requestConfig.debugSignals && len(outcome.LegScores) > 0 {
		// Same flagless writer as every other warning this SDK emits
		// (search_mode.go): one voice, one format, no timestamp glued to a
		// sentence three SDKs compare byte for byte. NOT deduplicated — this
		// one names a COUNT of dropped leg_scores, so a second call that
		// dropped a different number is genuinely new information, and it has
		// no TypeScript counterpart whose cadence it would have to match.
		sdkWarningLogger.Printf(warningPrefix+warnLegScoresDroppedByRetrievalForm, len(outcome.LegScores))
	}
	return outcome.Results, outcome.Retrieval, nil
}

// SearchWithSignals is Search plus EVERYTHING the server reported about how it
// found the hits: the RetrievalMeta and, under WithDebugSignals, the per-leg
// LegScoreSummary distributions.
//
// Junior Tip [why a third entry point rather than widening SearchWithRetrieval,
// 2026-09-05]: SearchWithRetrieval's ([]SearchResult, *RetrievalMeta, error)
// signature is public API that callers in other repositories already assign to
// three variables — adding a fourth return value would break every one of them
// at compile time, for a signal most of them never read. The same reasoning
// that gave SearchWithRetrieval its own method applies once more, and this time
// the return type is a STRUCT (SearchOutcome), so the next signal ADR adds a
// field instead of a fifth return value.
//
// Junior Tip [this is the method that matches the other two SDKs, 2026-09-05]:
// SearchOutcome{Results, Retrieval, LegScores} is field-for-field the TypeScript
// SearchWithRetrievalResult{results, retrieval, legScores} and the Python
// SearchResponse{.results, .retrieval, .leg_scores}. One envelope carrying all
// three blocks is the SHARED MENTAL MODEL of the three SDKs; only the Go method
// NAME differs, because SearchWithRetrieval was already taken by the narrower
// tuple that shipped first and cannot be widened without breaking callers.
// Reach for this one whenever you set WithDebugSignals().
func (m *Memory) SearchWithSignals(ctx context.Context, query string, sessions []string, opts ...SearchOption) (*SearchOutcome, error) {
	return m.runSearch(ctx, query, sessions, opts...)
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
