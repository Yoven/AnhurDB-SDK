package client

// record_by_id_types.go — the response shapes of the by-id reads in
// record_by_id.go: GetContext's topology (ContextResult) and GetGrounding's
// provenance traversal (GroundingResult and its nodes).

import (
	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/models"
)

// ContextResult contains the topological context around a record.
// Wire shape matches GET /api/v1/records/{id}/topology: {"target","neighbors"}.
type ContextResult struct {
	Target    models.Record   `json:"target"`
	Neighbors []models.Record `json:"neighbors"`
}

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
