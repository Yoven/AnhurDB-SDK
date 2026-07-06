package client

// parity.go holds the methods added in the 2026-06-18 SDK-parity pass so the
// Go surface mirrors the canonical MCP tool set one-for-one with the Python and
// TypeScript SDKs (see ../PARITY_SPEC.md). Keeping them in a dedicated file
// keeps the diff reviewable; they are ordinary methods on the same *Memory type
// and follow the EXACT conventions of client.go (HTTPConnection Get/PostRead/
// Patch, applyReadOptions, firstJSONToken envelope detection, fully explicit
// variable names, dense Junior Tips).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/anhurdb/sdk-go/v2/models"
)

// --------------------------------------------------------------------------
// Create — full-fidelity record creation (POST /api/v1/records)
// --------------------------------------------------------------------------

// Create writes a single record via POST /api/v1/records and returns the new
// record id (plus the Raft index for read-your-writes). It is the canonical
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
// Junior Tip [why a distinct method from Add — verified 2026-06-18 against
// server/handler/record_crud.go]: Add may route through the cloud /api/v1/ingest
// worker, which owns its OWN session policy and salience scoring (it overrides a
// caller score). Create ALWAYS POSTs to /api/v1/records (the synchronous service
// path, service.CreateRecord), so every supplied field — uuid, type, score,
// related_ids — is written verbatim and the real DB id comes back. This is the
// MCP create_memory contract: caller-owned placement, no worker override.
//
// Junior Tip [valid_from travels in metadata — verified against
// service/record_create.go:257-271]: the REST CreateRequest struct has NO
// valid_from field; the service layer reads valid_from/valid_until from the
// metadata JSON as the live REST path (in.ValidFrom is only populated by a
// future proto path). So WithCreateValidFrom injects "valid_from" into the
// metadata envelope — the ONLY field name the server actually parses on this
// route. Sending it as a top-level body key would be a silent no-op (the
// json.Decode into CreateRequest would drop it), exactly the silent-loss bug
// class this SDK exists to avoid.
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

	// summary is the FTS5-indexed short form; content is the full body written
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

	// Junior Tip [no anchor-seed — 2026-07-06]: the create is exactly ONE
	// request. The server AUTO-LINKS the episodic anchor
	// (record_create.go FindLastEpisodicConsistent, Rule 3a) and the RYW race is
	// closed by writeConn, so a derived record (fact/decision/…) placed in a
	// session that already has an episodic just works. A session GENUINELY with
	// no episodic returns an honest typed 422 ("create an episodic record
	// first") — the CORRECT contract, since callers write episodic-first
	// (agents/plugin already do). We do NOT fabricate a synthetic episodic
	// anchor: that polluted the graph (violates the perfect-brain invariant) and
	// diverged from the gRPC path. The typed error surfaces straight to the dev.
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
		Records:   []RecordSummary{{ID: resp.ID, Type: recordType, Summary: summary}},
		Status:    "ok",
		Mode:      "oss",
		RaftIndex: resp.RaftIndex,
	}, nil
}

// --------------------------------------------------------------------------
// SearchSession — session-scoped hybrid search (POST /api/v1/search)
// --------------------------------------------------------------------------

// SearchSession runs a hybrid (vector + full-text) search scoped to a single
// session UUID via POST /api/v1/search. Unlike Search (which fans out across
// every session for the container tag via /api/v1/search/global), this confines
// results to one chat — the MCP semantic_search(uuid=...) contract.
//
// An empty sessionUUID is allowed and means "unscoped within tenant" (the
// server treats an empty uuid as no session filter), but the common call passes
// a concrete chat uuid.
//
// Junior Tip [field name is text, NOT query — verified against
// server/handler/record_search.go + model.SearchRequest]: the /api/v1/search
// handler decodes ONLY into model.SearchRequest, whose keyword field is JSON
// "text". There is NO "query" or "mode" field — vector-only is semantic,
// text-only is FTS5, both is hybrid, decided implicitly server-side. Sending a
// "mode" key is silently dropped by json.Decode, so we never send one.
//
// Junior Tip [RYW]: like Search, this is a read behind POST, so PostRead
// carries the optional X-Anhur-Min-Index barrier from WithMinIndex.
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
	}
	if cfg.typeFilter != "" {
		payload["type_filter"] = cfg.typeFilter
	}

	respBytes, postErr := m.conn.PostRead(ctx, "/api/v1/search", payload, cfg.minIndex)
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
// Junior Tip [whitelisted fields — verified against
// server/handler/record_query.go]: filter and sort field names MUST be in the
// server column whitelist {id,uuid,type,dimension,weight,score,status,
// consolidated,archived,created_at,updated_at,prefix,metadata,summary,
// superseded_by,valid_from,valid_until} or the server returns HTTP 400 "invalid
// filter field" / "invalid sort field". The SDK forwards the field name as-is
// (no client-side whitelist) so a typo fails loud with the server's message
// rather than being silently dropped.
//
// Junior Tip [select does NOT project — verified in the same handler]: the
// 'select' list is parsed but the SQL SELECT list is fixed, so the FULL record
// always comes back regardless of select. We keep the field for forward-compat
// and parity with Python's QueryBuilder, but it never narrows the response.
//
// Junior Tip [flat array, not {record,similarity}]: /api/v1/query returns
// {"records": [Record], "count": int} — a FLAT array, unlike /search which
// wraps each hit. We decode straight into []models.Record.
func (m *Memory) Query(ctx context.Context, request *QueryRequest, opts ...ReadOption) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if request == nil {
		return nil, fmt.Errorf("Query: request is required")
	}

	cfg := applyReadOptions(opts)

	// The AST is a read behind POST; PostRead carries the optional RYW barrier.
	respBytes, postErr := m.conn.PostRead(ctx, "/api/v1/query", request, cfg.minIndex)
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
// keyword (optional) maps to ?q, the FTS5 filter. The temporal options
// (WithAsOf/WithSince/WithUntil) scope created_at; as_of is mutually exclusive
// with since/until (the server returns HTTP 400 on violation — we surface it).
//
// Junior Tip [has_more is a heuristic — verified against
// server/handler/record_search.go]: the server computes has_more as
// len(records)==limit, which can false-positive on an exactly-full last page.
// The cursor is still safe to follow (the next page simply returns 0 records);
// we surface the server's value verbatim rather than second-guessing it.
//
// Junior Tip [?q ignores offset]: when keyword is set the server takes the
// search path which does NOT apply offset. We still send offset for symmetry,
// but the caller should not rely on offset paging while filtering by keyword.
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

	respBytes, getErr := m.conn.Get(ctx, "/api/v1/manifest", params, cfg.minIndex)
	if getErr != nil {
		return nil, getErr
	}
	return decodeManifestPage(respBytes, "global manifest")
}

// ManifestSession returns a paginated page of one session's record manifest via
// GET /api/v1/chats/{uuid}/manifest. Same envelope as ManifestGlobal, scoped to
// the session.
//
// Junior Tip [no ?query alias here — verified against
// server/handler/record_session.go]: unlike the GLOBAL manifest (which accepts
// both ?q and ?query), the SESSION manifest reads ONLY ?q. We therefore send
// the keyword exclusively as ?q so it is honoured on this route.
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
	respBytes, getErr := m.conn.Get(ctx, path, params, cfg.minIndex)
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
// Junior Tip [consolidated=false semantics — verified against
// server/handler/record_session.go]: the server parses the consolidated query
// param as (value=="true"), so ANY non-empty value other than "true" (including
// "false") selects only NON-consolidated records. We only emit the param when
// the caller passes a non-nil *bool, so the default truly is "no filter".
func (m *Memory) ListChat(ctx context.Context, sessionUUID string, consolidated *bool, status string, opts ...ReadOption) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if sessionUUID == "" {
		return nil, fmt.Errorf("ListChat: sessionUUID is required")
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	if consolidated != nil {
		params.Set("consolidated", strconv.FormatBool(*consolidated))
	}
	if status != "" {
		params.Set("status", status)
	}

	path := fmt.Sprintf("/api/v1/chats/%s", url.PathEscape(sessionUUID))
	respBytes, getErr := m.conn.Get(ctx, path, params, cfg.minIndex)
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
// Junior Tip [why we page, not send limit=0 — verified against
// server/handler/record_search.go]: the manifest handler GUARDS limit with an
// l>0 check, so limit=0 does NOT return zero rows — it falls back to the default
// 100-row first page. A naive "GET /manifest?limit=0 and count" would silently
// undercount any tenant with >100 records (returning a tally of at most 100).
// To aggregate truthfully we page with offset until has_more is false and the
// page comes back empty, summing types across every page. Failing loud on a
// partial fetch is preferable to a confidently-wrong count.
func (m *Memory) CountByType(ctx context.Context, opts ...ReadOption) (map[string]int, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	counts := map[string]int{}
	const pageSize = 500 // server hard-caps manifest limit at 1000; 500 keeps pages cheap.
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
// fixed taxonomy that lives in models/enums.go (AnhurCore/core.yaml).
//
// Junior Tip [why local — by design across all three SDKs]: there is no REST
// route for the taxonomy; it is a compile-time constant shared by the server,
// agents, and SDKs. Returning it from the in-memory enum guarantees the SDK can
// never drift from a stale server response, and the call cannot fail. The order
// matches the const block in models/enums.go.
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
// Junior Tip [anchors/consolidations are always present arrays]: the server
// guarantees non-null anchors[] and consolidations[] even when empty, and sets
// anchors_capped / consolidations_capped when it truncated (>20 anchors / >10
// consolidations). We decode all of these so the caller can detect truncation
// instead of silently believing it saw the full set.
func (m *Memory) GetGrounding(ctx context.Context, recordID int64, maxDepth int, opts ...ReadOption) (*GroundingResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return nil, fmt.Errorf("GetGrounding: recordID must be > 0")
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	if maxDepth > 0 {
		// 0 means "let the server default to 3". Any explicit value is forwarded
		// as-is; the server validates the 1..5 range and rejects loudly.
		params.Set("max_depth", strconv.Itoa(maxDepth))
	}

	path := fmt.Sprintf("/api/v1/records/%d/grounding", recordID)
	respBytes, getErr := m.conn.Get(ctx, path, params, cfg.minIndex)
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
// Junior Tip [empty manifest is an OBJECT, never an error — mirrors Recent]: an
// empty manifest serialises as {"records":[]} (or records:null), NOT a bare
// array, so we branch on the first JSON token like Recent/ListSessions do. A
// "null"/empty body yields an empty page rather than a spurious decode error —
// empty is a valid result, never a format signal.
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
