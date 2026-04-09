/*
Package client provides the Memory struct — a dead-simple 3-method API
for AnhurDB that matches the Python and TypeScript SDKs exactly.

Usage:

	mem := client.NewMemory("anhur_xxx")                                    // cloud
	mem := client.NewMemory("my-key", client.WithURL("http://localhost:8000")) // self-hosted

	result, _ := mem.Add(ctx, "User is a data scientist at Google")
	hits, _   := mem.Search(ctx, "what does this user do?")
	profile, _ := mem.Profile(ctx)

Extended methods expose the full AnhurDB REST surface:
  - Batch operations (BatchReadContent, BatchUpdateStatus)
  - Entity knowledge graph (SearchEntities, UpsertEntity, EntityGraph, EntityTimeline)
  - File upload (UploadFile, UploadStatus)
  - Temporal versioning (Supersede)
  - Graph traversal (Walk, WalkSemantic)

Junior Tip: This package intentionally has ZERO external dependencies.
It uses only net/http, crypto/sha256, encoding/json, and other stdlib
packages. No third-party HTTP clients, no protobuf, no MCP.
*/
package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/anhurdb/sdk-go/v2/models"
)

// DefaultCloudURL is the production AnhurDB cloud endpoint.
// Self-hosted users pass WithURL("http://localhost:8000").
const DefaultCloudURL = "https://api.anhurdb.com"

// Memory is the primary entry point for the AnhurDB Go SDK.
//
// It mirrors the Python Memory class and TypeScript Memory class:
//   - Add()     stores a memory (cloud ingest with OSS fallback)
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
	conn            *HTTPConnection
	containerTag    string
	sessionUUID     string
	ingestAvailable *bool // nil = untested, true = yes, false = no
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

	cfg := &memoryConfig{
		url: DefaultCloudURL,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	conn := NewConnection(cfg.url, apiKey, cfg.timeout)
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

	// Session UUID: containerTag + random 12 hex chars.
	sessionUUID := containerTag + "-" + randomHex(12)

	return &Memory{
		conn:         conn,
		containerTag: containerTag,
		sessionUUID:  sessionUUID,
	}
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

// --------------------------------------------------------------------------
// Core methods — match Python/TS exactly
// --------------------------------------------------------------------------

// Add stores a memory. This is the simplest way to save information.
//
// It tries the cloud /api/v1/ingest endpoint first (which handles
// embedding + extraction automatically). If that returns 404, it
// falls back to /api/v1/records (OSS mode, stores as text).
func (m *Memory) Add(ctx context.Context, text string) (*AddResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if text == "" {
		return nil, ErrEmptyInput
	}

	// Try cloud ingest first (has auto-embedding).
	// Once we know ingest is unavailable (404), we skip it on subsequent
	// calls to avoid unnecessary round-trips.
	if m.ingestAvailable == nil || *m.ingestAvailable {
		result, err := m.tryIngest(ctx, text)
		if result != nil {
			return result, nil
		}
		// Only propagate non-404 errors.
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	// Fallback: direct record creation (OSS / self-hosted mode).
	return m.createRecord(ctx, text)
}

// tryIngest attempts the cloud ingest endpoint.
// Returns (nil, ErrNotFound) if the endpoint doesn't exist.
func (m *Memory) tryIngest(ctx context.Context, text string) (*AddResult, error) {
	payload := map[string]string{
		"content":       text,
		"container_tag": m.containerTag,
	}

	respBytes, err := m.conn.Post(ctx, "/api/v1/ingest", payload)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			f := false
			m.ingestAvailable = &f
			return nil, ErrNotFound
		}
		return nil, err
	}

	t := true
	m.ingestAvailable = &t

	var resp ingestResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parsing ingest response: %w", err)
	}

	records := resp.Records
	firstID := resp.ID
	if len(records) > 0 {
		firstID = records[0].ID
	}

	return &AddResult{
		ID:      firstID,
		Records: records,
		Status:  "ok",
		Mode:    "cloud",
	}, nil
}

// createRecord stores text directly via POST /api/v1/records.
//
// Without server-side embedding, we store the text in both summary
// (for FTS5 search) and content (for full retrieval). The vector is
// empty — the server handles records without vectors via text search.
func (m *Memory) createRecord(ctx context.Context, text string) (*AddResult, error) {
	summary := text
	if len(text) > 200 {
		summary = text[:200] + "..."
	}

	payload := map[string]interface{}{
		"uuid":           m.sessionUUID,
		"type":           "episodic",
		"dimension":      0,
		"prefix":         "",
		"weight":         0.5,
		"score":          5,
		"vector":         "",
		"related_ids":    []int{},
		"main_ids":       []int{},
		"consolidate_id": 0,
		"metadata":       m.containerTag,
		"summary":        summary,
		"content":        text,
		"consolidated":   false,
		"status":         "saved",
	}

	respBytes, err := m.conn.Post(ctx, "/api/v1/records", payload)
	if err != nil {
		return nil, err
	}

	var resp recordCreateResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parsing record response: %w", err)
	}

	return &AddResult{
		ID:      resp.ID,
		Records: []RecordSummary{{ID: resp.ID, Type: "episodic", Summary: summary}},
		Status:  "ok",
		Mode:    "oss",
	}, nil
}

// Search finds relevant memories using hybrid (vector + full-text) search.
//
// Uses global search (not session-scoped) so Memory finds facts across
// ALL sessions for this user, not just the current one.
func (m *Memory) Search(ctx context.Context, query string, opts ...SearchOption) ([]SearchResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if query == "" {
		return nil, ErrEmptyInput
	}

	cfg := &searchConfig{limit: 10}
	for _, opt := range opts {
		opt(cfg)
	}

	payload := map[string]interface{}{
		"text":  query,
		"limit": cfg.limit,
	}
	if cfg.typeFilter != "" {
		payload["type_filter"] = cfg.typeFilter
	}

	respBytes, err := m.conn.Post(ctx, "/api/v1/search/global", payload)
	if err != nil {
		return nil, err
	}

	var resp searchResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parsing search response: %w", err)
	}

	// Flatten the nested response into simple structs so users don't
	// need to know about MemoryRecord internals.
	results := make([]SearchResult, 0, len(resp.Results))
	for _, hit := range resp.Results {
		results = append(results, SearchResult{
			ID:         hit.Record.ID,
			Type:       hit.Record.Type,
			Summary:    hit.Record.Summary,
			Similarity: hit.Similarity,
			Metadata:   hit.Record.Metadata,
			Content:    hit.Record.Content,
		})
	}

	return results, nil
}

// Profile retrieves the memory profile for this container tag.
//
// If the server doesn't have a profile endpoint yet (OSS without agents),
// it returns an empty profile rather than failing — matching the Python
// SDK behaviour.
func (m *Memory) Profile(ctx context.Context) (*ProfileResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	params.Set("tag", m.containerTag)

	respBytes, err := m.conn.Get(ctx, "/api/v1/profile", params)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &ProfileResult{
				Static:  map[string]interface{}{},
				Dynamic: map[string]interface{}{},
				Stats:   map[string]interface{}{},
				Tag:     m.containerTag,
				Status:  "not_available",
			}, nil
		}
		return nil, err
	}

	var result ProfileResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing profile response: %w", err)
	}

	// Ensure maps are never nil.
	if result.Static == nil {
		result.Static = map[string]interface{}{}
	}
	if result.Dynamic == nil {
		result.Dynamic = map[string]interface{}{}
	}
	if result.Stats == nil {
		result.Stats = map[string]interface{}{}
	}

	return &result, nil
}

// --------------------------------------------------------------------------
// Extended methods — full REST tool set
// --------------------------------------------------------------------------

// SearchByType retrieves records filtered by memory type.
//
// Hits GET /api/v1/search/type which is a simple type-based index lookup —
// much faster than semantic search when you know the exact type you want.
func (m *Memory) SearchByType(ctx context.Context, memType string, limit int) ([]SearchResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if memType == "" {
		return nil, ErrEmptyInput
	}

	params := url.Values{}
	params.Set("type", memType)
	params.Set("limit", strconv.Itoa(limit))

	respBytes, err := m.conn.Get(ctx, "/api/v1/search/type", params)
	if err != nil {
		return nil, err
	}

	var resp searchResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parsing search-by-type response: %w", err)
	}

	results := make([]SearchResult, 0, len(resp.Results))
	for _, hit := range resp.Results {
		results = append(results, SearchResult{
			ID:         hit.Record.ID,
			Type:       hit.Record.Type,
			Summary:    hit.Record.Summary,
			Similarity: hit.Similarity,
			Metadata:   hit.Record.Metadata,
			Content:    hit.Record.Content,
		})
	}

	return results, nil
}

// SmartSearch performs full-text search with cognitive weight boosting.
//
// Uses the DuckDB-backed smart search engine that ranks results by a
// combination of text relevance and cognitive importance (score).
func (m *Memory) SmartSearch(ctx context.Context, query string, limit int) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if query == "" {
		return nil, ErrEmptyInput
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("limit", strconv.Itoa(limit))

	return m.conn.Get(ctx, "/api/v1/search/smart", params)
}

// Recall searches for memories using global search with a wider scope.
// Functionally identical to Search but named "Recall" to match the MCP
// tool set naming.
func (m *Memory) Recall(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	return m.Search(ctx, query, WithLimit(limit))
}

// Walk performs a BFS graph traversal starting from a given record.
//
// direction:"both" means traverse both incoming and outgoing edges.
// The server returns nodes and edges up to the specified depth.
func (m *Memory) Walk(ctx context.Context, startID int64, depth int) (*WalkResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	payload := map[string]interface{}{
		"seed_id":   startID,
		"depth":     depth,
		"direction": "both",
	}

	respBytes, err := m.conn.Post(ctx, "/api/v1/walk", payload)
	if err != nil {
		return nil, err
	}

	var result WalkResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing walk response: %w", err)
	}

	return &result, nil
}

// WalkSemantic performs a semantic graph walk that follows edges weighted
// by vector similarity rather than just structural edges.
func (m *Memory) WalkSemantic(ctx context.Context, startID int64, depth int) (*WalkResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	payload := map[string]interface{}{
		"seed_id": startID,
		"depth":   depth,
	}

	respBytes, err := m.conn.Post(ctx, "/api/v1/walk/semantic", payload)
	if err != nil {
		return nil, err
	}

	var result WalkResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing semantic walk response: %w", err)
	}

	return &result, nil
}

// ListSessions returns aggregate statistics for all sessions.
//
// The server returns {"sessions": [...]} so we unwrap the wrapper.
func (m *Memory) ListSessions(ctx context.Context) ([]SessionStats, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	respBytes, err := m.conn.Get(ctx, "/api/v1/sessions/stats", nil)
	if err != nil {
		return nil, err
	}

	// The server wraps sessions in {"sessions": [...]}.
	var wrapped sessionsWrapper
	if err := json.Unmarshal(respBytes, &wrapped); err == nil && len(wrapped.Sessions) > 0 {
		return wrapped.Sessions, nil
	}

	// Fallback: try bare array (in case server format changes).
	var stats []SessionStats
	if err := json.Unmarshal(respBytes, &stats); err != nil {
		return nil, fmt.Errorf("parsing sessions response: %w", err)
	}

	return stats, nil
}

// GetContext retrieves the topological context around a record.
//
// Returns parent, child, and sibling records — useful for understanding
// how a memory fits into the knowledge graph.
func (m *Memory) GetContext(ctx context.Context, recordID int64) (*ContextResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/records/%d/topology", recordID)
	respBytes, err := m.conn.Get(ctx, path, nil)
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
// be much larger. This endpoint returns the complete gzip-decompressed text.
func (m *Memory) ReadContent(ctx context.Context, recordID int64) (string, error) {
	if m.conn == nil {
		return "", ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/records/%d/content", recordID)
	respBytes, err := m.conn.Get(ctx, path, nil)
	if err != nil {
		return "", err
	}

	// Try JSON wrapper first, fall back to raw text.
	var resp contentResponse
	if err := json.Unmarshal(respBytes, &resp); err == nil && resp.Content != "" {
		return resp.Content, nil
	}

	return string(respBytes), nil
}

// RecentMemories retrieves the most recent records from the manifest.
//
// GET /api/v1/manifest returns records ordered by creation time
// (newest first).
func (m *Memory) RecentMemories(ctx context.Context, limit int) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))

	respBytes, err := m.conn.Get(ctx, "/api/v1/manifest", params)
	if err != nil {
		return nil, err
	}

	// The manifest endpoint may return {"records": [...]} or a bare array.
	var wrapped manifestResponse
	if err := json.Unmarshal(respBytes, &wrapped); err == nil && len(wrapped.Records) > 0 {
		records := make([]models.Record, 0, len(wrapped.Records))
		for _, mr := range wrapped.Records {
			records = append(records, models.Record{
				ID:       int(mr.ID),
				UUID:     mr.UUID,
				Type:     models.MemoryType(mr.Type),
				Summary:  mr.Summary,
				Metadata: mr.Metadata,
				Status:   models.MemoryStatus(mr.Status),
			})
		}
		return records, nil
	}

	// Try bare array of records.
	var records []models.Record
	if err := json.Unmarshal(respBytes, &records); err != nil {
		return nil, fmt.Errorf("parsing manifest response: %w", err)
	}

	return records, nil
}

// Update partially updates a record by ID.
//
// The updates map can contain any subset of record fields
// (e.g. {"summary": "new summary", "score": 8}).
func (m *Memory) Update(ctx context.Context, recordID int64, updates map[string]interface{}) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/records/%d", recordID)
	_, err := m.conn.Patch(ctx, path, updates)
	return err
}

// Delete removes a record by ID (hard delete).
//
// For soft delete, use Update with {"status": "archived"} instead.
func (m *Memory) Delete(ctx context.Context, recordID int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/records/%d", recordID)
	return m.conn.Delete(ctx, path)
}

// --------------------------------------------------------------------------
// Batch Operations
// --------------------------------------------------------------------------

// BatchReadContent fetches full content for multiple records in a single
// call (max 100). Eliminates the N+1 pattern of calling ReadContent
// in a loop.
func (m *Memory) BatchReadContent(ctx context.Context, ids []int64) (map[string]json.RawMessage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	payload := map[string]interface{}{"ids": ids}
	respBytes, err := m.conn.Post(ctx, "/api/v1/records/batch-content", payload)
	if err != nil {
		return nil, err
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing batch content response: %w", err)
	}

	return result, nil
}

// BatchUpdateStatus updates the status for multiple records at once.
//
// Useful for bulk operations like marking records as consolidated,
// archived, or hubbed.
func (m *Memory) BatchUpdateStatus(ctx context.Context, ids []int64, status string) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	payload := map[string]interface{}{"ids": ids, "status": status}
	_, err := m.conn.Patch(ctx, "/api/v1/records/mark-consolidated", payload)
	return err
}

// --------------------------------------------------------------------------
// Temporal Versioning
// --------------------------------------------------------------------------

// Supersede marks an old record as superseded by a new one.
//
// The old record remains in the graph but is annotated with superseded_by
// pointing to the new record. Search results prefer the newer version.
func (m *Memory) Supersede(ctx context.Context, oldID, newID int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	payload := map[string]interface{}{"old_id": oldID, "new_id": newID}
	_, err := m.conn.Post(ctx, "/api/v1/records/supersede", payload)
	return err
}

// --------------------------------------------------------------------------
// File Upload
// --------------------------------------------------------------------------

// UploadFile uploads a document for async ingestion.
//
// Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown, HTML, DOCX.
// The server processes the file asynchronously — use UploadStatus to poll.
func (m *Memory) UploadFile(ctx context.Context, filename, content string, sessionID string) (*UploadResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	payload := map[string]interface{}{
		"filename": filename,
		"content":  content,
	}
	if sessionID != "" {
		payload["session_id"] = sessionID
	}

	respBytes, err := m.conn.Post(ctx, "/api/v1/upload", payload)
	if err != nil {
		return nil, err
	}

	var result UploadResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing upload response: %w", err)
	}

	return &result, nil
}

// UploadStatus checks the processing status of a file upload.
func (m *Memory) UploadStatus(ctx context.Context, uploadID int64) (*UploadStatusResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/upload/%d/status", uploadID)
	respBytes, err := m.conn.Get(ctx, path, nil)
	if err != nil {
		return nil, err
	}

	var result UploadStatusResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing upload status response: %w", err)
	}

	return &result, nil
}

// --------------------------------------------------------------------------
// Entity Knowledge Graph (Layer 2)
// --------------------------------------------------------------------------

// SearchEntities searches named entities (people, organisations, concepts).
func (m *Memory) SearchEntities(ctx context.Context, query, entityType string, limit int) ([]Entity, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	if query != "" {
		params.Set("query", query)
	}
	if entityType != "" {
		params.Set("type", entityType)
	}
	params.Set("limit", strconv.Itoa(limit))

	respBytes, err := m.conn.Get(ctx, "/api/v1/entities", params)
	if err != nil {
		return nil, err
	}

	var resp entitiesResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		// Try bare array fallback.
		var entities []Entity
		if err2 := json.Unmarshal(respBytes, &entities); err2 != nil {
			return nil, fmt.Errorf("parsing entities response: %w", err)
		}
		return entities, nil
	}

	return resp.Entities, nil
}

// UpsertEntity creates or updates a named entity (idempotent by name).
func (m *Memory) UpsertEntity(ctx context.Context, name, entityType, summary string, attributes map[string]interface{}) (*EntityResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	payload := map[string]interface{}{"name": name}
	if entityType != "" {
		payload["entity_type"] = entityType
	}
	if summary != "" {
		payload["summary"] = summary
	}
	if attributes != nil {
		payload["attributes"] = attributes
	}

	respBytes, err := m.conn.Post(ctx, "/api/v1/entities", payload)
	if err != nil {
		return nil, err
	}

	var result EntityResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing upsert entity response: %w", err)
	}

	return &result, nil
}

// EntityGraph performs BFS traversal of entity relationships.
//
// Starting from an entity, discovers connected entities through typed
// edges (works_at, knows, part_of, etc.).
func (m *Memory) EntityGraph(ctx context.Context, entityID int64, depth int) (*EntityGraphResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	params.Set("depth", strconv.Itoa(depth))

	path := fmt.Sprintf("/api/v1/entities/%d/graph", entityID)
	respBytes, err := m.conn.Get(ctx, path, params)
	if err != nil {
		return nil, err
	}

	var result EntityGraphResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing entity graph response: %w", err)
	}

	return &result, nil
}

// EntityTimeline returns the full temporal history of an entity's
// relationships, including invalidated edges ordered by event time.
func (m *Memory) EntityTimeline(ctx context.Context, entityID int64) (*EntityTimelineResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/entities/%d/timeline", entityID)
	respBytes, err := m.conn.Get(ctx, path, nil)
	if err != nil {
		return nil, err
	}

	var result EntityTimelineResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing entity timeline response: %w", err)
	}

	return &result, nil
}

// UpsertEntityEdge creates or updates a typed relationship between
// two entities with optional temporal metadata.
func (m *Memory) UpsertEntityEdge(ctx context.Context, sourceID, targetID int64, relation string, opts ...EntityEdgeOption) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	payload := map[string]interface{}{
		"source_id": sourceID,
		"target_id": targetID,
		"relation":  relation,
	}

	cfg := &entityEdgeConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.eventTime != "" {
		payload["event_time"] = cfg.eventTime
	}
	if cfg.confidence > 0 {
		payload["confidence"] = cfg.confidence
	}
	if cfg.sourceRecordID > 0 {
		payload["source_record_id"] = cfg.sourceRecordID
	}

	_, err := m.conn.Post(ctx, "/api/v1/entities/edges", payload)
	return err
}

// LinkRecordEntity links a memory record to an entity (cross-layer).
func (m *Memory) LinkRecordEntity(ctx context.Context, recordID, entityID int64, role string) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	payload := map[string]interface{}{
		"record_id": recordID,
		"entity_id": entityID,
	}
	if role != "" {
		payload["role"] = role
	}

	_, err := m.conn.Post(ctx, "/api/v1/entities/link", payload)
	return err
}

// GetRecordEntities returns entities linked to a specific memory record.
func (m *Memory) GetRecordEntities(ctx context.Context, recordID int64) ([]Entity, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/records/%d/entities", recordID)
	respBytes, err := m.conn.Get(ctx, path, nil)
	if err != nil {
		return nil, err
	}

	var resp entitiesResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		var entities []Entity
		if err2 := json.Unmarshal(respBytes, &entities); err2 != nil {
			return nil, fmt.Errorf("parsing record entities response: %w", err)
		}
		return entities, nil
	}

	return resp.Entities, nil
}

// --------------------------------------------------------------------------
// Session history & clusters
// --------------------------------------------------------------------------

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

// GetSessionClusters returns thematic clusters within a session.
//
// Uses BSQ vectors and DBSCAN to identify topic groups among the
// session's records.
func (m *Memory) GetSessionClusters(ctx context.Context, sessionUUID string) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/sessions/%s/clusters", sessionUUID)
	return m.conn.Get(ctx, path, nil)
}

// --------------------------------------------------------------------------
// Session management
// --------------------------------------------------------------------------

// NewSession rotates the session UUID (generates a fresh random suffix).
// All subsequent Add() calls will be grouped under the new session.
func (m *Memory) NewSession() {
	m.sessionUUID = m.containerTag + "-" + randomHex(12)
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
