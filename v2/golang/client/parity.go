package client

// Parity methods for Memory — mirrors Python and TypeScript SDKs.
// See PARITY_SPEC.md for the canonical contract.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/models"
)

// --------------------------------------------------------------------------
// Create — full-fidelity record creation (POST /api/v1/records)
// --------------------------------------------------------------------------

// Create writes a single record via POST /api/v1/records and returns the new
// record id. It is the canonical full-fidelity create: the caller controls type,
// full-fidelity create: the caller controls type, score, related_ids, metadata,
// status, and valid_from through functional options, while a bare
// Create(ctx, sessionUUID, content) keeps episodic/score-5/saved defaults.
//
//	mem.Create(ctx, "chat-42", "user asked about pricing")                      // defaults
//	mem.Create(ctx, "chat-42", "Paulo works at Yoven",
//	    client.WithCreateType("fact"),
//	    client.WithCreateScore(9),
//	    client.WithCreateRelatedIDs([]int64{101, 102}),
//	    client.WithCreateValidFrom("2026-01-01T00:00:00Z"))
//
func (m *Memory) Create(ctx context.Context, sessionUUID, content string, opts ...CreateOption) (*AddResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if sessionUUID == "" {
		return nil, fmt.Errorf("Create: sessionUUID is required")
	}
	if content == "" {
		return nil, ErrEmptyInput
	}

	cfg := &createConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// to the gzip file store. Mirror createRecord's codepoint-safe truncation so
	// the summary column stays index-friendly while content keeps the full text.
	summary := truncateSummary(content)

	// Apply defaults, then let caller overrides win. score 0 and "" type are
	// LEGAL explicit values, so we only override when the option was actually
	// supplied (pointer non-nil) — same three-state rationale as addConfig.
	recordType := "episodic"
	score := 5
	status := "saved"
	relatedIDs := []int64{}
	var extraMetadata map[string]interface{}
	if cfg.memType != nil {
		recordType = *cfg.memType
	}
	if cfg.score != nil {
		score = *cfg.score
	}
	if cfg.status != nil {
		status = *cfg.status
	}
	if cfg.relatedIDs != nil {
		relatedIDs = cfg.relatedIDs
	}
	if cfg.metadata != nil {
		extraMetadata = cfg.metadata
	}

	// valid_from is honoured by the server ONLY as a metadata key on this route,
	// so fold it into the metadata envelope before building the JSON string.
	if cfg.validFrom != "" {
		if extraMetadata == nil {
			extraMetadata = map[string]interface{}{}
		}
		extraMetadata["valid_from"] = cfg.validFrom
	}

	payload := map[string]interface{}{
		"uuid":           sessionUUID,
		"type":           recordType,
		"dimension":      0,
		"prefix":         "",
		"weight":         float64(score) / 10,
		"score":          score,
		"vector":         "",
		"related_ids":    relatedIDs,
		"main_ids":       []int64{},
		"consolidate_id": 0,
		"metadata":       buildMetadataJSONWith(m.containerTag, extraMetadata),
		"summary":        summary,
		"content":        content,
		"consolidated":   false,
		"status":         status,
	}

	respBytes, postErr := m.conn.Post(ctx, "/api/v1/records", payload)
	if postErr != nil {
		return nil, postErr
	}

	var resp recordCreateResponse
	if decodeErr := json.Unmarshal(respBytes, &resp); decodeErr != nil {
		return nil, fmt.Errorf("parsing record response: %w", decodeErr)
	}

	return &AddResult{
		ID:        resp.ID,
		SessionID: sessionUUID,
		Records:   []RecordSummary{{ID: resp.ID, Type: recordType, Summary: summary}},
		Status:    "ok",
		Mode:      "oss",
	}, nil
}

// --------------------------------------------------------------------------
// SearchSession — session-scoped hybrid search (POST /api/v1/search)
// --------------------------------------------------------------------------

// SearchSession runs a hybrid (vector + full-text) search scoped to a single
// session UUID via POST /api/v1/search with scope=sessions. Unlike Search
// (tenant-wide sessions plane), this confines results to one chat — the MCP
// semantic_search(uuid=...) contract.
//
// An empty sessionUUID is allowed and means "unscoped within tenant" (the
// server treats an empty uuid as no session filter), but the common call passes
// a concrete chat uuid.
//
func (m *Memory) SearchSession(ctx context.Context, sessionUUID, query string, opts ...ReadOption) ([]SearchResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if query == "" {
		return nil, ErrEmptyInput
	}

	cfg := applyReadOptions(opts)
	limit := cfg.limit
	if limit <= 0 {
		// Match Search's historical default so the two methods behave alike when
		// the caller does not pass WithLimit.
		limit = 10
	}

	payload := map[string]interface{}{
		"uuid":  sessionUUID,
		"text":  query,
		"limit": limit,
		"scope": "sessions",
	}
	if cfg.typeFilter != "" {
		payload["type_filter"] = cfg.typeFilter
	}

	respBytes, postErr := m.conn.PostRead(ctx, "/api/v1/search", payload)
	if postErr != nil {
		return nil, postErr
	}

	var resp searchResponse
	if decodeErr := json.Unmarshal(respBytes, &resp); decodeErr != nil {
		return nil, fmt.Errorf("parsing session search response: %w", decodeErr)
	}

	// The nested {record, similarity} envelope IS the public SearchResult shape,
	// identical to Search/SearchByType — decode straight in so the full record
	// survives. Preserve the non-nil empty-slice contract.
	if resp.Results == nil {
		return []SearchResult{}, nil
	}
	return resp.Results, nil
}

// --------------------------------------------------------------------------
// Query — structured AST query (POST /api/v1/query)
// --------------------------------------------------------------------------

// Query executes a structured AST query via POST /api/v1/query and returns the
// matching records as a flat slice. This is the MCP execute_ast contract: a
// whitelisted filter/sort/pagination grammar over the record columns, evaluated
// server-side as SQL.
//
// The request is the QueryRequest struct (filters/sort/pagination/select). Build
// it directly, or with the small fluent helpers (NewQuery().Where(...).
// OrderBy(...).Limit(...)).
//
//	req := client.NewQuery().
//	    Where("type", client.QueryOp{Eq: "fact"}).
//	    Where("score", client.QueryOp{Gte: 7}).
//	    OrderBy("created_at", "desc").
//	    Limit(20)
//	records, _ := mem.Query(ctx, req)
//
func (m *Memory) Query(ctx context.Context, request *QueryRequest, opts ...ReadOption) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if request == nil {
		return nil, fmt.Errorf("Query: request is required")
	}

	_ = opts

	// The AST is a read behind POST.
	respBytes, postErr := m.conn.PostRead(ctx, "/api/v1/query", request)
	if postErr != nil {
		return nil, postErr
	}

	var wrapped queryResponse
	if decodeErr := json.Unmarshal(respBytes, &wrapped); decodeErr != nil {
		return nil, fmt.Errorf("parsing query response: %w", decodeErr)
	}
	// records:null (empty result set) decodes to a nil slice; normalise to an
	// empty slice so callers can range without a nil guard.
	if wrapped.Records == nil {
		return []models.Record{}, nil
	}
	return wrapped.Records, nil
}

// --------------------------------------------------------------------------
// Manifest — global & session paginated listings
// --------------------------------------------------------------------------

// ManifestGlobal returns a paginated page of the tenant's full record manifest
// via GET /api/v1/manifest, with the complete envelope (records + pagination
// cursor). Use this when you need has_more / effective limit / offset; use
// Recent when you only want the newest-N records as a flat slice.
//
// (WithAsOf/WithSince/WithUntil) scope created_at; as_of is mutually exclusive
// with since/until (the server returns HTTP 400 on violation — we surface it).
//
func (m *Memory) ManifestGlobal(ctx context.Context, keyword string, limit, offset int, opts ...ReadOption) (*ManifestPage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	if keyword != "" {
		params.Set("q", keyword)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	applyTemporalParams(params, cfg)

	respBytes, getErr := m.conn.Get(ctx, "/api/v1/manifest", params)
	if getErr != nil {
		return nil, getErr
	}
	return decodeManifestPage(respBytes, "global manifest")
}

// ManifestSession returns a paginated page of one session's record manifest via
// GET /api/v1/chats/{uuid}/manifest. Same envelope as ManifestGlobal, scoped to
// the session.
//
func (m *Memory) ManifestSession(ctx context.Context, sessionUUID, keyword string, limit, offset int, opts ...ReadOption) (*ManifestPage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if sessionUUID == "" {
		return nil, fmt.Errorf("ManifestSession: sessionUUID is required")
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	if keyword != "" {
		params.Set("q", keyword)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	applyTemporalParams(params, cfg)

	path := fmt.Sprintf("/api/v1/chats/%s/manifest", url.PathEscape(sessionUUID))
	respBytes, getErr := m.conn.Get(ctx, path, params)
	if getErr != nil {
		return nil, getErr
	}
	return decodeManifestPage(respBytes, "session manifest")
}

// ListChat returns every record for a single chat/session via GET
// /api/v1/chats/{uuid} — metadata only (no .gz body), no pagination. It is the
// MCP list_chat contract: the whole matching set for the session in one call.
//
// consolidated is a tri-state filter:
//   - nil      → no filter (all records for the session)
//   - &true    → only consolidated records
//   - &false   → only non-consolidated records
//
// status (optional, "" = no filter) is an exact status match
// (e.g. "saved","processing","failed").
//
func (m *Memory) ListChat(ctx context.Context, sessionUUID string, consolidated *bool, status string, opts ...ReadOption) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if sessionUUID == "" {
		return nil, fmt.Errorf("ListChat: sessionUUID is required")
	}

	_ = opts

	params := url.Values{}
	if consolidated != nil {
		params.Set("consolidated", strconv.FormatBool(*consolidated))
	}
	if status != "" {
		params.Set("status", status)
	}

	path := fmt.Sprintf("/api/v1/chats/%s", url.PathEscape(sessionUUID))
	respBytes, getErr := m.conn.Get(ctx, path, params)
	if getErr != nil {
		return nil, getErr
	}
	return decodeRecordsEnvelope(respBytes, "list chat")
}

// --------------------------------------------------------------------------
// CountByType — aggregate the manifest by record type
// --------------------------------------------------------------------------

// CountByType returns a {type: count} map by paging the global manifest and
// tallying each record's "type" field. It is the MCP count_by_type contract.
//
func (m *Memory) CountByType(ctx context.Context, opts ...ReadOption) (map[string]int, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	counts := map[string]int{}
	const pageSize = 1000 // match Python/TS; server hard-caps manifest limit at 1000.
	offset := 0
	for {
		page, pageErr := m.ManifestGlobal(ctx, "", pageSize, offset, opts...)
		if pageErr != nil {
			return nil, fmt.Errorf("CountByType: paging manifest at offset %d: %w", offset, pageErr)
		}
		for _, record := range page.Records {
			counts[string(record.Type)]++
		}
		// Stop when the page is short OR the server says there is no more. We
		// require BOTH an empty/short page AND !has_more so the exactly-full
		// last-page false-positive on has_more cannot loop forever: a follow-up
		// page that returns 0 records always terminates the loop.
		if len(page.Records) == 0 || !page.HasMore {
			break
		}
		offset += len(page.Records)
	}
	return counts, nil
}

// --------------------------------------------------------------------------
// ListTypes — LOCAL taxonomy (no HTTP)
// --------------------------------------------------------------------------

// ListTypes returns the canonical record-type taxonomy as a local, static slice
// — NO network call. It mirrors the MCP list_types tool, which exposes the same
//
func (m *Memory) ListTypes() []models.MemoryType {
	return []models.MemoryType{
		models.TypeEpisodic,
		models.TypeFact,
		models.TypePreference,
		models.TypeDecision,
		models.TypeTask,
		models.TypeRisk,
		models.TypeReasoning,
		models.TypeIdea,
		models.TypeEmotion,
		models.TypeConsolidated,
		models.TypeHub,
		models.TypeFile,
	}
}

// --------------------------------------------------------------------------
// GetGrounding — provenance/anchor traversal (GET /records/{id}/grounding)
// --------------------------------------------------------------------------

// GetGrounding returns the grounding (provenance) of a record via GET
// /api/v1/records/{id}/grounding: the episodic anchors and consolidations
// reachable from the target within a BFS depth budget. It is the MCP
// get_grounding contract — used to answer "what source turns back this memory?".
//
// maxDepth is the BFS depth budget. Pass 0 to use the server default (3); any
// other value MUST be 1..5 inclusive or the server returns HTTP 400
// "max_depth must be an integer between 1 and 5" (we surface that error rather
// than clamping, so an out-of-range value fails loud).
//
func (m *Memory) GetGrounding(ctx context.Context, recordID int64, maxDepth int, opts ...ReadOption) (*GroundingResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return nil, fmt.Errorf("GetGrounding: recordID must be > 0")
	}

	_ = opts

	params := url.Values{}
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

// --------------------------------------------------------------------------
// Shared helpers
// --------------------------------------------------------------------------

// applyTemporalParams folds the optional bi-temporal read filters
// (as_of / since / until) from a resolved searchConfig into a url.Values. Kept
// in one place so every temporal-aware read (the manifests) emits identical
// query keys; the server enforces the as_of-vs-since/until exclusivity and
// returns HTTP 400 on a violation, which the caller surfaces.
func applyTemporalParams(params url.Values, cfg searchConfig) {
	if cfg.asOf != "" {
		params.Set("as_of", cfg.asOf)
	}
	if cfg.since != "" {
		params.Set("since", cfg.since)
	}
	if cfg.until != "" {
		params.Set("until", cfg.until)
	}
}

// decodeManifestPage decodes the manifest envelope shared by ManifestGlobal and
// ManifestSession: {"records":[Record],"count":int,"limit":int,"offset":int,
// "has_more":bool}. label names the call site for clearer parse errors.
//
func decodeManifestPage(raw []byte, label string) (*ManifestPage, error) {
	switch firstJSONToken(raw) {
	case '{':
		var page ManifestPage
		if parseErr := json.Unmarshal(raw, &page); parseErr != nil {
			return nil, fmt.Errorf("parsing %s response (object): %w", label, parseErr)
		}
		if page.Records == nil {
			page.Records = []models.Record{}
		}
		return &page, nil
	case '[':
		// Defensive: a legacy server could emit a bare array. Wrap it so callers
		// always get the envelope shape.
		var records []models.Record
		if parseErr := json.Unmarshal(raw, &records); parseErr != nil {
			return nil, fmt.Errorf("parsing %s response (array): %w", label, parseErr)
		}
		return &ManifestPage{Records: records, Count: len(records)}, nil
	default:
		return &ManifestPage{Records: []models.Record{}}, nil
	}
}

// decodeRecordsEnvelope decodes a {"records":[Record],"count":int} envelope (no
// pagination) — used by ListChat. Same empty-is-not-an-error discipline as
// decodeManifestPage.
func decodeRecordsEnvelope(raw []byte, label string) ([]models.Record, error) {
	switch firstJSONToken(raw) {
	case '{':
		var wrapped queryResponse // identical shape: {records, count}
		if parseErr := json.Unmarshal(raw, &wrapped); parseErr != nil {
			return nil, fmt.Errorf("parsing %s response (object): %w", label, parseErr)
		}
		if wrapped.Records == nil {
			return []models.Record{}, nil
		}
		return wrapped.Records, nil
	case '[':
		var records []models.Record
		if parseErr := json.Unmarshal(raw, &records); parseErr != nil {
			return nil, fmt.Errorf("parsing %s response (array): %w", label, parseErr)
		}
		return records, nil
	default:
		return []models.Record{}, nil
	}
}
