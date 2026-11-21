/**
 * AnhurDB TypeScript SDK — the record graph: walking it, and writing its
 * links (parents, related ids, consolidation stars, batch status).
 *
 * One domain in one sentence: traverse the record topology (`/api/v1/walk`,
 * `/api/v1/walk/semantic`) and change it (`PATCH /api/v1/records/...`
 * append-main-ids, append-related-ids, consolidate-ids, mark-consolidated).
 * Moved out of `memory.ts` (1532 lines, house cut ~300) in 4.0.0; `Memory`
 * extends this class, so the public surface is identical.
 *
 * Junior Tip [one name per operation, 4.0.0]: `linkConsolidated` had an older
 * alias that forwarded to it verbatim since the 2026-06-18 rename to the MCP
 * tool name. Two names for one call is how SDKs drift, so the alias was removed
 * in 4.0.0 — the CHANGELOG carries the one-line migration.
 */

import { MemoryManifestApi } from "./manifestApi.js";
import type { BatchUpdateResult, WalkResult, WalkSemanticOptions } from "./types.js";

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

/** Graph surface of `Memory`. Holds no state: it borrows the transport. */
export abstract class MemoryGraphApi extends MemoryManifestApi {
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
    return this.searchTransport.postRead<WalkResult>(
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
    // Base body: the pure-Dijkstra request. With no `options` only seed_id +
    // depth go on the wire, and the server runs a plain Dijkstra traversal.
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

    return this.searchTransport.postRead<WalkResult>(
      "/api/v1/walk/semantic",
      payload);
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
    return this.searchTransport.patch<BatchUpdateResult>(
      "/api/v1/records/mark-consolidated",
      { ids, status });
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
    return this.searchTransport.patch<Record<string, unknown>>(
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
    return this.searchTransport.patch<Record<string, unknown>>(
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
    return this.searchTransport.patch<Record<string, unknown>>(
      "/api/v1/records/append-related-ids",
      { ids: [recordId], related_ids_to_append: relatedIds });
  }

  /**
   * Set `consolidate_id` on a batch of child records (star-link topology).
   * Batched so N children pointing at one star cost ONE server round-trip.
   *
   * Same name as the MCP `link_consolidated` tool and the Go
   * `LinkConsolidated` / Python `link_consolidated` methods.
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
    return this.searchTransport.patch<Record<string, unknown>>(
      "/api/v1/records/consolidate-ids",
      { ids, consolidate_id: consolidateId });
  }
}
