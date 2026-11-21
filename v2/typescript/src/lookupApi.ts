/**
 * AnhurDB TypeScript SDK — the non-hybrid lookups: exact type listing,
 * lexical "smart" search, and the structured AST query.
 *
 * One domain in one sentence: find records WITHOUT the hybrid ranker — by
 * exact cognitive type (`GET /api/v1/search/type`), by FTS text with weight
 * boosting (`GET /api/v1/search/smart`), or by whitelisted columns
 * (`POST /api/v1/query`). Moved out of `memory.ts` (1532 lines, house cut
 * ~300) in 4.0.0; `Memory` extends this class, so the public surface is
 * identical.
 */

import { MemoryFilesApi } from "./filesApi.js";
import { appendSessionsQueryParam, normalizeSessions } from "./sessionFilter.js";
import type {
  AstQuery,
  MemoryRecord,
  MemoryType,
  QueryResult,
  SearchScope,
  SmartSearchResponse,
  TypeListingResult,
} from "./types.js";

/** Lookup surface of `Memory`. Holds no state: it borrows the transport. */
export abstract class MemoryLookupApi extends MemoryFilesApi {
  /**
   * List/filter records by cognitive type in the tenant store.
   *
   * Faster than plane search when you know the exact type.
   *
   * Agent UX — not a plane switch: no `scope` parameter. Does **not** search
   * Shared Data. For specialty docs use {@link searchTenantShared} /
   * {@link searchClientShared} / {@link searchShared} (or `search` with scope).
   *
   * `sessions` is MANDATORY (ADR-0014), exactly as in {@link search}: this
   * endpoint had no session argument at all before, so "give me the facts of
   * this chat" quietly returned the facts of every chat.
   *
   * @param type     - The memory type to filter by (e.g. "fact", "episodic").
   * @param sessions - Session filter (required): `sessionsAll()` or uuids.
   * @param limit    - Maximum results to return (default 20).
   * @param query    - Optional keyword search within the type.
   */
  async searchByType(
    type: MemoryType,
    sessions: string[],
    limit?: number,
    query?: string): Promise<TypeListingResult[]> {
    const resolvedSessions = normalizeSessions(sessions);
    const params: Array<[string, string]> = [["type", type]];
    if (limit !== undefined) params.push(["limit", String(limit)]);
    appendSessionsQueryParam(params, resolvedSessions);
    if (query) params.push(["q", query]);

    // the `{results:[{record,similarity}]}` envelope of search/recall/searchSession.
    // BARE record array under `records`: `{records:[<Record>],count:N}`. Reading
    // `data.results` therefore matched nothing and returned `[]` for EVERY call —
    // the cross-SDK "searchByType returns empty" bug. We read `records` and nest each
    // full record so no field is dropped. Mirrors Go SearchByType and Python
    // search_by_type (same key, same records).
    //
    // Junior Tip [why no `similarity` here — and why that is the fix]: a type
    // filter computes no distance. This used to fabricate `similarity: 0` to
    // satisfy {@link SearchResult}, and `0` is not "unknown": it is the WORST
    // comparable score, so merging these hits with `search()` hits and sorting
    // buried every one of them, silently and forever. The return type is now
    // {@link TypeListingResult}, which has no `similarity` at all, so that merge
    // stops compiling instead of quietly mis-ranking. The ranking signal that
    // DOES exist travels untouched inside the record (`weight`, `score`).
    const data = await this.searchTransport.get<{
      records?: Array<Record<string, unknown>>;
    }>("/api/v1/search/type", params);
    return (data.records ?? []).map((rawRecord) => ({
      record: rawRecord as unknown as MemoryRecord,
    }));
  }

  /**
   * Full-text search with cognitive weight boosting.
   *
   * Prefer over {@link search} for conceptual text queries (no embedding
   * required). Uses `GET /api/v1/search/smart` with the same memory-plane
   * `scope` as {@link search} (default `sessions`).
   *
   * `sessions` is MANDATORY (ADR-0014), exactly as in {@link search}.
   * `smartSearch` is one of the two paths that had no session argument at all
   * before — it accepted the scope, dropped the chat filter, and answered from
   * every conversation.
   *
   * @param query    - Search query.
   * @param sessions - Session filter (required): `sessionsAll()` or uuids.
   * @param limit    - Maximum results (default 10).
   * @param type     - Optional memory type filter.
   * @param scope    - Search plane (default `sessions`).
   */
  async smartSearch(
    query: string,
    sessions: string[],
    limit?: number,
    type?: MemoryType,
    scope?: SearchScope): Promise<SmartSearchResponse> {
    const resolvedSessions = normalizeSessions(sessions);
    const params: Array<[string, string]> = [
      ["q", query],
      ["limit", String(limit ?? 10)],
      ["scope", scope ?? "sessions"],
    ];
    appendSessionsQueryParam(params, resolvedSessions);
    if (type) params.push(["type", type]);

    return this.searchTransport.get<SmartSearchResponse>(
      "/api/v1/search/smart",
      params);
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
    const data = await this.searchTransport.postRead<{
      records?: MemoryRecord[] | null;
      count?: number;
    }>("/api/v1/query", ast);
    const records = data.records ?? [];
    return {
      records,
      count: data.count ?? records.length,
    };
  }
}
