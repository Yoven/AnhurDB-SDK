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

This package uses only the Go standard library (net/http, encoding/json, etc.).
*/
package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/models"
)

// DefaultCloudURL is the production AnhurDB cloud endpoint.
// Self-hosted users pass WithURL("http://localhost:8000").
const DefaultCloudURL = "https://anhurdb.yoven.ai"

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
	// sessionRegistered is true only after CreateSession/OpenSession succeeds.
	// A local sessionUUID alone must not be sent on writes.
	sessionRegistered bool
	ingestAvailable   *bool // nil = untested, true = yes, false = no
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

	// Session UUID: containerTag + UTC timestamp + random 6 hex chars.
	sessionUUID := deriveSessionUUID(containerTag)

	return &Memory{
		conn:              conn,
		containerTag:      containerTag,
		sessionUUID:       sessionUUID,
		sessionRegistered: false,
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

// utcTimestamp returns the current UTC time as "YYYYMMDD-HHMMSS".
//
func utcTimestamp() string {
	return time.Now().UTC().Format("20060102-150405")
}

// deriveSessionUUID builds the default auto-derived session UUID:
//
//	<container_tag>-<YYYYMMDD-HHMMSS UTC>-<6 hex random>
//
// e.g. "mem-3f9a1b2c4d5e-20260703-143025-a1b2c3".
//
func deriveSessionUUID(containerTag string) string {
	return containerTag + "-" + utcTimestamp() + "-" + randomHex(6)
}

// --------------------------------------------------------------------------
// Core methods — match Python/TS exactly
// --------------------------------------------------------------------------

// buildMetadataJSON wraps containerTag into the canonical metadata JSON
// envelope `{"container_tag":"<tag>"}`. Used by every record-create code path
// so the `metadata` column always holds a valid JSON object — not the raw
// container_tag string.
//
func buildMetadataJSON(containerTag string) string {
	if containerTag == "" {
		return "{}"
	}
	encoded, marshalErr := json.Marshal(map[string]string{"container_tag": containerTag})
	if marshalErr != nil {
		// json.Marshal of map[string]string is guaranteed to succeed for any
		// valid Go string; treat this as a hard programming error.
		return "{}"
	}
	return string(encoded)
}

// buildMetadataJSONWith merges caller-supplied metadata keys on top of the
// canonical {"container_tag": tag} envelope and returns the JSON string for the
// `metadata` column.
//
func buildMetadataJSONWith(containerTag string, extra map[string]interface{}) string {
	if len(extra) == 0 {
		return buildMetadataJSON(containerTag)
	}
	merged := make(map[string]interface{}, len(extra)+1)
	for key, value := range extra {
		merged[key] = value
	}
	if containerTag != "" {
		merged["container_tag"] = containerTag
	}
	encoded, marshalErr := json.Marshal(merged)
	if marshalErr != nil {
		// Caller metadata may contain an unmarshalable value (e.g. a channel);
		// fall back to the safe canonical envelope rather than writing garbage.
		return buildMetadataJSON(containerTag)
	}
	return string(encoded)
}

// Add stores raw text via the ingest write path (default).
//
// Session-first servers require CreateSession before Add succeeds.
//
// Agent UX — pick the write path once:
//   - Raw chat/notes → Add(ctx, text) or WithMode("ingest") → POST /ingest
//     (1 episodic + async satellites; extraction LLM billed). MCP: ingest_memory.
//   - Direct episodic record → Add(ctx, text, WithMode("regular")) or
//     Create(...) → POST /records (no extraction). MCP: create_memory.
//
// Trap: WithScore / WithType / WithMetadata also forces /records on ingest mode
// (create path — no extraction). Plain Add(ctx, text) prefers ingest; 404
// falls back to /records (OSS).
//
//	mem.Add(ctx, "plain text") // ingest
//	mem.Add(ctx, "fact", client.WithMode("regular")) // records path
//	mem.Add(ctx, "fact", client.WithScore(9), client.WithType("fact")) // records path
//
func (m *Memory) Add(ctx context.Context, text string, opts ...AddOption) (*AddResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if text == "" {
		return nil, ErrEmptyInput
	}

	cfg := &addConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	writeMode := "ingest"
	if cfg.writeMode != "" {
		writeMode = cfg.writeMode
	}

	// When the caller pins score, type, OR metadata, the cloud ingest endpoint
	// would silently drop them — route to the synchronous records path that
	// persists all three.
	//
	forceRecordsPath := writeMode == "regular" ||
		cfg.score != nil || cfg.memType != nil || len(cfg.metadata) > 0

	if forceRecordsPath {
		return m.createRecord(ctx, text, cfg)
	}

	// Try cloud ingest first (has auto-embedding). Once we know ingest is
	// unavailable (404), we skip it on subsequent calls to avoid unnecessary
	// round-trips.
	//
	if m.ingestAvailable == nil || *m.ingestAvailable {
		result, ingestErr := m.tryIngest(ctx, text, cfg)
		if result != nil {
			return result, nil
		}
		// Only propagate non-404 errors.
		if ingestErr != nil && !errors.Is(ingestErr, ErrNotFound) {
			return nil, ingestErr
		}
	}

	// Synchronous record creation: OSS/self-hosted fallback when ingest 404s.
	return m.createRecord(ctx, text, cfg)
}

// resolveWriteSessionID returns the session uuid for a write.
// Explicit WithSessionID wins (server validates registration). Otherwise the
// client session must already be registered via CreateSession/OpenSession.
//
func (m *Memory) resolveWriteSessionID(cfg *addConfig) (string, error) {
	if cfg != nil {
		explicitSessionID := strings.TrimSpace(cfg.sessionID)
		if explicitSessionID != "" {
			return explicitSessionID, nil
		}
	}
	if !m.sessionRegistered || m.sessionUUID == "" {
		return "", fmt.Errorf(
			"session_id is required — create a session first (POST /api/v1/sessions)",
		)
	}
	return m.sessionUUID, nil
}

// tryIngest attempts the cloud ingest endpoint.
// Returns (nil, ErrNotFound) if the endpoint doesn't exist.
func (m *Memory) tryIngest(ctx context.Context, text string, cfg *addConfig) (*AddResult, error) {
	resolvedSessionID, resolveErr := m.resolveWriteSessionID(cfg)
	if resolveErr != nil {
		return nil, resolveErr
	}
	payload := map[string]interface{}{
		"content":       text,
		"container_tag": m.containerTag,
		"session_id":    resolvedSessionID,
	}

	respBytes, postErr := m.conn.Post(ctx, "/api/v1/ingest", payload)
	if postErr != nil {
		if errors.Is(postErr, ErrNotFound) {
			ingestUnavailable := false
			m.ingestAvailable = &ingestUnavailable
			return nil, ErrNotFound
		}
		return nil, postErr
	}

	ingestAvailable := true
	m.ingestAvailable = &ingestAvailable

	var resp ingestResponse
	if unmarshalErr := json.Unmarshal(respBytes, &resp); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing ingest response: %w", unmarshalErr)
	}

	records := resp.Records
	firstID := resp.ID
	if len(records) > 0 {
		firstID = records[0].ID
	}

	return &AddResult{
		ID:        firstID,
		SessionID: resolvedSessionID,
		Records:   records,
		Status:    "ok",
		Mode:      "cloud",
	}, nil
}

// CreateInSession stores `text` directly via POST /api/v1/records as an
// episodic record under the supplied sessionUUID. The session must exist
// (POST /api/v1/sessions first on session-first servers).
func (m *Memory) CreateInSession(ctx context.Context, text string, sessionUUID string) (*AddResult, error) {
	return m.Create(ctx, sessionUUID, text)
}

// AppendMainIDs appends parent record IDs to the main_ids array of a single
// record via PATCH /api/v1/records/append-main-ids. Server-side the operation
// reads, deduplicates, and writes back — safe to call repeatedly with the
// same payload (idempotent on the union of existing + supplied IDs).
//
func (m *Memory) AppendMainIDs(ctx context.Context, recordID int64, mainIDs []int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return fmt.Errorf("AppendMainIDs: recordID must be > 0")
	}
	if len(mainIDs) == 0 {
		return nil // nothing to append, server would no-op too
	}
	payload := map[string]interface{}{
		"ids":                []int64{recordID},
		"main_ids_to_append": mainIDs,
	}
	_, patchErr := m.conn.Patch(ctx, "/api/v1/records/append-main-ids", payload)
	return patchErr
}

// AppendMainLinks appends parent record IDs to a batch of records via the same
// PATCH /api/v1/records/append-main-ids route. Prefer AppendMainIDs for a
// single child record.
//
// Junior Tip [batch form]: the wire payload already accepts multiple `ids`;
// this helper exposes that shape so Go matches Python append_main_links /
// TypeScript appendMainLinks without callers hand-rolling the map.
func (m *Memory) AppendMainLinks(ctx context.Context, ids []int64, mainIDsToAppend []int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if len(ids) == 0 || len(mainIDsToAppend) == 0 {
		return nil
	}
	payload := map[string]interface{}{
		"ids":                ids,
		"main_ids_to_append": mainIDsToAppend,
	}
	_, patchErr := m.conn.Patch(ctx, "/api/v1/records/append-main-ids", payload)
	return patchErr
}

// AppendRelatedIDs appends peer record IDs to the related_ids array of a single
// record via PATCH /api/v1/records/append-related-ids. Server-side the operation
// reads, deduplicates, and writes back — safe to call repeatedly with the same
// payload (idempotent on the union of existing + supplied IDs).
//
func (m *Memory) AppendRelatedIDs(ctx context.Context, recordID int64, relatedIDs []int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return fmt.Errorf("AppendRelatedIDs: recordID must be > 0")
	}
	if len(relatedIDs) == 0 {
		return nil // nothing to append, server would no-op too
	}
	payload := map[string]interface{}{
		"ids":                   []int64{recordID},
		"related_ids_to_append": relatedIDs,
	}
	_, patchErr := m.conn.Patch(ctx, "/api/v1/records/append-related-ids", payload)
	return patchErr
}

// LinkConsolidated sets the consolidate_id column on a batch of child
// records via PATCH /api/v1/records/consolidate-ids so subsequent queries
// can navigate child → parent in one column read.
//
func (m *Memory) LinkConsolidated(ctx context.Context, ids []int64, consolidateID int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if len(ids) == 0 {
		return nil
	}
	if consolidateID <= 0 {
		return fmt.Errorf("LinkConsolidated: consolidateID must be > 0")
	}
	payload := map[string]interface{}{
		"ids":            ids,
		"consolidate_id": consolidateID,
	}
	_, patchErr := m.conn.Patch(ctx, "/api/v1/records/consolidate-ids", payload)
	return patchErr
}

// UpdateConsolidateIDs is the pre-2026-06-18 name for LinkConsolidated.
//
// Deprecated: use LinkConsolidated. This alias forwards verbatim and is kept
// compiling. New code MUST call LinkConsolidated.
func (m *Memory) UpdateConsolidateIDs(ctx context.Context, ids []int64, consolidateID int64) error {
	return m.LinkConsolidated(ctx, ids, consolidateID)
}

// createRecord stores text directly via POST /api/v1/records.
//
// Without server-side embedding, we store the text in both summary
// empty — the server handles records without vectors via text search.
//
// Caller-supplied score/type/metadata (via AddOption) override the historical
// defaults of score=5, type="episodic", and the bare container_tag envelope.
func (m *Memory) createRecord(ctx context.Context, text string, cfg *addConfig) (*AddResult, error) {
	summary := truncateSummary(text)
	resolvedSessionID, resolveErr := m.resolveWriteSessionID(cfg)
	if resolveErr != nil {
		return nil, resolveErr
	}

	// Apply defaults, then let caller overrides win. score 0 and type "" are
	// legal explicit values, so we only override when the option was actually
	// supplied (cfg field non-nil) — see addConfig's pointer rationale.
	score := 5
	recordType := "episodic"
	var extraMetadata map[string]interface{}
	if cfg != nil {
		if cfg.score != nil {
			score = *cfg.score
		}
		if cfg.memType != nil {
			recordType = *cfg.memType
		}
		extraMetadata = cfg.metadata
	}

	payload := map[string]interface{}{
		"uuid":           resolvedSessionID,
		"type":           recordType,
		"dimension":      0,
		"prefix":         "",
		"weight":         float64(score) / 10,
		"score":          score,
		"vector":         "",
		"related_ids":    []int{},
		"main_ids":       []int{},
		"consolidate_id": 0,
		"metadata":       buildMetadataJSONWith(m.containerTag, extraMetadata),
		"summary":        summary,
		"content":        text,
		"consolidated":   false,
		"status":         "saved",
	}

	respBytes, postErr := m.conn.Post(ctx, "/api/v1/records", payload)
	if postErr != nil {
		return nil, postErr
	}

	var resp recordCreateResponse
	if unmarshalErr := json.Unmarshal(respBytes, &resp); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing record response: %w", unmarshalErr)
	}

	return &AddResult{
		ID:        resp.ID,
		SessionID: resolvedSessionID,
		Records:   []RecordSummary{{ID: resp.ID, Type: recordType, Summary: summary}},
		Status:    "ok",
		Mode:      "oss",
	}, nil
}

// Search finds relevant memories using hybrid plane search.
//
// Uses POST /api/v1/search with default scope "sessions" (all chat sessions
// for the tenant, excluding shared-library uuids).
//
// Agent UX — text is not semantic: query is sent as body "text" (FTS5
// exact-word matching), not an embedding. For conceptual RAG without a
// vector, prefer SmartSearch (or MCP recall).
func (m *Memory) Search(ctx context.Context, query string, opts ...SearchOption) ([]SearchResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if query == "" {
		return nil, ErrEmptyInput
	}

	cfg := &searchConfig{limit: 10, scope: "sessions"}
	for _, opt := range opts {
		opt(cfg)
	}

	payload := map[string]interface{}{
		"text":  query,
		"limit": cfg.limit,
		"scope": cfg.scope,
	}
	if cfg.typeFilter != "" {
		payload["type_filter"] = cfg.typeFilter
	}

	respBytes, err := m.conn.PostRead(ctx, "/api/v1/search", payload)
	if err != nil {
		return nil, err
	}

	var resp searchResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parsing search response: %w", err)
	}

	// The wire envelope already IS the public SearchResult shape ({record,
	// similarity}), so return the decoded slice directly — no flatten step, and
	// the FULL nested models.Record survives (the old flatten kept only
	// id/type/summary/metadata/content). Preserve the historical non-nil
	// empty-slice contract when the server omits "results".
	if resp.Results == nil {
		return []SearchResult{}, nil
	}
	return resp.Results, nil
}

// SearchSessions searches chat sessions only (scope=sessions).
func (m *Memory) SearchSessions(ctx context.Context, query string, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, append([]SearchOption{WithScope("sessions")}, opts...)...)
}

// SearchTenantShared searches tenant-shared library docs (scope=tenant_shared).
func (m *Memory) SearchTenantShared(ctx context.Context, query string, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, append([]SearchOption{WithScope("tenant_shared")}, opts...)...)
}

// SearchClientShared searches the client-wide shared library (scope=client_shared).
func (m *Memory) SearchClientShared(ctx context.Context, query string, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, append([]SearchOption{WithScope("client_shared")}, opts...)...)
}

// SearchShared searches both shared planes (scope=shared_all).
func (m *Memory) SearchShared(ctx context.Context, query string, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, append([]SearchOption{WithScope("shared_all")}, opts...)...)
}

// Profile retrieves the memory profile for this container tag.
//
// If the server doesn't have a profile endpoint yet (OSS without agents),
// it returns an empty profile rather than failing — matching the Python
// SDK behaviour.
func (m *Memory) Profile(ctx context.Context, opts ...ReadOption) (*ProfileResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

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

// SearchByType retrieves records filtered by memory type in the tenant store.
//
// Hits GET /api/v1/search/type — a type-index lookup, faster than plane search
// when you know the exact type.
//
// Agent UX — not a plane switch: no scope parameter. Does not search Shared
// Data. For specialty docs use SearchTenantShared / SearchClientShared /
// SearchShared (or Search with WithScope).
func (m *Memory) SearchByType(ctx context.Context, memType string, limit int, opts ...ReadOption) ([]SearchResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if memType == "" {
		return nil, ErrEmptyInput
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	params.Set("type", memType)
	params.Set("limit", strconv.Itoa(limit))
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
func (m *Memory) SmartSearch(ctx context.Context, query string, limit int, opts ...ReadOption) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if query == "" {
		return nil, ErrEmptyInput
	}

	cfg := applyReadOptions(opts)
	scope := cfg.scope
	if scope == "" {
		scope = "sessions"
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("limit", strconv.Itoa(limit))
	params.Set("scope", scope)
	if cfg.typeFilter != "" {
		params.Set("type", cfg.typeFilter)
	}

	return m.conn.Get(ctx, "/api/v1/search/smart", params)
}

// Recall searches for memories using plane-aware search (default sessions).
// Functionally identical to Search but named "Recall" to match the MCP
// tool set naming. Extra search options (including WithScope) are forwarded.
func (m *Memory) Recall(ctx context.Context, query string, limit int, opts ...SearchOption) ([]SearchResult, error) {
	return m.Search(ctx, query, append([]SearchOption{WithLimit(limit)}, opts...)...)
}

// Walk performs a BFS graph traversal starting from a given record.
//
// direction:"both" means traverse both incoming and outgoing edges.
// The server returns nodes and edges up to the specified depth.
func (m *Memory) Walk(ctx context.Context, startID int64, depth int, opts ...ReadOption) (*WalkResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	payload := map[string]interface{}{
		"seed_id":   startID,
		"depth":     depth,
		"direction": "both",
	}

	respBytes, err := m.conn.PostRead(ctx, "/api/v1/walk", payload)
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
//
// With no goal options it is a plain cost-first Dijkstra over 1−similarity edge
// cost — byte-for-byte the previous behaviour. Passing WithTarget (plus its
// companion WithGoalVector / WithTargetTag) turns it into a goal-directed walk
// whose nodes come back ordered by proximity to the target. WithMaxCost tunes
// the cost budget.
//
func (m *Memory) WalkSemantic(ctx context.Context, startID int64, depth int, opts ...ReadOption) (*WalkResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	payload := map[string]interface{}{
		"seed_id": startID,
		"depth":   depth,
	}
	// Only attach a goal key when the caller actually set it, so a bare call
	// stays identical to the historical Dijkstra request. The server supplies
	// max_cost=2.0 / max_nodes=50 defaults when the keys are absent.
	if cfg.walkMaxCost > 0 {
		payload["max_cost"] = cfg.walkMaxCost
	}
	if cfg.walkTarget != "" {
		payload["target"] = cfg.walkTarget
	}
	if len(cfg.walkGoalVector) > 0 {
		// The SDK owns the base64 step so callers pass raw embedding bytes.
		payload["vector"] = base64.StdEncoding.EncodeToString(cfg.walkGoalVector)
	}
	if cfg.walkTargetTag != "" {
		payload["target_tag"] = cfg.walkTargetTag
	}

	respBytes, err := m.conn.PostRead(ctx, "/api/v1/walk/semantic", payload)
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
func (m *Memory) ListSessions(ctx context.Context, opts ...ReadOption) ([]SessionStats, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	respBytes, err := m.conn.Get(ctx, "/api/v1/sessions/stats", nil)
	if err != nil {
		return nil, err
	}

	// The endpoint returns either an envelope {"sessions": [...], "count": N, ...}
	// or (legacy) a bare array [...].
	//
	switch firstJSONToken(respBytes) {
	case '[':
		var stats []SessionStats
		if parseErr := json.Unmarshal(respBytes, &stats); parseErr != nil {
			return nil, fmt.Errorf("parsing sessions response (array): %w", parseErr)
		}
		return stats, nil
	case '{':
		var wrapped sessionsWrapper
		if parseErr := json.Unmarshal(respBytes, &wrapped); parseErr != nil {
			return nil, fmt.Errorf("parsing sessions response (object): %w", parseErr)
		}
		return wrapped.Sessions, nil
	default:
		// Empty body or "null": no sessions, not an error.
		return []SessionStats{}, nil
	}
}

// GetContext retrieves the topological context around a record.
//
// Returns parent, child, and sibling records — useful for understanding
// how a memory fits into the knowledge graph.
func (m *Memory) GetContext(ctx context.Context, recordID int64, opts ...ReadOption) (*ContextResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

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
// be much larger. This endpoint returns the complete decrypted file
// bytes for file records, or the inline content for episodic records.
//
func (m *Memory) ReadContent(ctx context.Context, recordID int64, opts ...ReadOption) (string, error) {
	if m.conn == nil {
		return "", ErrEmptyAPIKey
	}

	_ = opts

	path := fmt.Sprintf("/api/v1/records/%d/content", recordID)
	respBytes, err := m.conn.Get(ctx, path, nil)
	if err != nil {
		return "", err
	}
	return string(respBytes), nil
}

// Recent retrieves the most recent records.
//
// GET /api/v1/recent?limit=N hits the server's dedicated ListRecent endpoint,
// which returns records ordered by creation time (newest first).
//
func (m *Memory) Recent(ctx context.Context, limit int, opts ...ReadOption) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))

	respBytes, err := m.conn.Get(ctx, "/api/v1/recent", params)
	if err != nil {
		return nil, err
	}

	// The manifest endpoint may return either of two shapes:
	//   1. An envelope object: {"records": [...], "count": N, ...}
	//   2. A bare JSON array:  [...]
	//
	firstToken := firstJSONToken(respBytes)
	switch firstToken {
	case '[':
		// Bare array of records.
		var records []models.Record
		if parseErr := json.Unmarshal(respBytes, &records); parseErr != nil {
			return nil, fmt.Errorf("parsing manifest response (array): %w", parseErr)
		}
		return records, nil
	case '{':
		// Envelope object — valid whether or not records is empty.
		var wrapped manifestResponse
		if parseErr := json.Unmarshal(respBytes, &wrapped); parseErr != nil {
			return nil, fmt.Errorf("parsing manifest response (object): %w", parseErr)
		}
		if wrapped.Records == nil {
			return []models.Record{}, nil
		}
		return wrapped.Records, nil
	default:
		// Empty body or unexpected shape (e.g. "null"): treat as no records
		// rather than erroring, matching the "manifest can be empty" contract.
		return []models.Record{}, nil
	}
}

// RecentMemories is the pre-2026-06-18 name for Recent.
//
// Deprecated: use Recent. This alias forwards verbatim and is kept only so the
// keep compiling. New code MUST call Recent.
func (m *Memory) RecentMemories(ctx context.Context, limit int, opts ...ReadOption) ([]models.Record, error) {
	return m.Recent(ctx, limit, opts...)
}

// firstJSONToken returns the first non-whitespace byte of a JSON payload, or 0
// if the payload is empty/whitespace-only. Used to distinguish an object
// envelope ('{') from a bare array ('[') without a speculative Unmarshal.
func firstJSONToken(raw []byte) byte {
	for _, currentByte := range raw {
		switch currentByte {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return currentByte
		}
	}
	return 0
}

// truncateSummary returns text capped at 200 Unicode code points (runes) with an
// ellipsis when truncated.
//
func truncateSummary(text string) string {
	const maxSummaryRunes = 200
	runes := []rune(text)
	if len(runes) <= maxSummaryRunes {
		return text
	}
	return string(runes[:maxSummaryRunes]) + "..."
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

// Get fetches record metadata by ID via GET /api/v1/records/{id}.
//
// Junior Tip [vs ReadContent]: Get returns JSON metadata; ReadContent returns
// the full text/plain body. Both are part of the public Memory surface.
func (m *Memory) Get(ctx context.Context, recordID int64) (map[string]interface{}, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return nil, fmt.Errorf("Get: recordID must be > 0")
	}
	path := fmt.Sprintf("/api/v1/records/%d", recordID)
	respBytes, getErr := m.conn.Get(ctx, path, nil)
	if getErr != nil {
		return nil, getErr
	}
	var result map[string]interface{}
	if unmarshalErr := json.Unmarshal(respBytes, &result); unmarshalErr != nil {
		return nil, fmt.Errorf("Get: decode response: %w", unmarshalErr)
	}
	return result, nil
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

// Forget is a placeholder for a future decay API. It always returns an error.
//
// Junior Tip [parity stub]: Python forget() and TypeScript forget() raise the
// same "not yet available" contract — use Delete or Update(..., archived).
func (m *Memory) Forget(ctx context.Context, memoryID int64) error {
	_ = ctx
	_ = memoryID
	return fmt.Errorf(
		"Forget is not yet available. Use Delete for hard removal or Update with status=archived for soft delete",
	)
}

// --------------------------------------------------------------------------
// Batch Operations
// --------------------------------------------------------------------------

// BatchReadContent fetches full content for multiple records in a single
// call (max 100). Eliminates the N+1 pattern of calling ReadContent
// in a loop.
func (m *Memory) BatchReadContent(ctx context.Context, ids []int64, opts ...ReadOption) (map[string]json.RawMessage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	payload := map[string]interface{}{"ids": ids}
	// Read behind POST — PostRead for search-shaped endpoints.
	respBytes, err := m.conn.PostRead(ctx, "/api/v1/records/batch-content", payload)
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

// UploadFile uploads a document for async ingestion (multipart/form-data).
//
// Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown, HTML, DOCX.
// The server processes the file asynchronously — use UploadStatus to poll.
//
func (m *Memory) UploadFile(ctx context.Context, filename string, content []byte, sessionID string) (*UploadResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	extraFields := map[string]string{}
	if sessionID != "" {
		extraFields["session_id"] = sessionID
		extraFields["mode"] = "chat"
	}

	respBytes, postErr := m.conn.PostMultipart(ctx, "/api/v1/upload", "file", filename, content, extraFields)
	if postErr != nil {
		return nil, postErr
	}

	var result UploadResult
	if parseErr := json.Unmarshal(respBytes, &result); parseErr != nil {
		return nil, fmt.Errorf("parsing upload response: %w", parseErr)
	}

	return &result, nil
}

// UploadStatus checks the processing status of a file upload.
func (m *Memory) UploadStatus(ctx context.Context, uploadID int64, opts ...ReadOption) (*UploadStatusResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

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

// ListEntities returns a paginated snapshot of all entities for the tenant,
// ordered by id ASC (stable cursor — pages never shift under concurrent inserts).
//
// Use this instead of SearchEntities with a placeholder query whenever you
// need to walk EVERY entity (cluster discovery, exports, dashboards). The
// returned EntitiesPage carries HasMore + NextOffset so the caller can keep
// paging until exhausted:
//
//	offset := 0
//	for {
//	    page, err := mem.ListEntities(ctx, 200, offset)
//	    if err != nil { return err }
//	    process(page.Entities)
//	    if !page.HasMore { break }
//	    offset = page.NextOffset
//	}
//
// limit is server-clamped to [1, 500]; offset is clamped to >= 0.
//
func (m *Memory) ListEntities(ctx context.Context, limit, offset int, opts ...ReadOption) (*EntitiesPage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	params.Set("offset", strconv.Itoa(offset))

	respBytes, getErr := m.conn.Get(ctx, "/api/v1/entities/list", params)
	if getErr != nil {
		return nil, getErr
	}

	var page EntitiesPage
	if decodeErr := json.Unmarshal(respBytes, &page); decodeErr != nil {
		return nil, fmt.Errorf("parsing entities list response: %w", decodeErr)
	}
	return &page, nil
}

// SearchEntities searches named entities (people, organisations, concepts).
func (m *Memory) SearchEntities(ctx context.Context, query, entityType string, limit int, opts ...ReadOption) ([]Entity, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	params := url.Values{}
	if query != "" {
		params.Set("q", query)
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
func (m *Memory) EntityGraph(ctx context.Context, entityID int64, depth int, opts ...ReadOption) (*EntityGraphResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

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
func (m *Memory) EntityTimeline(ctx context.Context, entityID int64, opts ...ReadOption) (*EntityTimelineResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

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
func (m *Memory) GetRecordEntities(ctx context.Context, recordID int64, opts ...ReadOption) ([]Entity, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

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
func (m *Memory) GetSessionHistory(ctx context.Context, sessionUUID string, limit, offset int, opts ...ReadOption) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	params.Set("offset", strconv.Itoa(offset))

	path := fmt.Sprintf("/api/v1/sessions/%s/history", sessionUUID)
	return m.conn.Get(ctx, path, params)
}

// GetSessionClusters returns thematic clusters within a session.
//
// session's records.
func (m *Memory) GetSessionClusters(ctx context.Context, sessionUUID string, opts ...ReadOption) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	path := fmt.Sprintf("/api/v1/sessions/%s/clusters", sessionUUID)
	return m.conn.Get(ctx, path, nil)
}

// --------------------------------------------------------------------------
// Session management
// --------------------------------------------------------------------------

// CreateSession registers a write session via POST /api/v1/sessions.
//
// Session-first servers reject ingest and record writes until this succeeds.
// Call once per conversation before Add.
//
// Junior Tip [parity with Python/TS/MCP]: omit WithCreateSessionID → server
// generates a new UUID (empty JSON body). To register a caller-chosen id:
// NewSession() then CreateSession(ctx, WithCreateSessionID(m.SessionID())).
//
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
//
func (m *Memory) OpenSession(ctx context.Context, opts ...CreateSessionOption) (string, error) {
	localSessionID := m.NewSession()
	combinedOpts := append([]CreateSessionOption{WithCreateSessionID(localSessionID)}, opts...)
	return m.CreateSession(ctx, combinedOpts...)
}

// NewSession rotates the local session UUID (generates a fresh random suffix).
// This does NOT register the session on the server — call CreateSession before
// Add on session-first deployments. Returns the new local id (parity with
// Python new_session / TypeScript newSession).
//
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
