/**
 * AnhurDB TypeScript SDK — which STORE a record id names (ADR-0029, L2 tenant
 * library).
 *
 * One domain in one sentence: the closed set of planes a by-id call may
 * address, the local check for it, and the two rules that pick the right plane
 * for a search hit or an upload. Parity: Go `client/record_scope.go`, Python
 * `anhurdb/client/record_scope.py`.
 *
 * Junior Tip [why a by-id call needs a plane at all — server fix/l2-lib-scope,
 * 2026-11-20]: a record id is a SQLite rowid PER STORE. Since tenant-library
 * uploads land in `{c}_{t}_lib`, id 42 from a tenant_shared hit names a library
 * row there — and the SAME 42 names an unrelated chat row in `{c}_{t}`. Without
 * a scope the server reads (or PATCHes, archives, supersedes) the caller's own
 * store, i.e. the wrong record, and answers 200. With a scope the server
 * resolves the store from the authenticated caller and proves every id is a
 * live row of that plane, or answers 404
 * (`server/service/record_scoped_by_id.go:23-52`). No scope keeps the old
 * behaviour.
 */

import { AnhurQueryError } from "./errors.js";

/** A plane a by-id call may address. `shared_all` is absent on purpose: it
 * names TWO stores and the server refuses it for by-id as well. */
export type RecordScope =
  | "sessions"
  | "tenant_shared"
  | "tenant_shared_legacy"
  | "client_shared";

/** Every value a by-id call accepts. */
export const RECORD_SCOPES: readonly RecordScope[] = [
  "sessions",
  "tenant_shared",
  "tenant_shared_legacy",
  "client_shared",
];

/** Options carried by every by-id method. */
export interface ByIdOptions {
  /** Plane the id lives in. Omit for your own store (unchanged behaviour). */
  scope?: RecordScope;
}

const TENANT_LIBRARY_SESSION_SUFFIX = "_lib";

/**
 * By-id scope for a hit of a `tenant_shared` search: `tenant_shared` when the
 * hit's uuid (its session) ends in `_lib` — a row of the L2 library store —
 * otherwise `tenant_shared_legacy`.
 *
 * Junior Tip [why the search scope alone is not enough]: one tenant_shared
 * search reads TWO stores and merges them, so "tenant_shared" does not say
 * which store a given hit came from. The session uuid does; the server admits a
 * row into a leg with the same suffix rule.
 */
export function tenantSharedScopeForHit(recordUuid: string): RecordScope {
  return (recordUuid ?? "").trim().endsWith(TENANT_LIBRARY_SESSION_SUFFIX)
    ? "tenant_shared"
    : "tenant_shared_legacy";
}

/**
 * Plane an upload lands in, mirroring the server's `ResolveUploadDestination`:
 * chat (or a session without a mode) → `sessions`, `client_shared` →
 * `client_shared`, anything else (default, tenant_shared) → `tenant_shared`.
 */
export function uploadStatusScope(mode?: string, sessionId?: string): RecordScope {
  let normalizedMode = (mode ?? "").trim().toLowerCase();
  if (!normalizedMode && (sessionId ?? "").trim()) normalizedMode = "chat";
  if (normalizedMode === "chat") return "sessions";
  if (normalizedMode === "client_shared") return "client_shared";
  return "tenant_shared";
}

/**
 * Validate a scope locally; `undefined`/blank means "no scope". Throws an
 * `AnhurQueryError` classified 400 (invalid_request, not retryable) for
 * `shared_all` or any unknown value — before any request leaves.
 */
export function resolveRecordScope(
  method: string,
  scope: string | undefined,
): RecordScope | undefined {
  const trimmedScope = (scope ?? "").trim();
  if (!trimmedScope) return undefined;
  if (!(RECORD_SCOPES as readonly string[]).includes(trimmedScope)) {
    throw new AnhurQueryError(
      `INVALID_PARAM: ${method}: scope '${trimmedScope}' is not addressable by id ` +
        "(want sessions|tenant_shared|tenant_shared_legacy|client_shared)",
      400,
    );
  }
  return trimmedScope as RecordScope;
}

/** `{ scope }` as query params, or `undefined` — no scope leaves the request unchanged. */
export function scopeParams(scope: RecordScope | undefined): Record<string, string> | undefined {
  return scope ? { scope } : undefined;
}

/**
 * `path?scope=…` for the verbs whose transport takes no query params (POST,
 * PATCH). `scope` is already validated against the closed set, so it needs no
 * escaping; `encodeURIComponent` stays as defence in depth.
 */
export function withScopeQuery(path: string, scope: RecordScope | undefined): string {
  return scope ? `${path}?scope=${encodeURIComponent(scope)}` : path;
}
