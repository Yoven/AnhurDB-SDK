package client

import (
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/models"
)

// --------------------------------------------------------------------------
// Core response types — used by Add, Search, Profile
// --------------------------------------------------------------------------

// AddResult is returned by Memory.Add(). It contains the created
// record(s) and whether cloud ingest or OSS fallback was used.
type AddResult struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id,omitempty"`
	Records   []RecordSummary `json:"records"`
	Status    string          `json:"status"`
	Mode      string          `json:"mode"` // "cloud" or "oss"
}

// RecordSummary is a lightweight descriptor of a created record.
type RecordSummary struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// SearchResult represents a single search hit from the server: the COMPLETE
// record nested under "record" plus its relevance score under "similarity".
//
type SearchResult struct {
	Record     models.Record `json:"record"`
	Similarity float64       `json:"similarity"`
}

// ProfileResult contains the memory profile for a container tag.
type ProfileResult struct {
	Static  map[string]interface{} `json:"static"`
	Dynamic map[string]interface{} `json:"dynamic"`
	Stats   map[string]interface{} `json:"stats"`
	Tag     string                 `json:"tag,omitempty"`
	Status  string                 `json:"status,omitempty"`
}

// --------------------------------------------------------------------------
// Graph traversal types
// --------------------------------------------------------------------------

// WalkResult contains the graph traversal output from the walk endpoint.
type WalkResult struct {
	StartID int64      `json:"start_id"`
	Depth   int        `json:"depth"`
	Nodes   []WalkNode `json:"nodes"`
	Edges   []WalkEdge `json:"edges"`
}

// WalkNode is a single node in a graph walk result.
type WalkNode struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// WalkEdge is a single edge connecting two nodes in a graph walk.
// Wire shape matches the REST contract: {"source","target"}.
type WalkEdge struct {
	Source int64 `json:"source"`
	Target int64 `json:"target"`
}

// --------------------------------------------------------------------------
// Topology / context types
// --------------------------------------------------------------------------

// ContextResult contains the topological context around a record.
// Wire shape matches GET /api/v1/records/{id}/topology: {"target","neighbors"}.
type ContextResult struct {
	Target    models.Record   `json:"target"`
	Neighbors []models.Record `json:"neighbors"`
}

// --------------------------------------------------------------------------
// Session types
// --------------------------------------------------------------------------

// SessionStats describes aggregate information about a session.
type SessionStats struct {
	UUID         string         `json:"uuid"`
	RecordCount  int            `json:"record_count"`
	Types        map[string]int `json:"types"`
	LastActivity string         `json:"last_activity"`
	Summary      *string        `json:"summary,omitempty"`
}

// sessionsWrapper is the server's response envelope for ListSessions.
// The server returns {"sessions": [...]} rather than a bare array.
type sessionsWrapper struct {
	Sessions []SessionStats `json:"sessions"`
}

// --------------------------------------------------------------------------
// Entity Knowledge Graph types (Layer 2)
// --------------------------------------------------------------------------

// Entity is a Layer-2 named node (person, organisation, concept, location, …).
//
// Junior Tip [wire key 2026-07-16]: AnhurDB entityToResponse emits entity_type
// (not type). type collides with record.type mentally and silently decoded as "".
type Entity struct {
	ID           int64                  `json:"id"`
	Name         string                 `json:"name"`
	EntityType   string                 `json:"entity_type"`
	Summary      string                 `json:"summary,omitempty"`
	Attributes   map[string]interface{} `json:"attributes,omitempty"`
	Dimension    int                    `json:"dimension,omitempty"`
	FirstSeen    string                 `json:"first_seen,omitempty"`
	LastSeen     string                 `json:"last_seen,omitempty"`
	MentionCount int                    `json:"mention_count,omitempty"`
	Weight       float64                `json:"weight,omitempty"`
}

// EntityResult is returned by UpsertEntity — created/updated entity identity.
type EntityResult struct {
	ID         int64  `json:"id"`
	Name       string `json:"name,omitempty"`
	EntityType string `json:"entity_type,omitempty"`
}

// EntityGraphResult contains the BFS traversal output from the entity
// graph endpoint. Nodes are entities, edges are typed relationships.
type EntityGraphResult struct {
	EntityID  int64             `json:"entity_id"`
	Depth     int               `json:"depth"`
	Nodes     []EntityGraphNode `json:"nodes"`
	NodeCount int               `json:"node_count"`
}

// EntityGraphNode is a single entity node in a graph traversal result.
type EntityGraphNode struct {
	Entity Entity       `json:"entity"`
	Edges  []EntityEdge `json:"edges,omitempty"`
}

// EntityEdge is a typed, temporal relationship between two entities.
type EntityEdge struct {
	SourceID       int64    `json:"source_id"`
	TargetID       int64    `json:"target_id"`
	Relation       string   `json:"relation"`
	EventTime      string   `json:"event_time,omitempty"`
	ValidUntil     string   `json:"valid_until,omitempty"`
	Confidence     *float64 `json:"confidence,omitempty"`
	SourceRecordID *int64   `json:"source_record_id,omitempty"`
}

// EntityTimelineResult contains the full temporal history of an entity's
// relationships, including invalidated edges ordered by event time.
type EntityTimelineResult struct {
	Entity    Entity       `json:"entity"`
	Timeline  []EntityEdge `json:"timeline"`
	RecordIDs []int64      `json:"record_ids,omitempty"`
}

// EntityEdgeOption configures optional fields on UpsertEntityEdge.
type EntityEdgeOption func(*entityEdgeConfig)

// entityEdgeConfig holds optional parameters for entity edge creation.
type entityEdgeConfig struct {
	eventTime      string
	confidence     float64
	sourceRecordID int64
}

// WithEventTime sets the ISO 8601 timestamp when the relationship became true.
func WithEventTime(t string) EntityEdgeOption {
	return func(cfg *entityEdgeConfig) {
		cfg.eventTime = t
	}
}

// WithConfidence sets the confidence score (0.0-1.0) for the relationship.
func WithConfidence(c float64) EntityEdgeOption {
	return func(cfg *entityEdgeConfig) {
		cfg.confidence = c
	}
}

// WithSourceRecordID links the entity edge to the memory record that
// evidences the relationship.
func WithSourceRecordID(id int64) EntityEdgeOption {
	return func(cfg *entityEdgeConfig) {
		cfg.sourceRecordID = id
	}
}

// entitiesResponse is the server's envelope for entity list endpoints.
type entitiesResponse struct {
	Entities []Entity `json:"entities"`
}

// EntitiesPage carries the paginated /api/v1/entities/list response: a slice
// of entities for the requested page plus cursor metadata for the next call.
//
type EntitiesPage struct {
	Entities   []Entity `json:"entities"`
	Count      int      `json:"count"`
	Total      int64    `json:"total"`
	Limit      int      `json:"limit"`
	Offset     int      `json:"offset"`
	HasMore    bool     `json:"has_more"`
	NextOffset int      `json:"next_offset"`
}

// --------------------------------------------------------------------------
// File upload types
// --------------------------------------------------------------------------

// UploadResult is returned by UploadFile — contains the upload ID
// for status polling. Server returns ``record_id`` (preferred).
type UploadResult struct {
	RecordID int64  `json:"record_id"`
	ID       int64  `json:"id"`
	Status   string `json:"status,omitempty"`
	Filename string `json:"filename,omitempty"`
	UUID     string `json:"uuid,omitempty"`
}

// UploadID returns the server record id used for UploadStatus polling.
func (uploadResult UploadResult) UploadID() int64 {
	if uploadResult.RecordID != 0 {
		return uploadResult.RecordID
	}
	return uploadResult.ID
}

// UploadStatusResult describes the processing status of a file upload.
type UploadStatusResult struct {
	RecordID  int64  `json:"record_id"`
	ID        int64  `json:"id"`
	Status    string `json:"status"` // "processing", "completed", "failed", "saved"
	Completed bool   `json:"completed"`
	Filename  string `json:"filename,omitempty"`
	Error     string `json:"error,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Metadata  string `json:"metadata,omitempty"`
}

// --------------------------------------------------------------------------
// Constructor options
// --------------------------------------------------------------------------

// Option configures the Memory client at construction time.
// Functional options let us add new config knobs without breaking
// existing callers.
type Option func(*memoryConfig)

// memoryConfig holds all constructor parameters for Memory.
type memoryConfig struct {
	url      string
	userID   string
	tenantID string
	timeout  time.Duration
}

// WithURL sets the AnhurDB server URL (default: https://anhurdb.yoven.ai).
func WithURL(url string) Option {
	return func(cfg *memoryConfig) {
		cfg.url = url
	}
}

// WithUserID sets an explicit user identifier used as the container_tag.
// If not set, the SDK derives a stable tag from the API key hash.
func WithUserID(id string) Option {
	return func(cfg *memoryConfig) {
		cfg.userID = id
	}
}

// WithTenantID sets the tenant ID header for multi-tenant deployments.
func WithTenantID(id string) Option {
	return func(cfg *memoryConfig) {
		cfg.tenantID = id
	}
}

// WithTimeout sets the HTTP client timeout (default: 30s).
func WithTimeout(d time.Duration) Option {
	return func(cfg *memoryConfig) {
		cfg.timeout = d
	}
}

// AddOption configures a single Memory.Add call.
//
type AddOption func(*addConfig)

// addConfig holds the per-call overrides for Memory.Add. A nil pointer field
// means "caller did not specify — use the server/SDK default", which is how we
// keep score/type from being silently forced when the caller omits them.
//
type addConfig struct {
	score     *int
	memType   *string
	metadata  map[string]interface{}
	sessionID string
}

// WithScore sets the salience score (typically 0-10) on the record being added.
// When omitted, the SDK falls back to the historical default of 5.
func WithScore(score int) AddOption {
	return func(cfg *addConfig) {
		cfg.score = &score
	}
}

// WithType sets the memory type (e.g. "episodic", "semantic", "procedural").
// When omitted, the SDK falls back to the historical default of "episodic".
//
func WithType(memType string) AddOption {
	return func(cfg *addConfig) {
		cfg.memType = &memType
	}
}

// WithMetadata merges caller-supplied keys into the record metadata. The SDK
// always sets container_tag; caller keys are layered on top (caller wins on a
// key collision, except container_tag which the SDK owns).
//
func WithMetadata(metadata map[string]interface{}) AddOption {
	return func(cfg *addConfig) {
		cfg.metadata = metadata
	}
}

// WithSessionID pins the SESSION (uuid) the ingested record lands in. The tenant
// comes from the API key; the session is the caller's own unit of conversation.
//
func WithSessionID(sessionID string) AddOption {
	return func(cfg *addConfig) {
		cfg.sessionID = sessionID
	}
}

// ReadOption configures a single read call. It is the idiomatic Go surface
// for search shaping and bi-temporal filters, and is the SAME type used by
// Search (so WithLimit / WithTypeFilter all compose on a read).
//
type ReadOption func(*searchConfig)

// SearchOption is retained as an exported alias of ReadOption for backward
// compatibility — existing code using client.SearchOption / WithLimit /
// WithTypeFilter is unaffected.
type SearchOption = ReadOption

// searchConfig holds parameters for a read request: search shaping (limit,
// typeFilter), the optional bi-temporal window (asOf/since/until), and
// goal-directed semantic-walk parameters.
type searchConfig struct {
	limit      int
	typeFilter string
	// scope is the search plane for POST /api/v1/search (sessions,
	// tenant_shared, client_shared, shared_all). Empty means the Search
	// method applies its own default ("sessions").
	scope string
	// keyword is an optional free-text filter (query param "q") honoured by
	// SearchByType. Empty means omit.
	keyword string
	// asOf / since / until are optional RFC3339 UTC bi-temporal filters honoured
	// by the manifest reads. asOf is mutually exclusive with since/until — the
	// server returns HTTP 400 on a violation, which the SDK surfaces verbatim.
	asOf  string
	since string
	until string
	// Goal-directed semantic-walk parameters (2026-07-03). These are honoured
	// ONLY by WalkSemantic; every other read method ignores them — the exact
	// same shared-ReadOption pattern already used by asOf/since/until (fields
	// meaningful only to the manifest reads). The zero value of each field means
	// "caller did not set it": walkTarget "" and walkMaxCost 0 both omit their
	// JSON key so the server falls through to its plain-Dijkstra default,
	// preserving the historical behaviour of an option-free WalkSemantic call.
	//
	walkTarget     string
	walkGoalVector []byte
	walkTargetTag  string
	walkMaxCost    float64
}

// applyReadOptions folds a variadic ReadOption slice into a searchConfig.
// Centralised so every read method resolves options identically. The default
// limit is 0 here (read methods that need a different default set it before
// applying options — e.g. Search seeds limit=10).
func applyReadOptions(opts []ReadOption) searchConfig {
	cfg := searchConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// WithLimit sets the maximum number of search results.
func WithLimit(n int) SearchOption {
	return func(cfg *searchConfig) {
		cfg.limit = n
	}
}

// WithTypeFilter restricts search results to a specific memory type.
func WithTypeFilter(t string) SearchOption {
	return func(cfg *searchConfig) {
		cfg.typeFilter = t
	}
}

// WithScope selects the search plane for POST /api/v1/search.
func WithScope(scope string) SearchOption {
	return func(cfg *searchConfig) {
		cfg.scope = scope
	}
}

// WithKeyword sets an optional free-text filter (query param "q"), honoured by
// SearchByType. Empty string is a no-op.
func WithKeyword(keyword string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.keyword = keyword
	}
}

// WithAsOf scopes a temporal-aware read (the manifests) to a bi-temporal
// snapshot instant — an RFC3339 UTC timestamp, e.g. "2026-03-15T12:00:00Z".
// Mutually exclusive with WithSince/WithUntil; the server rejects the
// combination with HTTP 400, which the read method surfaces.
func WithAsOf(asOf string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.asOf = asOf
	}
}

// WithSince scopes a temporal-aware read to records whose created_at is >= the
// supplied RFC3339 UTC lower bound. Combine with WithUntil for a window.
func WithSince(since string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.since = since
	}
}

// WithUntil scopes a temporal-aware read to records whose created_at is <= the
// supplied RFC3339 UTC upper bound. Combine with WithSince for a window.
func WithUntil(until string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.until = until
	}
}

// --------------------------------------------------------------------------
// Goal-directed WalkSemantic options (2026-07-03)
//
// These four options steer Memory.WalkSemantic from a plain cost-first
// Dijkstra into a goal-directed traversal that is pulled toward a target. They
// are ReadOption values (the repo's one shared read-option type), so a caller
// can still compose WithTarget / WithGoalVector on the same call. Only
// WalkSemantic reads them; passing them to any other read is an inert no-op,
// mirroring how WithAsOf/WithSince/WithUntil are honoured only by the manifest
// reads. Calling WalkSemantic with none of them is byte-for-byte the previous
// behaviour (Dijkstra, server defaults max_cost=2.0 / max_nodes=50).
//
// --------------------------------------------------------------------------

// WithTarget selects the goal-directed heuristic that steers a WalkSemantic
// traversal. Accepted values match the locked REST contract:
//
//   - "semantic" — pull toward a caller-supplied guide embedding (pair with
//     WithGoalVector; the server returns HTTP 400 if the vector is missing or
//     not valid base64).
//   - "tag"      — pull toward records carrying an entity/tag name (pair with
//     WithTargetTag; the server returns HTTP 400 if the tag is empty).
//   - "recency"  — pull toward the newest records (no companion option needed).
//
// Omitting this option (or passing "" / "dijkstra") leaves the walk as a plain
// Dijkstra over 1−similarity edge cost, exactly as before.
func WithTarget(target string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.walkTarget = target
	}
}

// WithGoalVector supplies the raw guide embedding (float bytes) that a
// target="semantic" walk is pulled toward. The SDK base64-encodes it into the
// request body's "vector" field, so callers pass the bytes verbatim and never
// touch base64 themselves. Has no effect unless WithTarget("semantic") is set.
func WithGoalVector(goalVector []byte) ReadOption {
	return func(cfg *searchConfig) {
		cfg.walkGoalVector = goalVector
	}
}

// WithTargetTag supplies the entity/tag name that a target="tag" walk is pulled
// toward. It maps to the request body's "target_tag" field. Has no effect
// unless WithTarget("tag") is set.
func WithTargetTag(targetTag string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.walkTargetTag = targetTag
	}
}

// WithMaxCost overrides the semantic-walk cost budget (the request body's
// "max_cost"). Larger budgets explore further from the seed. A value <= 0 is
// treated as "unset": the key is omitted and the server applies its default of
// 2.0, so this option is safe to thread through unconditionally.
func WithMaxCost(maxCost float64) ReadOption {
	return func(cfg *searchConfig) {
		cfg.walkMaxCost = maxCost
	}
}

// --------------------------------------------------------------------------
// Internal wire-format types (not exported)
// --------------------------------------------------------------------------

// ingestResponse is the wire format returned by POST /api/v1/ingest.
type ingestResponse struct {
	SessionID string          `json:"session_id"`
	Records   []RecordSummary `json:"records"`
	ID        int64           `json:"id"`
}

// recordCreateResponse is the wire format returned by POST /api/v1/records.
type recordCreateResponse struct {
	ID int64 `json:"id"`
}

// searchResponse is the wire format returned by POST /api/v1/search:
// {"results":[{"record":{...},"similarity":...}, ...]}.
//
type searchResponse struct {
	Results []SearchResult `json:"results"`
}

// manifestResponse is the object envelope for GET /api/v1/recent
// ({"records":[...],"count":N}).
//
type manifestResponse struct {
	Records []models.Record `json:"records"`
}

// --------------------------------------------------------------------------
// Parity (2026-06-18) — Create options
// --------------------------------------------------------------------------

// CreateOption configures a single Memory.Create call. It is the full-fidelity
// counterpart to AddOption: Create always POSTs to /api/v1/records (no ingest
// worker override), so every option below is written to the record verbatim.
//
type CreateOption func(*createConfig)

// createConfig holds the per-call overrides for Memory.Create. Pointer fields
// give the same nil/set-to-zero/set three-state as addConfig: score 0 and ""
// type/status are LEGAL explicit values, so the zero value cannot double as the
// "unset" sentinel.
type createConfig struct {
	memType    *string
	score      *int
	status     *string
	relatedIDs []int64
	metadata   map[string]interface{}
	// validFrom is an RFC3339 UTC instant folded into the metadata envelope —
	// REST create reads valid_from from metadata only.
	// "" means "not supplied".
	validFrom string
}

// WithCreateType sets the record type (e.g. "fact","semantic","decision").
// Defaults to "episodic" when omitted.
func WithCreateType(memType string) CreateOption {
	return func(cfg *createConfig) {
		cfg.memType = &memType
	}
}

// WithCreateScore sets the salience score (typically 0-10). Defaults to 5.
func WithCreateScore(score int) CreateOption {
	return func(cfg *createConfig) {
		cfg.score = &score
	}
}

// WithCreateStatus sets the lifecycle status (e.g. "saved","processing").
// Defaults to "saved".
func WithCreateStatus(status string) CreateOption {
	return func(cfg *createConfig) {
		cfg.status = &status
	}
}

// WithCreateRelatedIDs sets the related_ids horizontal-edge array. The server
// still enforces graph topology on top of these (see service.enforceGraphTopology).
func WithCreateRelatedIDs(relatedIDs []int64) CreateOption {
	return func(cfg *createConfig) {
		cfg.relatedIDs = relatedIDs
	}
}

// WithCreateMetadata merges caller-supplied keys into the record metadata. The
// SDK always sets container_tag (it wins on a collision); caller keys are
// layered on top, identical to Add's WithMetadata.
func WithCreateMetadata(metadata map[string]interface{}) CreateOption {
	return func(cfg *createConfig) {
		cfg.metadata = metadata
	}
}

// WithCreateValidFrom sets the bi-temporal valid_from instant (RFC3339 UTC) for
// the new record. It is delivered inside the metadata JSON; the REST create
// route reads valid_from from metadata only.
func WithCreateValidFrom(validFrom string) CreateOption {
	return func(cfg *createConfig) {
		cfg.validFrom = validFrom
	}
}

// --------------------------------------------------------------------------
// Parity (2026-06-18) — Query AST (POST /api/v1/query)
// --------------------------------------------------------------------------

// QueryRequest is the structured AST sent to POST /api/v1/query. It mirrors the
// filter/sort/pagination grammar over the record columns.
//
// Build it directly, or fluently via NewQuery().Where(...).OrderBy(...).Limit(...).
type QueryRequest struct {
	// Select lists columns to return. The server parses this field but does not
	// project columns; the full record is always returned.
	// the server — the SELECT list is fixed and the FULL record always returns.
	// Kept for forward-compat / parity with Python's QueryBuilder.
	Select []string `json:"select,omitempty"`
	// Filters maps a whitelisted column name to an operator object. An invalid
	// column name yields HTTP 400 "invalid filter field" from the server.
	Filters map[string]QueryOp `json:"filters,omitempty"`
	// Sort is an ordered list of {field, order} maps. order is "asc"/"desc"
	// (anything else falls back to DESC server-side); an invalid field yields
	// HTTP 400 "invalid sort field". Default when omitted: ORDER BY id DESC.
	Sort []map[string]string `json:"sort,omitempty"`
	// Pagination carries {"limit":int,"offset":int}. limit defaults to 50,
	// hard-capped at 1000; offset defaults to 0 and must be >= 0.
	Pagination map[string]int `json:"pagination,omitempty"`
}

// QueryOp is a per-column operator object for QueryRequest.Filters. Each field
// maps to one of the server-supported operators; set only the ones you need —
// the omitempty tags ensure unset operators never reach the wire.
//
type QueryOp struct {
	Eq  interface{}   `json:"$eq,omitempty"`
	Gt  interface{}   `json:"$gt,omitempty"`
	Gte interface{}   `json:"$gte,omitempty"`
	Lt  interface{}   `json:"$lt,omitempty"`
	Lte interface{}   `json:"$lte,omitempty"`
	In  []interface{} `json:"$in,omitempty"`
}

// NewQuery starts a fluent QueryRequest builder. The zero builder is a valid
// "match everything (server defaults)" query.
//
//	req := client.NewQuery().
//	    Where("type", client.QueryOp{Eq: "fact"}).
//	    OrderBy("created_at", "desc").
//	    Limit(20)
func NewQuery() *QueryRequest {
	return &QueryRequest{}
}

// Where adds (or replaces) the operator object for a column. Returns the
// receiver so calls chain.
func (request *QueryRequest) Where(field string, operator QueryOp) *QueryRequest {
	if request.Filters == nil {
		request.Filters = map[string]QueryOp{}
	}
	request.Filters[field] = operator
	return request
}

// OrderBy appends a sort clause ({field, order}). order should be "asc" or
// "desc"; an unrecognised value falls back to DESC server-side. Returns the
// receiver so calls chain.
func (request *QueryRequest) OrderBy(field, order string) *QueryRequest {
	request.Sort = append(request.Sort, map[string]string{"field": field, "order": order})
	return request
}

// Limit sets the pagination limit (server default 50, hard cap 1000). Returns
// the receiver so calls chain.
func (request *QueryRequest) Limit(limit int) *QueryRequest {
	if request.Pagination == nil {
		request.Pagination = map[string]int{}
	}
	request.Pagination["limit"] = limit
	return request
}

// Offset sets the pagination offset (server default 0, must be >= 0). Returns
// the receiver so calls chain.
func (request *QueryRequest) Offset(offset int) *QueryRequest {
	if request.Pagination == nil {
		request.Pagination = map[string]int{}
	}
	request.Pagination["offset"] = offset
	return request
}

// queryResponse is the wire format for POST /api/v1/query and the
// {"records":[Record],"count":int} envelope shared by ListChat.
type queryResponse struct {
	Records []models.Record `json:"records"`
	Count   int             `json:"count"`
}

// --------------------------------------------------------------------------
// Parity (2026-06-18) — Manifest pagination envelope
// --------------------------------------------------------------------------

// ManifestPage is the paginated envelope returned by ManifestGlobal and
// ManifestSession (GET /api/v1/manifest and /api/v1/chats/{uuid}/manifest):
// a page of records plus the server's pagination cursor.
//
type ManifestPage struct {
	Records []models.Record `json:"records"`
	Count   int             `json:"count"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
	HasMore bool            `json:"has_more"`
}

// --------------------------------------------------------------------------
// Parity (2026-06-18) — Grounding (GET /api/v1/records/{id}/grounding)
// --------------------------------------------------------------------------

// GroundingResult is the provenance traversal returned by GetGrounding. It
// the target record plus the episodic anchors and consolidations reachable
// within the BFS depth budget.
type GroundingResult struct {
	Target               GroundingTarget          `json:"target"`
	Anchors              []GroundingAnchor        `json:"anchors"`
	Consolidations       []GroundingConsolidation `json:"consolidations"`
	DepthUsed            int                      `json:"depth_used"`
	MaxDepth             int                      `json:"max_depth"`
	FoundCount           int                      `json:"found_count"`
	AnchorsCapped        bool                     `json:"anchors_capped,omitempty"`
	ConsolidationsCapped bool                     `json:"consolidations_capped,omitempty"`
}

// GroundingTarget is the record the grounding traversal started from.
type GroundingTarget struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	UUID    string `json:"uuid"`
}

// GroundingAnchor is an episodic anchor reachable from the target. Content holds
// the whitelisted excerpt keys ("user"/"assistant"/"full_text") and is nil when
// the .gz body is missing or unparseable.
type GroundingAnchor struct {
	ID              int64             `json:"id"`
	Type            string            `json:"type"`
	UUID            string            `json:"uuid"`
	Summary         string            `json:"summary"`
	Content         map[string]string `json:"content,omitempty"`
	HopsFromTarget  int               `json:"hops_from_target"`
	SessionPosition int64             `json:"session_position,omitempty"`
}

// GroundingConsolidation is a consolidated-star node reachable from the target.
type GroundingConsolidation struct {
	ID             int64  `json:"id"`
	UUID           string `json:"uuid"`
	Summary        string `json:"summary"`
	HopsFromTarget int    `json:"hops_from_target"`
}
