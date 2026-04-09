package client

import "time"

// --------------------------------------------------------------------------
// Core response types — used by Add, Search, Profile
// --------------------------------------------------------------------------

// AddResult is returned by Memory.Add(). It contains the created
// record(s) and whether cloud ingest or OSS fallback was used.
type AddResult struct {
	ID      int64           `json:"id"`
	Records []RecordSummary `json:"records"`
	Status  string          `json:"status"`
	Mode    string          `json:"mode"` // "cloud" or "oss"
}

// RecordSummary is a lightweight descriptor of a created record.
type RecordSummary struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// SearchResult represents a single search hit from the server.
type SearchResult struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`
	Summary    string  `json:"summary"`
	Similarity float64 `json:"similarity"`
	Metadata   string  `json:"metadata,omitempty"`
	Content    string  `json:"content,omitempty"`
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
type WalkEdge struct {
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Relation string `json:"relation"`
}

// --------------------------------------------------------------------------
// Topology / context types
// --------------------------------------------------------------------------

// ContextResult contains the topological context around a record.
type ContextResult struct {
	RecordID int64       `json:"record_id"`
	Parents  []WalkNode  `json:"parents"`
	Children []WalkNode  `json:"children"`
	Siblings []WalkNode  `json:"siblings"`
	Raw      interface{} `json:"raw,omitempty"`
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

// Entity represents a named entity in the knowledge graph
// (person, organisation, concept, location, etc.).
type Entity struct {
	ID         int64                  `json:"id"`
	Name       string                 `json:"name"`
	EntityType string                 `json:"type"`
	Summary    string                 `json:"summary,omitempty"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`
	CreatedAt  *time.Time             `json:"created_at,omitempty"`
	UpdatedAt  *time.Time             `json:"updated_at,omitempty"`
}

// EntityResult is returned by UpsertEntity — contains the created/updated
// entity's ID.
type EntityResult struct {
	ID      int64  `json:"id"`
	Name    string `json:"name,omitempty"`
	Status  string `json:"status,omitempty"`
}

// EntityGraphResult contains the BFS traversal output from the entity
// graph endpoint. Nodes are entities, edges are typed relationships.
type EntityGraphResult struct {
	EntityID  int64              `json:"entity_id"`
	Depth     int                `json:"depth"`
	Nodes     []EntityGraphNode  `json:"nodes"`
	NodeCount int                `json:"node_count"`
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
	Entity    Entity         `json:"entity"`
	Timeline  []EntityEdge   `json:"timeline"`
	RecordIDs []int64        `json:"record_ids,omitempty"`
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

// --------------------------------------------------------------------------
// File upload types
// --------------------------------------------------------------------------

// UploadResult is returned by UploadFile — contains the upload ID
// for status polling.
type UploadResult struct {
	ID     int64  `json:"id"`
	Status string `json:"status,omitempty"`
}

// UploadStatusResult describes the processing status of a file upload.
type UploadStatusResult struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`    // "processing", "completed", "failed"
	Filename  string `json:"filename,omitempty"`
	Error     string `json:"error,omitempty"`
	RecordIDs []int64 `json:"record_ids,omitempty"`
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

// WithURL sets the AnhurDB server URL (default: https://api.anhurdb.com).
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

// SearchOption configures a search call.
type SearchOption func(*searchConfig)

// searchConfig holds parameters for a search request.
type searchConfig struct {
	limit      int
	typeFilter string
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

// searchResponse is the wire format returned by POST /api/v1/search/global.
type searchResponse struct {
	Results []searchHit `json:"results"`
}

// searchHit is a single element inside searchResponse.Results.
type searchHit struct {
	Record     searchRecord `json:"record"`
	Similarity float64      `json:"similarity"`
}

// searchRecord is the record sub-object inside a search hit.
type searchRecord struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Summary  string `json:"summary"`
	Metadata string `json:"metadata"`
	Content  string `json:"content"`
}

// manifestResponse is the wire format for GET /api/v1/manifest.
type manifestResponse struct {
	Records []manifestRecord `json:"records"`
}

// manifestRecord is a single record from the manifest endpoint.
type manifestRecord struct {
	ID        int64  `json:"id"`
	UUID      string `json:"uuid"`
	Type      string `json:"type"`
	Summary   string `json:"summary"`
	Metadata  string `json:"metadata"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// contentResponse wraps the content endpoint response.
type contentResponse struct {
	Content string `json:"content"`
}
