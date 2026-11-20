/**
 * AnhurDB TypeScript SDK — by-id record reads and writes, addressed to one
 * store.
 *
 * One domain in one sentence: read or change one record (or a batch) by id, in
 * the store the caller names with `{ scope }`, or in the caller's own store when
 * no scope is given. Parity: Go `client/record_by_id.go` +
 * `client/record_mutate.go`, Python `anhurdb/client/record_by_id.py`.
 *
 * Moved out of `memory.ts` (far past the house 300-line cut) on 2026-11-20 when
 * every one of these methods gained the same `ByIdOptions`; the bodies are
 * unchanged apart from it. `Memory` extends this class, so the public surface
 * is identical.
 *
 * Server truth (AnhurDB fix/l2-lib-scope): `server/router.go` wraps
 * GET/PATCH/DELETE `/api/v1/records/{id}` and `/content`, `/topology`,
 * `/grounding` in `rh.ScopedByID`; `POST /records/batch-content` and
 * `/records/supersede` read `?scope=` themselves (`handler/record_batch.go`,
 * `handler/record_supersede.go`). A row outside the named plane is 404 for a
 * single id and silently OMITTED from a batch.
 *
 * Junior Tip [why a mutation needs the scope even more than a read]: an id
 * taken from a tenant_shared hit names a row of `{c}_{t}_lib`; the same integer
 * names an unrelated chat row in `{c}_{t}`. An unscoped PATCH/DELETE/supersede
 * would change THAT chat row and answer 200 — a valid-looking success on the
 * wrong record. With a scope the server proves the row lives in the plane or
 * answers 404.
 */

import { MemorySearchApi } from "./search.js";
import { resolveRecordScope, scopeParams, withScopeQuery } from "./recordScope.js";
import type { ByIdOptions } from "./recordScope.js";
import type { ContextResult, GroundingResult } from "./types.js";

/** By-id surface of `Memory`. Holds no state: it borrows the transport. */
export abstract class MemoryRecordsApi extends MemorySearchApi {
  /**
   * Fetch record metadata by ID via `GET /api/v1/records/{id}`.
   *
   * Junior Tip [vs readContent]: returns JSON metadata; use `readContent`
   * for the full text body.
   *
   * @param options - `{ scope }`: plane the id lives in. For a tenant_shared
   *                  hit use `tenantSharedScopeForHit(hit.uuid)`.
   */
  async get(recordId: number, options?: ByIdOptions): Promise<Record<string, unknown>> {
    if (recordId <= 0) {
      throw new Error("get: recordId must be > 0");
    }
    const scope = resolveRecordScope("get", options?.scope);
    return this.searchTransport.get<Record<string, unknown>>(
      `/api/v1/records/${recordId}`,
      scopeParams(scope));
  }

  /**
   * Read the full content body of a specific record.
   *
   * @param recordId - The record ID whose content to retrieve.
   * @param options  - `{ scope }`: plane the id lives in (see `get`).
   */
  async readContent(recordId: number, options?: ByIdOptions): Promise<string> {
    const scope = resolveRecordScope("readContent", options?.scope);
    // text/plain body — use getText (raw), not JSON parse. Matches Go raw
    // bytes and Python raw_text=True.
    return this.searchTransport.getText(
      `/api/v1/records/${recordId}/content`,
      scopeParams(scope));
  }

  /**
   * Get the topological context (neighbours) around a specific record.
   *
   * @param recordId - The record ID to inspect.
   * @param options  - `{ scope }`: plane the id lives in (see `get`).
   */
  async getContext(recordId: number, options?: ByIdOptions): Promise<ContextResult> {
    const scope = resolveRecordScope("getContext", options?.scope);
    return this.searchTransport.get<ContextResult>(
      `/api/v1/records/${recordId}/topology`,
      scopeParams(scope));
  }

  /**
   * Build the provenance grounding of a record via
   * `GET /api/v1/records/{id}/grounding`.
   *
   * Walks the graph (BFS) back to the source episodic anchors and the
   * consolidations that summarise the record — the "why does the DB believe
   * this" trail.
   *
   * `max_depth` is an integer 1..5 inclusive (default 3); anything outside that
   * range is HTTP 400 server-side. `anchors`/`consolidations` are always
   * present arrays (may be empty). A missing/archived/superseded target is
   * HTTP 404. Mirrors Python `get_grounding` and Go `GetGrounding`.
   *
   * @param recordId - The target record ID (must be > 0).
   * @param maxDepth - BFS depth budget 1..5 (default server-side: 3).
   * @param options  - `{ scope }`: plane the id lives in (see `get`).
   */
  async getGrounding(
    recordId: number,
    maxDepth?: number,
    options?: ByIdOptions): Promise<GroundingResult> {
    if (recordId <= 0) {
      throw new Error("getGrounding: recordId must be > 0");
    }
    const scope = resolveRecordScope("getGrounding", options?.scope);
    const params: Record<string, string> = {};
    if (maxDepth !== undefined) params.max_depth = String(maxDepth);
    if (scope) params.scope = scope;

    return this.searchTransport.get<GroundingResult>(
      `/api/v1/records/${recordId}/grounding`,
      Object.keys(params).length > 0 ? params : undefined);
  }

  /**
   * Fetch full content for multiple records in a single call (max 100).
   *
   * Eliminates the N+1 pattern of calling `readContent()` in a loop. With a
   * scope, ids whose row is outside that plane are OMITTED from the map (the
   * server treats them like missing ids) — check the keys.
   *
   * @param ids     - Array of record IDs (max 100).
   * @param options - `{ scope }`: plane the ids live in (see `get`).
   * @returns Map of `record_id → content_payload`.
   */
  async batchReadContent(ids: number[], options?: ByIdOptions): Promise<Record<string, unknown>> {
    const scope = resolveRecordScope("batchReadContent", options?.scope);
    // Read-shaped POST endpoint.
    return this.searchTransport.postRead<Record<string, unknown>>(
      withScopeQuery("/api/v1/records/batch-content", scope),
      { ids });
  }

  /**
   * Partially update a record.
   *
   * `score` is NOT accepted here. `PATCH /api/v1/records/{id}` has no score
   * field, so the server answers 200 and drops the key — the same shape as an
   * earlier defect with `archived`. Succeeding silently while writing nothing
   * is worse than throwing, so this throws and names `setScore`.
   * Measured 2026-08-15.
   *
   * @param options - `{ scope }`: plane the id lives in (see `get`).
   */
  async update(
    recordId: number,
    fields: Partial<{
      summary: string;
      metadata: string;
      status: string;
      type: string;
    }> & { score?: never },
    options?: ByIdOptions): Promise<void> {
    if (fields && Object.prototype.hasOwnProperty.call(fields, "score")) {
      throw new Error(
        "update: cannot change score — PATCH /api/v1/records/{id} has no score " +
        "field and drops the key while answering 200; use setScore(recordId, score)");
    }
    const scope = resolveRecordScope("update", options?.scope);
    await this.searchTransport.patch(withScopeQuery(`/api/v1/records/${recordId}`, scope), fields);
  }

  /**
   * Set a record's importance score (1-10) durably.
   *
   * `update` cannot do this — see its note. This posts to
   * `/api/v1/records/set-score`, which dispatches a replicated command and
   * invalidates the read cache.
   *
   * The score feeds the value term of the cognitive weight, and the weight
   * selects the record's embedding fidelity band, so the correction reaches
   * ranking on the next maintenance pass rather than immediately.
   *
   * @param recordId - The record ID.
   * @param score - Importance, 1-10.
   */
  async setScore(recordId: number, score: number): Promise<void> {
    if (recordId <= 0) {
      throw new Error("setScore: recordId must be > 0");
    }
    if (!Number.isInteger(score) || score < 1 || score > 10) {
      throw new Error(`setScore: score must be an integer between 1 and 10, got ${score}`);
    }
    await this.searchTransport.post("/api/v1/records/set-score", { ids: [recordId], score });
  }

  /**
   * Archive a specific record by ID (`DELETE /api/v1/records/{id}` — the
   * server soft-deletes; the record disappears from reads but is not erased).
   *
   * @param recordId - The record ID to archive.
   * @param options  - `{ scope }`: plane the id lives in (see `get`).
   */
  async delete(recordId: number, options?: ByIdOptions): Promise<void> {
    const scope = resolveRecordScope("delete", options?.scope);
    await this.searchTransport.delete(`/api/v1/records/${recordId}`, scopeParams(scope));
  }

  /**
   * Mark an old record as superseded by a new one.
   *
   * The old record remains in the graph but search results prefer the
   * newer version (temporal versioning).
   *
   * @param oldId   - The record being superseded.
   * @param newId   - The replacement record.
   * @param options - `{ scope }`: plane BOTH ids live in; the server answers
   *                  404 and supersedes nothing when either is outside it.
   */
  async supersede(oldId: number, newId: number, options?: ByIdOptions): Promise<void> {
    const scope = resolveRecordScope("supersede", options?.scope);
    await this.searchTransport.post(withScopeQuery("/api/v1/records/supersede", scope), {
      old_id: oldId,
      new_id: newId,
    });
  }
}
