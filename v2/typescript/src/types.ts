/**
 * TypeScript type definitions for the AnhurDB SDK.
 *
 * These interfaces model every request/response shape used by the
 * Memory class. They cover the full AnhurDB REST surface including:
 *   - Core memory CRUD
 *   - Batch operations
 *   - Entity knowledge graph (Layer 2)
 *   - File upload & ingestion
 *   - Temporal versioning
 *   - Graph traversal
 *
 * Junior Tip: Must match core.yaml memory taxonomy exactly.
 */

// ── Memory types (cognitive epistemology) ────────────────────

/** Cognitive memory types defined by the AnhurDB epistemology. */
export type MemoryType =
  | "episodic"
  | "fact"
  | "preference"
  | "decision"
  | "task"
  | "risk"
  | "reasoning"
  | "idea"
  | "emotion"
  | "consolidated"
  | "hub"
  | "file";

/**
 * Status of a cognitive record in the cluster.
 *
 * Sourced from:
 *   - Go server: create.go, update.go, record_batch.go, upload.go
 *   - Python agents: consolidator.py, judge.py, regression/worker.py
 */
export type MemoryStatus =
  | "saved"
  | "pending"
  | "consolidated"
  | "archived"
  | "decayed"
  | "processing"
  | "completed"
  | "linked"
  | "hubbed"
  | "failed"
  | "pending_judge"
  | "failed_judge"
  | "failed_consolidation";

// ── Constructor options ──────────────────────────────────────

/** Options passed to `new Memory(...)`. */
export interface MemoryOptions {
  /** AnhurDB API key (required). */
  apiKey: string;

  /**
   * Server URL. Defaults to `https://api.anhurdb.com`.
   * Pass `http://localhost:8000` for self-hosted / OSS.
   */
  url?: string;

  /**
   * Optional user identifier. Used as `container_tag` for grouping
   * memories. When omitted the SDK derives a stable tag from a hash
   * of the API key.
   */
  userId?: string;

  /**
   * Optional tenant ID for multi-tenant deployments.
   * Sent as `X-Tenant-ID` header on every request.
   */
  tenantId?: string;
}

// ── add() ────────────────────────────────────────────────────

/** Options for `Memory.add()`. */
export interface AddOptions {
  /** Importance rating 1-10 (default 5). */
  score?: number;
  /** Memory type (default "episodic"). */
  type?: MemoryType;
  /**
   * Caller-supplied metadata.
   *
   * Junior Tip [SDK parity, 2026-06]: kept as an arbitrary object so the
   * three SDKs expose the identical capability — `add(text, {score, type,
   * metadata})`. The SDK merges it into the canonical
   * `{"container_tag": "..."}` envelope before sending so it never
   * overwrites the container tag (see the 2026-05-22 metadata corruption
   * incident). Server stores `metadata` as a JSON string.
   */
  metadata?: Record<string, unknown>;
}

/** A single record descriptor returned inside `AddResult`. */
export interface AddRecordSummary {
  id: number;
  type: MemoryType;
  summary: string;
}

/** Value returned by `Memory.add()`. */
export interface AddResult {
  /** Session UUID that groups this memory. */
  sessionId: string;
  /** Records created by the server. */
  records: AddRecordSummary[];
  /** Whether the cloud ingest or OSS fallback was used. */
  mode: "cloud" | "oss";
  /**
   * Raft log index at which this write was applied, reported by the server in
   * the write response (`raft_index`). Enables read-your-writes: pass it as
   * `{ minIndex }` (see {@link ReadOptions}) on a subsequent read and the
   * server blocks that read until the contacted node has replicated up to this
   * index, so the just-written record is visible even on a lagging follower.
   *
   * Junior Tip [RYW, 2026-06-17]: 0 (or undefined) when the server did not
   * report one — e.g. the async cloud `/api/v1/ingest` path or an older
   * server. A 0 passed as `minIndex` is treated as "no barrier", so threading
   * it through is always safe. Mirrors Go `AddResult.RaftIndex` and the Python
   * `add()` result's `raft_index`.
   */
  raftIndex?: number;
}

/**
 * Per-call read options shared by every read method. Today it carries only the
 * optional read-your-writes barrier, but using an options object (rather than a
 * positional parameter) keeps the read API extensible without future breaking
 * changes — the idiomatic TypeScript shape, matching the Go SDK's
 * `WithMinIndex` option and the Python SDK's `min_index=` keyword.
 */
export interface ReadOptions {
  /**
   * Read-your-writes barrier: the `raftIndex` returned by a prior write. When
   * set to a positive value, the read blocks until the contacted node has
   * applied that Raft index for the tenant. Omit / 0 → default eventually-
   * consistent, load-balanced read.
   */
  minIndex?: number;
}

// ── search() ─────────────────────────────────────────────────

/** Options for `Memory.search()`. */
export interface SearchOptions {
  /** Maximum results to return (default 10). */
  limit?: number;
  /** Filter by memory type. */
  typeFilter?: MemoryType;
}

/**
 * A single search hit returned by every search method
 * (`search`/`searchSession`/`searchByType`/`recall`).
 *
 * Junior Tip [nested shape parity, 2026-07-03]: this is the CANONICAL wire shape
 * the server emits (server/model/record.go SearchResult) —
 * `{ "record": {<full Record>}, "similarity": 0.63 }`. We nest the complete
 * {@link MemoryRecord} verbatim and carry the score in a SIBLING `similarity`
 * field (NOT inside the record). The previous flat shape
 * (`{id,type,summary,score,...}`) DROPPED every other record field
 * (uuid/weight/related_ids/main_ids/status/valid_from/...) — silent data loss.
 * All three SDKs must match: Python is `SearchResult(record=Record,
 * similarity=float)` (the reference), Go is `SearchResult{Record, Similarity}`.
 * NOTE the score key is `similarity`, NOT `score`.
 */
export interface SearchResult {
  /** The full memory record, verbatim (no fields dropped). */
  record: MemoryRecord;
  /** Similarity score (0-1), a sibling of `record` — not inside it. */
  similarity: number;
}

// ── profile() ────────────────────────────────────────────────

/** Value returned by `Memory.profile()`. */
export interface ProfileResult {
  /** Static profile facts (identity, preferences, etc.). */
  static: Record<string, unknown>;
  /** Dynamic profile state (recent topics, mood, etc.). */
  dynamic: Record<string, unknown>;
  /** Aggregate statistics. */
  stats: Record<string, unknown>;
  /** Raw server response (in case fields differ by version). */
  [key: string]: unknown;
}

// ── Extended Memory types ────────────────────────────────────

/**
 * A full record as returned by the AnhurDB API.
 *
 * Junior Tip [contract, verified against model.Record JSON tags]: these are the
 * exact JSON keys the server serialises (internal fields are `json:"-"` and
 * never appear). The optional fields are omitempty server-side: `content` only
 * appears on history endpoints; `superseded_by`/`valid_from`/`valid_until`
 * only when set; `raft_index` only on write responses. Kept optional so the one
 * interface can model every read endpoint (search/query/manifest/list_chat).
 */
export interface MemoryRecord {
  id: number;
  uuid: string;
  type: string;
  summary: string;
  status: string;
  weight: number;
  score: number;
  created_at: string;
  updated_at: string;
  /** Raw JSON-as-string metadata envelope (e.g. `{"container_tag":"..."}`). */
  metadata?: string;
  related_ids?: number[];
  main_ids?: number[];
  consolidated?: boolean;
  archived?: boolean;
  /** Present only on history endpoints (the .gz body); omitted elsewhere. */
  content?: string;
  superseded_by?: number;
  valid_from?: string;
  valid_until?: string;
  /** Raft log index; present only on write responses. */
  raft_index?: number;
}

/** Result of a graph walk starting from a given record. */
export interface WalkResult {
  nodes: Array<{ id: number; type: string; summary: string; weight: number }>;
  edges: Array<{ source: number; target: number; type: string }>;
}

/**
 * Goal-directed steering mode for {@link WalkSemanticOptions.target}.
 *
 * Junior Tip [contract, verified against POST /api/v1/walk/semantic]: the
 * server selects the traversal frontier by this exact string — `"semantic"`
 * steers toward `vector` (cosine to the goal), `"tag"` toward `target_tag`,
 * `"recency"` toward the most recent records. Omitting `target` entirely = the
 * pre-existing pure-Dijkstra walk. These three literals must match Go/Python.
 */
export type WalkTarget = "semantic" | "tag" | "recency";

/**
 * Optional goal-directed steering for {@link WalkResult}-returning
 * `Memory.walkSemantic`. Every field is omitted from the wire body when unset,
 * so calling `walkSemantic` with no options preserves the pre-existing
 * pure-Dijkstra walk verbatim (backward-compatible).
 *
 * Junior Tip [parity]: field names mirror the Go SDK
 * (`Target`/`GoalVector`/`TargetTag`/`MaxCost`) and the Python SDK
 * (`target`/`goal_vector`/`target_tag`/`max_cost`). `goalVector` is raw bytes
 * (a BSQ-quantised vector); the SDK base64-encodes it into the wire `vector`
 * field so callers never touch base64.
 */
export interface WalkSemanticOptions {
  /** Goal-directed mode. Omit for a pure-Dijkstra walk (default). */
  target?: WalkTarget;
  /**
   * Goal vector as raw bytes; used when `target === "semantic"`. The SDK
   * base64-encodes it into the request's `vector` field (server default:
   * treated as absent when omitted).
   */
  goalVector?: Uint8Array;
  /** Target tag to steer toward; used when `target === "tag"`. */
  targetTag?: string;
  /**
   * Maximum accumulated path cost before the walk stops. Maps to the wire
   * `max_cost` field; the server defaults it to 2.0 when omitted.
   */
  maxCost?: number;
}

/** Topology context around a specific record. */
export interface ContextResult {
  target: MemoryRecord;
  neighbors: MemoryRecord[];
}

/** Aggregate stats for a single session. */
export interface SessionStats {
  uuid: string;
  record_count: number;
  last_active: string;
}

// ── Entity Knowledge Graph (Layer 2) ─────────────────────────

/**
 * A named entity in the AnhurDB knowledge graph.
 *
 * Entities represent real-world objects (people, organisations, concepts)
 * that are extracted from or linked to memory records.
 */
export interface EntityRecord {
  id: number;
  name: string;
  type: string;
  summary?: string;
  attributes?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

/**
 * A typed, temporal relationship between two entities.
 *
 * Examples: "works_at", "knows", "part_of", "created_by".
 */
export interface EntityEdge {
  source_id: number;
  target_id: number;
  relation: string;
  event_time?: string;
  valid_until?: string;
  confidence?: number;
  source_record_id?: number;
}

/** Options for `Memory.upsertEntity()`. */
export interface UpsertEntityOptions {
  entityType?: string;
  summary?: string;
  attributes?: Record<string, unknown>;
}

/** Options for `Memory.upsertEntityEdge()`. */
export interface UpsertEntityEdgeOptions {
  eventTime?: string;
  confidence?: number;
  sourceRecordId?: number;
}

/** Result from entity graph traversal. */
export interface EntityGraphResult {
  entity_id: number;
  depth: number;
  nodes: Array<{
    entity: EntityRecord;
    edges?: EntityEdge[];
  }>;
  node_count: number;
}

/** Result from entity timeline query. */
export interface EntityTimelineResult {
  entity: EntityRecord;
  timeline: EntityEdge[];
  record_ids?: number[];
}

// ── File Upload ──────────────────────────────────────────────

/** Result from file upload. Contains ID for status polling. */
export interface UploadResult {
  id: number;
  status?: string;
}

/** Result from upload status polling. */
export interface UploadStatusResult {
  id: number;
  /** "processing", "completed", or "failed". */
  status: string;
  filename?: string;
  error?: string;
  record_ids?: number[];
}

// ── Batch Operations ─────────────────────────────────────────

/** Result from batch status update. */
export interface BatchUpdateResult {
  updated_count: number;
}

// ── Internal / wire-level types ──────────────────────────────

/**
 * Payload sent to POST /api/v1/ingest (cloud mode).
 *
 * Junior Tip [score/type drop fix, 2026-06]: the ingest worker previously
 * received ONLY `content` + `container_tag`, so a caller's `score`/`type`
 * were silently discarded on this path — the exact parity bug the TS and
 * Python SDKs shared. We now forward `score`, `type`, and `metadata` as
 * optional hints. The server treats them as ingest hints; the OSS
 * `/api/v1/records` fallback persists them authoritatively. All three SDKs
 * must forward these identically.
 */
export interface IngestPayload {
  content: string;
  container_tag: string;
  score?: number;
  type?: string;
  metadata?: string;
}

/** Payload sent to POST /api/v1/records (OSS fallback + full-fidelity create). */
export interface RecordPayload {
  uuid: string;
  type: string;
  dimension: number;
  prefix: string;
  weight: number;
  score: number;
  vector: string;
  related_ids: number[];
  main_ids: number[];
  consolidate_id: number;
  metadata: string;
  summary: string;
  content: string;
  consolidated: boolean;
  status: string;
  /**
   * RFC3339 UTC start of the temporal validity window. Omitted when unset.
   * Junior Tip [full-fidelity create, 2026-06-18]: surfaced so `create()` can
   * persist a bitemporal window verbatim, matching Go/Python `create`.
   */
  valid_from?: string;
  /** RFC3339 UTC end of the temporal validity window. Omitted when unset. */
  valid_until?: string;
}

/** Payload sent to POST /api/v1/search/global (cross-session search). */
export interface SearchPayload {
  // Junior Tip [no phantom fields, 2026-06-18]: the global-search handler reads
  // ONLY `text` (json:"text", server handler/record_search.go) — a `query` key
  // was being sent alongside and silently ignored. Removed so the wire payload
  // is exactly what the handler consumes, matching Go/Python.
  text: string;
  limit: number;
  type_filter?: string;
}

/**
 * Payload sent to POST /api/v1/search (session-scoped hybrid search).
 *
 * Junior Tip [contract, verified against server/handler/record_search.go]: the
 * session Search handler decodes ONLY into model.SearchRequest — EITHER `text`
 * (FTS5) OR `vector` (base64 BSQ) is required, else HTTP 400. There is NO
 * `mode` field (the vector/text/hybrid mode is implicit and json.Decode
 * silently drops unknown keys). `uuid` scopes the search to a single chat; an
 * empty uuid is tenant-wide. Temporal `as_of`/`since`/`until` are RFC3339 UTC
 * and `as_of` is mutually exclusive with since/until. All three SDKs must send
 * these exact field names.
 */
export interface SearchSessionPayload {
  uuid?: string;
  text?: string;
  vector?: string;
  type_filter?: string;
  limit?: number;
  as_of?: string;
  since?: string;
  until?: string;
}

// ── query() — AST query engine ───────────────────────────────

/**
 * Comparison operators supported by the AST query engine.
 *
 * Junior Tip [contract, verified against server/handler/record_query.go]: the
 * server implements EXACTLY these six operators. `$neq`/`$nin`/`$like` were
 * deliberately omitted — the server silently ignores them, so exposing them
 * would be a silent-loss bug. `$in` takes an array value; the rest take a
 * scalar. Mirrors the Python QueryBuilder `_OP_MAP`.
 */
export type QueryOperator = "$eq" | "$gt" | "$gte" | "$lt" | "$lte" | "$in";

/**
 * A single column's filter condition: a map of operator → value.
 *
 * Example: `{ "$gt": 0.8 }` or `{ "$in": ["risk", "decision"] }`.
 */
export type QueryFilterCondition = Partial<Record<QueryOperator, unknown>>;

/**
 * One sort clause for the AST query. `field` MUST be in the server column
 * whitelist (else HTTP 400 'invalid sort field'); `order` falls back to DESC
 * when absent or invalid.
 */
export interface QuerySortClause {
  field: string;
  order: "asc" | "desc";
}

/**
 * Pagination block of the AST query. `limit` defaults to 50 and is hard-capped
 * at 1000 server-side; `offset` defaults to 0 and must be >= 0.
 */
export interface QueryPagination {
  limit?: number;
  offset?: number;
}

/**
 * The compiled JSON Abstract Syntax Tree sent flat as the body of
 * POST /api/v1/query.
 *
 * Junior Tip [contract, verified against server/handler/record_query.go]: the
 * server deserialises this directly into its `AstQuery` struct, so the fields
 * are sent FLAT at the top level — NOT wrapped in `{"query": ...}` (mirrors the
 * Python QueryExecutor note). `select` is parsed but ignored (the SQL SELECT
 * list is fixed). Filter/sort column names are whitelist-validated server-side.
 */
export interface AstQuery {
  filters?: Record<string, QueryFilterCondition>;
  sort?: QuerySortClause[];
  pagination?: QueryPagination;
  /** Parsed but ignored by the server; included for forward-compat. */
  select?: string[];
}

/**
 * Response from POST /api/v1/query.
 *
 * Junior Tip [contract]: `records` is a FLAT array of model.Record (NOT wrapped
 * in `{record, similarity}` like /search). An empty result set serialises as
 * `records: null` with `count: 0`, so callers must default to `[]`.
 */
export interface QueryResult {
  records: MemoryRecord[];
  count: number;
}

// ── manifest / list_chat / count_by_type ─────────────────────

/**
 * Paginated manifest envelope returned by GET /api/v1/manifest and
 * GET /api/v1/chats/{uuid}/manifest.
 *
 * Junior Tip [contract]: `has_more = (len(records) == limit)` is a server-side
 * heuristic that can false-positive on an exactly-full last page — page until
 * an empty/short page to be certain, do not trust a single `has_more: true`.
 */
export interface ManifestResult {
  records: MemoryRecord[];
  count: number;
  limit: number;
  offset: number;
  has_more: boolean;
}

/**
 * Envelope returned by GET /api/v1/chats/{uuid} (list_chat).
 *
 * Junior Tip [contract]: unlike the manifest endpoints this has NO
 * limit/offset/has_more — the entire matching set for the session is returned.
 * `content` is omitted (metadata only, not the .gz body).
 */
export interface ListChatResult {
  records: MemoryRecord[];
  count: number;
}

/** Options for `Memory.manifestGlobal()`. */
export interface ManifestGlobalOptions {
  /** Keyword filter (FTS5). Sent as the `q` query param. */
  q?: string;
  /** Max records (default 100, server-capped at 1000). */
  limit?: number;
  /** Pagination offset (default 0). Ignored when `q` is set. */
  offset?: number;
  /** RFC3339 UTC snapshot instant. Mutually exclusive with since/until. */
  asOf?: string;
  /** RFC3339 UTC lower bound (created_at >= since). */
  since?: string;
  /** RFC3339 UTC upper bound (created_at <= until). */
  until?: string;
}

/** Options for `Memory.manifestSession()`. */
export interface ManifestSessionOptions {
  /** Keyword filter (FTS5) scoped to the session. Sent as `q`. */
  q?: string;
  /** Max records (default 500, server-capped at 2000). */
  limit?: number;
  /** Pagination offset (default 0). */
  offset?: number;
  /** RFC3339 UTC snapshot instant. Mutually exclusive with since/until. */
  asOf?: string;
  /** RFC3339 UTC lower bound (created_at >= since). */
  since?: string;
  /** RFC3339 UTC upper bound (created_at <= until). */
  until?: string;
}

/** Options for `Memory.listChat()`. */
export interface ListChatOptions {
  /**
   * Tri-state consolidation filter. `true` returns only consolidated records;
   * `false` returns only non-consolidated; omitted returns all.
   */
  consolidated?: boolean;
  /** Exact status filter (e.g. "saved", "processing", "failed"). */
  status?: string;
}

// ── get_grounding ────────────────────────────────────────────

/** The target record of a grounding lookup. */
export interface GroundingTarget {
  id: number;
  type: string;
  summary: string;
  uuid: string;
}

/**
 * An anchor (source episodic record) discovered during grounding BFS.
 *
 * Junior Tip [contract, verified against server/handler/record_grounding.go]:
 * `content` is omitempty and holds ONLY whitelisted keys
 * ("user"/"assistant"/"full_text"); it is null/absent when the .gz is missing
 * or unparseable. `session_position` is omitempty (absent when unknown).
 */
export interface GroundingAnchor {
  id: number;
  type: string;
  uuid: string;
  summary: string;
  content?: Record<string, string>;
  hops_from_target: number;
  session_position?: number;
}

/** A consolidation node discovered during grounding BFS. */
export interface GroundingConsolidation {
  id: number;
  uuid: string;
  summary: string;
  hops_from_target: number;
}

/**
 * Response from GET /api/v1/records/{id}/grounding.
 *
 * Junior Tip [contract]: `anchors` and `consolidations` are ALWAYS present
 * arrays (may be empty []). `found_count = len(anchors) + len(consolidations)`.
 * The cap flags are omitempty and use the JSON keys `anchors_capped` /
 * `consolidations_capped`.
 */
export interface GroundingResult {
  target: GroundingTarget;
  anchors: GroundingAnchor[];
  consolidations: GroundingConsolidation[];
  depth_used: number;
  max_depth: number;
  found_count: number;
  anchors_capped?: boolean;
  consolidations_capped?: boolean;
}

// ── create() full-fidelity ───────────────────────────────────

/**
 * Options for `Memory.create()` — the full-fidelity record-create surface.
 *
 * Junior Tip [SDK parity, 2026-06-18]: `create()` writes verbatim to
 * POST /api/v1/records (NOT the ingest pipeline), so every field is persisted
 * exactly as supplied — unlike `add()`, whose cloud-ingest path owns its own
 * type/score. Mirrors the Go `Create` opts (type/score/related_ids/valid_from)
 * and the Python `create(req)`. All optional; `type` defaults to "episodic"
 * and `score` to 5, matching the other two SDKs.
 */
export interface CreateOptions {
  /** Session UUID to place the record under. Defaults to the current session. */
  sessionUuid?: string;
  /** Memory type (default "episodic"). Written verbatim. */
  type?: MemoryType;
  /** Importance rating 1-10 (default 5). Written verbatim. */
  score?: number;
  /** Parent/sibling record IDs to attach as `related_ids`. */
  relatedIds?: number[];
  /** RFC3339 UTC start of the record's validity window (`valid_from`). */
  validFrom?: string;
  /** RFC3339 UTC end of the record's validity window (`valid_until`). */
  validUntil?: string;
  /** Caller-supplied metadata, merged under the canonical container_tag. */
  metadata?: Record<string, unknown>;
}

// ── Error types ──────────────────────────────────────────────

/** Base error for all AnhurDB SDK errors. */
export class AnhurError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "AnhurError";
  }
}

/** Raised when authentication fails (invalid API key, expired token). */
export class AnhurAuthError extends AnhurError {
  constructor(message: string) {
    super(message);
    this.name = "AnhurAuthError";
  }
}

/** Raised when a request is malformed or rejected by the server. */
export class AnhurQueryError extends AnhurError {
  constructor(message: string) {
    super(message);
    this.name = "AnhurQueryError";
  }
}

/** Raised when the SDK cannot reach the AnhurDB server. */
export class AnhurConnectionError extends AnhurError {
  constructor(message: string) {
    super(message);
    this.name = "AnhurConnectionError";
  }
}
