package client

// record_by_id.go — the by-id READS: metadata, content, topology, grounding and the
// batch content read.
//
// One domain in one sentence: "read one record (or a batch) by id, from the store the
// caller names with WithRecordScope, or from the caller's own store when no scope is
// given".
//
// Moved out of client.go and parity.go (both past the house 300-line cut) on
// 2026-11-20 when every one of these methods gained the same ...ByIDOption; the
// bodies are unchanged except for the scope.
//
// Server truth: server/router.go wraps GET /api/v1/records/{id}, /content, /topology,
// /explain and /grounding in rh.ScopedByID, and POST /records/batch-content resolves
// ?scope= itself (server/handler/record_batch.go). A row outside the named plane is
// 404 for a single id and silently OMITTED from a batch — exactly like a missing id.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// scopeQuery returns url.Values carrying scope, or an empty set when no scope.
func scopeQuery(scope RecordScope) url.Values {
	params := url.Values{}
	if scope != "" {
		params.Set("scope", string(scope))
	}
	return params
}

// Get fetches record metadata by ID via GET /api/v1/records/{id}.
//
// Junior Tip [vs ReadContent]: Get returns JSON metadata; ReadContent returns
// the full text/plain body. Both are part of the public Memory surface.
func (m *Memory) Get(ctx context.Context, recordID int64, opts ...ByIDOption) (map[string]interface{}, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return nil, fmt.Errorf("Get: recordID must be > 0")
	}
	scope, scopeErr := resolveByIDScope("Get", opts)
	if scopeErr != nil {
		return nil, scopeErr
	}
	path := fmt.Sprintf("/api/v1/records/%d", recordID)
	respBytes, getErr := m.conn.Get(ctx, path, scopeQuery(scope))
	if getErr != nil {
		return nil, getErr
	}
	var result map[string]interface{}
	if unmarshalErr := json.Unmarshal(respBytes, &result); unmarshalErr != nil {
		return nil, fmt.Errorf("Get: decode response: %w", unmarshalErr)
	}
	return result, nil
}

// GetContext retrieves the topological context around a record.
//
// Returns parent, child, and sibling records — useful for understanding
// how a memory fits into the knowledge graph.
func (m *Memory) GetContext(ctx context.Context, recordID int64, opts ...ByIDOption) (*ContextResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	scope, scopeErr := resolveByIDScope("GetContext", opts)
	if scopeErr != nil {
		return nil, scopeErr
	}

	path := fmt.Sprintf("/api/v1/records/%d/topology", recordID)
	respBytes, err := m.conn.Get(ctx, path, scopeQuery(scope))
	if err != nil {
		return nil, err
	}

	var result ContextResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing context response: %w", err)
	}

	return &result, nil
}

// ReadContent retrieves the full content payload for a record.
//
// Records store a summary for search indexing, but the full content may
// be much larger. This endpoint returns the complete decrypted file
// bytes for file records, or the inline content for episodic records.
func (m *Memory) ReadContent(ctx context.Context, recordID int64, opts ...ByIDOption) (string, error) {
	if m.conn == nil {
		return "", ErrEmptyAPIKey
	}
	scope, scopeErr := resolveByIDScope("ReadContent", opts)
	if scopeErr != nil {
		return "", scopeErr
	}

	path := fmt.Sprintf("/api/v1/records/%d/content", recordID)
	respBytes, err := m.conn.Get(ctx, path, scopeQuery(scope))
	if err != nil {
		return "", err
	}
	return string(respBytes), nil
}

// GetGrounding returns the grounding (provenance) of a record via GET
// /api/v1/records/{id}/grounding: the episodic anchors and consolidations
// reachable from the target within a BFS depth budget. It is the MCP
// get_grounding contract — used to answer "what source turns back this memory?".
//
// maxDepth is the BFS depth budget. Pass 0 to use the server default (3); any
// other value MUST be 1..5 inclusive or the server returns HTTP 400
// "max_depth must be an integer between 1 and 5" (we surface that error rather
// than clamping, so an out-of-range value fails loud).
func (m *Memory) GetGrounding(ctx context.Context, recordID int64, maxDepth int, opts ...ByIDOption) (*GroundingResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return nil, fmt.Errorf("GetGrounding: recordID must be > 0")
	}
	scope, scopeErr := resolveByIDScope("GetGrounding", opts)
	if scopeErr != nil {
		return nil, scopeErr
	}

	params := scopeQuery(scope)
	if maxDepth > 0 {
		// 0 means "let the server default to 3". Any explicit value is forwarded
		// as-is; the server validates the 1..5 range and rejects loudly.
		params.Set("max_depth", strconv.Itoa(maxDepth))
	}

	path := fmt.Sprintf("/api/v1/records/%d/grounding", recordID)
	respBytes, getErr := m.conn.Get(ctx, path, params)
	if getErr != nil {
		return nil, getErr
	}

	var result GroundingResult
	if decodeErr := json.Unmarshal(respBytes, &result); decodeErr != nil {
		return nil, fmt.Errorf("parsing grounding response: %w", decodeErr)
	}
	return &result, nil
}

// BatchReadContent fetches full content for multiple records in a single
// call (max 100). Eliminates the N+1 pattern of calling ReadContent
// in a loop.
//
// With WithRecordScope, ids whose row is outside that plane are OMITTED from the
// map (the server treats them like missing ids) — check the map, never assume
// every id came back.
func (m *Memory) BatchReadContent(ctx context.Context, ids []int64, opts ...ByIDOption) (map[string]json.RawMessage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	scope, scopeErr := resolveByIDScope("BatchReadContent", opts)
	if scopeErr != nil {
		return nil, scopeErr
	}

	payload := map[string]interface{}{"ids": ids}
	// Read behind POST — PostRead for search-shaped endpoints.
	respBytes, err := m.conn.PostRead(ctx, byIDPath("/api/v1/records/batch-content", scope), payload)
	if err != nil {
		return nil, err
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing batch content response: %w", err)
	}

	return result, nil
}
