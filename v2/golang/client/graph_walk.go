package client

// graph_walk.go — POST /api/v1/walk and POST /api/v1/walk/semantic.
//
// Domain: graph traversal from a seed record. Split out of client.go on
// 2026-09-05 (client.go was 1679 lines, far past the ~300-line house cut) so
// the depth-default fix below could land without growing an oversized file.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v3/models"
)

// WalkResult contains the graph traversal output from the walk endpoints.
//
// Wire envelope is exactly {nodes, edges, truncated}
// (server/handler/record_search_graph.go:219-223). It carries NO start_id and
// NO depth: this struct declared both until 2026-09-14 and neither has ever
// been sent, so a caller reading result.Depth got 0 and concluded the traversal
// went nowhere.
//
// Junior Tip [Truncated is the honest half of an empty-looking answer]: the
// handler stops the BFS the moment it holds max_nodes records and sets
// truncated=true. Without this field a capped walk and a genuinely small
// subgraph decode identically, and "the graph is sparse here" is exactly the
// wrong conclusion to reach silently.
type WalkResult struct {
	Nodes     []models.Record `json:"nodes"`
	Edges     []WalkEdge      `json:"edges"`
	Truncated bool            `json:"truncated"`
}

// WalkEdge is a single edge connecting two nodes in a graph walk.
//
// Wire shape is exactly {"source","target"} — the handler builds an anonymous
// struct with those two int64 fields and nothing else. There is no edge type,
// no weight and no direction on this route.
type WalkEdge struct {
	Source int64 `json:"source"`
	Target int64 `json:"target"`
}

// defaultWalkDepth is the traversal depth used when the caller passes a
// non-positive depth.
//
// Junior Tip [why the SDK defaults instead of forwarding 0, 2026-09-05]: the
// TypeScript SDK sends `depth ?? 3` and Python defaults `depth=3`, so the same
// call written in three languages produced three different requests — Go alone
// put `"depth": 0` on the wire. A caller who omits depth means "use the usual
// traversal", not "traverse nothing", and a zero-depth walk that returns just
// the seed is the kind of empty result nobody debugs because it looks like the
// graph is simply sparse. Three SDKs, one default.
const defaultWalkDepth = 3

// resolveWalkDepth applies defaultWalkDepth to any non-positive depth.
// Negative and zero collapse to the same case on purpose: neither is a
// traversal a caller could have meant.
func resolveWalkDepth(requestedDepth int) int {
	if requestedDepth <= 0 {
		return defaultWalkDepth
	}
	return requestedDepth
}

// Walk performs a BFS graph traversal starting from a given record.
//
// direction:"both" means traverse both incoming and outgoing edges.
// The server returns nodes and edges up to the specified depth; a depth <= 0
// falls back to defaultWalkDepth, matching the TypeScript and Python SDKs.
//
// WithAsOf is the ONLY ReadOption this method honours. Any other option is
// refused at call time with *UnsupportedOptionError.
//
// Junior Tip [why WithSince/WithUntil are refused rather than forwarded,
// live-proved 2026-09-14]: the same walk answered 5 nodes / 7 edges bare,
// 0 / 0 with as_of=2026-01-01, and 5 / 7 with since=2026-01-01 — i.e. `since`
// went over the wire, the server answered HTTP 200, and the filter was dropped.
// A window nobody applied is indistinguishable from a window that matched
// everything. "Graph at instant T" is the only semantically clean snapshot of a
// connected traversal, which is why the handler supports as_of alone
// (server/handler/record_search_graph.go:32-38).
func (m *Memory) Walk(ctx context.Context, startID int64, depth int, opts ...ReadOption) (*WalkResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)
	if optionErr := rejectUnsupportedReadOptions(cfg, "Walk",
		"POST /api/v1/walk honours as_of only", "WithAsOf"); optionErr != nil {
		return nil, optionErr
	}

	payload := map[string]interface{}{
		"seed_id":   startID,
		"depth":     resolveWalkDepth(depth),
		"direction": "both",
	}
	// as_of turns the traversal into a snapshot: every BFS frontier is
	// materialised with GetRecordsByIDsAsOf, so records created after the
	// instant — and versions superseded before it — are simply absent.
	if cfg.asOf != "" {
		payload["as_of"] = cfg.asOf
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
// cost — byte-for-byte the previous behaviour. A depth <= 0 falls back to
// defaultWalkDepth, matching the TypeScript and Python SDKs. Passing WithTarget (plus its
// companion WithGoalVector / WithTargetTag) turns it into a goal-directed walk
// whose nodes come back ordered by proximity to the target. WithMaxCost tunes
// the cost budget.
func (m *Memory) WalkSemantic(ctx context.Context, startID int64, depth int, opts ...ReadOption) (*WalkResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	payload := map[string]interface{}{
		"seed_id": startID,
		"depth":   resolveWalkDepth(depth),
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
