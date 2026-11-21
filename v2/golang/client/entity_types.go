package client

// entity_types.go — the shapes of the Layer-2 entity graph (entities.go):
// Entity, the upsert result, graph/timeline results, typed edges and their
// options, and the list/search envelopes.

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
type EntitiesPage struct {
	Entities   []Entity `json:"entities"`
	Count      int      `json:"count"`
	Total      int64    `json:"total"`
	Limit      int      `json:"limit"`
	Offset     int      `json:"offset"`
	HasMore    bool     `json:"has_more"`
	NextOffset int      `json:"next_offset"`
}
