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
}

// ── search() ─────────────────────────────────────────────────

/** Options for `Memory.search()`. */
export interface SearchOptions {
  /** Maximum results to return (default 10). */
  limit?: number;
  /** Filter by memory type. */
  typeFilter?: MemoryType;
}

/** A single search hit returned by `Memory.search()`. */
export interface SearchResult {
  id: number;
  type: string;
  summary: string;
  /** Similarity score (0-1). */
  score: number;
  metadata?: string;
  content?: string;
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

/** A full record as returned by the AnhurDB API. */
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
}

/** Result of a graph walk starting from a given record. */
export interface WalkResult {
  nodes: Array<{ id: number; type: string; summary: string; weight: number }>;
  edges: Array<{ source: number; target: number; type: string }>;
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

/** Payload sent to POST /api/v1/records (OSS fallback). */
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
}

/** Payload sent to POST /api/v1/search/global (cross-session search). */
export interface SearchPayload {
  query: string;
  text: string;
  limit: number;
  type_filter?: string;
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
