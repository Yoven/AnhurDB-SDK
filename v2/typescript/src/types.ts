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
 */

// ── Memory types (cognitive epistemology) ────────────────────

/**
 * Cognitive memory types defined by the AnhurDB epistemology (13 types).
 *
 * The authority is AnhurCore's `core.yaml`, mirrored by the server's
 * `schema.MemoryTypes` and the MCP `list_types` tool — not this union. Because
 * this is a closed union, a type the server serves but that is missing here is a
 * COMPILE ERROR for the caller, not a runtime warning: it is the strictest of
 * the three SDKs, so it drifts loudest. Keep Go, Python, TypeScript and
 * `core.yaml` in step, in the same change.
 */
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
  | "file"
  /** Macro-theme backbone: a hub of hubs (ADR-0005). */
  | "router";

/** Lifecycle status of a cognitive record. */
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
   * Server URL. Defaults to `https://anhurdb.yoven.ai`.
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
   * Write path: `ingest` (POST /api/v1/ingest — default) or `regular`
   * (POST /api/v1/records as episodic). Session-first servers require
   * {@link Memory.createSession} before either path succeeds.
   */
  mode?: "ingest" | "regular";
  /**
   * Caller-supplied metadata.
   *
   * three SDKs expose the identical capability — `add(text, {score, type,
   * metadata})`. The SDK merges it into the canonical
   * `{"container_tag": "..."}` envelope before sending so it never
   * overwrites the container tag (see the 2026-05-22 metadata corruption
   * incident). Server stores `metadata` as a JSON string.
   */
  metadata?: Record<string, unknown>;
  /**
   * Pins the SESSION (uuid) the ingested record lands in. The tenant comes from
   * the API key; the session is the caller's unit of conversation.
   *
   * Omit to use the client's current sessionUuid (from constructor, newSession,
   * or createSession). Writes require createSession on session-first servers.
   */
  sessionId?: string;
}

/** Options for `Memory.createSession()`. */
export interface CreateSessionOptions {
  /** Session uuid to register. Omit to use the client's current sessionUuid. */
  sessionId?: string;
  /** Optional session-level metadata copied onto every record in this session. */
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

/** Search scope planes for POST /api/v1/search. */
export type SearchScope =
  | "sessions"
  | "tenant_shared"
  | "client_shared"
  | "shared_all";

/** Options for `Memory.search()`. */
export interface SearchOptions {
  /** Maximum results to return (default 10). */
  limit?: number;
  /** Filter by memory type. */
  typeFilter?: MemoryType;
  /** Search plane (default `sessions`). */
  scope?: SearchScope;
  /**
   * Desliga a perna vetorial da consulta (busca só léxico+SimHash) — a perna
   * "keyword" das medições. Paridade REST/MCP/Go/Py (2026-08-07).
   */
  skipQueryEmbed?: boolean;
  /**
   * Desliga o rerank cognitivo (recência/tipo/peso — ADR-0011 A3), mantendo o
   * score RRF puro.
   */
  skipCognitiveRerank?: boolean;
  /**
   * Overrides `ANHUR_SEARCH_ASTAR_WEIGHT` for THIS query only (ablation / A-B
   * sweep, no server restart needed). `undefined` (the default — field not
   * sent) keeps the server's configured weight; an explicit `0` rescales the
   * A* leg to zero contribution for this query only, WITHOUT touching the
   * server-wide default. Whether the A* arm runs AT ALL still comes from the
   * server-side `ANHUR_SEARCH_ASTAR` toggle — this can only rescale an
   * already-enabled leg, never turn it on for a tenant where it is off.
   * Mirrors `model.SearchRequest.AstarWeight` (`*float64`, same nil-vs-zero
   * contract) in `AnhurDB/server/model/record.go`.
   */
  astarWeight?: number;
  /**
   * Overrides `ANHUR_ENTITY_JACCARD_WEIGHT` for THIS query only. Same
   * nil-vs-zero contract as {@link astarWeight}: `undefined` preserves the
   * server default, `0` explicitly zeroes the leg for this query. Only takes
   * effect when `ANHUR_ENTITY_JACCARD_ENABLED` is already true server-side.
   */
  entityJaccardWeight?: number;
  /**
   * Opt-in, budget-bounded expansion (ADR-0021): when `true`, each top-K hit
   * that survives final ranking gets a small summary of its DIRECTLY
   * connected graph nodes (depth 1, `related_ids`/`main_ids`) attached as
   * {@link SearchResult.related_nodes}. Reuses the same A-star / WalkAdmission
   * session/plane boundary the A* RRF leg already enforces — it can never
   * surface a record outside the caller's session scope. Omitted from the
   * wire payload when `false`/unset (default false = zero extra cost, byte
   * -identical response to today). Mirrors `model.SearchRequest.ExpandRelated`
   * in `AnhurDB/server/model/record.go`; Go `WithExpandRelated()` / Python
   * `expand_related=True` are the equivalent knobs in the other two SDKs.
   */
  expandRelated?: boolean;
}

/**
 * A single search hit returned by every search method
 * (`search`/`searchSession`/`searchByType`/`recall`).
 *
 * the server emits —
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
  /**
   * The plane that produced this hit (`sessions` | `tenant_shared` |
   * `client_shared`). Present on every hit — always sent by the server,
   * `omitempty` only trims it when the resolver could not label a leg.
   */
  provenance?: string;
  /**
   * Echoes the REQUEST scope (may read `shared_all` while {@link provenance}
   * names the specific leg that actually produced this hit).
   */
  scope?: string;
  /**
   * Per-hit ablation debug (fts/semantic/simhash ranks, RRF score, raw
   * cosine). Server only populates this when the request carries
   * `debug_signals=true` — a knob this SDK does not yet expose via
   * {@link SearchOptions} (query-string only today, `?debug_signals=true`).
   * Passing `signals` through here fixes the pre-existing bug where the SDK
   * silently dropped it even on requests that DID ask for it via raw query
   * string; it does not by itself make `search()` request debug signals.
   */
  signals?: SearchHitSignals;
  /**
   * Bounded summary of this hit's directly connected graph nodes (depth 1),
   * populated ONLY when the request set {@link SearchOptions.expandRelated}
   * to `true` (ADR-0021). Absent/undefined is not an error: it means either
   * `expand_related` was false/unset, the caller's SDK/server predates
   * ADR-0021, or the budget-bounded walk found nothing in scope for this hit
   * (an empty `[]` vs. `undefined` both read as "nothing to show"). Field
   * name kept snake_case (`related_nodes`, matching `provenance`/`scope`
   * above already being plain server keys and {@link SearchHitSignals}'s own
   * `fts_rank`-style fields) — this interface mirrors the server's wire JSON
   * verbatim, the same convention {@link MemoryRecord} uses for
   * `related_ids`/`main_ids`.
   */
  related_nodes?: RelatedNode[];
}

/**
 * A bounded, admission-filtered summary of one node connected to a search
 * hit's `related_ids`/`main_ids` graph edges (ADR-0021, `expand_related`).
 *
 * Deliberately excludes `content` and every internal `Record` field — this
 * is a SUMMARY projection, not a full {@link MemoryRecord}; attaching N full
 * records per hit would multiply the response payload. Mirrors
 * `model.RelatedNode` in `AnhurDB/server/model/record.go` and
 * AnhurCore's `RelatedNode` proto message field-for-field.
 */
export interface RelatedNode {
  id: number;
  type: string;
  summary: string;
  weight: number;
}

/**
 * Per-hit ablation debug attached to a {@link SearchResult} (ADR-0012).
 * Mirrors `model.SearchHitSignals` in `AnhurDB/server/model/record.go`
 * exactly — server field names, snake_case, all `omitempty`.
 */
export interface SearchHitSignals {
  fts_rank?: number;
  semantic_rank?: number;
  simhash_rank?: number;
  simhash_hamming?: number;
  rrf_score?: number;
  semantic_cosine?: number;
}

/**
 * Retrieval diagnostics for one search response (ADR-0012): which arms ran,
 * whether semantic degraded, and the RESOLVED weights actually used (after
 * any per-request {@link SearchOptions.astarWeight} /
 * {@link SearchOptions.entityJaccardWeight} override was applied). Mirrors
 * `model.RetrievalMeta` in `AnhurDB/server/model/record.go` field-for-field.
 *
 * Junior Tip [why this is not on `search()`'s return type]: `search()` keeps
 * returning `SearchResult[]` for backward compatibility — existing callers
 * destructure/iterate the array directly and would break if it became
 * `{results, retrieval}`. Use {@link Memory.searchWithRetrieval} instead when
 * this diagnostic block is needed.
 */
export interface RetrievalMeta {
  mode: string;
  signals_used: string[];
  semantic_attempted: boolean;
  semantic_used: boolean;
  degraded: boolean;
  reason?: string;
  elapsed_ms: number;
  content_simhash_enabled: boolean;
  content_simhash_weight: number;
  astar_enabled: boolean;
  astar_weight: number;
  entity_jaccard_enabled: boolean;
  entity_jaccard_weight: number;
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
 * exact JSON keys the server serialises (internal fields are `json:"-"` and
 * never appear). The optional fields are omitempty server-side: `content` only
 * appears on history endpoints; `superseded_by`/`valid_from`/`valid_until`
 * only when set.
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
}

/** Result of a graph walk starting from a given record. */
export interface WalkResult {
  nodes: Array<{ id: number; type: string; summary: string; weight: number }>;
  edges: Array<{ source: number; target: number; type: string }>;
}

/**
 * Goal-directed steering mode for {@link WalkSemanticOptions.target}.
 *
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
 * (`Target`/`GoalVector`/`TargetTag`/`MaxCost`) and the Python SDK
 * (`target`/`goal_vector`/`target_tag`/`max_cost`). `goalVector` is raw bytes
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
 * A named entity in the AnhurDB knowledge graph (Layer 2).
 *
 * Entities are real-world objects (people, organisations, concepts) linked to
 * memory records. Wire field is `entity_type` — not `record.type`
 * (episodic / fact / decision). The cross-layer link is the “tag”.
 */
export interface EntityRecord {
  id: number;
  name: string;
  entity_type: string;
  summary?: string;
  attributes?: Record<string, unknown>;
  dimension?: number;
  first_seen?: string;
  last_seen?: string;
  mention_count?: number;
  weight?: number;
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
  record_id?: number;
  id?: number;
  status?: string;
  filename?: string;
  uuid?: string;
}

/** Result from upload status polling. */
export interface UploadStatusResult {
  record_id?: number;
  id?: number;
  /** "processing", "completed", "saved", or "failed". */
  status: string;
  completed?: boolean;
  filename?: string;
  error?: string;
  summary?: string;
  metadata?: string;
  record_ids?: number[];
}

// ── Batch Operations ─────────────────────────────────────────

/** Result from batch status update. */
export interface BatchUpdateResult {
  updated_count: number;
}

// ── Delete file (whole ingested document) ────────────────────

/**
 * Response of `DELETE /api/v1/records/by-file` — apagar TODO o rastro de um
 * arquivo ingerido (root + capítulos + satélites) de uma sessão.
 *
 * `matched_count` é o que o prefixo ENCONTROU; `deleted_count` é o que o
 * cluster realmente apagou. Em dry-run só `matched_count` é preenchido: nada
 * foi escrito, então `deleted_count` fica 0 de propósito.
 *
 * Junior Tip [por que a contagem faz parte do contrato]: "apaguei 0 registros"
 * tem de ser visível para o chamador. Um método que devolvesse `void`
 * transformaria um prefixo errado em sucesso silencioso — e perda silenciosa é
 * a falha número um deste projeto.
 *
 * `deleted_ids` e `raft_index` são OPCIONAIS porque o servidor os emite com
 * `omitempty`: em dry-run, ou quando nada casou, as chaves não existem no JSON.
 * Ausência aqui é informação legítima, não erro de parse.
 */
export interface DeleteFileResult {
  session_uuid: string;
  ingest_key_prefix: string;
  matched_count: number;
  deleted_count: number;
  deleted_ids?: number[];
  dry_run: boolean;
  raft_index?: number;
}

/** Optional arguments of `Memory.deleteFile`. */
export interface DeleteFileOptions {
  /**
   * Count only, write nothing.
   *
   * Junior Tip [dry-run é a rede de segurança, não um detalhe de debug]: a
   * interface mostra "isto vai apagar 511 registros" ANTES de o usuário
   * confirmar. Apagar um documento inteiro é operação de mão pesada; a contagem
   * prévia é o que separa "removi a edição velha da lei" de "removi a
   * biblioteca".
   */
  dryRun?: boolean;
}

// ── Internal / wire-level types ──────────────────────────────

/**
 * Payload sent to POST /api/v1/ingest (cloud mode).
 *
 * Wire contract: `content` + `container_tag` + required `session_id`.
 * Create the session first via POST /api/v1/sessions. Callers that pin
 * score/type/metadata use `/api/v1/records` instead — matching Go and Python.
 */
export interface IngestPayload {
  content: string;
  container_tag: string;
  /** Session UUID from POST /api/v1/sessions — required on every ingest. */
  session_id: string;
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
   * persist a bitemporal window verbatim, matching Go/Python `create`.
   */
  valid_from?: string;
  /** RFC3339 UTC end of the temporal validity window. Omitted when unset. */
  valid_until?: string;
}

/**
 * Payload sent to POST /api/v1/search (scope boundary + session subset).
 *
 * `sessions` is required on the wire (ADR-0014) and is ALWAYS an array, even
 * for the wildcard: one type, one code path, no `oneOf` in the schema — an
 * agent generating JSON from the schema errs less when the type is fixed. The
 * retired singular `uuid` is deliberately absent: it could only express "one
 * session or none" and two of the four search paths dropped it in silence.
 */
export interface SearchPayload {
  text: string;
  limit: number;
  scope: SearchScope;
  /** `["*"]` for every session in the scope, or up to 1000 explicit uuids. */
  sessions: string[];
  type_filter?: string;
  /** Omitido quando false — preserva o default do servidor. */
  skip_query_embed?: boolean;
  skip_cognitive_rerank?: boolean;
  /**
   * Omitido quando `undefined` — mesma disciplina nil-vs-zero de
   * `model.SearchRequest.AstarWeight` no servidor (Go `*float64`): um `0`
   * explícito precisa chegar como `0` no JSON, nunca ser confundido com
   * "campo não enviado".
   */
  astar_weight?: number;
  /** Omitido quando `undefined`. Mesmo contrato nil-vs-zero acima. */
  entity_jaccard_weight?: number;
  /** Omitido quando `false`/`undefined` — default false = zero custo extra. */
  expand_related?: boolean;
}

/**
 * Payload sent to POST /api/v1/search for a single-session query.
 *
 * Same wire shape as {@link SearchPayload} with `scope` pinned to `sessions`;
 * the one-chat case is just `sessions: [uuid]`.
 */
export interface SearchSessionPayload {
  sessions: string[];
  text?: string;
  type_filter?: string;
  limit?: number;
  scope: "sessions";
}

// ── query() — AST query engine ───────────────────────────────

/**
 * Comparison operators supported by the AST query engine.
 *
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
 * limit/offset/has_more — the entire matching set for the session is returned.
 * `content` is omitted (metadata only, not the .gz body).
 */
export interface ListChatResult {
  records: MemoryRecord[];
  count: number;
}

/** Options for `Memory.manifestGlobal()`. */
export interface ManifestGlobalOptions {
  /** Keyword filter. Sent as the `q` query param. */
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
  /** Keyword filter scoped to the session. Sent as `q`. */
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
 * POST /api/v1/records (NOT the ingest pipeline), so every field is persisted
 * exactly as supplied — unlike `add()`, whose cloud-ingest path owns its own
 * type/score. Mirrors the Go `Create` opts (type/score/related_ids/valid_from)
 * and the Python `create(req)`. All optional; `type` defaults to "episodic"
 * and `score` to 5, matching the other two SDKs.
 */
export interface CreateOptions {
  /** Session UUID to place the record under. Defaults to the current session. */
  sessionUuid?: string;
  /** Alias for sessionUuid (same meaning as ingest add options.sessionId). */
  sessionId?: string;
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

/** Base error for all AnhurDB SDK errors.
 *
 * `statusCode` carries the HTTP status when the error came from an HTTP
 * response (undefined otherwise) — callers branch on the REAL status instead
 * of parsing the message (e.g. `waitForUpload` treats a transient 404 as
 * "pending"). Additive and backward-compatible. */
/** Failure classification, so callers branch on meaning instead of on strings. */
export type AnhurErrorKind =
  | "auth"
  | "invalid_request"
  | "not_found"
  | "conflict"
  | "rate_limited"
  | "unavailable"
  | "timeout"
  | "transport"
  | "server";

const RETRYABLE_KINDS: ReadonlySet<AnhurErrorKind> = new Set([
  "rate_limited",
  "unavailable",
  "timeout",
  "transport",
  "server",
]);

/** Classify an HTTP status. `undefined` means the request never reached the server. */
export function kindForStatus(statusCode?: number): AnhurErrorKind {
  if (statusCode === undefined) return "transport";
  if (statusCode === 401 || statusCode === 403) return "auth";
  if (statusCode === 404) return "not_found";
  if (statusCode === 409) return "conflict";
  if (statusCode === 429) return "rate_limited";
  if (statusCode === 503) return "unavailable";
  if (statusCode >= 400 && statusCode < 500) return "invalid_request";
  return "server";
}

export class AnhurError extends Error {
  readonly statusCode?: number;
  readonly kind: AnhurErrorKind;
  /** Whether repeating the same call could give a different result.
   *
   * Junior Tip [not the same as "safe to retry"]: a timeout on a WRITE means
   * the server may or may not have committed it. The SDK never auto-retries
   * writes — idempotency is the caller's decision. See SDK_ERROR_CONTRACT.md. */
  readonly retryable: boolean;

  constructor(message: string, statusCode?: number, kind?: AnhurErrorKind) {
    const resolvedKind = kind ?? kindForStatus(statusCode);
    // A message is never empty: an unexplained failure is unactionable.
    super(message || defaultMessageFor(resolvedKind, statusCode));
    this.name = "AnhurError";
    this.statusCode = statusCode;
    this.kind = resolvedKind;
    this.retryable = RETRYABLE_KINDS.has(resolvedKind);
  }
}

function defaultMessageFor(kind: AnhurErrorKind, statusCode?: number): string {
  if (kind === "timeout")
    return "request timed out (the server may still have processed it)";
  if (kind === "transport") return "could not reach AnhurDB";
  if (kind === "unavailable") return "service temporarily unavailable — retry";
  return statusCode !== undefined
    ? `AnhurDB request failed (HTTP ${statusCode})`
    : "AnhurDB request failed";
}

/** Raised when authentication fails (invalid API key, expired token). */
export class AnhurAuthError extends AnhurError {
  constructor(message: string, statusCode?: number) {
    super(message, statusCode);
    this.name = "AnhurAuthError";
  }
}

/** Raised when a request is malformed or rejected by the server. */
export class AnhurQueryError extends AnhurError {
  constructor(message: string, statusCode?: number) {
    super(message, statusCode);
    this.name = "AnhurQueryError";
  }
}

/** Raised when the SDK cannot reach the AnhurDB server. */
export class AnhurConnectionError extends AnhurError {
  constructor(message: string, statusCode?: number, kind: AnhurErrorKind = "transport") {
    super(message, statusCode, kind);
    this.name = "AnhurConnectionError";
  }
}

/** Raised by `waitForUpload` when the upload did not reach a terminal status
 * within the timeout. Parity: Go `ErrUploadWaitTimeout` / Python
 * `AnhurUploadWaitTimeout`. */
export class AnhurUploadWaitTimeout extends AnhurError {
  constructor(message: string) {
    super(message);
    this.name = "AnhurUploadWaitTimeout";
  }
}
