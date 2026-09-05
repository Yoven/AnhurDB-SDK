package client

import (
	"fmt"
	"encoding/json"
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
	// writeMode selects ingest (POST /api/v1/ingest) or regular (POST
	// /api/v1/records). Empty means ingest — the historical default.
	writeMode string
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

// WithMode selects the write path for Memory.Add. Valid values are "ingest"
// (POST /api/v1/ingest — default) and "regular" (POST /api/v1/records as
// episodic). Callers must register the session via CreateSession before
// either path succeeds on session-first servers.
//
func WithMode(writeMode string) AddOption {
	return func(cfg *addConfig) {
		cfg.writeMode = writeMode
	}
}

// CreateSessionOption configures Memory.CreateSession.
//
type CreateSessionOption func(*createSessionConfig)

// createSessionConfig holds optional overrides for POST /api/v1/sessions.
//
type createSessionConfig struct {
	sessionID string
	metadata  map[string]interface{}
}

// WithCreateSessionID registers an explicit session uuid. When omitted,
// CreateSession leaves session_id out of the body so the server generates one
// (parity with Python create_session / TypeScript createSession / MCP).
// To register a local id: NewSession() then CreateSession(WithCreateSessionID(...))
// or OpenSession().
//
func WithCreateSessionID(sessionID string) CreateSessionOption {
	return func(cfg *createSessionConfig) {
		cfg.sessionID = sessionID
	}
}

// WithCreateSessionMetadata attaches optional session-level metadata copied
// onto every record written in this session.
//
func WithCreateSessionMetadata(metadata map[string]interface{}) CreateSessionOption {
	return func(cfg *createSessionConfig) {
		cfg.metadata = metadata
	}
}

// UploadOption configures Memory.UploadFile (shared-plane mode).
type UploadOption func(*uploadConfig)

type uploadConfig struct {
	mode string
}

// WithUploadMode selects the upload plane: "tenant_shared" or "client_shared".
// Chat mode is inferred when sessionID is non-empty (linkedEpisodicID required).
func WithUploadMode(uploadMode string) UploadOption {
	return func(cfg *uploadConfig) {
		cfg.mode = uploadMode
	}
}

// createSessionResponse is the wire format returned by POST /api/v1/sessions.
//
type createSessionResponse struct {
	SessionID string          `json:"session_id"`
	Metadata  json.RawMessage `json:"metadata"`
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
	// Pagination carries {"limit":int,"offset":int}. limit defaults to 50 and is
	// hard-capped at 1000 server-side; offset defaults to 0.
	//
	// Junior Tip [o doc dizia "must be >= 0" e o servidor nao recusava]: um
	// offset negativo era SILENCIOSAMENTE trocado por 0 (record_ast_query.go),
	// entao "must be" descrevia uma regra que ninguem aplicava. Agora Validate()
	// a aplica no cliente, igual a Python e TypeScript.
	Pagination map[string]int `json:"pagination,omitempty"`

	// buildErrors coleta problemas detectados pelos metodos fluentes, que nao
	// podem devolver erro sem quebrar o encadeamento. Validate() os entrega.
	buildErrors []error
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
	// Junior Tip [checagem aqui E em Validate(), de proposito]: aqui ela aponta
	// a LINHA da cadeia que errou, o que e o que o desenvolvedor precisa; em
	// Validate() ela cobre quem monta o QueryRequest como struct literal, sem
	// passar por Where(). Python e TypeScript tem as duas pelo mesmo motivo.
	if !astAllowedFilterColumns[field] {
		request.buildErrors = append(request.buildErrors,
			fmt.Errorf("query: field %q is not allowed in filters — allowed: %s", field, sortedAllowedColumns()))
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
	if limit < 1 || limit > astQueryLimitMax {
		request.buildErrors = append(request.buildErrors,
			fmt.Errorf("query: limit must be between 1 and %d, got %d", astQueryLimitMax, limit))
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
	if offset < 0 {
		request.buildErrors = append(request.buildErrors,
			fmt.Errorf("query: offset cannot be negative, got %d", offset))
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
