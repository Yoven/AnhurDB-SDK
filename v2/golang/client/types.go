package client

import (
	"time"

	"github.com/anhurdb/sdk-go/v2/models"
)

// --------------------------------------------------------------------------
// Core response types — used by Add, Search, Profile
// --------------------------------------------------------------------------

// AddResult is returned by Memory.Add(). It contains the created
// record(s) and whether cloud ingest or OSS fallback was used.
//
// RaftIndex carries the Raft log index at which this write was applied, as
// reported by the server in the write response (model.Record.RaftIndex →
// JSON "raft_index"). It enables read-your-writes (RYW): pass it to a
// subsequent read via WithMinIndex(result.RaftIndex) and the server will
// block that read until the contacted node has replicated up to this index,
// guaranteeing the just-written record is visible even on a lagging follower.
//
// Junior Tip [RYW, 2026-06-17]: RaftIndex is 0 when the server did not report
// one — e.g. the cloud /api/v1/ingest path (async pipeline, no synchronous
// log index) or an older server. A 0 value passed to WithMinIndex is treated
// as "no barrier", so threading it through is always safe. Only the
// synchronous /api/v1/records write path returns a non-zero index today.
type AddResult struct {
	ID        int64           `json:"id"`
	Records   []RecordSummary `json:"records"`
	Status    string          `json:"status"`
	Mode      string          `json:"mode"` // "cloud" or "oss"
	RaftIndex uint64          `json:"raft_index,omitempty"`
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
// Junior Tip [nested shape is the cross-SDK contract, 2026-07-03]: the server
// wire is {"results":[{"record":{<full record>},"similarity":0.63}, ...]} and
// the score key is "similarity" at the hit level, NOT a "score" inside the
// record. The three SDKs (Go/Python/TS) MUST expose this identical nested shape
// so a hit serialized by one and consumed by another carries every record field
// — the previous flat SearchResult silently dropped everything except
// id/type/summary/metadata/content, which is exactly the kind of field loss the
// SDK-parity invariant forbids. Record is the SDK's own typed models.Record, so
// callers get related_ids, main_ids, status, valid_from, created_at, etc. — not
// a lossy subset. Python's SearchResult(record=Record, similarity) is the
// reference shape; Go mirrors it field-for-field here.
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
	ID     int64  `json:"id"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
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
// Junior Tip [pagination contract]: HasMore + NextOffset is the read-side
// of the cursor protocol. Loop until HasMore is false; for each iteration,
// pass NextOffset as the next call's offset argument.
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
// for status polling.
type UploadResult struct {
	ID     int64  `json:"id"`
	Status string `json:"status,omitempty"`
}

// UploadStatusResult describes the processing status of a file upload.
type UploadStatusResult struct {
	ID        int64   `json:"id"`
	Status    string  `json:"status"` // "processing", "completed", "failed"
	Filename  string  `json:"filename,omitempty"`
	Error     string  `json:"error,omitempty"`
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

// AddOption configures a single Memory.Add call.
//
// Junior Tip [SDK parity 2026-06 — the 516-record incident]: the three SDKs
// (Go/Python/TS) must accept the SAME capabilities on Add — caller-supplied
// score, type, and metadata — or a record written by one SDK and read by
// another carries different defaults, which is exactly how the 2026-05-22
// metadata-corruption incident slipped past review. The idiomatic surface
// differs per language (functional options here, keyword args in Python, an
// options object in TS) but the CAPABILITY is identical. Add(ctx, text) with
// zero options stays byte-for-byte backward compatible with the old call.
type AddOption func(*addConfig)

// addConfig holds the per-call overrides for Memory.Add. A nil pointer field
// means "caller did not specify — use the server/SDK default", which is how we
// keep score/type from being silently forced when the caller omits them.
//
// Junior Tip [why pointers, not bare values]: score 0 and "" type are both
// LEGAL values the caller might intend, so we cannot use the zero value as the
// "unset" sentinel — we must distinguish "score not provided" from "score
// explicitly 0". Pointers give us that three-state (nil / set-to-zero / set).
type addConfig struct {
	score    *int
	memType  *string
	metadata map[string]interface{}
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
// Junior Tip [server invariant]: the server rejects a "semantic" (or other
// non-episodic) record created in a session that has no episodic anchor yet,
// with HTTP 422 "without an episodic anchor". Add's retry logic treats that
// rejection as transient (the anchor may be landing concurrently) — see
// isTransientWriteError.
func WithType(memType string) AddOption {
	return func(cfg *addConfig) {
		cfg.memType = &memType
	}
}

// WithMetadata merges caller-supplied keys into the record metadata. The SDK
// always sets container_tag; caller keys are layered on top (caller wins on a
// key collision, except container_tag which the SDK owns).
//
// Junior Tip [bug it prevents]: the previous Add hardcoded metadata to just
// {"container_tag": tag} and dropped any caller intent on the floor. Python/TS
// had the same silent drop — this is the WRITE-path half of the parity fix.
func WithMetadata(metadata map[string]interface{}) AddOption {
	return func(cfg *addConfig) {
		cfg.metadata = metadata
	}
}

// ReadOption configures a single read call. It is the idiomatic Go surface
// for opt-in read-your-writes consistency and is the SAME type used by Search
// (so WithLimit / WithTypeFilter / WithMinIndex all compose on a read).
//
// Junior Tip [SDK parity 2026-06-17 — RYW]: every read method that hits the
// server accepts ...ReadOption, so a caller can thread the raft_index it got
// from a prior write (AddResult.RaftIndex) into the next read via
// WithMinIndex. The Python SDK exposes the same capability as a keyword arg
// `min_index=`, the TypeScript SDK as a `{ minIndex }` options object — same
// semantics, idiomatic shape per language. Calling a read with no options is
// byte-for-byte backward compatible (default eventually-consistent read).
//
// Junior Tip [why reuse searchConfig — non-breaking]: SearchOption is an
// exported alias of ReadOption and the existing WithLimit / WithTypeFilter
// keep working unchanged, so external callers (AnhurAgents, the dspy
// retriever) compile as-is. Read methods that have no limit/type-filter
// concept simply ignore those fields and honour only minIndex.
type ReadOption func(*searchConfig)

// SearchOption is retained as an exported alias of ReadOption for backward
// compatibility — existing code using client.SearchOption / WithLimit /
// WithTypeFilter is unaffected.
type SearchOption = ReadOption

// searchConfig holds parameters for a read request: search shaping (limit,
// typeFilter), the optional bi-temporal window (asOf/since/until), and the
// optional read-your-writes barrier (minIndex).
type searchConfig struct {
	limit      int
	typeFilter string
	// asOf / since / until are optional RFC3339 UTC bi-temporal filters honoured
	// by the manifest reads. asOf is mutually exclusive with since/until — the
	// server returns HTTP 400 on a violation, which the SDK surfaces verbatim.
	// Junior Tip [why empty-string sentinels, not pointers]: unlike score (where
	// 0 is a legal value needing a 3-state pointer), "" is never a valid RFC3339
	// instant, so the empty string is an unambiguous "unset" sentinel here.
	asOf  string
	since string
	until string
	// minIndex, when > 0, sets the X-Anhur-Min-Index read barrier so the
	// server blocks the read until the node has applied this Raft index for
	// the tenant. 0 = default eventually-consistent read.
	minIndex uint64
	// Goal-directed semantic-walk parameters (2026-07-03). These are honoured
	// ONLY by WalkSemantic; every other read method ignores them — the exact
	// same shared-ReadOption pattern already used by asOf/since/until (fields
	// meaningful only to the manifest reads). The zero value of each field means
	// "caller did not set it": walkTarget "" and walkMaxCost 0 both omit their
	// JSON key so the server falls through to its plain-Dijkstra default,
	// preserving the historical behaviour of an option-free WalkSemantic call.
	//
	// Junior Tip [why []byte, not a base64 string, for the goal vector]: callers
	// hold the guide embedding as raw float bytes; making them pre-encode base64
	// would leak a wire detail into every call site and invite double-encoding
	// bugs. The SDK owns the base64 step (WalkSemantic), matching how the Python
	// SDK takes `goal_vector: bytes` and the TS SDK takes a `Uint8Array` —
	// identical capability, idiomatic shape per language.
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

// WithMinIndex requests read-your-writes consistency for a single read: the
// server blocks the read until the contacted node's applied Raft index for
// the tenant reaches minIndex, then serves it. Pass the RaftIndex returned by
// a prior write (AddResult.RaftIndex) so a read issued immediately after the
// write cannot miss it on a lagging follower.
//
// A minIndex of 0 is a no-op (the default eventually-consistent read), so it
// is always safe to pass through an index that may be unset.
func WithMinIndex(minIndex uint64) ReadOption {
	return func(cfg *searchConfig) {
		cfg.minIndex = minIndex
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
// can still compose WithMinIndex for read-your-writes on the same call. Only
// WalkSemantic reads them; passing them to any other read is an inert no-op,
// mirroring how WithAsOf/WithSince/WithUntil are honoured only by the manifest
// reads. Calling WalkSemantic with none of them is byte-for-byte the previous
// behaviour (Dijkstra, server defaults max_cost=2.0 / max_nodes=50).
//
// Junior Tip [SDK parity — option names are the contract, 2026-07-03]: the
// three SDKs must expose the SAME knobs under predictable names so a walk
// issued from Go, Python or TS hits the identical REST body. The names here
// (target / goalVector / targetTag / maxCost) map 1:1 to the Python kwargs
// (target=, goal_vector=, target_tag=, max_cost=) and the TS options object
// ({ target, goalVector, targetTag, maxCost }). Keep them in lockstep on any
// future change.
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
	// RaftIndex is usually absent on this async path (the ingest worker has no
	// synchronous log index to report); decoded defensively in case a future
	// server surfaces it.
	RaftIndex uint64 `json:"raft_index,omitempty"`
}

// recordCreateResponse is the wire format returned by POST /api/v1/records.
//
// RaftIndex is the Raft log index the server stamps on the created record
// (model.Record.RaftIndex, JSON "raft_index"). Used to populate
// AddResult.RaftIndex for read-your-writes.
type recordCreateResponse struct {
	ID        int64  `json:"id"`
	RaftIndex uint64 `json:"raft_index,omitempty"`
}

// searchResponse is the wire format returned by the hybrid search endpoints
// (POST /api/v1/search/global, POST /api/v1/search):
// {"results":[{"record":{...},"similarity":...}, ...]}.
//
// Junior Tip [decode straight into the public type — no flatten]: each element
// already IS the exported SearchResult ({record, similarity} with record a full
// models.Record), so we decode directly into []SearchResult. The old searchHit
// / searchRecord subset structs (which flattened the record and dropped every
// field except id/type/summary/metadata/content) are gone — that lossy step was
// the bug this reform removes.
//
// Junior Tip [NOT search/type, 2026-07-04]: GET /api/v1/search/type does NOT use
// this envelope — it returns a BARE {"records":[<Record>],"count":N} array with no
// per-hit similarity (server/handler/record_search.go: SearchByType). SearchByType
// decodes "records" directly and wraps into SearchResult; reusing searchResponse
// there read the absent "results" key and silently returned an empty slice.
type searchResponse struct {
	Results []SearchResult `json:"results"`
}

// manifestResponse is the object envelope for GET /api/v1/recent
// ({"records":[...],"count":N}).
//
// Junior Tip [recent full-record parity, 2026-07-03]: Records is []models.Record (the
// FULL record), not a lightweight subset — Recent() must return the same complete record
// shape as Python/TS recent() and as this decoder's own bare-array branch. The old
// manifestRecord subset silently dropped weight/score/related_ids/main_ids/content/
// valid_from/valid_until/superseded_by from every /recent envelope result.
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
// Junior Tip [SDK parity 2026-06-18]: the three SDKs must accept the SAME
// create capabilities (type/score/related_ids/status/metadata/valid_from) so a
// record created via one SDK and read via another carries identical fields. The
// idiomatic surface differs (functional options here, kwargs in Python, an
// options object in TS) but the capability set is identical.
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
	// the ONLY place the REST create route reads it (see Create's Junior Tip).
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
// the new record. It is delivered inside the metadata JSON because that is the
// only field name the REST create route parses (see Create's Junior Tip).
func WithCreateValidFrom(validFrom string) CreateOption {
	return func(cfg *createConfig) {
		cfg.validFrom = validFrom
	}
}

// --------------------------------------------------------------------------
// Parity (2026-06-18) — Query AST (POST /api/v1/query)
// --------------------------------------------------------------------------

// QueryRequest is the structured AST sent to POST /api/v1/query. It mirrors the
// server's AstQuery (server/handler/record_query.go): a whitelisted
// filter/sort/pagination grammar over the record columns.
//
// Build it directly, or fluently via NewQuery().Where(...).OrderBy(...).Limit(...).
type QueryRequest struct {
	// Select lists columns to return. Junior Tip: parsed but NOT projected by
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
// Junior Tip [operator names match the server exactly — verified against
// server/handler/record_query.go]: the server keys are $eq/$gt/$gte/$lt/$lte
// (scalar) and $in (array). The Go field tags below reproduce those dollar-sign
// keys verbatim so the marshalled JSON is the contract the handler expects.
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
// Junior Tip [HasMore is a server heuristic]: the server sets HasMore =
// (len(records)==limit), which can false-positive on an exactly-full last page.
// Follow the cursor by re-querying with Offset = previous Offset + len(Records);
// a page that returns zero records is the true terminator.
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
// matches the server's groundingResponse (server/handler/record_grounding.go):
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
