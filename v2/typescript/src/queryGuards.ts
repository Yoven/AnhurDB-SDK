/**
 * Client-side grammar guards for the AnhurDB AST query engine.
 *
 * Split out of `query.ts` (house cut, ~300 lines) because these are two
 * different domains: `query.ts` is a fluent BUILDER — accumulate, then compile
 * — while this file is the GRAMMAR the server enforces, transcribed so a
 * mistake is caught before a request is spent. The builder changes when the
 * ergonomics change; this file changes only when the server's grammar changes.
 *
 * Junior Tip [what belongs in here and what deliberately does not]: a guard
 * earns its place when the server's answer to the mistake is SILENT — a 200
 * that quietly means something other than what the caller asked for. Those are
 * unfindable from the outside: no error, no status, just wrong rows. A mistake
 * the server rejects with a NAMED 400 is already loud, and duplicating it here
 * only risks the copy drifting from the server and rejecting something the
 * server would have accepted. Every rule below is one of:
 *   - silent-wrong  → guarded here (null value, unknown sort direction)
 *   - named 400     → guarded here ONLY where the other two SDKs guard it too,
 *                     so one mistake does not cost a round trip in one SDK and
 *                     nothing in the others (unknown column, unknown operator,
 *                     empty `$in`)
 * Any change here MUST land in Go (`golang/client/query_validation.go`) and
 * Python (`python/anhurdb/query/builder.py`) in the same change: a guard that
 * exists in one SDK only is a parity break.
 *
 * Grammar source of truth: AnhurDB `server/service/record_ast_query.go`,
 * re-confirmed against the live router on 2026-09-13.
 *
 * @module
 */

import { AnhurQueryError } from "./errors.js";
import type { QueryOperator } from "./types.js";

/**
 * Columns the server allows in `filters` and `sort` — all 17, CASE-SENSITIVE.
 *
 * Junior Tip [why the case matters]: the server compares these byte-for-byte,
 * so `"TYPE"` is HTTP 400 'invalid filter field', not a friendly fold to
 * `"type"`. Keep this set identical to the Python `ALLOWED_WHERE_COLUMNS` and
 * the Go `astAllowedFilterColumns`; none of the three can import the server.
 */
export const ALLOWED_WHERE_COLUMNS: ReadonlySet<string> = new Set([
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
 * `$ne`/`$nin`/`$like`/`$exists`/`$regex` are absent on purpose: the server
 * answers 400 for them, and there is no `$or`/`$and`/`$not` either — `filters`
 * is FLAT and every predicate is ANDed. Offering any of those would be
 * inventing a capability the engine does not have.
 */
export const ALLOWED_OPERATORS: ReadonlySet<QueryOperator> =
  new Set<QueryOperator>(["$eq", "$gt", "$gte", "$lt", "$lte", "$in"]);

/**
 * Sort directions the server recognises, lower-cased.
 *
 * The server's whitelist is `ASC/DESC/asc/desc`; this SDK emits lower case
 * only, and accepts either case from the caller before folding it.
 */
export const ALLOWED_SORT_ORDERS: ReadonlySet<string> = new Set([
  "asc",
  "desc",
]);

/** Hard cap the server applies to `pagination.limit`. */
export const MAX_QUERY_LIMIT = 1000;

/**
 * Build the ONE error every client-side query rejection is thrown as.
 *
 * Junior Tip [why this is not `new Error(...)`, 2026-09-14]: it used to be. A
 * bare `Error` has no `kind`, no `retryable` and no `statusCode`, while the
 * SERVER's answer to the very same mistake arrives as an `AnhurQueryError`
 * (kind `"invalid_request"`, retryable false, statusCode 400). A caller could
 * not write one catch block: `if (error instanceof AnhurQueryError)` silently
 * dropped every builder rejection on the floor, and `error.kind` read
 * `undefined`. Same class, same `kind`, same `retryable` — the only honest
 * difference is the absent `statusCode`, because no HTTP request happened.
 *
 * @param message - Actionable text naming the offending value AND the fix.
 */
export function invalidQueryError(message: string): AnhurQueryError {
  return new AnhurQueryError(message, undefined, "invalid_request");
}

/** The whitelist, sorted, for an error message that names the fix. */
function allowedColumnList(): string {
  return [...ALLOWED_WHERE_COLUMNS].sort().join(", ");
}

/**
 * Reject a column the server's whitelist does not carry.
 *
 * @param methodName    - Builder method quoted in the message (`where`/`orderBy`).
 * @param requestedField - The column the caller asked for.
 * @throws {AnhurQueryError} kind `"invalid_request"`, when not whitelisted.
 */
export function assertFilterColumn(
  methodName: string,
  requestedField: string,
): void {
  if (!ALLOWED_WHERE_COLUMNS.has(requestedField)) {
    throw invalidQueryError(
      `QueryBuilder.${methodName}: field "${requestedField}" is not allowed. ` +
        `Allowed: ${allowedColumnList()}`,
    );
  }
}

/**
 * Reject an operator outside the server's six.
 *
 * @param requestedOperator - The operator the caller asked for.
 * @throws {AnhurQueryError} kind `"invalid_request"`, when unsupported.
 */
export function assertOperator(requestedOperator: QueryOperator): void {
  if (!ALLOWED_OPERATORS.has(requestedOperator)) {
    throw invalidQueryError(
      `QueryBuilder.where: operator "${requestedOperator}" is not supported. ` +
        `Allowed: ${[...ALLOWED_OPERATORS].sort().join(", ")}`,
    );
  }
}

/**
 * Reject a sort direction the server does not recognise.
 *
 * Junior Tip [the compiler is not a runtime check, 2026-09-14]: `orderBy`'s
 * `"asc" | "desc"` union only exists until `tsc` finishes. A plain-JavaScript
 * caller, an `as any`, or a direction read out of JSON reaches this function
 * with anything at all — and the server does NOT reject an unknown direction:
 * `record_ast_query.go:240-242` falls back to DESC and answers 200. So the
 * caller asks for ascending, receives descending, and is told nothing. That is
 * the exact silent-wrong shape this SDK refuses to pass through.
 *
 * @param requestedOrder - The raw direction, any case, before folding.
 * @returns The direction folded to the lower case the server is sent.
 * @throws {AnhurQueryError} kind `"invalid_request"`, on anything else.
 */
export function normalizeSortOrder(requestedOrder: string): "asc" | "desc" {
  const foldedOrder =
    typeof requestedOrder === "string" ? requestedOrder.toLowerCase() : "";
  if (!ALLOWED_SORT_ORDERS.has(foldedOrder)) {
    throw invalidQueryError(
      `QueryBuilder.orderBy: order "${String(requestedOrder)}" is not allowed. ` +
        `Allowed: asc, desc. The server does not reject an unknown direction — ` +
        `it SILENTLY sorts DESC and answers 200.`,
    );
  }
  return foldedOrder as "asc" | "desc";
}

/**
 * Reject filter values that compile to a query which can never match.
 *
 * Junior Tip [why `null` is refused instead of forwarded, 2026-09-14]: `null`
 * IS in the server's scalar set, so `{"$eq": null}` is accepted and compiled to
 * `col = ?` bound to NULL. In SQL that comparison is never true, for any row,
 * for any column — the answer is HTTP 200 with an empty page, forever. Proven
 * live against `superseded_by`, where the TRUE answer is every row (the
 * endpoint only ever returns rows whose `superseded_by` IS NULL). And it cannot
 * be repaired by writing it differently: the grammar has no `$exists`, no `$ne`
 * and no IS NULL operator, so "this column is null" is not expressible at all.
 * Forwarding it would hand the caller a query that lies quietly.
 *
 * The same reasoning covers a null ELEMENT inside `$in`: `col IN (..., NULL)`
 * never matches on the NULL, so that element is dead weight the caller believes
 * is doing work.
 *
 * @param filterField      - Column being filtered, quoted in the message.
 * @param requestedOperator - Operator the value belongs to.
 * @param filterValue      - The value the caller supplied.
 * @throws {AnhurQueryError} kind `"invalid_request"`, on null or an empty `$in`.
 */
export function assertFilterValue(
  filterField: string,
  requestedOperator: QueryOperator,
  filterValue: unknown,
): void {
  if (filterValue === null) {
    throw invalidQueryError(nullValueMessage(filterField, requestedOperator));
  }
  if (requestedOperator !== "$in" || !Array.isArray(filterValue)) {
    return;
  }
  if (filterValue.length === 0) {
    throw invalidQueryError(
      `QueryBuilder.where: filter "${filterField}": $in requires a non-empty ` +
        `array of values (the server answers 400 for an empty one).`,
    );
  }
  for (const candidateValue of filterValue) {
    if (candidateValue === null) {
      throw invalidQueryError(nullValueMessage(filterField, "$in"));
    }
  }
}

/** The one wording for a refused null, so both call sites read alike. */
function nullValueMessage(
  filterField: string,
  requestedOperator: QueryOperator,
): string {
  return (
    `QueryBuilder.where: filter "${filterField}": ${requestedOperator} cannot ` +
    `take null. SQL never matches \`col = NULL\`, so the server would answer ` +
    `200 with zero rows for every possible input, and the AST grammar has no ` +
    `$exists, $ne or IS NULL operator to express it another way. Drop the ` +
    `filter, or compare against a real value.`
  );
}
