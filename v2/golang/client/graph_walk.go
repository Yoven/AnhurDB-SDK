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
)

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
func (m *Memory) Walk(ctx context.Context, startID int64, depth int, opts ...ReadOption) (*WalkResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	_ = opts

	payload := map[string]interface{}{
		"seed_id":   startID,
		"depth":     resolveWalkDepth(depth),
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
