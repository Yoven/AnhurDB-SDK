/**
 * AnhurDB TypeScript SDK — the listings: recent records, the tenant and
 * session manifests, one chat's records, the per-type tally and the local type
 * list.
 *
 * One domain in one sentence: enumerate what is stored, in pages, without any
 * relevance ranking. Moved out of `memory.ts` (1532 lines, house cut ~300) in
 * 4.0.0; `Memory` extends this class, so the public surface is identical.
 */

import { AnhurError } from "./errors.js";
import { MemoryLookupApi } from "./lookupApi.js";
import type {
  ListChatOptions,
  ListChatResult,
  ManifestGlobalOptions,
  ManifestResult,
  ManifestSessionOptions,
  MemoryRecord,
  MemoryType,
} from "./types.js";

/** Listing surface of `Memory`. Holds no state: it borrows the transport. */
export abstract class MemoryManifestApi extends MemoryLookupApi {
  /**
   * Fetch the most recent records via the dedicated `GET /api/v1/recent`
   * endpoint (server handler `ListRecent`).
   *
   * DEDICATED recents route (`/api/v1/recent`), NOT the paginated
   * `/api/v1/manifest` (`ManifestGlobal`) — they are different endpoints and
   * only {@link manifestGlobal} should use the manifest. The server always
   * answers the envelope `{"records": [...], "count": N}`
   * (`handler/record_session.go` `ListRecent`), with `records: null` for zero
   * rows — hence the `?? []`. Mirrors Python `recent` and Go `Recent`.
   *
   * Junior Tip [no bare-array branch, 4.0.0]: this used to also accept a naked
   * `[...]` from servers that predated the envelope. None exists any more, and
   * reading `.records` off an array would answer `[]` — "no memories" — in
   * silence. A non-envelope body now fails loudly instead (as Go's decoder
   * does).
   *
   * @param limit - Maximum records to return (default 20).
   */
  async recent(
    limit?: number): Promise<MemoryRecord[]> {
    const params: Record<string, string> = {};
    if (limit !== undefined) params.limit = String(limit);

    const data = await this.searchTransport.get<{ records?: MemoryRecord[] | null }>(
      "/api/v1/recent", params);
    if (data === null || typeof data !== "object" || Array.isArray(data)) {
      throw new AnhurError(
        "recent: GET /api/v1/recent answered a non-envelope body (expected {records, count})",
        undefined,
        "server",
      );
    }
    return data.records ?? [];
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
    return this.searchTransport.get<ManifestResult>(
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
    return this.searchTransport.get<ManifestResult>(
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

    const data = await this.searchTransport.get<ListChatResult>(
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
      "router",
    ];
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
}
