package client

// search_scopes.go — the scope shortcuts and the sibling search ENDPOINTS.
//
// Domain, in one sentence: the convenience grammar over the one hybrid search
// port — "search only chat sessions", "search only the shared library" — plus
// the two searches that are a DIFFERENT endpoint entirely (SearchByType,
// SmartSearch) and the MCP-flavoured alias Recall.
//
// Junior Tip [why these left search.go, 2026-09-05]: search.go had reached 330
// lines against this project's ~300-line cut, and the ADR-0031 knobs grew it
// again. The seam is the same one the Python SDK has drawn since ADR-0014
// (search.py vs search_scopes.py): search.go owns the MECHANISM — build the
// payload, send it, read the response, verify the server honoured the knobs —
// while this file owns nothing but WHICH plane or WHICH endpoint. Every
// function here is one line of delegation, and that is the point: a wrapper
// that grows a payload builder of its own is exactly how this SDK twice
// shipped a knob that silently never reached the wire.
//
// Junior Tip [why every wrapper takes and forwards opts]: a caller must be able
// to write SearchShared(ctx, q, sessions, WithSearchMode(SearchModeSemantic)).
// The wrapper PREPENDS its scope so a caller-supplied WithScope still wins if
// they pass one — options are applied in order, last write wins. Forwarding
// opts is not decoration; a wrapper with a fixed parameter list is a wrapper
// that goes stale the next time a knob is added, which is the divergence the
// Python SDK carried in recall/search_session until 2026-09-05.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/models"
)

// SearchSessions searches chat sessions only (scope=sessions).
// sessions is mandatory — see Search.
func (m *Memory) SearchSessions(ctx context.Context, query string, sessions []string, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, sessions, append([]SearchOption{WithScope(searchScopeSessions)}, opts...)...)
}

// SearchTenantShared searches tenant-shared library docs (scope=tenant_shared).
// sessions is mandatory and selects inside the shared boundary — see Search.
func (m *Memory) SearchTenantShared(ctx context.Context, query string, sessions []string, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, sessions, append([]SearchOption{WithScope("tenant_shared")}, opts...)...)
}

// SearchClientShared searches the client-wide shared library (scope=client_shared).
// sessions is mandatory and selects inside the shared boundary — see Search.
func (m *Memory) SearchClientShared(ctx context.Context, query string, sessions []string, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, sessions, append([]SearchOption{WithScope("client_shared")}, opts...)...)
}

// SearchShared searches both shared planes (scope=shared_all).
// sessions is mandatory and selects inside both shared boundaries — see Search.
func (m *Memory) SearchShared(ctx context.Context, query string, sessions []string, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, sessions, append([]SearchOption{WithScope(searchScopeSharedAll)}, opts...)...)
}

// SearchByType retrieves records filtered by memory type in the tenant store.
//
// Hits GET /api/v1/search/type — a type-index lookup, faster than plane search
// when you know the exact type.
//
// Agent UX — not a plane switch: no scope parameter. Does not search Shared
// Data. For specialty docs use SearchTenantShared / SearchClientShared /
// SearchShared (or Search with WithScope).
//
// sessions is MANDATORY (ADR-0014), exactly as in Search: this endpoint had no
// session argument at all before, so "give me the facts of this chat" quietly
// returned the facts of every chat.
func (m *Memory) SearchByType(ctx context.Context, memType string, sessions []string, limit int, opts ...ReadOption) ([]SearchResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if memType == "" {
		return nil, ErrEmptyInput
	}

	resolvedSessions, sessionsErr := normalizeSessionFilter(sessions)
	if sessionsErr != nil {
		return nil, sessionsErr
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	params.Set("type", memType)
	params.Set("limit", strconv.Itoa(limit))
	appendSessionsQueryParam(params, resolvedSessions)
	if cfg.keyword != "" {
		params.Set("q", cfg.keyword)
	}

	respBytes, err := m.conn.Get(ctx, "/api/v1/search/type", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Records []models.Record `json:"records"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parsing search-by-type response: %w", err)
	}

	// Preserve the non-nil empty-slice contract (callers range over the result).
	results := make([]SearchResult, 0, len(resp.Records))
	for _, record := range resp.Records {
		results = append(results, SearchResult{Record: record})
	}
	return results, nil
}

// SmartSearch performs full-text search with cognitive weight boosting.
//
// Prefer over Search for conceptual text queries (no embedding required).
// Uses GET /api/v1/search/smart with the same memory-plane scope as Search
// (default "sessions"). Pass WithScope to select a shared plane.
//
// sessions is MANDATORY (ADR-0014), exactly as in Search. SmartSearch is one of
// the two paths that had no session argument at all before — it accepted the
// scope, dropped the chat filter, and answered from every conversation.
func (m *Memory) SmartSearch(ctx context.Context, query string, sessions []string, limit int, opts ...ReadOption) ([]byte, error) {
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

	cfg := applyReadOptions(opts)
	scope := cfg.scope
	if scope == "" {
		scope = searchScopeSessions
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("limit", strconv.Itoa(limit))
	params.Set("scope", scope)
	appendSessionsQueryParam(params, resolvedSessions)
	if cfg.typeFilter != "" {
		params.Set("type", cfg.typeFilter)
	}

	return m.conn.Get(ctx, "/api/v1/search/smart", params)
}

// Recall searches for memories using plane-aware search (default sessions).
// Functionally identical to Search but named "Recall" to match the MCP
// tool set naming. Extra search options (including WithScope) are forwarded.
//
// sessions is MANDATORY (ADR-0014) — see Search.
func (m *Memory) Recall(ctx context.Context, query string, sessions []string, limit int, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, sessions, append([]SearchOption{WithLimit(limit)}, opts...)...)
}
