/**
 * Fluent query builder for the AnhurDB AST query engine.
 *
 * Generates the JSON Abstract Syntax Tree (AST) that the server processes via
 * `POST /api/v1/query`. The AST is validated server-side against a column
 * whitelist and a fixed operator set.
 *
 *
 * (anhurdb/query/builder.py) so all three SDKs build the identical AST. The
 * builder produces a plain {@link AstQuery} object; pass it to
 * `Memory.query(ast)` (or call `.execute(memory)`) to run it. The fluent
 * surface is intentionally thin — the WHERE/SORT column whitelist and operator
 * set are validated CLIENT-side here as an early, actionable error, AND again
 * server-side (HTTP 400) as the source of truth.
 *
 * Usage:
 *   ```ts
 *   import { Memory, QueryBuilder } from "anhurdb";
 *
 *   const mem = new Memory({ apiKey: "anhur_xxx" });
 *   const ast = new QueryBuilder()
 *     .where("type", "$eq", "risk")
 *     .where("weight", "$gt", 0.8)
 *     .orderBy("weight", "desc")
 *     .limit(10)
 *     .build();
 *   const { records } = await mem.query(ast);
 *
 *   // Or execute directly:
 *   const { records: r2 } = await new QueryBuilder()
 *     .where("status", "$eq", "saved")
 *     .execute(mem);
 *   ```
 *
 * @module
 */

import type { Memory } from "./memory.js";
import type {
  AstQuery,
  QueryFilterCondition,
  QueryOperator,
  QueryResult,
  QuerySortClause,
} from "./types.js";

/**
 * Columns the server allows in `filters` and `sort`.
 *
 * allowedFilterColumns]: MUST stay identical to the Python
 * `ALLOWED_WHERE_COLUMNS` set and the server whitelist. A column outside this
 * set is HTTP 400 ('invalid filter field' / 'invalid sort field') server-side;
 * we reject it client-side too for a faster, clearer error.
 */
const ALLOWED_WHERE_COLUMNS: ReadonlySet<string> = new Set([
  "id",
  "uuid",
  "type",
  "dimension",
  "weight",
  "score",
  "status",
  "consolidated",
  "archived",
  "created_at",
  "updated_at",
  "prefix",
  "metadata",
  "summary",
  "superseded_by",
  "valid_from",
  "valid_until",
]);

/**
 * Operators the server actually implements.
 *
 * server silently ignores them (Python dropped them from `_OP_MAP` for the same
 * reason). Exposing an operator the server ignores would be a silent-loss bug.
 */
const ALLOWED_OPERATORS: ReadonlySet<QueryOperator> = new Set<QueryOperator>([
  "$eq",
  "$gt",
  "$gte",
  "$lt",
  "$lte",
  "$in",
]);

/** Hard cap the server applies to `pagination.limit`. */
const MAX_QUERY_LIMIT = 1000;

/**
 * Fluent builder for AnhurDB AST queries.
 *
 * Every mutator returns `this` for chaining. Call {@link build} to get the
 * plain AST object, or {@link execute} to run it against a {@link Memory}.
 */
export class QueryBuilder {
  private readonly filters: Record<string, QueryFilterCondition> = {};
  private readonly sortClauses: QuerySortClause[] = [];
  private readonly selectFields: string[] = [];
  private limitValue = 50;
  private offsetValue = 0;

  /**
   * Restrict which fields are returned.
   *
   * SELECT list is fixed and the full Record is always returned. Included only
   * for forward-compatibility and parity with the Python `select()`.
   *
   * @param fields - Column names to request.
   */
  select(...fields: string[]): this {
    this.selectFields.push(...fields);
    return this;
  }

  /**
   * Add a filter condition on a single column.
   *
   * @param field    - Column name (must be in the server whitelist).
   * @param operator - One of `$eq`/`$gt`/`$gte`/`$lt`/`$lte`/`$in`.
   * @param value    - Scalar for most operators; an array for `$in`.
   * @throws Error if the column or operator is not allowed.
   */
  where(field: string, operator: QueryOperator, value: unknown): this {
    if (!ALLOWED_WHERE_COLUMNS.has(field)) {
      throw new Error(
        `QueryBuilder.where: field "${field}" is not allowed. ` +
          `Allowed: ${[...ALLOWED_WHERE_COLUMNS].sort().join(", ")}`,
      );
    }
    if (!ALLOWED_OPERATORS.has(operator)) {
      throw new Error(
        `QueryBuilder.where: operator "${operator}" is not supported. ` +
          `Allowed: ${[...ALLOWED_OPERATORS].sort().join(", ")}`,
      );
    }
    // merge their operators into one condition object (e.g. weight $gt + $lt),
    // matching the Python builder's per-field dict accumulation.
    const condition = this.filters[field] ?? {};
    condition[operator] = value;
    this.filters[field] = condition;
    return this;
  }

  /**
   * Shorthand for an exact-match (`$eq`) filter.
   *
   * @param field - Column name (must be in the whitelist).
   * @param value - The value to match.
   */
  whereEquals(field: string, value: unknown): this {
    return this.where(field, "$eq", value);
  }

  /**
   * Add a sort clause.
   *
   * @param field - Column to sort by (must be in the whitelist).
   * @param order - "asc" or "desc" (default "desc").
   * @throws Error if the column is not allowed.
   */
  orderBy(field: string, order: "asc" | "desc" = "desc"): this {
    if (!ALLOWED_WHERE_COLUMNS.has(field)) {
      throw new Error(
        `QueryBuilder.orderBy: field "${field}" is not allowed. ` +
          `Allowed: ${[...ALLOWED_WHERE_COLUMNS].sort().join(", ")}`,
      );
    }
    this.sortClauses.push({ field, order });
    return this;
  }

  /**
   * Set the maximum number of results.
   *
   * @param maxResults - 1..1000 (the server hard-caps at 1000).
   * @throws Error if out of range.
   */
  limit(maxResults: number): this {
    if (maxResults < 1 || maxResults > MAX_QUERY_LIMIT) {
      throw new Error(
        `QueryBuilder.limit must be between 1 and ${MAX_QUERY_LIMIT}.`,
      );
    }
    this.limitValue = maxResults;
    return this;
  }

  /**
   * Set the pagination offset.
   *
   * @param skip - Number of results to skip (>= 0).
   * @throws Error if negative.
   */
  offset(skip: number): this {
    if (skip < 0) {
      throw new Error("QueryBuilder.offset cannot be negative.");
    }
    this.offsetValue = skip;
    return this;
  }

  /**
   * Compile the builder state into the plain {@link AstQuery} object the server
   * expects (sent FLAT as the request body — never wrapped in `{"query": ...}`).
   */
  build(): AstQuery {
    const ast: AstQuery = {
      filters: { ...this.filters },
      pagination: {
        limit: this.limitValue,
        offset: this.offsetValue,
      },
    };
    if (this.selectFields.length > 0) {
      // Dedupe while preserving the caller's first-seen order.
      ast.select = [...new Set(this.selectFields)];
    }
    if (this.sortClauses.length > 0) {
      ast.sort = this.sortClauses.map((clause) => ({ ...clause }));
    }
    return ast;
  }

  /**
   * Compile and run the query against a {@link Memory} instance.
   *
   * `QueryBuilder.execute()` — it keeps the builder ignorant of HTTP by
   * delegating to `memory.query(ast)`.
   *
   * @param memory - The Memory client to execute against.
   */
  execute(memory: Memory): Promise<QueryResult> {
    return memory.query(this.build());
  }
}
