package client

// walk_options.go — the goal-directed options for Memory.WalkSemantic.
//
// Domain, in one sentence: the four knobs that turn a WalkSemantic traversal
// from a plain cost-first Dijkstra into a walk pulled toward a target.
//
// Junior Tip [why these are not search options, 2026-09-05]: they share the
// ReadOption type and the searchConfig struct with search — that is a Go
// convenience, not a shared meaning. Only WalkSemantic reads walkTarget /
// walkGoalVector / walkTargetTag / walkMaxCost; handed to a search they are an
// inert no-op. Keeping them beside WithLimit and WithSearchMode made
// search_options.go read as if a search could be "steered toward a tag", which
// it cannot. The file was also 318 lines against a ~300-line cut, and the honest
// cure for that was to name the second domain rather than to trim comments.

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
