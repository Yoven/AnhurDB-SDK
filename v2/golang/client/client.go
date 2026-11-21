/*
Package client provides the Memory struct — a dead-simple 3-method API
for AnhurDB that matches the Python and TypeScript SDKs exactly.

Usage:

	mem := client.NewMemory("anhur_xxx")                                    // cloud
	mem := client.NewMemory("my-key", client.WithURL("http://localhost:8000")) // self-hosted

	result, _ := mem.Add(ctx, "User is a data scientist at Google")
	hits, _   := mem.Search(ctx, "what does this user do?", client.SessionsAll())
	profile, _ := mem.Profile(ctx)

Extended methods expose the full AnhurDB REST surface:
  - Batch operations (BatchReadContent, BatchUpdateStatus)
  - Entity knowledge graph (SearchEntities, UpsertEntity, EntityGraph, EntityTimeline)
  - File upload (UploadFile, UploadStatus)
  - Temporal versioning (Supersede)
  - Graph traversal (Walk, WalkSemantic)

This package uses only the Go standard library (net/http, encoding/json, etc.).
*/
package client

// client.go — the Memory handle itself: construction, identity (container tag
// and session id), liveness and String. Every REST capability lives in its own
// domain file (add.go, create.go, sessions.go, manifest.go, entities.go,
// record_links.go, search*.go, record_*.go, upload*.go, ...).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// DefaultCloudURL is the production AnhurDB cloud endpoint.
// Self-hosted users pass WithURL("http://localhost:8000").
const DefaultCloudURL = "https://anhurdb.yoven.ai"

// Memory is the primary entry point for the AnhurDB Go SDK.
//
// It mirrors the Python Memory class and TypeScript Memory class:
//   - Add()     stores a memory (POST /ingest, or /records when pinned)
//   - Search()  finds relevant memories via hybrid search
//   - Profile() retrieves the user/agent memory profile
//
// Extended methods provide access to the full REST tool set including
// batch operations, entity knowledge graph, file upload, and temporal
// versioning.
//
// Memory is safe for concurrent use. The underlying http.Client handles
// connection pooling and is goroutine-safe.
type Memory struct {
	conn         *HTTPConnection
	containerTag string
	sessionUUID  string
	// sessionRegistered is true only after CreateSession/OpenSession succeeds.
	// A local sessionUUID alone must not be sent on writes.
	sessionRegistered bool
}

// NewMemory creates a new Memory client.
//
// The apiKey is required. Use functional options to configure URL,
// user ID, and tenant ID:
//
//	mem := client.NewMemory("key", client.WithURL("http://localhost:8000"))
//
// If no user ID is provided, the SDK derives a stable container_tag from
// a SHA-256 hash of the API key — same algorithm as the Python and
// TypeScript SDKs.
func NewMemory(apiKey string, opts ...Option) *Memory {
	if apiKey == "" {
		// Return a Memory that will fail on every call rather than panicking.
		return &Memory{}
	}
	// Reject keys that cannot be sent as a safe HTTP header value (CRLF etc.).
	if err := validateHeaderValue(apiKey, "apiKey"); err != nil {
		return &Memory{}
	}

	cfg := &memoryConfig{
		url: DefaultCloudURL,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	conn := newConnectionFromConfig(apiKey, cfg)
	if cfg.tenantID != "" {
		conn.TenantID = cfg.tenantID
	}

	// Derive container tag: explicit user ID or SHA-256 of API key.
	// Must match Python: hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:12]
	var containerTag string
	if cfg.userID != "" {
		containerTag = cfg.userID
	} else {
		hash := sha256.Sum256([]byte(apiKey))
		containerTag = "mem-" + hex.EncodeToString(hash[:])[:12]
	}

	// Session UUID: containerTag + UTC timestamp + random 6 hex chars.
	sessionUUID := deriveSessionUUID(containerTag)

	return &Memory{
		conn:              conn,
		containerTag:      containerTag,
		sessionUUID:       sessionUUID,
		sessionRegistered: false,
	}
}

// newConnectionFromConfig builds the transport shared by NewMemory and
// NewClientAPI: URL, timeout and an optional injected *http.Client. It never
// sets TenantID — that decision belongs to the caller (NewClientAPI must
// never send X-Tenant-ID).
//
// Junior Tip [the redirect guard travels with the injected client]: an
// injected *http.Client usually has CheckRedirect == nil, which FOLLOWS 3xx
// and replays X-API-Key to whatever host Location names. The SDK's guard is
// copied onto a shallow copy, so the caller's client is never mutated and no
// constructor can forget the guard.
func newConnectionFromConfig(apiKey string, cfg *memoryConfig) *HTTPConnection {
	conn := NewConnection(cfg.url, apiKey, cfg.timeout)
	if cfg.httpClient != nil {
		injected := *cfg.httpClient // shallow copy: never mutate the caller's client
		injected.CheckRedirect = conn.HTTPClient.CheckRedirect
		if injected.Timeout == 0 {
			injected.Timeout = conn.HTTPClient.Timeout
		}
		conn.HTTPClient = &injected
	}
	return conn
}

// randomHex generates n random hex characters using crypto/rand
// for unpredictable session IDs.
func randomHex(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)[:n]
}

// utcTimestamp returns the current UTC time as "YYYYMMDD-HHMMSS".
func utcTimestamp() string {
	return time.Now().UTC().Format("20060102-150405")
}

// deriveSessionUUID builds the default auto-derived session UUID:
//
//	<container_tag>-<YYYYMMDD-HHMMSS UTC>-<6 hex random>
//
// e.g. "mem-3f9a1b2c4d5e-20260703-143025-a1b2c3".
func deriveSessionUUID(containerTag string) string {
	return containerTag + "-" + utcTimestamp() + "-" + randomHex(6)
}

// Health checks server liveness via GET /api/v1/health.
//
// Junior Tip [parity]: matches Python Memory.health and TypeScript health —
// a cheap probe before write/search traffic.
func (m *Memory) Health(ctx context.Context) (map[string]interface{}, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	respBytes, getErr := m.conn.Get(ctx, "/api/v1/health", nil)
	if getErr != nil {
		return nil, getErr
	}
	var result map[string]interface{}
	if unmarshalErr := json.Unmarshal(respBytes, &result); unmarshalErr != nil {
		return nil, fmt.Errorf("Health: decode response: %w", unmarshalErr)
	}
	return result, nil
}

// NewSession rotates the local session UUID (generates a fresh random suffix).
// This does NOT register the session on the server — call CreateSession before
// Add on session-first deployments. Returns the new local id (parity with
// Python new_session / TypeScript newSession).
func (m *Memory) NewSession() string {
	m.sessionUUID = deriveSessionUUID(m.containerTag)
	m.sessionRegistered = false
	return m.sessionUUID
}

// SessionID returns the current session UUID (read-only).
func (m *Memory) SessionID() string {
	return m.sessionUUID
}

// ContainerTag returns the container tag used for grouping memories.
func (m *Memory) ContainerTag() string {
	return m.containerTag
}

// String returns a human-readable representation for debugging.
func (m *Memory) String() string {
	if m.conn == nil {
		return "Memory(not initialized)"
	}
	return fmt.Sprintf("Memory(url=%q, container_tag=%q, session=%q)",
		m.conn.BaseURL, m.containerTag, m.sessionUUID)
}
