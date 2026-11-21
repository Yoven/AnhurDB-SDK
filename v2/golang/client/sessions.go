package client

// sessions.go — the session lifecycle and session-level reads: registering a
// write session (CreateSession / OpenSession), listing every session's stats
// (paged), and the per-session history and cluster reads.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// listSessionsPageLimit is the server max for GET /api/v1/sessions/stats (capped at 500).
// Agents always request this page size and follow has_more until the tenant is exhausted.
const listSessionsPageLimit = 500

// ListSessions returns aggregate statistics for all sessions in the tenant.
//
// Junior Tip [paginate past default 50]: the server defaults limit=50 for MCP
// token budgets. Agents need the full tenant, so we request limit=500 and follow
// has_more/next_offset until the last page. Skipping pagination left consolidation
// scanning only ~4 of 64 sessions that still needed work on HEL1 bench-1.
func (m *Memory) ListSessions(ctx context.Context) ([]SessionStats, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	allSessions := make([]SessionStats, 0)
	pageOffset := 0
	for {
		queryParams := url.Values{}
		queryParams.Set("limit", strconv.Itoa(listSessionsPageLimit))
		queryParams.Set("offset", strconv.Itoa(pageOffset))

		respBytes, getErr := m.conn.Get(ctx, "/api/v1/sessions/stats", queryParams)
		if getErr != nil {
			return nil, getErr
		}

		// Junior Tip [envelope only, 4.0.0]: the server always answers with
		// {"sessions","count","limit","offset","has_more","next_offset"}
		// (server/handler/record_session.go:135-142). The 3.x bare-array branch
		// for pre-envelope servers is gone: an array is now a decode error, not a
		// silently truncated single page. An empty body (or "null") is still
		// "no sessions", not an error.
		if firstJSONToken(respBytes) == 0 {
			return allSessions, nil
		}
		var wrapped sessionsWrapper
		if parseErr := json.Unmarshal(respBytes, &wrapped); parseErr != nil {
			return nil, fmt.Errorf("parsing sessions response: %w", parseErr)
		}
		allSessions = append(allSessions, wrapped.Sessions...)
		if !wrapped.HasMore {
			return allSessions, nil
		}
		// Junior Tip [prefer next_offset]: when the server sets next_offset we
		// advance by that; otherwise fall back to offset+page size so a buggy
		// has_more=true with next_offset=0 cannot infinite-loop on page 0.
		if wrapped.NextOffset > pageOffset {
			pageOffset = wrapped.NextOffset
		} else {
			pageOffset += listSessionsPageLimit
		}
	}
}

// GetSessionHistory retrieves paginated full-text history for a session.
//
// Returns actual message content, unlike ListSessions which returns
// metadata only.
func (m *Memory) GetSessionHistory(ctx context.Context, sessionUUID string, limit, offset int) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	params.Set("offset", strconv.Itoa(offset))

	path := fmt.Sprintf("/api/v1/sessions/%s/history", sessionUUID)
	return m.conn.Get(ctx, path, params)
}

// GetSessionClusters returns thematic clusters within a session — a DBSCAN-like
// grouping of the session's episodic records over BSQ Hamming space.
//
// eps is the Hamming tolerance ratio and must be in (0, 1]; minPoints is the
// minimum group size. Pass 0 for either to let the server apply its own tuned
// default (0.45 and 3 — server/handler/record_clustering.go:31-42).
//
// Junior Tip [why these are explicit parameters, 2026-09-14]: the server has
// honoured eps and min since the endpoint shipped, and Go could not reach
// either — the method took `opts ...ReadOption` and threw them away, so every
// Go caller got exactly one clustering no matter what they asked for. A knob
// that exists on the server and not in the SDK is a knob that reads as
// "unsupported" to anyone using the SDK as the contract.
func (m *Memory) GetSessionClusters(ctx context.Context, sessionUUID string, eps float64, minPoints int) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	// Junior Tip [0 means "let the server decide", 2026-09-14]: the handler
	// (server/handler/record_clustering.go:31-42) only overrides its defaults
	// when the key parses to a value in range — eps in (0,1], min > 0 — so
	// sending eps=0 would be indistinguishable from sending nothing anyway.
	// Omitting the key makes that explicit instead of hoping the server
	// re-validates, and keeps the server's own tuned defaults (0.45 / 3) the
	// single source of truth rather than restating them here.
	if eps > 0 {
		params.Set("eps", strconv.FormatFloat(eps, 'f', -1, 64))
	}
	if minPoints > 0 {
		params.Set("min", strconv.Itoa(minPoints))
	}

	path := fmt.Sprintf("/api/v1/sessions/%s/clusters", sessionUUID)
	return m.conn.Get(ctx, path, params)
}

// CreateSession registers a write session via POST /api/v1/sessions.
//
// Session-first servers reject ingest and record writes until this succeeds.
// Call once per conversation before Add.
//
// Junior Tip [parity with Python/TS/MCP]: omit WithCreateSessionID → server
// generates a new UUID (empty JSON body). To register a caller-chosen id:
// NewSession() then CreateSession(ctx, WithCreateSessionID(m.SessionID())).
func (m *Memory) CreateSession(ctx context.Context, opts ...CreateSessionOption) (string, error) {
	if m.conn == nil {
		return "", ErrEmptyAPIKey
	}

	cfg := &createSessionConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// Only send session_id when explicitly requested — same as Python/MCP.
	sessionIDToRegister := strings.TrimSpace(cfg.sessionID)

	payload := map[string]interface{}{}
	if sessionIDToRegister != "" {
		payload["session_id"] = sessionIDToRegister
	}
	if len(cfg.metadata) > 0 {
		payload["metadata"] = cfg.metadata
	}

	respBytes, postErr := m.conn.Post(ctx, "/api/v1/sessions", payload)
	if postErr != nil {
		return "", postErr
	}

	var resp createSessionResponse
	if unmarshalErr := json.Unmarshal(respBytes, &resp); unmarshalErr != nil {
		return "", fmt.Errorf("parsing create session response: %w", unmarshalErr)
	}

	registeredSessionID := resp.SessionID
	if registeredSessionID == "" {
		registeredSessionID = sessionIDToRegister
	}
	if registeredSessionID == "" {
		return "", fmt.Errorf("create session: server returned empty session_id")
	}
	m.sessionUUID = registeredSessionID
	m.sessionRegistered = true
	return registeredSessionID, nil
}

// OpenSession generates a fresh local session id and registers it (Python open_session).
// Equivalent to NewSession() then CreateSession(ctx, WithCreateSessionID(...)).
func (m *Memory) OpenSession(ctx context.Context, opts ...CreateSessionOption) (string, error) {
	localSessionID := m.NewSession()
	combinedOpts := append([]CreateSessionOption{WithCreateSessionID(localSessionID)}, opts...)
	return m.CreateSession(ctx, combinedOpts...)
}
