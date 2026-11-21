/**
 * AnhurDB TypeScript SDK — the exact-column read shapes: the AST query engine
 * (`POST /api/v1/query`) and the manifest / list_chat listings.
 *
 * One domain in one sentence: reads that filter, sort and page on real columns
 * instead of ranking by relevance. Split out of `types.ts` (681 lines, house
 * cut ~300) in 4.0.0; `types.ts` re-exports everything here.
 */

import type { MemoryRecord } from "./coreTypes.js";

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
