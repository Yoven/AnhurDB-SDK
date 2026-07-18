/**
 * AnhurDB TypeScript SDK — Memory client.
 *
 * Single entry point for the REST API. Core methods: `add`, `search`, `profile`.
 * The same class also exposes CRUD, batch ops, entities, uploads, graph walk,
 * query AST, manifests, and temporal versioning.
 *
 * @module
 */

import { HttpClient } from "./client.js";
import type {
  AddOptions,
  AddResult,
  AstQuery,
  BatchUpdateResult,
  ContextResult,
  CreateOptions,
  CreateSessionOptions,
  EntityGraphResult,
  EntityRecord,
  EntityTimelineResult,
  GroundingResult,
  IngestPayload,
  ListChatOptions,
  ListChatResult,
  ManifestGlobalOptions,
  ManifestResult,
  ManifestSessionOptions,
  MemoryOptions,
  MemoryRecord,
  MemoryType,
  ProfileResult,
  QueryResult,
  RecordPayload,
  SearchOptions,
  SearchPayload,
  SearchResult,
  SearchScope,
  SearchSessionPayload,
  SessionStats,
  UploadResult,
  UploadStatusResult,
  UpsertEntityEdgeOptions,
  UpsertEntityOptions,
  WalkResult,
  WalkSemanticOptions,
} from "./types.js";

/** Default cloud endpoint. Self-hosted users pass `url` explicitly. */
const DEFAULT_CLOUD_URL = "https://anhurdb.yoven.ai";

/**
 * Derive a short, stable hex tag from a string using the Web Crypto
 * SubtleCrypto API (available in Node 18+ and all modern runtimes).
 *
 * Falls back to a simple DJB2 hash when SubtleCrypto is unavailable
 * (e.g. some edge runtimes without `globalThis.crypto`).
 *
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

/**
 * Base64-encode a raw byte vector for a wire `vector` field.
 *
 * carries quantised vectors as base64 strings (search's `vector`, walk's
 * `vector`), but the SDK's public surface takes raw `Uint8Array` bytes so
 * callers never hand-roll base64. This mirrors Go's
 * `base64.StdEncoding.EncodeToString` and Python's `base64.b64encode`. `btoa`
 * is a global in Node 18+ and every browser; each element of a `Uint8Array`
 * is already 0-255, so `String.fromCharCode` maps it to a code unit that
 * `btoa` round-trips byte-for-byte (no UTF-8 widening).
 */
function encodeVectorBase64(vector: Uint8Array): string {
  let binary = "";
  for (let byteIndex = 0; byteIndex < vector.length; byteIndex++) {
    binary += String.fromCharCode(vector[byteIndex]);
  }
  return btoa(binary);
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
 * Generate a 6-lowercase-hex random suffix (3 crypto-random bytes) for the
 * default auto-derived session UUID.
 *
 * default session UUID is `<container_tag>-<YYYYMMDD-HHMMSS UTC>-<6 hex>`. The
 * UTC timestamp alone collides for two sessions opened in the same wall-clock
 * second, so a 3-byte crypto-random suffix disambiguates them. The 6-hex width
 * and the crypto source MUST match the other SDKs byte-for-byte: Python
 * `secrets.token_hex(3)`, Go `crypto/rand` 3 bytes -> hex, TS
 * `crypto.getRandomValues` over a 3-byte `Uint8Array` -> hex (3 bytes = exactly
 * 6 hex chars). `crypto` is a global in Node 18+ and every browser, mirroring
 * deriveTag's use of `crypto.subtle`.
 */
function randomHexSuffix(): string {
  const randomBytes = new Uint8Array(3);
  crypto.getRandomValues(randomBytes);
  return Array.from(randomBytes)
    .map((byteValue) => byteValue.toString(16).padStart(2, "0"))
    .join("");
}

/**
 * Wrap a container tag into the canonical metadata JSON envelope
 * `{"container_tag":"<tag>"}`.
 *
 * path historically wrote `metadata` as the bare container_tag string
 * ("mem-3f9...") instead of a JSON object. On the server that poisoned every
 * downstream code running `JSON.parse(metadata)` — keep the wire value valid JSON.
 * Go and Python SDKs carry the identical fix (buildMetadataJSON /
 * _build_metadata_json). ALL THREE SDKs MUST stay byte-identical here — see
 * the SDK-sync rule in project memory. Returns "{}" when the tag is empty.
 */
function buildMetadataJson(
  containerTag: string,
  extra?: Record<string, unknown>): string {
  // Merge caller extras first, then force container_tag last so a caller key
  // named "container_tag" can never clobber the SDK-owned tag.
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
  /** True only after a successful createSession/openSession (server ledger). */
  private sessionRegistered = false;
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
      this.sessionUuid = `${this.containerTag}-${utcTimestamp()}-${randomHexSuffix()}`;
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
        this.sessionUuid = `${this.containerTag}-${utcTimestamp()}-${randomHexSuffix()}`;
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
   * Store raw text via the ingest write path (default).
   *
   * Session-first servers require {@link createSession} before add succeeds.
   *
   * Agent UX — pick the write path once:
   * - Raw chat/notes → plain `add(text)` or `{ mode: "ingest" }` →
   *   `POST /ingest` (episodic + async satellites; extraction LLM billed).
   *   MCP: `ingest_memory`.
   * - Direct episodic record → `{ mode: "regular" }` or {@link create} →
   *   `POST /records` (no extraction). MCP: `create_memory`.
   *
   * Trap: pinning `score`, `type`, or `metadata` also forces `/records` on
   * ingest mode. Plain `add(text)` prefers ingest; 404 falls back to
   * `/records` (OSS).
   *
   * @param text    - The text to remember.
   * @param options - Optional mode, score/type/metadata, or session override.
   * @returns A result containing the session ID and created records.
   *
   * @example
   * ```ts
   * await mem.createSession();
   * const result = await mem.add("I'm a data scientist at Google");
   * console.log(result.sessionId, result.records);
   * ```
   */
  async add(text: string, options?: AddOptions): Promise<AddResult> {
    if (!text) {
      throw new Error("text cannot be empty");
    }
    await this.tagReady;

    const writeMode = options?.mode ?? "ingest";
    const resolvedSessionId = this.resolveWriteSessionId(options?.sessionId);

    // read the RAW caller intent (`!== undefined`) before `?? 5` / `??
    // "episodic"` collapse the unset-ness — otherwise every add would look
    // "pinned" and never use the ingest pipeline. See the doc-comment above.
    //
    // force the records path. The ingest endpoint's request body is only
    // metadata, so a metadata-only add routed to /ingest would lose it. Go and
    // Python route metadata to /records for the same reason — the three agree.
    const forceRecordsPath =
      writeMode === "regular" ||
      options?.score !== undefined ||
      options?.type !== undefined ||
      options?.metadata !== undefined;

    const score = options?.score ?? 5;
    const type: MemoryType = options?.type ?? "episodic";
    const metadata = options?.metadata;

    if (forceRecordsPath) {
      return this.createRecord(text, score, type, metadata, {
        sessionUuid: resolvedSessionId,
      });
    }

    // Try cloud ingest first (has auto-embedding).
    if (this.ingestAvailable !== false) {
      const result = await this.tryIngest(text, resolvedSessionId);
      if (result !== null) return result;
    }

    // Fallback: direct record creation (OSS / self-hosted).
    return this.createRecord(text, score, type, metadata, {
      sessionUuid: resolvedSessionId,
    });
  }

  // ── search() — find relevant memories ───────────────────────

  /**
   * Hybrid plane search via `POST /api/v1/search`.
   *
   * Default scope `sessions` (tenant chat; excludes shared-library uuids).
   *
   * Agent UX — text is not semantic: `query` is sent as body `text` (FTS5
   * exact-word matching), not an embedding. For conceptual RAG without a
   * vector, prefer {@link smartSearch} (or MCP `recall`).
   *
   * @param query   - Query string sent as FTS `text`.
   * @param options - Optional limit, type filter, and scope plane.
   * @returns Array of search results sorted by relevance.
   *
   * @example
   * ```ts
   * const results = await mem.search("what does this user do?", { limit: 5 });
   * results.forEach(r => console.log(r.record.summary, r.similarity));
   * ```
   */
  async search(
    query: string,
    options?: SearchOptions): Promise<SearchResult[]> {
    if (!query) {
      throw new Error("query cannot be empty");
    }
    await this.tagReady;

    const payload: SearchPayload = {
      text: query,
      limit: options?.limit ?? 10,
      scope: options?.scope ?? "sessions",
    };
    if (options?.typeFilter) {
      payload.type_filter = options.typeFilter;
    }

    // Search is a read-shaped POST endpoint.
    const data = await this.client.postRead<{
      results?: Array<{
        record?: Record<string, unknown>;
        similarity?: number;
      }>;
    }>("/api/v1/search", payload);

    return this.nestSearchResults(data.results);
  }

  /** Search chat sessions only (`scope=sessions`). */
  async searchSessions(
    query: string,
    options?: SearchOptions): Promise<SearchResult[]> {
    return this.search(query, { ...options, scope: "sessions" });
  }

  /** Search tenant-shared library docs (`scope=tenant_shared`). */
  async searchTenantShared(
    query: string,
    options?: SearchOptions): Promise<SearchResult[]> {
    return this.search(query, { ...options, scope: "tenant_shared" });
  }

  /** Search client-wide shared library (`scope=client_shared`). */
  async searchClientShared(
    query: string,
    options?: SearchOptions): Promise<SearchResult[]> {
    return this.search(query, { ...options, scope: "client_shared" });
  }

  /** Search both shared planes (`scope=shared_all`). */
  async searchShared(
    query: string,
    options?: SearchOptions): Promise<SearchResult[]> {
    return this.search(query, { ...options, scope: "shared_all" });
  }

  // ── searchSession() — session-scoped hybrid search ──────────

  /**
   * Search for relevant memories WITHIN a single chat/session.
   *
   * Uses `POST /api/v1/search` with `scope=sessions` and a session `uuid`.
   *
   * @param query       - Natural language query (sent as `text`).
   * @param sessionUuid - Session UUID to scope to. Empty/omitted = current session.
   * @param options     - Optional limit and type filter.
   */
  async searchSession(
    query: string,
    sessionUuid?: string,
    options?: SearchOptions): Promise<SearchResult[]> {
    if (!query) {
      throw new Error("query cannot be empty");
    }
    await this.tagReady;

    const payload: SearchSessionPayload = {
      uuid: sessionUuid ?? this.sessionUuid,
      text: query,
      limit: options?.limit ?? 10,
      scope: "sessions",
    };
    if (options?.typeFilter) {
      payload.type_filter = options.typeFilter;
    }

    // Search is a read-shaped POST endpoint.
    const data = await this.client.postRead<{
      results?: Array<{
        record?: Record<string, unknown>;
        similarity?: number;
      }>;
    }>("/api/v1/search", payload);

    return this.nestSearchResults(data.results);
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
      const data = await this.client.get<ProfileResult>(
        "/api/v1/profile",
        { tag: this.containerTag });
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
   * List/filter records by cognitive type in the tenant store.
   *
   * Faster than plane search when you know the exact type.
   *
   * Agent UX — not a plane switch: no `scope` parameter. Does **not** search
   * Shared Data. For specialty docs use {@link searchTenantShared} /
   * {@link searchClientShared} / {@link searchShared} (or `search` with scope).
   *
   * @param type  - The memory type to filter by (e.g. "fact", "episodic").
   * @param limit - Maximum results to return (default 20).
   */
  async searchByType(
    type: MemoryType,
    limit?: number,
    query?: string): Promise<SearchResult[]> {
    const params: Record<string, string> = { type };
    if (limit !== undefined) params.limit = String(limit);
    if (query) params.q = query;

    // the `{results:[{record,similarity}]}` envelope of search/recall/searchSession.
    // BARE record array under `records`: `{records:[<Record>],count:N}`. Reading
    // `data.results` therefore matched nothing and returned `[]` for EVERY call —
    // the cross-SDK "searchByType returns empty" bug. We read `records` and wrap each
    // full record into the canonical {@link SearchResult} so the shape stays identical
    // to the other search methods. A type filter has no semantic distance, so
    // `similarity` is 0 — the ranking lives in the record's own weight/score, kept
    // verbatim. Mirrors Go SearchByType and Python search_by_type (same key/shape).
    const data = await this.client.get<{
      records?: Array<Record<string, unknown>>;
    }>("/api/v1/search/type", params);
    return (data.records ?? []).map((rawRecord) => ({
      record: rawRecord as unknown as MemoryRecord,
      similarity: 0,
    }));
  }

  /**
   * Full-text search with cognitive weight boosting.
   *
   * a combination of text relevance and cognitive importance (score).
   *
   * @param query - Search query.
   * @param limit - Maximum results (default 10).
   * @param type  - Optional memory type filter.
   */
  /**
   * Full-text search with cognitive weight boosting.
   *
   * Prefer over {@link search} for conceptual text queries (no embedding
   * required). Uses `GET /api/v1/search/smart` with the same memory-plane
   * `scope` as {@link search} (default `sessions`).
   *
   * @param query - Search query.
   * @param limit - Maximum results (default 10).
   * @param type - Optional memory type filter.
   * @param scope - Search plane (default `sessions`).
   */
  async smartSearch(
    query: string,
    limit?: number,
    type?: MemoryType,
    scope?: SearchScope): Promise<unknown> {
    const params: Record<string, string> = {
      q: query,
      limit: String(limit ?? 10),
      scope: scope ?? "sessions",
    };
    if (type) params.type = type;

    return this.client.get(
      "/api/v1/search/smart",
      params);
  }

  /**
   * Recall memories via plane-aware search.
   *
   * Explicit alias for `search()` (default `scope=sessions`).
   * Named to match the MCP `recall` tool.
   *
   * @param query - Natural language query.
   * @param limit - Maximum results (default 10).
   * @param options - Optional scope (and other search options except limit).
   */
  async recall(
    query: string,
    limit?: number,
    options?: Omit<SearchOptions, "limit">): Promise<SearchResult[]> {
    return this.search(query, {
      limit: limit ?? 10,
      scope: options?.scope ?? "sessions",
      typeFilter: options?.typeFilter,
    });
  }

  /**
   * Fetch the most recent records via the dedicated `GET /api/v1/recent`
   * endpoint (server handler `ListRecent`).
   *
   * DEDICATED recents route (`/api/v1/recent`), NOT the paginated
   * `/api/v1/manifest` (`ManifestGlobal`) — they are different endpoints and
   * only {@link manifestGlobal} should use the manifest. The server emits either
   * a bare JSON array `[...]` OR an envelope object `{"records":[...],"count":N}`,
   * so we accept BOTH: an `Array.isArray` check returns the bare array verbatim,
   * otherwise we unwrap the `records` key. Not handling the bare-array shape
   * would silently drop every record. Mirrors Python `recent`
   * (`data if isinstance(data, list) else data.get("records", [])`) and Go
   * `Recent`.
   *
   * @param limit - Maximum records to return (default 20).
   */
  async recent(
    limit?: number): Promise<MemoryRecord[]> {
    const params: Record<string, string> = {};
    if (limit !== undefined) params.limit = String(limit);

    const data = await this.client.get<
      MemoryRecord[] | { records?: MemoryRecord[] }
    >("/api/v1/recent", params);
    return Array.isArray(data) ? data : (data.records ?? []);
  }

  /**
   * Run a structured AST query against `POST /api/v1/query`.
   *
   * This is the precise, SQL-like counterpart to the fuzzy `search()` — you
   * filter on exact columns (type/weight/status/...), sort, and paginate. Build
   * the AST by hand or fluently via {@link QueryBuilder}.
   *
   * AST fields (`filters`, `sort`, `pagination`, `select`) are sent FLAT at the
   * top level of the body — NOT wrapped in `{"query": ...}` (this is the exact
   * note the Python QueryExecutor carries). Filter/sort column names are
   * whitelist-validated server-side (HTTP 400 'invalid filter field' / 'invalid
   * sort field'); `select` is parsed but ignored (the SELECT list is fixed).
   * The response `records` is a FLAT array (NOT `{record, similarity}`) and is
   * `null` on an empty set, so we default to `[]`. Mirrors Python `query()` /
   * `QueryExecutor.execute_query` and the Go `Query` fluent surface.
   *
   *
   * @param ast - The compiled AST (see {@link QueryBuilder} to build fluently).
   * @returns The matching records plus a count.
   */
  async query(
    ast: AstQuery): Promise<QueryResult> {
    const data = await this.client.postRead<{
      records?: MemoryRecord[] | null;
      count?: number;
    }>("/api/v1/query", ast);
    const records = data.records ?? [];
    return {
      records,
      count: data.count ?? records.length,
    };
  }

  /**
   * List the tenant-wide manifest (every record, paginated) via
   * `GET /api/v1/manifest`.
   *
   * Returns the full pagination envelope (limit/offset/has_more), unlike
   * {@link recent} which only returns the bare records array. Use this to walk
   * the whole tenant by paging on `offset` until `has_more` is false.
   *
   * keyword filter is the `q` query param (the handler also accepts `query` as an
   * alias, with `q` winning — we send `q`). When `q` is set the server IGNORES
   * `offset` (the search path does not paginate). `has_more = len(records) ==
   * limit`, a heuristic that can false-positive on an exactly-full last page.
   * `as_of` is mutually exclusive with `since`/`until` (HTTP 400 otherwise).
   * Mirrors Python `manifest_global` and Go `ManifestGlobal`.
   *
   * @param options - Optional keyword, pagination, and temporal bounds.
   */
  async manifestGlobal(
    options?: ManifestGlobalOptions): Promise<ManifestResult> {
    const params = this.buildManifestParams(options);
    return this.client.get<ManifestResult>(
      "/api/v1/manifest",
      Object.keys(params).length > 0 ? params : undefined);
  }

  /**
   * List a single session's manifest (paginated) via
   * `GET /api/v1/chats/{uuid}/manifest`.
   *
   * Same envelope as {@link manifestGlobal} but scoped to one chat.
   *
   * the session endpoint reads ONLY `q` (there is NO `query` alias here, unlike
   * the global manifest). Default limit is 500 (capped 2000). Setting any
   * temporal param bypasses the response cache. Mirrors Python
   * `manifest_session` and Go `ManifestSession`.
   *
   * @param sessionUuid - The session/chat UUID (required).
   * @param options     - Optional keyword, pagination, and temporal bounds.
   */
  async manifestSession(
    sessionUuid: string,
    options?: ManifestSessionOptions): Promise<ManifestResult> {
    if (!sessionUuid) {
      throw new Error("manifestSession: sessionUuid is required");
    }
    const params = this.buildManifestParams(options);
    return this.client.get<ManifestResult>(
      `/api/v1/chats/${encodeURIComponent(sessionUuid)}/manifest`,
      Object.keys(params).length > 0 ? params : undefined);
  }

  /**
   * List every record in a single chat via `GET /api/v1/chats/{uuid}`.
   *
   * Returns the entire matching set for the session (no pagination), with an
   * optional tri-state `consolidated` filter and an exact `status` filter.
   *
   * the `consolidated` filter is tri-state — omit for ALL, `true` for only
   * consolidated, and ANY other value (including `false`) for only
   * non-consolidated (the server parses `val == "true"`). We therefore send the
   * literal string `"true"`/`"false"`. `content` is omitted (metadata only).
   * Mirrors Python `list_chat` and Go `ListChat`.
   *
   * @param sessionUuid - The session/chat UUID (required).
   * @param options     - Optional consolidated and status filters.
   */
  async listChat(
    sessionUuid: string,
    options?: ListChatOptions): Promise<ListChatResult> {
    if (!sessionUuid) {
      throw new Error("listChat: sessionUuid is required");
    }
    const params: Record<string, string> = {};
    if (options?.consolidated !== undefined) {
      params.consolidated = options.consolidated ? "true" : "false";
    }
    if (options?.status) params.status = options.status;

    const data = await this.client.get<ListChatResult>(
      `/api/v1/chats/${encodeURIComponent(sessionUuid)}`,
      Object.keys(params).length > 0 ? params : undefined);
    return {
      records: data.records ?? [],
      count: data.count ?? (data.records ?? []).length,
    };
  }

  /**
   * Aggregate the tenant's record counts by cognitive type.
   *
   * there is NO server-side aggregation endpoint — `count_by_type` is a CLIENT
   * roll-up. We page the global manifest (`GET /api/v1/manifest`) and tally
   * `records[].type`. The handler's `limit=0` does NOT return zero rows (the
   * `limit > 0` guard makes it fall back to the default 100-row page), so the
   * ONLY correct way to count ALL types is to page via `offset` until
   * `has_more` is false — never rely on `limit=0`. We page at 1000/req (the
   * server hard cap) and stop on a short/empty page (defensive against the
   * exactly-full-last-page `has_more` false-positive). Mirrors Python
   * `count_by_type` and Go `CountByType`.
   *
   * @returns A map of `type → count` over the full tenant manifest.
   */
  async countByType(): Promise<Record<string, number>> {
    // Paging at the cap minimises round-trips while staying within bounds.
    const pageSize = 1000;
    const counts: Record<string, number> = {};
    let offset = 0;

    // (e.g. has_more stuck true) can never spin forever — fail-loud-ish by
    // stopping after a generous ceiling rather than hanging the caller.
    const maxPages = 10_000;
    for (let page = 0; page < maxPages; page++) {
      const result = await this.manifestGlobal(
        { limit: pageSize, offset });
      const records = result.records ?? [];
      for (const record of records) {
        const recordType = record.type ?? "unknown";
        counts[recordType] = (counts[recordType] ?? 0) + 1;
      }
      // Stop on a short/empty page — robust against the has_more
      // exactly-full-last-page false-positive.
      if (records.length < pageSize) break;
      offset += records.length;
    }

    return counts;
  }

  /**
   * List the known cognitive memory types (LOCAL — no network call).
   *
   * SDK returns it from its local enum so the value is identical and offline.
   * The list is the {@link MemoryType} union, kept byte-identical to the Go
   * `ListTypes` slice and the Python `list_types` list.
   *
   * @returns The cognitive type names.
   */
  listTypes(): MemoryType[] {
    return [
      "episodic",
      "fact",
      "preference",
      "decision",
      "task",
      "risk",
      "reasoning",
      "idea",
      "emotion",
      "consolidated",
      "hub",
      "file",
    ];
  }

  /**
   * Build the provenance grounding of a record via
   * `GET /api/v1/records/{id}/grounding`.
   *
   * Walks the graph (BFS) back to the source episodic anchors and the
   * consolidations that summarise the record — the "why does the DB believe
   * this" trail.
   *
   * the ONLY query param is `max_depth`, an integer 1..5 inclusive (default 3);
   * anything outside that range is HTTP 400 server-side. `anchors`/
   * `consolidations` are always present arrays (may be empty). A missing/
   * archived/superseded target is HTTP 404. Mirrors Python `get_grounding` and
   * Go `GetGrounding`.
   *
   * @param recordId - The target record ID (must be > 0).
   * @param maxDepth - BFS depth budget 1..5 (default server-side: 3).
   */
  async getGrounding(
    recordId: number,
    maxDepth?: number): Promise<GroundingResult> {
    if (recordId <= 0) {
      throw new Error("getGrounding: recordId must be > 0");
    }
    const params: Record<string, string> = {};
    if (maxDepth !== undefined) params.max_depth = String(maxDepth);

    return this.client.get<GroundingResult>(
      `/api/v1/records/${recordId}/grounding`,
      Object.keys(params).length > 0 ? params : undefined);
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
  async walk(
    startId: number,
    depth?: number): Promise<WalkResult> {
    const payload = {
      seed_id: startId,
      depth: depth ?? 3,
      direction: "both",
    };
    return this.client.postRead<WalkResult>(
      "/api/v1/walk",
      payload);
  }

  /**
   * Semantic graph walk — follows edges weighted by vector similarity
   * rather than just structural edges.
   *
   * With no `options` this is the pre-existing pure-Dijkstra walk. Passing
   * `options.target` turns it into a goal-directed walk: `"semantic"` steers
   * toward `options.goalVector`, `"tag"` toward `options.targetTag`,
   * `"recency"` toward the freshest records. See {@link WalkSemanticOptions}.
   *
   * the goal-directed fields are added to the body ONLY when set, so an
   * existing caller (no `options`) sends the exact same wire shape as before
   * and the server falls back to a plain Dijkstra traversal. `goalVector` is
   * raw bytes and is base64-encoded into the wire `vector` field here, so
   * base64 never leaks into the public API — matching the Go and Python SDKs.
   *
   * @param startId     - The record ID to start from.
   * @param depth       - How many hops (default 3).
   * @param options     - Optional goal-directed steering (target / goalVector
   *                      / targetTag / maxCost). Omit for a pure Dijkstra walk.
   */
  async walkSemantic(
    startId: number,
    depth?: number,
    options?: WalkSemanticOptions): Promise<WalkResult> {
    // Base body: the unchanged pure-Dijkstra request. seed_id + depth are sent
    // exactly as before so callers that pass no `options` are byte-for-byte
    // backward-compatible.
    const payload: Record<string, unknown> = {
      seed_id: startId,
      depth: depth ?? 3,
    };

    // Goal-directed steering: each field is attached ONLY when explicitly set,
    // so an absent option never appears on the wire and the server applies its
    // own default (or pure Dijkstra when `target` is absent). Mirrors the Go
    // and Python SDKs field-by-field: target → target, maxCost → max_cost,
    // targetTag → target_tag, goalVector → base64 → vector.
    if (options?.target !== undefined) payload.target = options.target;
    if (options?.maxCost !== undefined) payload.max_cost = options.maxCost;
    if (options?.targetTag !== undefined) {
      payload.target_tag = options.targetTag;
    }
    if (options?.goalVector !== undefined) {
      payload.vector = encodeVectorBase64(options.goalVector);
    }

    return this.client.postRead<WalkResult>(
      "/api/v1/walk/semantic",
      payload);
  }

  /**
   * Get the topological context (neighbours) around a specific record.
   *
   * @param recordId - The record ID to inspect.
   */
  async getContext(
    recordId: number): Promise<ContextResult> {
    return this.client.get<ContextResult>(
      `/api/v1/records/${recordId}/topology`,
      undefined);
  }

  /**
   * Read the full content body of a specific record.
   *
   * @param recordId - The record ID whose content to retrieve.
   */
  async readContent(
    recordId: number): Promise<string> {
    // text/plain body — use getText (raw), not JSON parse. Matches Go raw
    // bytes and Python raw_text=True.
    return this.client.getText(
      `/api/v1/records/${recordId}/content`,
      undefined);
  }

  // ══════════════════════════════════════════════════════════════
  // EXTENDED METHODS — Session Management
  // ══════════════════════════════════════════════════════════════

  /**
   * Register a write session via POST /api/v1/sessions.
   *
   * Session-first servers reject ingest and record writes until this succeeds.
   * Call once per conversation before {@link add}.
   *
   * Junior Tip [parity with Python/Go/MCP]: omit `sessionId` → server generates
   * a new UUID. To register a caller-chosen id (e.g. after {@link newSession}):
   * `await mem.createSession({ sessionId: await mem.newSession() })`.
   *
   * @returns The registered session id (also stored on the client).
   */
  async createSession(options?: CreateSessionOptions): Promise<string> {
    await this.tagReady;

    // Only send session_id when the caller explicitly chose one. Omitting it
    // matches Python create_session() / MCP create_session / REST {} body.
    const sessionIdToRegister = options?.sessionId?.trim() || "";
    const payload: Record<string, unknown> = {};
    if (sessionIdToRegister) {
      payload.session_id = sessionIdToRegister;
    }
    if (options?.metadata) {
      payload.metadata = options.metadata;
    }

    const data = await this.client.post<{ session_id?: string }>(
      "/api/v1/sessions",
      payload);

    const registeredSessionId = data.session_id ?? sessionIdToRegister;
    if (!registeredSessionId) {
      throw new Error("createSession: server returned empty session_id");
    }
    this.sessionUuid = registeredSessionId;
    this.sessionRegistered = true;
    return registeredSessionId;
  }

  /**
   * Generate a fresh local session id and register it (Python `open_session`).
   *
   * Equivalent to `createSession({ sessionId: await newSession() })`.
   */
  async openSession(options?: { metadata?: Record<string, unknown> }): Promise<string> {
    const localSessionId = await this.newSession();
    return this.createSession({
      sessionId: localSessionId,
      metadata: options?.metadata,
    });
  }

  /**
   * Start a new local session (generates a fresh UUID).
   *
   * Does NOT register the session on the server — call {@link createSession}
   * before {@link add} on session-first deployments.
   *
   * @returns The new session ID.
   */
  async newSession(): Promise<string> {
    await this.tagReady;
    // session_uuid — `<container_tag>-<YYYYMMDD-HHMMSS UTC>-<6 random hex>`. The random
    // suffix (randomHexSuffix) stops two rotations in the same UTC second from colliding
    // onto one session, byte-for-byte with Python new_session and Go NewSession.
    this.sessionUuid = `${this.containerTag}-${utcTimestamp()}-${randomHexSuffix()}`;
    this.sessionRegistered = false;
    return this.sessionUuid;
  }

  /**
   * List all sessions with aggregate statistics.
   */
  async listSessions(): Promise<SessionStats[]> {
    const data = await this.client.get<{
      sessions?: SessionStats[];
    }>("/api/v1/sessions/stats", undefined);
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
    offset?: number): Promise<unknown> {
    const params: Record<string, string> = {};
    if (limit !== undefined) params.limit = String(limit);
    if (offset !== undefined) params.offset = String(offset);

    return this.client.get(
      `/api/v1/sessions/${sessionUuid}/history`,
      Object.keys(params).length > 0 ? params : undefined);
  }

  /**
   * Get mathematically clustered topological groups for a session.
   *
   *
   * @param sessionUuid - The session UUID.
   */
  async getSessionClusters(
    sessionUuid: string): Promise<unknown> {
    return this.client.get(
      `/api/v1/sessions/${sessionUuid}/clusters`,
      undefined);
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
  /**
   * Check server health via `GET /api/v1/health`.
   *
   * Junior Tip [parity]: matches Python `health` and Go `Health`.
   */
  async health(): Promise<Record<string, unknown>> {
    return this.client.get<Record<string, unknown>>("/api/v1/health");
  }

  /**
   * Fetch record metadata by ID via `GET /api/v1/records/{id}`.
   *
   * Junior Tip [vs readContent]: returns JSON metadata; use `readContent`
   * for the full text body.
   */
  async get(recordId: number): Promise<Record<string, unknown>> {
    if (recordId <= 0) {
      throw new Error("get: recordId must be > 0");
    }
    return this.client.get<Record<string, unknown>>(
      `/api/v1/records/${recordId}`);
  }

  async update(
    recordId: number,
    fields: Partial<{
      summary: string;
      metadata: string;
      status: string;
      type: string;
      score: number;
    }>): Promise<void> {
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
    ids: number[]): Promise<Record<string, unknown>> {
    // Read-shaped POST endpoint.
    return this.client.postRead<Record<string, unknown>>(
      "/api/v1/records/batch-content",
      { ids });
  }

  /**
   * Update status for multiple records at once.
   *
   * @param ids    - Array of record IDs.
   * @param status - Target status (e.g. "consolidated", "archived").
   */
  async batchUpdateStatus(
    ids: number[],
    status: string): Promise<BatchUpdateResult> {
    return this.client.patch<BatchUpdateResult>(
      "/api/v1/records/mark-consolidated",
      { ids, status });
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
   * Upload a document for async ingestion (multipart/form-data).
   *
   * Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown,
   * HTML, DOCX.
   *
   * The server processes the file asynchronously — use
   * `uploadStatus()` to poll for completion.
   *
   * @param filename  - Original filename (used for format detection).
   * @param content   - Raw file bytes (`Uint8Array` / `Buffer`) or UTF-8 text.
   * @param sessionId - Optional session UUID to associate with.
   *
   * server never accepted. Content is now RAW bytes via FormData field `file`.
   */
  async uploadFile(
    filename: string,
    content: Uint8Array | ArrayBuffer | string,
    sessionId?: string): Promise<UploadResult> {
    const form = new FormData();
    let blobParts: BlobPart[];
    if (typeof content === "string") {
      blobParts = [content];
    } else if (content instanceof ArrayBuffer) {
      blobParts = [content];
    } else {
      // Uint8Array — copy into a plain ArrayBuffer slice for BlobPart typing.
      blobParts = [content.buffer.slice(content.byteOffset, content.byteOffset + content.byteLength) as ArrayBuffer];
    }
    form.append("file", new Blob(blobParts), filename);
    if (sessionId) {
      form.append("session_id", sessionId);
      form.append("mode", "chat");
    }
    return this.client.postMultipart<UploadResult>("/api/v1/upload", form);
  }

  /**
   * Check the processing status of a file upload.
   *
   * @param uploadId - The upload ID returned by `uploadFile()`.
   */
  async uploadStatus(
    uploadId: number): Promise<UploadStatusResult> {
    return this.client.get<UploadStatusResult>(
      `/api/v1/upload/${uploadId}/status`,
      undefined);
  }

  // ══════════════════════════════════════════════════════════════
  // ENTITY KNOWLEDGE GRAPH (Layer 2)
  // ══════════════════════════════════════════════════════════════

  /**
   * Search named entities (people, organisations, concepts).
   *
   * @param query      - Name or keyword search.
   * @param entityType - Filter by entity type: person, organization, concept, location, product.
   * @param limit      - Maximum results (default 20).
   */
  async searchEntities(
    query?: string,
    entityType?: string,
    limit?: number): Promise<EntityRecord[]> {
    const params: Record<string, string> = {};
    if (query) params.q = query;
    if (entityType) params.type = entityType;
    if (limit !== undefined) params.limit = String(limit);

    const data = await this.client.get<{ entities?: EntityRecord[] }>(
      "/api/v1/entities",
      Object.keys(params).length > 0 ? params : undefined);
    return data.entities ?? [];
  }

  /**
   * Paginated walk of ALL entities for the tenant, ordered by id ASC.
   *
   * Unlike `searchEntities` (keyword LIKE filter, limited match set), this
   * walks every row with a stable cursor — pages never shift under concurrent
   * inserts. Loop until `has_more` is false to consume the full set.
   *
   * `list_entities`. All three SDKs must expose this identically.
   *
   * @param limit  - Page size (default 200, server-clamped to [1, 500]).
   * @param offset - 0-based offset (default 0).
   */
  async listEntities(
    limit = 200,
    offset = 0): Promise<{
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
    return this.client.get(
      "/api/v1/entities/list",
      {
        limit: String(limit),
        offset: String(offset),
      });
  }

  /**
   * Resolve session id for writes. Explicit override is passed through;
   * otherwise require a prior createSession/openSession.
   */
  private resolveWriteSessionId(explicitSessionId?: string): string {
    const trimmedExplicit = explicitSessionId?.trim() ?? "";
    if (trimmedExplicit) {
      return trimmedExplicit;
    }
    if (!this.sessionRegistered || !this.sessionUuid) {
      throw new Error(
        "session_id is required — create a session first (POST /api/v1/sessions)",
      );
    }
    return this.sessionUuid;
  }

  /**
   * Create exactly one typed record via `POST /api/v1/records` (no extraction).
   *
   * Agent UX — write path: use when you already know `type` + content.
   * For raw text use plain {@link add} / MCP `ingest_memory` instead.
   * Every supplied field (type, score, related_ids, valid_from/until, metadata)
   * is persisted verbatim — no satellite LLM job.
   *
   * Empty session + derived type → HTTP 422 ("create an episodic first").
   * The SDK does not fabricate that anchor. MCP: `create_memory`.
   *
   * @param text    - Record text (stored in summary + content).
   * @param options - Full-fidelity fields (all optional).
   */
  async create(text: string, options?: CreateOptions): Promise<AddResult> {
    if (!text) {
      throw new Error("text cannot be empty");
    }
    await this.tagReady;

    const type: MemoryType = options?.type ?? "episodic";
    const score = options?.score ?? 5;
    return this.createRecord(text, score, type, options?.metadata, {
      sessionUuid: this.resolveWriteSessionId(
        options?.sessionId ?? options?.sessionUuid,
      ),
      relatedIds: options?.relatedIds,
      validFrom: options?.validFrom,
      validUntil: options?.validUntil,
    });
  }

  /**
   * Truncate `text` to 200 Unicode code points, with an ellipsis when cut.
   *
   * UTF-16 code unit and can split a surrogate pair (astral emoji / CJK ext),
   * emitting a lone surrogate. `Array.from` iterates by code point, so the three
   * SDKs (Python str[:200], Go []rune, TS Array.from) truncate at the SAME point.
   */
  private truncateSummary(text: string): string {
    const codePoints = Array.from(text);
    return codePoints.length > 200
      ? codePoints.slice(0, 200).join("") + "..."
      : text;
  }

  /**
   * Store `text` as an episodic record under a caller-owned session uuid.
   *
   * Python `create_in_session`. Metadata is wrapped through the canonical
   * JSON envelope to avoid the container_tag corruption bug. Session must be
   * registered via {@link createSession} first on session-first servers.
   *
   * @param text        - Record text (stored in summary + content).
   * @param sessionUuid - Session UUID to place the record under (required).
   */
  async createInSession(text: string, sessionUuid: string): Promise<AddResult> {
    if (!sessionUuid) {
      throw new Error("createInSession: sessionUuid is required");
    }
    // On the auto-derived path the constructor starts with placeholder
    // "mem-init" until `tagReady` resolves (async sha256). Await it before
    // buildMetadataJson so records never persist with the placeholder tag and
    // fall out of container-scoped search/profile. Go/Python set the tag
    // synchronously; awaiting here matches add/search/create/newSession.
    await this.tagReady;
    const summary = this.truncateSummary(text);
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
      payload);
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
   * `append_main_ids`.
   *
   * @param recordId - Child record that receives the parents.
   * @param mainIds  - Parent IDs to append.
   */
  async appendMainIds(
    recordId: number,
    mainIds: number[]): Promise<Record<string, unknown>> {
    if (recordId <= 0) {
      throw new Error("appendMainIds: recordId must be > 0");
    }
    if (mainIds.length === 0) return {};
    return this.client.patch<Record<string, unknown>>(
      "/api/v1/records/append-main-ids",
      { ids: [recordId], main_ids_to_append: mainIds });
  }

  /**
   * Append parent record IDs to a batch of records (non-destructive).
   *
   * Junior Tip [parity]: batch form of {@link appendMainIds}; matches Python
   * `append_main_links` and Go `AppendMainLinks`.
   */
  async appendMainLinks(
    ids: number[],
    mainIdsToAppend: number[]): Promise<Record<string, unknown>> {
    if (ids.length === 0 || mainIdsToAppend.length === 0) return {};
    return this.client.patch<Record<string, unknown>>(
      "/api/v1/records/append-main-ids",
      { ids, main_ids_to_append: mainIdsToAppend });
  }

  /**
   * Append related record IDs to a single record's `related_ids` array.
   * Server-side: read, dedup, write back — idempotent (append, not replace).
   *
   * — same validation, same PATCH verb, same `{ ids: [recordId], ..._to_append }`
   * payload shape. Delegates to the server `AppendRelatedIDs` topology handler;
   * the SDK does no dedup itself so the read-modify-write stays transactional on
   * the server.
   *
   * @param recordId   - Record that receives the related links.
   * @param relatedIds - Related IDs to append.
   */
  async appendRelatedIds(
    recordId: number,
    relatedIds: number[]): Promise<Record<string, unknown>> {
    if (recordId <= 0) {
      throw new Error("appendRelatedIds: recordId must be > 0");
    }
    if (relatedIds.length === 0) return {};
    return this.client.patch<Record<string, unknown>>(
      "/api/v1/records/append-related-ids",
      { ids: [recordId], related_ids_to_append: relatedIds });
  }

  /**
   * Set `consolidate_id` on a batch of child records (star-link topology).
   * Batched so N children pointing at one star cost ONE server round-trip.
   *
   * (matches the MCP `link_consolidated` tool and the Go `LinkConsolidated` /
   * Python `link_consolidated` methods). The old `updateConsolidateIds` name is
   * callers import it — see {@link updateConsolidateIds}.
   *
   * @param ids           - Child record IDs.
   * @param consolidateId - The consolidated star's ID.
   */
  async linkConsolidated(
    ids: number[],
    consolidateId: number): Promise<Record<string, unknown>> {
    if (ids.length === 0) return {};
    if (consolidateId <= 0) {
      throw new Error("linkConsolidated: consolidateId must be > 0");
    }
    return this.client.patch<Record<string, unknown>>(
      "/api/v1/records/consolidate-ids",
      { ids, consolidate_id: consolidateId });
  }

  /**
   * Set `consolidate_id` on a batch of child records.
   *
   * @deprecated Use {@link linkConsolidated} instead — renamed 2026-06-18 to
   * match the canonical MCP `link_consolidated` tool name and the Go/Python
   * SDKs. This alias forwards verbatim and will be removed in a future major
   *
   * @param ids           - Child record IDs.
   * @param consolidateId - The consolidated star's ID.
   */
  async updateConsolidateIds(
    ids: number[],
    consolidateId: number): Promise<Record<string, unknown>> {
    return this.linkConsolidated(ids, consolidateId);
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
    options?: UpsertEntityOptions): Promise<{ id: number }> {
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
    depth?: number): Promise<EntityGraphResult> {
    const params: Record<string, string> = {};
    if (depth !== undefined) params.depth = String(depth);

    return this.client.get<EntityGraphResult>(
      `/api/v1/entities/${entityId}/graph`,
      Object.keys(params).length > 0 ? params : undefined);
  }

  /**
   * Get the full temporal history of an entity's relationships.
   *
   * Shows ALL edges including invalidated ones, ordered by event time.
   * Use to understand how an entity's context evolved over time.
   *
   * @param entityId - The entity ID.
   */
  async entityTimeline(
    entityId: number): Promise<EntityTimelineResult> {
    return this.client.get<EntityTimelineResult>(
      `/api/v1/entities/${entityId}/timeline`,
      undefined);
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
    options?: UpsertEntityEdgeOptions): Promise<void> {
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
    role?: string): Promise<void> {
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
  async getRecordEntities(
    recordId: number): Promise<EntityRecord[]> {
    const data = await this.client.get<{ entities?: EntityRecord[] }>(
      `/api/v1/records/${recordId}/entities`,
      undefined);
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
        "for soft delete.");
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
    sessionId?: string): Promise<AddResult | null> {
    const resolvedSessionId = sessionId ?? this.sessionUuid;
    // Ingest accepts content + container_tag + required session_id on
    // session-first servers. score/type/metadata are intentionally omitted —
    // the server drops them on this path. Callers that pin those fields take
    // the /records path instead (see add() forceRecordsPath).
    const payload: IngestPayload = {
      content: text,
      container_tag: this.containerTag,
      session_id: resolvedSessionId,
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
          summary: this.truncateSummary(text),
        },
      ];

      return {
        sessionId: resolvedSessionId,
        records: records.map((recordRow) => ({
          id: recordRow.id,
          type: (recordRow.type ?? "episodic") as MemoryType,
          summary: recordRow.summary ?? this.truncateSummary(text),
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
   * retrieval).
   *
   * lets `create()` thread session-uuid override, related_ids, and the
   * valid_from/valid_until temporal window through verbatim. `add()` calls this
   * WITHOUT `extra`, so its behaviour is byte-for-byte unchanged (defaults:
   * current session, no related_ids, no validity window).
   */
  private async createRecord(
    text: string,
    score: number,
    type: MemoryType,
    metadata?: Record<string, unknown>,
    extra?: {
      sessionUuid?: string;
      relatedIds?: number[];
      validFrom?: string;
      validUntil?: string;
    }): Promise<AddResult> {
    const summary = this.truncateSummary(text);
    const sessionUuid = extra?.sessionUuid ?? this.sessionUuid;

    const payload: RecordPayload = {
      uuid: sessionUuid,
      type,
      dimension: 0,
      prefix: "",
      weight: score / 10,
      score,
      vector: "",
      related_ids: extra?.relatedIds ?? [],
      main_ids: [],
      consolidate_id: 0,
      metadata: buildMetadataJson(this.containerTag, metadata),
      summary,
      content: text,
      consolidated: false,
      status: "saved",
    };
    // Only attach temporal fields when supplied — the server treats absent
    // valid_from/valid_until as "no window", so omitting keeps the wire payload
    // identical to the pre-existing add() path.
    if (extra?.validFrom) payload.valid_from = extra.validFrom;
    if (extra?.validUntil) payload.valid_until = extra.validUntil;

    // Exactly one request. If the session has no episodic anchor yet, the
    // server returns HTTP 422 ("create an episodic record first"). The SDK
    // surfaces that error unchanged — callers write an episodic first; the
    // client never fabricates a synthetic anchor.
    const data = await this.client.post<{ id?: number }>(
      "/api/v1/records",
      payload);

    return {
      // actually landed in (a create() may override it), not the instance
      // default — so the caller's AddResult.sessionId is truthful.
      sessionId: sessionUuid,
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
   * Build the shared query-param map for the manifest endpoints.
   *
   * `GET /api/v1/chats/{uuid}/manifest` accept the same `q`/`limit`/`offset`/
   * `as_of`/`since`/`until` query params. The keyword is sent as `q` (the only
   * key the session endpoint reads; the global endpoint also takes `query` but
   * `q` wins there too). `asOf` maps to the server key `as_of`. Centralised here
   * so the two public methods stay byte-identical and DRY.
   */
  private buildManifestParams(options?: {
    q?: string;
    limit?: number;
    offset?: number;
    asOf?: string;
    since?: string;
    until?: string;
  }): Record<string, string> {
    const params: Record<string, string> = {};
    if (options?.q) params.q = options.q;
    if (options?.limit !== undefined) params.limit = String(options.limit);
    if (options?.offset !== undefined) params.offset = String(options.offset);
    if (options?.asOf) params.as_of = options.asOf;
    if (options?.since) params.since = options.since;
    if (options?.until) params.until = options.until;
    return params;
  }

  /**
   * Map the server's nested search envelope into typed {@link SearchResult}s.
   *
   * The server emits `{ results: [{ record: {...}, similarity: N }] }` and the
   * SDK surface preserves that exact shape — the full record NESTED under
   * `record`, with the score as a SIBLING `similarity`.
   *
   * `{id,type,summary,score,metadata,content}`, silently DROPPING every other
   * record field (uuid/weight/related_ids/main_ids/status/valid_from/...) — a
   * data-loss bug. We now keep the whole record: cast the raw JSON object to the
   * typed {@link MemoryRecord} (the server serialises exactly those keys) and
   * default a missing `similarity` to 0. Matches Python `SearchResult(record,
   * similarity)` (the reference) and Go `SearchResult{Record, Similarity}`. NOTE
   * the score key is `similarity`, NOT `score`.
   */
  private nestSearchResults(
    results?: Array<{
      record?: Record<string, unknown>;
      similarity?: number;
    }>): SearchResult[] {
    return (results ?? []).map((item) => ({
      record: (item.record ?? {}) as unknown as MemoryRecord,
      similarity: item.similarity ?? 0,
    }));
  }

  /** String representation for logging / debugging. */
  toString(): string {
    return `Memory(containerTag=${this.containerTag}, session=${this.sessionUuid})`;
  }
}
