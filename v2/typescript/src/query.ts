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
 * surface is intentionally thin — the grammar rules live in `queryGuards.ts`
 * and are checked CLIENT-side here as an early, actionable error, AND again
 * server-side (HTTP 400) as the source of truth.
 *
 * What is checked before a request is spent: the filter/sort COLUMN, the
 * OPERATOR, the sort DIRECTION, a `null` VALUE, an empty `$in`, and the
 * pagination window. Every rejection is an `AnhurQueryError` carrying kind
 * `"invalid_request"` and `retryable` false — the same shape a server 400
 * arrives as, so one catch block covers both. `queryGuards.ts` documents which
 * remaining mistakes are deliberately left to the server.
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

import {
  MAX_QUERY_LIMIT,
  assertFilterColumn,
  assertFilterValue,
  assertOperator,
  invalidQueryError,
  normalizeSortOrder,
} from "./queryGuards.js";
import type { Memory } from "./memory.js";
import type {
  AstQuery,
  QueryFilterCondition,
  QueryOperator,
  QueryResult,
  QuerySortClause,
} from "./types.js";

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
   * @param value    - Scalar for most operators; a non-empty array for `$in`.
   *                   `null` is refused — see {@link assertFilterValue}.
   * @throws {AnhurQueryError} kind `"invalid_request"`, when the column, the
   *         operator or the value is one the grammar cannot honour.
   */
  where(field: string, operator: QueryOperator, value: unknown): this {
    assertFilterColumn("where", field);
    assertOperator(operator);
    // Junior Tip [the value is checked LAST and on EVERY path]: `whereEquals`
    // funnels here, so one call site covers both public spellings. Order
    // matters only for the message a caller sees first — naming a bogus column
    // is more useful than complaining about its value.
    assertFilterValue(field, operator, value);
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
   * @param order - "asc" or "desc", any case (default "desc").
   * @throws {AnhurQueryError} kind `"invalid_request"`, when the column is not
   *         allowed or the direction is not one the server recognises.
   */
  orderBy(field: string, order: "asc" | "desc" = "desc"): this {
    assertFilterColumn("orderBy", field);
    // The union above is erased at compile time; this is the check that still
    // exists when a plain-JavaScript caller passes "sideways".
    this.sortClauses.push({ field, order: normalizeSortOrder(order) });
    return this;
  }

  /**
   * Set the maximum number of results.
   *
   * @param maxResults - 1..1000 (the server hard-caps at 1000).
   * @throws {AnhurQueryError} kind `"invalid_request"`, if out of range.
   */
  limit(maxResults: number): this {
    if (maxResults < 1 || maxResults > MAX_QUERY_LIMIT) {
      throw invalidQueryError(
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
   * @throws {AnhurQueryError} kind `"invalid_request"`, if negative.
   */
  offset(skip: number): this {
    if (skip < 0) {
      throw invalidQueryError("QueryBuilder.offset cannot be negative.");
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
