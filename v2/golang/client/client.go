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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

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

	// Session UUID: containerTag + UTC timestamp + random 6 hex chars.
	sessionUUID := deriveSessionUUID(containerTag)

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

// utcTimestamp returns the current UTC time as "YYYYMMDD-HHMMSS".
//
// Junior Tip [SDK parity 2026-07-03]: this MUST byte-for-byte match the
// Python SDK's _utc_timestamp (strftime("%Y%m%d-%H%M%S")) and the TypeScript
// SDK's utcTimestamp. The Go reference layout "20060102-150405" is the exact
// equivalent — do NOT reorder the layout fields or the three SDKs will emit
// differently-shaped session UUIDs, breaking the cross-SDK session_uuid
// invariant.
func utcTimestamp() string {
	return time.Now().UTC().Format("20060102-150405")
}

// deriveSessionUUID builds the default auto-derived session UUID:
//
//	<container_tag>-<YYYYMMDD-HHMMSS UTC>-<6 hex random>
//
// e.g. "mem-3f9a1b2c4d5e-20260703-143025-a1b2c3".
//
// Junior Tip [SDK parity 2026-07-03]: both the UTC timestamp layout AND the
// width of the random suffix (6 hex = 3 crypto/rand bytes) MUST match the
// Python and TypeScript SDKs byte-for-byte. The timestamp alone is not unique
// enough (two sessions in the same second would collide), so the 6-hex random
// tail disambiguates while staying identical in shape across languages. This
// is the single source of truth for the format — NewMemory and NewSession both
// call it so the two paths can never drift apart.
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
// Junior Tip [Bug 2026-05-21 — metadata corruption]: every consolidated
// record (and several record-create code paths) had `metadata` written as the
// bare containerTag string ("mem-3f9...") rather than a JSON object. That
// poisoned every downstream agent that does json.Unmarshal(metadata) — entity
// taggers logged "tagged_no_entities" on these records because the metadata
// parse failed at the very first step. We now centralise the wrapping here so
// the bug cannot regress through a different write path.
//
// Returns "{}" when containerTag is empty, so callers can rely on the column
// always holding a parseable JSON object.
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
// Junior Tip [container_tag is SDK-owned]: container_tag is set LAST so a
// caller cannot accidentally (or maliciously) clobber the tenant routing key
// via WithMetadata. Every other key the caller supplies is preserved verbatim,
// which is the WRITE-path half of the SDK-parity fix (the old Add silently
// dropped all caller metadata).
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

// Add stores a memory. This is the simplest way to save information.
//
// It tries the cloud /api/v1/ingest endpoint first (which handles
// embedding + extraction automatically). If that returns 404, it
// falls back to /api/v1/records (OSS mode, stores as text).
//
// Optional functional options let the caller control the record's score,
// type, and metadata while keeping Add(ctx, text) — with no options —
// fully backward compatible:
//
//	mem.Add(ctx, "plain text")                                  // defaults
//	mem.Add(ctx, "fact", client.WithScore(9), client.WithType("semantic"))
//	mem.Add(ctx, "x", client.WithMetadata(map[string]any{"source": "import"}))
//
// Junior Tip [why explicit score/type bypasses ingest — verified 2026-06-09
// against the live cluster]: the /api/v1/ingest worker owns its own salience
// scoring and type classification. When probed, it ACCEPTS a "score" field in
// the body but stores the record with its OWN computed score (observed: a
// supplied score=8 landed as score=0). Routing WithScore/WithType through
// ingest would therefore SILENTLY DROP them — the exact bug class this
// hardening pass exists to kill. So when the caller explicitly sets score OR
// type, Add takes the SYNCHRONOUS /api/v1/records path, which writes the
// supplied values verbatim and returns the real DB id. WithMetadata alone does
// NOT force the records path, because ingest preserves caller metadata.
//
// Junior Tip [retry semantics]: this is a WRITE, so Add transparently retries
// a small set of TRANSIENT failures (Raft not_leader during a leadership flap,
// and the episodic-anchor 422 that resolves once a concurrent anchor lands)
// with exponential backoff. Permanent validation errors (empty input, bad
// score, 401) are returned immediately — retrying those just wastes time and
// can amplify load. See isTransientWriteError + withWriteRetry.
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

	// When the caller pins score, type, OR metadata, the cloud ingest endpoint
	// would silently drop them — its request struct is exactly {content,
	// container_tag} (server handler/ingest.go) — so go straight to the
	// synchronous records path that persists all three.
	//
	// Junior Tip [metadata silent-drop parity, 2026-06-18]: metadata was missing
	// from this condition, so a metadata-only Add was routed to /ingest and the
	// caller's metadata vanished. The Python SDK already routed metadata to
	// /records for exactly this reason; the three SDKs now agree — any pinned
	// score/type/metadata forces the records path.
	forceRecordsPath := cfg.score != nil || cfg.memType != nil || len(cfg.metadata) > 0

	return withWriteRetry(ctx, func() (*AddResult, error) {
		// Try cloud ingest first (has auto-embedding) UNLESS the caller pinned
		// score/type. Once we know ingest is unavailable (404), we skip it on
		// subsequent calls to avoid unnecessary round-trips.
		if !forceRecordsPath && (m.ingestAvailable == nil || *m.ingestAvailable) {
			result, err := m.tryIngest(ctx, text, cfg)
			if result != nil {
				return result, nil
			}
			// Only propagate non-404 errors.
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, err
			}
		}

		// Synchronous record creation: OSS/self-hosted fallback, OR the
		// score/type-pinned path that ingest cannot honour.
		return m.createRecord(ctx, text, cfg)
	})
}

// tryIngest attempts the cloud ingest endpoint.
// Returns (nil, ErrNotFound) if the endpoint doesn't exist.
func (m *Memory) tryIngest(ctx context.Context, text string, cfg *addConfig) (*AddResult, error) {
	payload := map[string]interface{}{
		"content":       text,
		"container_tag": m.containerTag,
	}
	// Junior Tip [ingest ignores metadata, 2026-06-18]: the server's ingest
	// request struct is exactly {content, container_tag} (handler/ingest.go), so
	// score/type/metadata would all be silently dropped here. Add routes any
	// pinned score/type/metadata to the synchronous records path precisely so they
	// are NOT lost — tryIngest is therefore only reached for a plain add(text),
	// and we deliberately forward nothing beyond content + container_tag.
	_ = cfg // no addConfig field is honoured by the ingest endpoint

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
		// Usually 0 on the ingest path (async pipeline reports no synchronous
		// Raft index); surfaced for parity in case the server starts returning one.
		RaftIndex: resp.RaftIndex,
	}, nil
}

// CreateInSession stores `text` directly via POST /api/v1/records as an
// episodic record under the supplied sessionUUID — bypassing m.sessionUUID
// and any auto-session assignment in /api/v1/ingest. Used by agents that
// must write into the SAME session as the source content they are
// summarising (e.g. consolidation creating a consolidated star inside the
// chat-* session it synthesised, so the judge can locate the source
// records by session uuid).
//
// Junior Tip [why not Memory.Add]: Memory.Add routes through /api/v1/ingest
// when the client is connected to a hosted endpoint, and the ingest worker
// owns its own auto-session policy ("mem-xxxx-random" container tags). For
// CreateInSession we need to place the row in a CALLER-OWNED session uuid,
// so we POST to /api/v1/records directly. The SDK does not advertise
// /api/v1/records as part of the public surface for application use; this
// method is an agent-internal write path.
func (m *Memory) CreateInSession(ctx context.Context, text string, sessionUUID string) (*AddResult, error) {
	// Junior Tip [thin wrapper over Create — 2026-06-18 parity pass]: this is now
	// exactly Create(ctx, sessionUUID, text) with the default options (episodic /
	// score 5 / status "saved" / no related_ids), which is byte-for-byte the
	// payload this method built inline before. The full-fidelity logic (type /
	// score / related_ids / valid_from / metadata) lives in Create so there is a
	// SINGLE create code path — agents that need a non-default field call Create
	// directly. CreateInSession stays as the named convenience the consolidation
	// and judge agents already import.
	return m.Create(ctx, sessionUUID, text)
}

// AppendMainIDs appends parent record IDs to the main_ids array of a single
// record via PATCH /api/v1/records/append-main-ids. Server-side the operation
// reads, deduplicates, and writes back — safe to call repeatedly with the
// same payload (idempotent on the union of existing + supplied IDs).
//
// Junior Tip [server contract]: the endpoint accepts a list of target record
// IDs (children that will receive the new parents) and a list of parent IDs
// to append to EACH child. The SDK wrapper exposes the single-record shape
// (one child, N parents) because that is the common consolidation-time
// call site; agents that need multi-child fan-out can call the REST
// endpoint directly with `ids` being a slice.
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

// LinkConsolidated sets the consolidate_id column on a batch of children
// records via PATCH /api/v1/records/consolidate-ids. Used by the judge agent
// after a consolidated star is approved: every source record gets its
// consolidate_id pointed at the star so subsequent queries can navigate
// child → parent in one column read.
//
// Junior Tip [why batch, not per-child]: the typical session has 5-15
// children pointing at the same star. Looping per-id would cost N Raft
// round-trips; the batch endpoint compresses to one log entry.
//
// Junior Tip [name parity 2026-06-18]: this method is the canonical name for
// the MCP `link_consolidated` tool, aligned across Go/Python/TS. It was renamed
// from UpdateConsolidateIDs; the old name survives as a Deprecated alias below
// (AnhurAgents' judge still imports it) — never delete the alias, only the call
// sites migrate over time.
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
// only so existing callers (AnhurAgents' judge at cmd/judge/main.go) keep
// compiling. New code MUST call LinkConsolidated.
func (m *Memory) UpdateConsolidateIDs(ctx context.Context, ids []int64, consolidateID int64) error {
	return m.LinkConsolidated(ctx, ids, consolidateID)
}

// createRecord stores text directly via POST /api/v1/records.
//
// Without server-side embedding, we store the text in both summary
// (for FTS5 search) and content (for full retrieval). The vector is
// empty — the server handles records without vectors via text search.
//
// Caller-supplied score/type/metadata (via AddOption) override the historical
// defaults of score=5, type="episodic", and the bare container_tag envelope.
func (m *Memory) createRecord(ctx context.Context, text string, cfg *addConfig) (*AddResult, error) {
	summary := truncateSummary(text)

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
		"uuid":           m.sessionUUID,
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

	respBytes, err := m.postRecordSeedingAnchor(ctx, payload, recordType)
	if err != nil {
		return nil, err
	}

	var resp recordCreateResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("parsing record response: %w", err)
	}

	return &AddResult{
		ID:        resp.ID,
		Records:   []RecordSummary{{ID: resp.ID, Type: recordType, Summary: summary}},
		Status:    "ok",
		Mode:      "oss",
		RaftIndex: resp.RaftIndex,
	}, nil
}

// Search finds relevant memories using hybrid (vector + full-text) search.
//
// Uses global search (not session-scoped) so Memory finds facts across
// ALL sessions for this user, not just the current one.
//
// Junior Tip [RYW]: WithMinIndex is accepted alongside the search options so a
// caller can require its own just-written record to be visible. Search is a
// read behind POST; PostRead carries the X-Anhur-Min-Index barrier the same
// way Get does for GET reads (server middleware honours both — see app.go).
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

	respBytes, err := m.conn.PostRead(ctx, "/api/v1/search/global", payload, cfg.minIndex)
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

// Profile retrieves the memory profile for this container tag.
//
// If the server doesn't have a profile endpoint yet (OSS without agents),
// it returns an empty profile rather than failing — matching the Python
// SDK behaviour.
func (m *Memory) Profile(ctx context.Context, opts ...ReadOption) (*ProfileResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	params.Set("tag", m.containerTag)

	respBytes, err := m.conn.Get(ctx, "/api/v1/profile", params, cfg.minIndex)
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

	respBytes, err := m.conn.Get(ctx, "/api/v1/search/type", params, cfg.minIndex)
	if err != nil {
		return nil, err
	}

	// Junior Tip [envelope-key fix, 2026-07-04]: GET /api/v1/search/type does NOT use
	// the {"results":[{record,similarity}]} envelope of Search/SearchGlobal — the server
	// handler (server/handler/record_search.go: SearchByType) writes a BARE record array
	// under "records": {"records":[<Record>],"count":N}. Decoding into searchResponse
	// (which reads "results") therefore matched NOTHING and returned an empty slice for
	// EVERY call — the cross-SDK "search_by_type returns empty" bug. We decode "records"
	// and wrap each full record into the canonical SearchResult so the return shape stays
	// identical to the other search methods. A type filter carries no semantic distance,
	// so Similarity is 0 — the ranking lives in the record's own weight/score, preserved
	// verbatim.
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
// Uses the DuckDB-backed smart search engine that ranks results by a
// combination of text relevance and cognitive importance (score).
func (m *Memory) SmartSearch(ctx context.Context, query string, limit int, opts ...ReadOption) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if query == "" {
		return nil, ErrEmptyInput
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	params.Set("q", query)
	params.Set("limit", strconv.Itoa(limit))

	return m.conn.Get(ctx, "/api/v1/search/smart", params, cfg.minIndex)
}

// Recall searches for memories using global search with a wider scope.
// Functionally identical to Search but named "Recall" to match the MCP
// tool set naming. Extra read options (e.g. WithMinIndex) are forwarded.
func (m *Memory) Recall(ctx context.Context, query string, limit int, opts ...ReadOption) ([]SearchResult, error) {
	return m.Search(ctx, query, append([]ReadOption{WithLimit(limit)}, opts...)...)
}

// Walk performs a BFS graph traversal starting from a given record.
//
// direction:"both" means traverse both incoming and outgoing edges.
// The server returns nodes and edges up to the specified depth.
func (m *Memory) Walk(ctx context.Context, startID int64, depth int, opts ...ReadOption) (*WalkResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	payload := map[string]interface{}{
		"seed_id":   startID,
		"depth":     depth,
		"direction": "both",
	}

	respBytes, err := m.conn.PostRead(ctx, "/api/v1/walk", payload, cfg.minIndex)
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
// the cost budget; WithMinIndex still composes for read-your-writes.
//
// Junior Tip [goal-directed contract, verified against
// server/handler/record_search_graph.go on 2026-07-03]: the server reads
// target ∈ {semantic,tag,recency} and, for semantic, a base64 "vector"; an ""
// / "dijkstra" target yields a nil goal (plain Dijkstra). We only add a JSON
// key when its option was set, so an option-free call sends exactly the legacy
// {seed_id, depth} body. NOTE: the handler struct has no Depth field, so the
// server has always IGNORED "depth" on this route — we keep sending it purely
// so the wire body is unchanged for existing callers; max_nodes (server default
// 50) governs breadth. Extending depth→max_nodes would be a behaviour change,
// so it is intentionally left out of this minimal goal-target addition.
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

	respBytes, err := m.conn.PostRead(ctx, "/api/v1/walk/semantic", payload, cfg.minIndex)
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

	cfg := applyReadOptions(opts)

	respBytes, err := m.conn.Get(ctx, "/api/v1/sessions/stats", nil, cfg.minIndex)
	if err != nil {
		return nil, err
	}

	// The endpoint returns either an envelope {"sessions": [...], "count": N, ...}
	// or (legacy) a bare array [...].
	//
	// Junior Tip [Bug — empty-sessions crash, 2026-06-11]: the previous code
	// branched on `len(wrapped.Sessions) > 0`, so a tenant with ZERO sessions —
	// which the live server returns as {"sessions": []} (an OBJECT) — fell
	// through to the bare-array fallback and crashed with "cannot unmarshal
	// object into []SessionStats". That silently broke the RunCycle of EVERY
	// pipeline agent (judge, entity_tagger, consolidation, decay, linker,
	// back_tagger) on any empty or brand-new tenant — exactly the state of a
	// tenant right after a fresh data load. We now branch on the actual JSON
	// kind (first token), mirroring RecentMemories, so an empty envelope yields
	// an empty slice instead of an error. NUNCA fazer fallback de shape por
	// "está vazio" — vazio é resultado válido, não sinal de formato.
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

	cfg := applyReadOptions(opts)

	path := fmt.Sprintf("/api/v1/records/%d/topology", recordID)
	respBytes, err := m.conn.Get(ctx, path, nil, cfg.minIndex)
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
// Junior Tip [no JSON-wrapper unwrap, fixed 2026-05-28]:
// An earlier version tried `json.Unmarshal` into a `{"content": "…"}`
// wrapper before falling back to raw text. The server has never
// returned that shape — RecordHandler.GetContent writes raw bytes with
// `Content-Type: text/plain` — so the unwrap branch was dead AND
// actively dangerous: a user uploading a `.json` file whose own
// contents happen to be `{"content": "hello"}` would have the SDK
// silently return "hello" instead of the 18-byte file, mangling
// subsequent checksum verification and extractor input. The wrapper
// is gone; bytes round-trip verbatim. Discovered while wiring
// the file_ingestor agent to use this method for chat-mode uploads.
func (m *Memory) ReadContent(ctx context.Context, recordID int64, opts ...ReadOption) (string, error) {
	if m.conn == nil {
		return "", ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	path := fmt.Sprintf("/api/v1/records/%d/content", recordID)
	respBytes, err := m.conn.Get(ctx, path, nil, cfg.minIndex)
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
// Junior Tip [endpoint parity 2026-07-03]: this MUST call /api/v1/recent — the
// server's dedicated "recent" route — NOT /api/v1/manifest (that is the
// paginated ManifestGlobal, a different endpoint). Python's recent() already
// uses /api/v1/recent; Go and TS were pointed at /manifest and were realigned
// here. Only `limit` is sent (no type/offset). The dual-shape parse below is
// unchanged: ListRecent emits the envelope {"records":[],"count":N}, and we
// also accept a bare array for forward compatibility.
//
// Junior Tip [name parity 2026-06-18]: this is the canonical name for the MCP
// `recent_memories` tool, aligned across Go/Python/TS. It was renamed from
// RecentMemories; the old name survives as a Deprecated alias below — many
// AnhurAgents binaries (validator, file_ingestor, recovery, pkg/db) still call
// the old name, so the alias MUST stay until they migrate.
func (m *Memory) Recent(ctx context.Context, limit int, opts ...ReadOption) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))

	respBytes, err := m.conn.Get(ctx, "/api/v1/recent", params, cfg.minIndex)
	if err != nil {
		return nil, err
	}

	// The manifest endpoint may return either of two shapes:
	//   1. An envelope object: {"records": [...], "count": N, ...}
	//   2. A bare JSON array:  [...]
	//
	// Junior Tip [Bug — empty-manifest crash]: the previous code decided which
	// shape it had by checking len(wrapped.Records) > 0. That is WRONG for an
	// EMPTY manifest, which the live server returns as {"records": []} (an
	// OBJECT). With len == 0 the code fell through to json.Unmarshal(bytes,
	// &[]Record) and crashed with "cannot unmarshal object into Go value of
	// type []models.Record". We now branch on the actual JSON kind (first
	// non-whitespace byte) so an empty envelope correctly yields an empty
	// slice instead of an error.
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
		// Junior Tip [recent full-record parity, 2026-07-03]: wrapped.Records is now
		// []models.Record (full), so return it directly — no subset mapping that used to
		// drop weight/score/related_ids/content/valid_*/superseded_by. Guard a nil slice
		// (absent "records" key) into a non-nil empty slice to keep the historical contract.
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
// many AnhurAgents call sites (validator, file_ingestor, pkg/recovery, pkg/db)
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
// Junior Tip [codepoint-safe parity, 2026-06-18]: a byte slice text[:200] can
// split a multibyte UTF-8 rune and emit an invalid summary for non-ASCII content.
// Slicing []rune keeps the cut on a code-point boundary, so the three SDKs
// (Python str[:200], TS Array.from, Go runes) truncate at the SAME point and
// never corrupt a rune.
func truncateSummary(text string) string {
	const maxSummaryRunes = 200
	runes := []rune(text)
	if len(runes) <= maxSummaryRunes {
		return text
	}
	return string(runes[:maxSummaryRunes]) + "..."
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
func (m *Memory) BatchReadContent(ctx context.Context, ids []int64, opts ...ReadOption) (map[string]json.RawMessage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	payload := map[string]interface{}{"ids": ids}
	// Read behind POST — PostRead carries the optional X-Anhur-Min-Index barrier.
	respBytes, err := m.conn.PostRead(ctx, "/api/v1/records/batch-content", payload, cfg.minIndex)
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
func (m *Memory) UploadStatus(ctx context.Context, uploadID int64, opts ...ReadOption) (*UploadStatusResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	path := fmt.Sprintf("/api/v1/upload/%d/status", uploadID)
	respBytes, err := m.conn.Get(ctx, path, nil, cfg.minIndex)
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
// Junior Tip [why a dedicated method, not SearchEntities("")]: SearchEntities
// runs a LIKE filter that the server escapes — semantically it is a "find by
// name" call. Even though LIKE %% would match every entity, that contract
// could change (e.g. minimum query length added) and silently break the
// caller. ListEntities has a hard guarantee: deterministic full-set walk.
func (m *Memory) ListEntities(ctx context.Context, limit, offset int, opts ...ReadOption) (*EntitiesPage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

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

	respBytes, getErr := m.conn.Get(ctx, "/api/v1/entities/list", params, cfg.minIndex)
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

	cfg := applyReadOptions(opts)

	params := url.Values{}
	if query != "" {
		params.Set("query", query)
	}
	if entityType != "" {
		params.Set("type", entityType)
	}
	params.Set("limit", strconv.Itoa(limit))

	respBytes, err := m.conn.Get(ctx, "/api/v1/entities", params, cfg.minIndex)
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

	cfg := applyReadOptions(opts)

	params := url.Values{}
	params.Set("depth", strconv.Itoa(depth))

	path := fmt.Sprintf("/api/v1/entities/%d/graph", entityID)
	respBytes, err := m.conn.Get(ctx, path, params, cfg.minIndex)
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

	cfg := applyReadOptions(opts)

	path := fmt.Sprintf("/api/v1/entities/%d/timeline", entityID)
	respBytes, err := m.conn.Get(ctx, path, nil, cfg.minIndex)
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

	cfg := applyReadOptions(opts)

	path := fmt.Sprintf("/api/v1/records/%d/entities", recordID)
	respBytes, err := m.conn.Get(ctx, path, nil, cfg.minIndex)
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

	cfg := applyReadOptions(opts)

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	params.Set("offset", strconv.Itoa(offset))

	path := fmt.Sprintf("/api/v1/sessions/%s/history", sessionUUID)
	return m.conn.Get(ctx, path, params, cfg.minIndex)
}

// GetSessionClusters returns thematic clusters within a session.
//
// Uses BSQ vectors and DBSCAN to identify topic groups among the
// session's records.
func (m *Memory) GetSessionClusters(ctx context.Context, sessionUUID string, opts ...ReadOption) ([]byte, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	path := fmt.Sprintf("/api/v1/sessions/%s/clusters", sessionUUID)
	return m.conn.Get(ctx, path, nil, cfg.minIndex)
}

// --------------------------------------------------------------------------
// Session management
// --------------------------------------------------------------------------

// NewSession rotates the session UUID (generates a fresh random suffix).
// All subsequent Add() calls will be grouped under the new session.
//
// Junior Tip [SDK parity 2026-07-03]: this shares deriveSessionUUID with
// NewMemory so a rotated session has the exact same
// <container_tag>-<UTC timestamp>-<6 hex> shape as the initial one — mirroring
// the Python SDK's new_session, which reuses the same _utc_timestamp helper.
func (m *Memory) NewSession() {
	m.sessionUUID = deriveSessionUUID(m.containerTag)
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
