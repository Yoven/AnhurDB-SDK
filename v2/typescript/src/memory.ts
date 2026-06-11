/**
 * AnhurDB TypeScript SDK — Memory class.
 *
 * This is the "I just want memory to work" wrapper. It hides all
 * internals (vectors, binary quantisation, UUIDs, dimensions) behind
 * a clean API surface.
 *
 * **Core methods** (match Python & Go SDKs exactly):
 *   - `add()`     — store a memory
 *   - `search()`  — find relevant memories
 *   - `profile()` — get user/agent profile
 *
 * **Extended methods** (full AnhurDB surface):
 *   - Batch: `batchReadContent()`, `batchUpdateStatus()`
 *   - Entities: `searchEntities()`, `upsertEntity()`, `entityGraph()`,
 *     `entityTimeline()`, `upsertEntityEdge()`, `linkRecordEntity()`
 *   - Upload: `uploadFile()`, `uploadStatus()`
 *   - Temporal: `supersede()`
 *   - Graph: `walk()`, `walkSemantic()`
 *   - Session: `listSessions()`, `getSessionHistory()`, `getSessionClusters()`
 *
 * Usage:
 *   ```ts
 *   import { Memory } from "anhurdb";
 *
 *   const mem = new Memory({ apiKey: "anhur_xxx" });
 *   await mem.add("User said: I'm a data scientist at Google");
 *   const ctx = await mem.search("what does this user do?");
 *   const profile = await mem.profile();
 *   ```
 *
 * @module
 */

import { HttpClient } from "./client.js";
import type {
  AddOptions,
  AddResult,
  BatchUpdateResult,
  ContextResult,
  EntityGraphResult,
  EntityRecord,
  EntityTimelineResult,
  IngestPayload,
  MemoryOptions,
  MemoryRecord,
  MemoryType,
  ProfileResult,
  RecordPayload,
  SearchOptions,
  SearchPayload,
  SearchResult,
  SessionStats,
  UploadResult,
  UploadStatusResult,
  UpsertEntityEdgeOptions,
  UpsertEntityOptions,
  WalkResult,
} from "./types.js";

/** Default cloud endpoint. Self-hosted users pass `url` explicitly. */
const DEFAULT_CLOUD_URL = "https://api.anhurdb.com";

/**
 * Derive a short, stable hex tag from a string using the Web Crypto
 * SubtleCrypto API (available in Node 18+ and all modern runtimes).
 *
 * Falls back to a simple DJB2 hash when SubtleCrypto is unavailable
 * (e.g. some edge runtimes without `globalThis.crypto`).
 *
 * Junior Tip: Must match the Python and Go SDKs:
 *   Python: hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:12]
 *   Go:     hex.EncodeToString(sha256.Sum256([]byte(apiKey)))[:12]
 */
async function deriveTag(input: string): Promise<string> {
  try {
    const encoder = new TextEncoder();
    const data = encoder.encode(input);
    const hashBuffer = await crypto.subtle.digest("SHA-256", data);
    const hashArray = Array.from(new Uint8Array(hashBuffer));
    const hex = hashArray
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    return hex.slice(0, 12);
  } catch {
    // Fallback: simple DJB2 hash (for runtimes without SubtleCrypto).
    let hash = 5381;
    for (let i = 0; i < input.length; i++) {
      hash = ((hash << 5) + hash + input.charCodeAt(i)) >>> 0;
    }
    return hash.toString(16).padStart(8, "0").slice(0, 12);
  }
}

/** Format a Date as `YYYYMMDD-HHMMSS` in UTC. */
function utcTimestamp(d: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getUTCFullYear()}${pad(d.getUTCMonth() + 1)}${pad(d.getUTCDate())}` +
    `-${pad(d.getUTCHours())}${pad(d.getUTCMinutes())}${pad(d.getUTCSeconds())}`
  );
}

/**
 * Wrap a container tag into the canonical metadata JSON envelope
 * `{"container_tag":"<tag>"}`.
 *
 * Junior Tip [metadata corruption parity, 2026-05-22]: every record-create
 * path historically wrote `metadata` as the bare container_tag string
 * ("mem-3f9...") instead of a JSON object. On the server that poisoned every
 * downstream agent running `JSON.parse(metadata)` — entity taggers logged
 * `tagged_no_entities` and a one-shot repair had to fix 516 corrupted records.
 * Go and Python SDKs carry the identical fix (buildMetadataJSON /
 * _build_metadata_json). ALL THREE SDKs MUST stay byte-identical here — see
 * the SDK-sync rule in project memory. Returns "{}" when the tag is empty.
 */
function buildMetadataJson(
  containerTag: string,
  extra?: Record<string, unknown>,
): string {
  // Junior Tip [score/type/metadata parity, 2026-06]: caller metadata is
  // merged UNDER the container_tag so the canonical tag can never be
  // clobbered by a caller key named "container_tag" — that exact overwrite
  // is what corrupted 516 records in 2026-05-22. Spread extra first, then
  // force container_tag last.
  if (!containerTag && !extra) return "{}";
  const merged: Record<string, unknown> = { ...(extra ?? {}) };
  if (containerTag) merged.container_tag = containerTag;
  return JSON.stringify(merged);
}

/**
 * Dead-simple memory interface for AnhurDB.
 *
 * Handles session management, container tagging, and fallback
 * between cloud (`/api/v1/ingest`) and OSS (`/api/v1/records`)
 * automatically.
 */
export class Memory {
  private readonly client: HttpClient;
  private readonly containerTag: string;
  private sessionUuid: string;
  private ingestAvailable: boolean | null = null;
  private tagReady: Promise<void>;

  /**
   * Create a new Memory instance.
   *
   * @param options - Connection options (apiKey is required).
   *
   * @example
   * ```ts
   * // Cloud
   * const mem = new Memory({ apiKey: "anhur_xxx" });
   *
   * // Self-hosted
   * const mem = new Memory({
   *   url: "http://localhost:8000",
   *   apiKey: "my-key",
   * });
   * ```
   */
  constructor(options: MemoryOptions) {
    if (!options.apiKey) {
      throw new Error("apiKey is required");
    }

    const baseUrl = options.url ?? DEFAULT_CLOUD_URL;
    this.client = new HttpClient(baseUrl, options.apiKey, options.tenantId);

    // Container tag: explicit userId or derived from apiKey.
    if (options.userId) {
      this.containerTag = options.userId;
      this.sessionUuid = `${this.containerTag}-${utcTimestamp()}`;
      this.tagReady = Promise.resolve();
    } else {
      // Temporary tag — replaced once the async hash resolves.
      this.containerTag = "mem-init";
      this.sessionUuid = "";
      this.tagReady = deriveTag(options.apiKey).then((hash) => {
        Object.defineProperty(this, "containerTag", {
          value: `mem-${hash}`,
          writable: false,
          configurable: false,
        });
        this.sessionUuid = `${this.containerTag}-${utcTimestamp()}`;
      });
    }
  }

  // ── Public properties ────────────────────────────────────────

  /**
   * Current session UUID.
   *
   * If no userId was provided, containerTag is derived async from the
   * API key hash. The sessionUuid depends on containerTag, so it may be
   * empty until tagReady resolves. Use `getSessionId()` for safety.
   */
  get sessionId(): string {
    return this.sessionUuid;
  }

  /** Async-safe session ID getter (waits for tag derivation). */
  async getSessionId(): Promise<string> {
    await this.tagReady;
    return this.sessionUuid;
  }

  // ══════════════════════════════════════════════════════════════
  // CORE METHODS (match Python & Go exactly)
  // ══════════════════════════════════════════════════════════════

  // ── add() — store a memory ──────────────────────────────────

  /**
   * Add a memory. Simplest way to store information.
   *
   * Tries the cloud `/api/v1/ingest` endpoint first (which handles
   * embedding + extraction automatically). If that returns 404,
   * falls back to `/api/v1/records` (OSS mode, stores as text).
   *
   * @param text    - The text to remember.
   * @param options - Optional score (1-10) and memory type.
   * @returns A result containing the session ID and created records.
   *
   * @example
   * ```ts
   * const result = await mem.add("I'm a data scientist at Google");
   * console.log(result.sessionId, result.records);
   * ```
   */
  async add(text: string, options?: AddOptions): Promise<AddResult> {
    if (!text) {
      throw new Error("text cannot be empty");
    }
    await this.tagReady;

    const score = options?.score ?? 5;
    const type: MemoryType = options?.type ?? "episodic";
    const metadata = options?.metadata;

    // Try cloud ingest first (has auto-embedding).
    if (this.ingestAvailable !== false) {
      const result = await this.tryIngest(text, score, type, metadata);
      if (result !== null) return result;
    }

    // Fallback: direct record creation (OSS / self-hosted).
    return this.createRecord(text, score, type, metadata);
  }

  // ── search() — find relevant memories ───────────────────────

  /**
   * Search for relevant memories using hybrid (vector + full-text) search.
   *
   * Uses global search (not session-scoped) so it finds facts across
   * ALL sessions for this user.
   *
   * @param query   - Natural language query.
   * @param options - Optional limit and type filter.
   * @returns Array of search results sorted by relevance.
   *
   * @example
   * ```ts
   * const results = await mem.search("what does this user do?", { limit: 5 });
   * results.forEach(r => console.log(r.summary, r.score));
   * ```
   */
  async search(
    query: string,
    options?: SearchOptions,
  ): Promise<SearchResult[]> {
    if (!query) {
      throw new Error("query cannot be empty");
    }
    await this.tagReady;

    const payload: SearchPayload = {
      query: query,
      text: query,
      limit: options?.limit ?? 10,
    };
    if (options?.typeFilter) {
      payload.type_filter = options.typeFilter;
    }

    const data = await this.client.post<{
      results?: Array<{
        record?: Record<string, unknown>;
        similarity?: number;
      }>;
    }>("/api/v1/search/global", payload);

    return this.flattenSearchResults(data.results);
  }

  // ── profile() — get user/agent profile ──────────────────────

  /**
   * Get the memory profile for this container tag (user/agent).
   *
   * Returns profile information including static facts, dynamic state,
   * and aggregate statistics. If the server does not support profiles
   * (OSS without agents), returns an empty profile rather than throwing.
   *
   * @example
   * ```ts
   * const profile = await mem.profile();
   * console.log(profile.static, profile.stats);
   * ```
   */
  async profile(): Promise<ProfileResult> {
    await this.tagReady;

    try {
      const data = await this.client.get<ProfileResult>("/api/v1/profile", {
        tag: this.containerTag,
      });
      return {
        static: data.static ?? {},
        dynamic: data.dynamic ?? {},
        stats: data.stats ?? {},
      };
    } catch (err: unknown) {
      // If the endpoint doesn't exist (OSS), return empty profile.
      if (err instanceof Error && err.message.includes("404")) {
        return {
          static: {},
          dynamic: {},
          stats: {},
          tag: this.containerTag,
          status: "not_available",
        };
      }
      throw err;
    }
  }

  // ══════════════════════════════════════════════════════════════
  // EXTENDED METHODS — Search & Discovery
  // ══════════════════════════════════════════════════════════════

  /**
   * Search for memories filtered by cognitive type.
   *
   * Faster than semantic search when you know the exact type.
   *
   * @param type  - The memory type to filter by (e.g. "fact", "episodic").
   * @param limit - Maximum results to return (default 20).
   */
  async searchByType(
    type: MemoryType,
    limit?: number,
  ): Promise<SearchResult[]> {
    const params: Record<string, string> = { type };
    if (limit !== undefined) params.limit = String(limit);

    const data = await this.client.get<{ results?: SearchResult[] }>(
      "/api/v1/search/type",
      params,
    );
    return data.results ?? [];
  }

  /**
   * Full-text search with cognitive weight boosting.
   *
   * Uses the DuckDB-backed smart search engine that ranks results by
   * a combination of text relevance and cognitive importance (score).
   *
   * @param query - Search query.
   * @param limit - Maximum results (default 10).
   * @param type  - Optional memory type filter.
   */
  async smartSearch(
    query: string,
    limit?: number,
    type?: MemoryType,
  ): Promise<unknown> {
    const params: Record<string, string> = {
      q: query,
      limit: String(limit ?? 10),
    };
    if (type) params.type = type;

    return this.client.get("/api/v1/search/smart", params);
  }

  /**
   * Recall memories via global search.
   *
   * Explicit alias for `search()` that always uses the global endpoint.
   * Named to match the MCP `recall` tool.
   *
   * @param query - Natural language query.
   * @param limit - Maximum results (default 10).
   */
  async recall(query: string, limit?: number): Promise<SearchResult[]> {
    if (!query) {
      throw new Error("query cannot be empty");
    }

    const payload: SearchPayload = {
      query: query,
      text: query,
      limit: limit ?? 10,
    };
    const data = await this.client.post<{
      results?: Array<{
        record?: Record<string, unknown>;
        similarity?: number;
      }>;
    }>("/api/v1/search/global", payload);

    return this.flattenSearchResults(data.results);
  }

  /**
   * Fetch the most recent records from the manifest.
   *
   * @param limit - Maximum records to return (default 20).
   */
  async recent(limit?: number): Promise<MemoryRecord[]> {
    const params: Record<string, string> = {};
    if (limit !== undefined) params.limit = String(limit);

    const data = await this.client.get<{ records?: MemoryRecord[] }>(
      "/api/v1/manifest",
      params,
    );
    return data.records ?? [];
  }

  // ══════════════════════════════════════════════════════════════
  // EXTENDED METHODS — Graph Traversal
  // ══════════════════════════════════════════════════════════════

  /**
   * Walk the memory graph starting from a given record (BFS).
   *
   * @param startId - The record ID to start the walk from.
   * @param depth   - How many hops to traverse (default 3).
   */
  async walk(startId: number, depth?: number): Promise<WalkResult> {
    const payload = {
      seed_id: startId,
      depth: depth ?? 3,
      direction: "both",
    };
    return this.client.post<WalkResult>("/api/v1/walk", payload);
  }

  /**
   * Semantic graph walk — follows edges weighted by vector similarity
   * rather than just structural edges.
   *
   * @param startId - The record ID to start from.
   * @param depth   - How many hops (default 3).
   */
  async walkSemantic(startId: number, depth?: number): Promise<WalkResult> {
    const payload = {
      seed_id: startId,
      depth: depth ?? 3,
    };
    return this.client.post<WalkResult>("/api/v1/walk/semantic", payload);
  }

  /**
   * Get the topological context (neighbours) around a specific record.
   *
   * @param recordId - The record ID to inspect.
   */
  async getContext(recordId: number): Promise<ContextResult> {
    return this.client.get<ContextResult>(
      `/api/v1/records/${recordId}/topology`,
    );
  }

  /**
   * Read the full content body of a specific record.
   *
   * @param recordId - The record ID whose content to retrieve.
   */
  async readContent(recordId: number): Promise<string> {
    // Junior Tip [parity-fix 2026-06-11]: GET /content responde text/plain cru
    // (não JSON). getText devolve o corpo verbatim — antes, get<{content}> via
    // JSON.parse falhava, embrulhava em {message} e isto retornava "" (perda
    // total do conteúdo). Agora alinhado com Go (raw bytes) e Python (raw_text).
    return this.client.getText(`/api/v1/records/${recordId}/content`);
  }

  // ══════════════════════════════════════════════════════════════
  // EXTENDED METHODS — Session Management
  // ══════════════════════════════════════════════════════════════

  /**
   * Start a new session (generates a fresh UUID).
   *
   * @returns The new session ID.
   */
  async newSession(): Promise<string> {
    await this.tagReady;
    this.sessionUuid = `${this.containerTag}-${utcTimestamp()}`;
    return this.sessionUuid;
  }

  /**
   * List all sessions with aggregate statistics.
   */
  async listSessions(): Promise<SessionStats[]> {
    const data = await this.client.get<{
      sessions?: SessionStats[];
    }>("/api/v1/sessions/stats");
    return data.sessions ?? [];
  }

  /**
   * Get paginated full-text history for a session.
   *
   * Returns actual message content, unlike `listSessions` which
   * returns metadata only.
   *
   * @param sessionUuid - The session UUID.
   * @param limit       - Max records per page (default 50).
   * @param offset      - Pagination offset (default 0).
   */
  async getSessionHistory(
    sessionUuid: string,
    limit?: number,
    offset?: number,
  ): Promise<unknown> {
    const params: Record<string, string> = {};
    if (limit !== undefined) params.limit = String(limit);
    if (offset !== undefined) params.offset = String(offset);

    return this.client.get(
      `/api/v1/sessions/${sessionUuid}/history`,
      Object.keys(params).length > 0 ? params : undefined,
    );
  }

  /**
   * Get mathematically clustered topological groups for a session.
   *
   * Uses BSQ vectors and DBSCAN to identify thematic clusters.
   *
   * @param sessionUuid - The session UUID.
   */
  async getSessionClusters(sessionUuid: string): Promise<unknown> {
    return this.client.get(`/api/v1/sessions/${sessionUuid}/clusters`);
  }

  // ══════════════════════════════════════════════════════════════
  // EXTENDED METHODS — Record CRUD
  // ══════════════════════════════════════════════════════════════

  /**
   * Update fields on an existing record.
   *
   * @param recordId - The record ID to update.
   * @param fields   - Partial fields to update.
   */
  async update(
    recordId: number,
    fields: Partial<{
      summary: string;
      metadata: string;
      status: string;
      type: string;
      score: number;
    }>,
  ): Promise<void> {
    await this.client.patch(`/api/v1/records/${recordId}`, fields);
  }

  /**
   * Delete a specific record by ID (hard delete).
   *
   * For soft delete, use `update(id, { status: "archived" })` instead.
   *
   * @param recordId - The record ID to delete.
   */
  async delete(recordId: number): Promise<void> {
    await this.client.delete(`/api/v1/records/${recordId}`);
  }

  // ══════════════════════════════════════════════════════════════
  // BATCH OPERATIONS
  // ══════════════════════════════════════════════════════════════

  /**
   * Fetch full content for multiple records in a single call (max 100).
   *
   * Eliminates the N+1 pattern of calling `readContent()` in a loop.
   *
   * @param ids - Array of record IDs (max 100).
   * @returns Map of `record_id → content_payload`.
   */
  async batchReadContent(
    ids: number[],
  ): Promise<Record<string, unknown>> {
    return this.client.post<Record<string, unknown>>(
      "/api/v1/records/batch-content",
      { ids },
    );
  }

  /**
   * Update status for multiple records at once.
   *
   * @param ids    - Array of record IDs.
   * @param status - Target status (e.g. "consolidated", "archived").
   */
  async batchUpdateStatus(
    ids: number[],
    status: string,
  ): Promise<BatchUpdateResult> {
    return this.client.patch<BatchUpdateResult>(
      "/api/v1/records/mark-consolidated",
      { ids, status },
    );
  }

  // ══════════════════════════════════════════════════════════════
  // TEMPORAL VERSIONING
  // ══════════════════════════════════════════════════════════════

  /**
   * Mark an old record as superseded by a new one.
   *
   * The old record remains in the graph but search results prefer the
   * newer version. This implements temporal versioning — a killer
   * feature that no competitor (Mem0, Zep, LangMem) offers.
   *
   * @param oldId - The record being superseded.
   * @param newId - The replacement record.
   */
  async supersede(oldId: number, newId: number): Promise<void> {
    await this.client.post("/api/v1/records/supersede", {
      old_id: oldId,
      new_id: newId,
    });
  }

  // ══════════════════════════════════════════════════════════════
  // FILE UPLOAD
  // ══════════════════════════════════════════════════════════════

  /**
   * Upload a document for async ingestion.
   *
   * Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown,
   * HTML, DOCX.
   *
   * The server processes the file asynchronously — use
   * `uploadStatus()` to poll for completion.
   *
   * @param filename  - Original filename (used for format detection).
   * @param content   - Base64-encoded file content.
   * @param sessionId - Optional session UUID to associate with.
   */
  async uploadFile(
    filename: string,
    content: string,
    sessionId?: string,
  ): Promise<UploadResult> {
    const payload: Record<string, string> = { filename, content };
    if (sessionId) payload.session_id = sessionId;
    return this.client.post<UploadResult>("/api/v1/upload", payload);
  }

  /**
   * Check the processing status of a file upload.
   *
   * @param uploadId - The upload ID returned by `uploadFile()`.
   */
  async uploadStatus(uploadId: number): Promise<UploadStatusResult> {
    return this.client.get<UploadStatusResult>(
      `/api/v1/upload/${uploadId}/status`,
    );
  }

  // ══════════════════════════════════════════════════════════════
  // ENTITY KNOWLEDGE GRAPH (Layer 2)
  // ══════════════════════════════════════════════════════════════

  /**
   * Search named entities (people, organisations, concepts).
   *
   * @param query      - Name or keyword search.
   * @param entityType - Filter by entity type (e.g. "person", "org").
   * @param limit      - Maximum results (default 20).
   */
  async searchEntities(
    query?: string,
    entityType?: string,
    limit?: number,
  ): Promise<EntityRecord[]> {
    const params: Record<string, string> = {};
    if (query) params.query = query;
    if (entityType) params.type = entityType;
    if (limit !== undefined) params.limit = String(limit);

    const data = await this.client.get<{ entities?: EntityRecord[] }>(
      "/api/v1/entities",
      Object.keys(params).length > 0 ? params : undefined,
    );
    return data.entities ?? [];
  }

  /**
   * Paginated walk of ALL entities for the tenant, ordered by id ASC.
   *
   * Unlike `searchEntities` (keyword LIKE filter, limited match set), this
   * walks every row with a stable cursor — pages never shift under concurrent
   * inserts. Loop until `has_more` is false to consume the full set.
   *
   * Junior Tip [SDK parity, 2026-05-22]: mirrors Go `ListEntities` and Python
   * `list_entities`. All three SDKs must expose this identically.
   *
   * @param limit  - Page size (default 200, server-clamped to [1, 500]).
   * @param offset - 0-based offset (default 0).
   */
  async listEntities(
    limit = 200,
    offset = 0,
  ): Promise<{
    entities: EntityRecord[];
    count: number;
    total: number;
    limit: number;
    offset: number;
    has_more: boolean;
    next_offset: number;
  }> {
    if (limit <= 0) limit = 200;
    if (limit > 500) limit = 500;
    if (offset < 0) offset = 0;
    return this.client.get("/api/v1/entities/list", {
      limit: String(limit),
      offset: String(offset),
    });
  }

  /**
   * Store `text` as an episodic record under a CALLER-OWNED session uuid,
   * bypassing the auto-session assignment of the ingest path.
   *
   * Junior Tip [SDK parity, 2026-05-22]: mirrors Go `CreateInSession` and
   * Python `create_in_session`. Metadata is wrapped through the canonical
   * JSON envelope to avoid the container_tag corruption bug.
   *
   * @param text        - Record text (stored in summary + content).
   * @param sessionUuid - Session UUID to place the record under (required).
   */
  async createInSession(text: string, sessionUuid: string): Promise<AddResult> {
    if (!sessionUuid) {
      throw new Error("createInSession: sessionUuid is required");
    }
    const summary = text.length > 200 ? text.slice(0, 200) + "..." : text;
    const payload: RecordPayload = {
      uuid: sessionUuid,
      type: "episodic" as MemoryType,
      dimension: 0,
      prefix: "",
      weight: 0.5,
      score: 5,
      vector: "",
      related_ids: [],
      main_ids: [],
      consolidate_id: 0,
      metadata: buildMetadataJson(this.containerTag),
      summary,
      content: text,
      consolidated: false,
      status: "saved",
    };
    const data = await this.client.post<{ id?: number }>(
      "/api/v1/records",
      payload,
    );
    return {
      sessionId: sessionUuid,
      records: [{ id: data.id ?? 0, type: "episodic", summary }],
      mode: "oss",
    };
  }

  /**
   * Append parent record IDs to a single record's `main_ids` array.
   * Server-side: read, dedup, write back — idempotent.
   *
   * Junior Tip [SDK parity]: mirrors Go `AppendMainIDs` and Python
   * `append_main_ids`.
   *
   * @param recordId - Child record that receives the parents.
   * @param mainIds  - Parent IDs to append.
   */
  async appendMainIds(
    recordId: number,
    mainIds: number[],
  ): Promise<Record<string, unknown>> {
    if (recordId <= 0) {
      throw new Error("appendMainIds: recordId must be > 0");
    }
    if (mainIds.length === 0) return {};
    return this.client.patch<Record<string, unknown>>(
      "/api/v1/records/append-main-ids",
      { ids: [recordId], main_ids_to_append: mainIds },
    );
  }

  /**
   * Set `consolidate_id` on a batch of child records (judge → star link).
   * Batched so N children pointing at one star cost ONE Raft round-trip.
   *
   * Junior Tip [SDK parity]: mirrors Go `UpdateConsolidateIDs` and Python
   * `update_consolidate_ids`.
   *
   * @param ids           - Child record IDs.
   * @param consolidateId - The consolidated star's ID.
   */
  async updateConsolidateIds(
    ids: number[],
    consolidateId: number,
  ): Promise<Record<string, unknown>> {
    if (ids.length === 0) return {};
    if (consolidateId <= 0) {
      throw new Error("updateConsolidateIds: consolidateId must be > 0");
    }
    return this.client.patch<Record<string, unknown>>(
      "/api/v1/records/consolidate-ids",
      { ids, consolidate_id: consolidateId },
    );
  }

  /**
   * Create or update a named entity (idempotent by name).
   *
   * @param name    - Entity name (required).
   * @param options - Optional type, summary, and attributes.
   * @returns The entity ID.
   */
  async upsertEntity(
    name: string,
    options?: UpsertEntityOptions,
  ): Promise<{ id: number }> {
    const payload: Record<string, unknown> = { name };
    if (options?.entityType) payload.entity_type = options.entityType;
    if (options?.summary) payload.summary = options.summary;
    if (options?.attributes) payload.attributes = options.attributes;

    return this.client.post<{ id: number }>("/api/v1/entities", payload);
  }

  /**
   * BFS traversal of entity relationships.
   *
   * Starting from an entity, discovers connected entities through
   * typed edges (works_at, knows, part_of, etc.).
   *
   * @param entityId - The starting entity ID.
   * @param depth    - How many hops (default 2, max 5).
   */
  async entityGraph(
    entityId: number,
    depth?: number,
  ): Promise<EntityGraphResult> {
    const params: Record<string, string> = {};
    if (depth !== undefined) params.depth = String(depth);

    return this.client.get<EntityGraphResult>(
      `/api/v1/entities/${entityId}/graph`,
      Object.keys(params).length > 0 ? params : undefined,
    );
  }

  /**
   * Get the full temporal history of an entity's relationships.
   *
   * Shows ALL edges including invalidated ones, ordered by event time.
   * Use to understand how an entity's context evolved over time.
   *
   * @param entityId - The entity ID.
   */
  async entityTimeline(entityId: number): Promise<EntityTimelineResult> {
    return this.client.get<EntityTimelineResult>(
      `/api/v1/entities/${entityId}/timeline`,
    );
  }

  /**
   * Create or update a typed relationship between two entities.
   *
   * @param sourceId - Source entity ID.
   * @param targetId - Target entity ID.
   * @param relation - Relationship type (e.g. "works_at", "knows").
   * @param options  - Optional event time, confidence, source record ID.
   */
  async upsertEntityEdge(
    sourceId: number,
    targetId: number,
    relation: string,
    options?: UpsertEntityEdgeOptions,
  ): Promise<void> {
    const payload: Record<string, unknown> = {
      source_id: sourceId,
      target_id: targetId,
      relation,
    };
    if (options?.eventTime) payload.event_time = options.eventTime;
    if (options?.confidence !== undefined)
      payload.confidence = options.confidence;
    if (options?.sourceRecordId !== undefined)
      payload.source_record_id = options.sourceRecordId;

    await this.client.post("/api/v1/entities/edges", payload);
  }

  /**
   * Link a memory record to an entity (cross-layer connection).
   *
   * @param recordId - Memory record ID.
   * @param entityId - Entity ID.
   * @param role     - Optional role description.
   */
  async linkRecordEntity(
    recordId: number,
    entityId: number,
    role?: string,
  ): Promise<void> {
    const payload: Record<string, unknown> = {
      record_id: recordId,
      entity_id: entityId,
    };
    if (role) payload.role = role;

    await this.client.post("/api/v1/entities/link", payload);
  }

  /**
   * Get entities linked to a specific memory record.
   *
   * @param recordId - The record ID.
   */
  async getRecordEntities(recordId: number): Promise<EntityRecord[]> {
    const data = await this.client.get<{ entities?: EntityRecord[] }>(
      `/api/v1/records/${recordId}/entities`,
    );
    return data.entities ?? [];
  }

  // ══════════════════════════════════════════════════════════════
  // STUB — forget()
  // ══════════════════════════════════════════════════════════════

  /**
   * Forget a specific memory or trigger cognitive decay.
   *
   * Not yet implemented — placeholder for the decay API.
   * Use `delete()` for hard removal or `update(id, { status: "archived" })`
   * for soft delete.
   *
   * @param memoryId - If provided, forget this specific memory.
   * @throws Always throws until the API is available.
   */
  async forget(memoryId?: number): Promise<void> {
    void memoryId;
    throw new Error(
      "forget() is not yet available. " +
        "Use delete() for hard removal or update(id, {status: 'archived'}) " +
        "for soft delete.",
    );
  }

  // ══════════════════════════════════════════════════════════════
  // INTERNAL HELPERS
  // ══════════════════════════════════════════════════════════════

  /**
   * Attempt cloud ingest. Returns `null` if the endpoint
   * responds with 404 (server does not support ingest).
   */
  private async tryIngest(
    text: string,
    score: number,
    type: MemoryType,
    metadata?: Record<string, unknown>,
  ): Promise<AddResult | null> {
    // Junior Tip [score/type drop fix, 2026-06]: previously this method took
    // the score/type as `_score`/`_type` and threw them away — the ingest
    // payload carried only content + container_tag, silently dropping the
    // caller's intent. We now forward them so the ingest worker can honour
    // (or hint off) them, matching the Python/Go SDK fix.
    const payload: IngestPayload = {
      content: text,
      container_tag: this.containerTag,
      score,
      type,
      metadata: buildMetadataJson(this.containerTag, metadata),
    };

    try {
      const data = await this.client.post<{
        id?: number;
        records?: Array<{ id: number; type: string; summary: string }>;
      }>("/api/v1/ingest", payload);

      this.ingestAvailable = true;

      const records = data.records ?? [
        {
          id: data.id ?? 0,
          type: "episodic" as string,
          summary: text.slice(0, 200),
        },
      ];

      return {
        sessionId: this.sessionUuid,
        records: records.map((r) => ({
          id: r.id,
          type: (r.type ?? "episodic") as MemoryType,
          summary: r.summary ?? text.slice(0, 200),
        })),
        mode: "cloud",
      };
    } catch (err: unknown) {
      if (err instanceof Error && err.message.includes("404")) {
        this.ingestAvailable = false;
        return null;
      }
      throw err;
    }
  }

  /**
   * Fallback: create a record directly via POST /api/v1/records.
   *
   * In OSS mode there is no server-side embedding, so we store text
   * in both `summary` (for FTS5 search) and `content` (for full
   * retrieval).
   */
  private async createRecord(
    text: string,
    score: number,
    type: MemoryType,
    metadata?: Record<string, unknown>,
  ): Promise<AddResult> {
    const summary = text.length > 200 ? text.slice(0, 200) + "..." : text;

    const payload: RecordPayload = {
      uuid: this.sessionUuid,
      type,
      dimension: 0,
      prefix: "",
      weight: score / 10,
      score,
      vector: "",
      related_ids: [],
      main_ids: [],
      consolidate_id: 0,
      metadata: buildMetadataJson(this.containerTag, metadata),
      summary,
      content: text,
      consolidated: false,
      status: "saved",
    };

    let data: { id?: number };
    try {
      data = await this.client.post<{ id?: number }>(
        "/api/v1/records",
        payload,
      );
    } catch (err: unknown) {
      // Junior Tip [transient anchor, 2026-06]: the server refuses a
      // non-episodic record (fact/preference/decision/...) in a session that
      // has no episodic "anchor" yet, returning HTTP 422 "cannot create
      // <type> without an episodic anchor in session ...". When the caller
      // explicitly asked for such a type, we seed the missing anchor with the
      // same text (episodic) and retry once. This keeps score/type honoured
      // instead of forcing the caller to manually pre-create an anchor.
      const isAnchorError =
        err instanceof Error &&
        err.message.includes("episodic anchor") &&
        type !== "episodic";
      if (!isAnchorError) throw err;

      const anchorPayload: RecordPayload = {
        ...payload,
        type: "episodic",
        weight: 0.5,
      };
      await this.client.post<{ id?: number }>(
        "/api/v1/records",
        anchorPayload,
      );
      data = await this.client.post<{ id?: number }>(
        "/api/v1/records",
        payload,
      );
    }

    return {
      sessionId: this.sessionUuid,
      records: [
        {
          id: data.id ?? 0,
          type,
          summary,
        },
      ],
      mode: "oss",
    };
  }

  /**
   * Flatten nested search response into simple SearchResult array.
   *
   * The server returns `{ results: [{ record: {...}, similarity: N }] }`
   * but our SDK surface exposes flat objects.
   */
  private flattenSearchResults(
    results?: Array<{
      record?: Record<string, unknown>;
      similarity?: number;
    }>,
  ): SearchResult[] {
    return (results ?? []).map((item) => {
      const rec = (item.record ?? {}) as Record<string, unknown>;
      return {
        id: (rec.id as number) ?? 0,
        type: (rec.type as string) ?? "",
        summary: (rec.summary as string) ?? "",
        score: (item.similarity as number) ?? 0,
        metadata: (rec.metadata as string) ?? undefined,
        content: (rec.content as string) ?? undefined,
      };
    });
  }

  /** String representation for logging / debugging. */
  toString(): string {
    return `Memory(containerTag=${this.containerTag}, session=${this.sessionUuid})`;
  }
}
