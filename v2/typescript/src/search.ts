/**
 * AnhurDB TypeScript SDK — the public hybrid-search surface of `Memory`.
 *
 * `search`, `searchWithRetrieval`, the four scope wrappers, `searchSession`
 * and `recall` all live here, as `MemorySearchApi`, which `Memory` extends.
 * The methods appear on a `Memory` instance exactly as before — this is a file
 * boundary, not an API boundary.
 *
 * Junior Tip [why a base class and not a helper object]: `memory.ts` was 2150
 * lines and house law forbids growing a file already past the ~300-line cut;
 * ADR-0031 needed three new request knobs plus a cross-version guard. Moving
 * the domain into its own file had to happen WITHOUT moving a single method
 * off the public class, because `mem.search(...)` is the SDK's most-called
 * entry point and the Go/Python SDKs expose it flat too. An abstract base
 * class keeps the method on the instance and keeps the file honest; the three
 * `protected abstract` accessors below are the entire contract `Memory` owes
 * this file.
 *
 * @module
 */

import type { HttpClient } from "./client.js";
import { normalizeSessions } from "./sessionFilter.js";
import { buildSearchPayload, nestSearchResults } from "./searchRequest.js";
import { checkSearchKnobsHonored } from "./searchMode.js";
import type { RawSearchResponse } from "./searchRequest.js";
import type { SearchOptions } from "./searchTypes.js";
import type { SearchResult, SearchWithRetrievalResult } from "./searchResults.js";

/**
 * The hybrid-search half of `Memory`.
 *
 * Abstract on purpose: it owns no state. Everything it needs — the HTTP
 * transport, the container-tag readiness promise, the current session — is
 * supplied by the concrete `Memory` through the three accessors below, so
 * this file can never disagree with `memory.ts` about who owns what.
 */
export abstract class MemorySearchApi {
  /** The HTTP transport `Memory` built from the constructor options. */
  protected abstract get searchTransport(): HttpClient;
  /**
   * Resolves once the container tag exists.
   *
   * Junior Tip [why every search awaits it]: with no explicit `userId` the
   * tag — and therefore the default session uuid — is derived from an ASYNC
   * SHA-256 of the API key. A search issued in the same tick as the
   * constructor would otherwise filter on an empty session.
   */
  protected abstract get searchTagReady(): Promise<void>;
  /** Current session uuid, or `""` when none has been opened yet. */
  protected abstract get searchCurrentSession(): string;

  // ── search() — find relevant memories ───────────────────────

  /**
   * Hybrid plane search via `POST /api/v1/search`.
   *
   * Default scope `sessions` (tenant chat; excludes shared-library uuids).
   *
   * `sessions` is MANDATORY (ADR-0014): pass `sessionsAll()` for every
   * session inside the scope, or the explicit uuids to confine the query to
   * those chats. An empty array is an error, never "all".
   *
   * Junior Tip [scope vs sessions]: the two are orthogonal. `scope` picks the
   * BOUNDARY (which store/plane is reachable at all); `sessions` picks the
   * SUBSET inside that boundary. `["*"]` means "everything in this boundary" —
   * it is not a way to cross into a shared plane.
   *
   * Ranking effort is picked with {@link SearchOptions.mode} (ADR-0031):
   * `fast` never embeds, `balanced` (the default) degrades to the lexical
   * legs when the embedder is down, `semantic` refuses to answer without
   * semantics. `semantic` is enforced END TO END — see {@link runSearch}.
   *
   * @param query    - Query string sent as FTS `text`.
   * @param sessions - Session filter (required): `sessionsAll()` or uuids.
   * @param options  - Optional limit, type filter, scope plane and ADR-0031 knobs.
   * @returns Array of search results sorted by relevance.
   * @throws {AnhurError} `INVALID_PARAM: ...` when the session filter is
   *   absent, empty, contradictory, or above the cap; when `mode` is not a
   *   known mode; or when the server ignored `mode: "semantic"`.
   *
   * @example
   * ```ts
   * const results = await mem.search(
   *   "what does this user do?", sessionsAll(), { limit: 5 });
   * results.forEach(r => console.log(r.record.summary, r.similarity));
   * ```
   */
  async search(
    query: string,
    sessions: string[],
    options?: SearchOptions): Promise<SearchResult[]> {
    const { results } = await this.runSearch(query, sessions, options);
    return results;
  }

  /**
   * Same request/ranking as {@link search}, but also returns the ADR-0012
   * `retrieval` diagnostics block (which arms ran, degradation reason,
   * RESOLVED astar/entity-jaccard weights after any per-request override) and
   * the ADR-0031 `legScores` block when
   * {@link SearchOptions.debugSignals} was set.
   *
   * Junior Tip [why a separate method instead of changing `search()`'s
   * return type]: `search()` already ships as `Promise<SearchResult[]>` and
   * existing callers iterate/destructure that array directly — widening it to
   * `{results, retrieval}` would be a breaking change for every current
   * caller. This method is opt-in additive surface for callers that want the
   * diagnostics, e.g. an ablation harness confirming its
   * {@link SearchOptions.astarWeight} override actually took effect
   * server-side. Named `searchWithRetrieval` (not `searchWithMeta`) to match
   * Go `SearchWithRetrieval` / Python `search_with_retrieval` — same concept,
   * TS camelCase (2026-08-10, cross-SDK name alignment).
   *
   * @returns `results` (same shape as {@link search}) plus `retrieval` and
   *   `legScores` — each `undefined` only if the server omitted that block.
   */
  async searchWithRetrieval(
    query: string,
    sessions: string[],
    options?: SearchOptions): Promise<SearchWithRetrievalResult> {
    return this.runSearch(query, sessions, options);
  }

  /**
   * Shared implementation behind {@link search}, {@link searchWithRetrieval}
   * and {@link searchSession} — builds the wire payload, posts
   * `/api/v1/search`, checks that the server honoured the ADR-0031 knobs, and
   * nests the response. Kept private so no public method can drift on payload
   * construction (the exact bug class ADR-0014's session-filter Junior Tips
   * warn about: two assembly sites for the same request silently diverging).
   */
  private async runSearch(
    query: string,
    sessions: string[],
    options?: SearchOptions): Promise<SearchWithRetrievalResult> {
    if (!query) {
      throw new Error("query cannot be empty");
    }
    const resolvedSessions = normalizeSessions(sessions);
    await this.searchTagReady;

    const payload = buildSearchPayload(query, resolvedSessions, options);

    // Search is a read-shaped POST endpoint.
    const data = await this.searchTransport.postRead<RawSearchResponse>(
      "/api/v1/search",
      payload);

    // The RESPONSE is the only honest witness that the knobs were understood
    // (ADR-0031): a server that predates them answers 200 and ignores them.
    // This runs BEFORE the results are handed back, so a caller who asked for
    // strict semantics never sees lexical hits presented as semantic ones.
    checkSearchKnobsHonored(options, data.retrieval, payload.scope);

    return {
      results: nestSearchResults(data.results),
      retrieval: data.retrieval,
      legScores: data.leg_scores,
    };
  }

  /**
   * Search chat sessions only (`scope=sessions`).
   * `sessions` is mandatory — see {@link search}.
   */
  async searchSessions(
    query: string,
    sessions: string[],
    options?: SearchOptions): Promise<SearchResult[]> {
    return this.search(query, sessions, { ...options, scope: "sessions" });
  }

  /**
   * Search tenant-shared library docs (`scope=tenant_shared`).
   * `sessions` is mandatory and selects inside the shared boundary.
   */
  async searchTenantShared(
    query: string,
    sessions: string[],
    options?: SearchOptions): Promise<SearchResult[]> {
    return this.search(query, sessions, { ...options, scope: "tenant_shared" });
  }

  /**
   * Search client-wide shared library (`scope=client_shared`).
   * `sessions` is mandatory and selects inside the shared boundary.
   */
  async searchClientShared(
    query: string,
    sessions: string[],
    options?: SearchOptions): Promise<SearchResult[]> {
    return this.search(query, sessions, { ...options, scope: "client_shared" });
  }

  /**
   * Search both shared planes (`scope=shared_all`).
   * `sessions` is mandatory and selects inside both shared boundaries.
   */
  async searchShared(
    query: string,
    sessions: string[],
    options?: SearchOptions): Promise<SearchResult[]> {
    return this.search(query, sessions, { ...options, scope: "shared_all" });
  }

  // ── searchSession() — session-scoped hybrid search ──────────

  /**
   * Search for relevant memories WITHIN a single chat/session.
   *
   * Sugar over `search(query, [sessionUuid])` — the one-chat case expressed in
   * the ADR-0014 grammar.
   *
   * Junior Tip [why the empty uuid stopped meaning "everything"]: this method
   * used to send `uuid: ""` when there was no current session, and the server
   * read that as "no session filter". A method named `searchSession` silently
   * searching every session is the exact defect ADR-0014 exists to kill.
   * Widening is now spelled `search(query, sessionsAll())`.
   *
   * Junior Tip [why it stopped building its own payload, 2.1.0]: it used to
   * assemble a `SearchSessionPayload` by hand and forward only `limit` and
   * `typeFilter`, so `skipQueryEmbed`, the weight overrides, `expandRelated`
   * — and, once ADR-0031 landed, `mode` — were dropped in silence. Same wire
   * body as before for every existing caller; every knob honoured from now on.
   *
   * @param query       - Natural language query (sent as `text`).
   * @param sessionUuid - Session UUID to scope to. Empty/omitted = current session.
   * @param options     - Same options bag as {@link search}; `scope` is pinned.
   * @throws {AnhurError} `INVALID_PARAM: ...` when neither an explicit session
   *   nor a current session is available.
   */
  async searchSession(
    query: string,
    sessionUuid?: string,
    options?: SearchOptions): Promise<SearchResult[]> {
    const targetSession = (sessionUuid ?? this.searchCurrentSession) || "";
    const { results } = await this.runSearch(query, [targetSession], {
      ...options,
      scope: "sessions",
    });
    return results;
  }

  /**
   * Recall memories via plane-aware search.
   *
   * Explicit alias for `search()` (default `scope=sessions`).
   * Named to match the MCP `recall` tool.
   *
   * `sessions` is MANDATORY (ADR-0014) — see {@link search}.
   *
   * Junior Tip [why it now spreads `options`, 2.1.0]: it used to copy exactly
   * `scope` and `typeFilter` across, so an `mem.recall(q, s, 5, {mode:
   * "semantic"})` would have run balanced without a word. A wrapper that
   * re-lists the fields it forwards is a wrapper that goes stale on the next
   * knob; spreading keeps `recall` and `search` the same request forever.
   *
   * @param query    - Natural language query.
   * @param sessions - Session filter (required): `sessionsAll()` or uuids.
   * @param limit    - Maximum results (default 10). Wins over `options.limit`
   *   only because `limit` is this method's dedicated parameter.
   * @param options  - Optional scope (and other search options except limit).
   */
  async recall(
    query: string,
    sessions: string[],
    limit?: number,
    options?: Omit<SearchOptions, "limit">): Promise<SearchResult[]> {
    return this.search(query, sessions, {
      ...options,
      limit: limit ?? 10,
      scope: options?.scope ?? "sessions",
    });
  }
}
