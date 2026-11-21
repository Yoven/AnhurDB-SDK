package client

// session_types.go — the shapes of the session domain (sessions.go): the
// per-session stats ListSessions returns and its paging envelope, and the
// CreateSession options and response.

import (
	"encoding/json"
)

// SessionStats describes aggregate information about a session.
type SessionStats struct {
	UUID         string         `json:"uuid"`
	RecordCount  int            `json:"record_count"`
	Types        map[string]int `json:"types"`
	LastActivity string         `json:"last_activity"`
	Summary      *string        `json:"summary,omitempty"`
}

// sessionsWrapper is the server's response envelope for ListSessions.
// The server returns {"sessions": [...], "has_more": bool, ...} rather than a bare array.
//
// Junior Tip [has_more paging]: without following has_more/next_offset, agents
// only ever saw the first page (server default limit=50). That silently stalled
// consolidation on large tenants (bench-1 had 356 sessions; page 0 looked "done").
type sessionsWrapper struct {
	Sessions   []SessionStats `json:"sessions"`
	Count      int            `json:"count"`
	Limit      int            `json:"limit"`
	Offset     int            `json:"offset"`
	HasMore    bool           `json:"has_more"`
	NextOffset int            `json:"next_offset"`
}

// CreateSessionOption configures Memory.CreateSession.
type CreateSessionOption func(*createSessionConfig)

// createSessionConfig holds optional overrides for POST /api/v1/sessions.
type createSessionConfig struct {
	sessionID string
	metadata  map[string]interface{}
}

// WithCreateSessionID registers an explicit session uuid. When omitted,
// CreateSession leaves session_id out of the body so the server generates one
// (parity with Python create_session / TypeScript createSession / MCP).
// To register a local id: NewSession() then CreateSession(WithCreateSessionID(...))
// or OpenSession().
func WithCreateSessionID(sessionID string) CreateSessionOption {
	return func(cfg *createSessionConfig) {
		cfg.sessionID = sessionID
	}
}

// WithCreateSessionMetadata attaches optional session-level metadata copied
// onto every record written in this session.
func WithCreateSessionMetadata(metadata map[string]interface{}) CreateSessionOption {
	return func(cfg *createSessionConfig) {
		cfg.metadata = metadata
	}
}

// createSessionResponse is the wire format returned by POST /api/v1/sessions.
type createSessionResponse struct {
	SessionID string          `json:"session_id"`
	Metadata  json.RawMessage `json:"metadata"`
}
