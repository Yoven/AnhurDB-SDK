/**
 * AnhurDB TypeScript SDK — the entity knowledge graph (Layer 2): search, list
 * and upsert entities, their typed edges, their history, and their links to
 * memory records.
 *
 * One domain in one sentence: everything under `/api/v1/entities` plus
 * `GET /api/v1/records/{id}/entities`. Server truth:
 * `AnhurDB/server/handler/entity.go` — search answers `{"entities": [...]}`
 * (`entities: null` for zero rows, hence `?? []`), and an entity's kind is
 * `entity_type` (the `?type=` QUERY parameter of the search is a real filter,
 * `entity.go:217`, not a response alias). Moved out of `memory.ts` (1532
 * lines, house cut ~300) in 4.0.0; `Memory` extends this class.
 */

import { MemoryGraphApi } from "./graphApi.js";
import type {
  EntityGraphResult,
  EntityRecord,
  EntityTimelineResult,
  UpsertEntityEdgeOptions,
  UpsertEntityOptions,
} from "./types.js";

/** Entity surface of `Memory`. Holds no state: it borrows the transport. */
export abstract class MemoryEntitiesApi extends MemoryGraphApi {
  /**
   * Search named entities (people, organisations, concepts).
   *
   * @param query      - Name or keyword search.
   * @param entityType - Filter by entity type: person, organization, concept, location, product.
   * @param limit      - Maximum results (default 20).
   */
  async searchEntities(
    query?: string,
    entityType?: string,
    limit?: number): Promise<EntityRecord[]> {
    const params: Record<string, string> = {};
    if (query) params.q = query;
    if (entityType) params.type = entityType;
    if (limit !== undefined) params.limit = String(limit);

    const data = await this.searchTransport.get<{ entities?: EntityRecord[] }>(
      "/api/v1/entities",
      Object.keys(params).length > 0 ? params : undefined);
    return data.entities ?? [];
  }

  /**
   * Paginated walk of ALL entities for the tenant, ordered by id ASC.
   *
   * Unlike `searchEntities` (keyword LIKE filter, limited match set), this
   * walks every row with a stable cursor — pages never shift under concurrent
   * inserts. Loop until `has_more` is false to consume the full set.
   *
   * `list_entities`. All three SDKs must expose this identically.
   *
   * @param limit  - Page size (default 200, server-clamped to [1, 500]).
   * @param offset - 0-based offset (default 0).
   */
  async listEntities(
    limit = 200,
    offset = 0): Promise<{
    entities: EntityRecord[];
    count: number;
    total: number;
    limit: number;
    offset: number;
    has_more: boolean;
    next_offset: number;
  }> {
    if (limit <= 0) limit = 200;
    if (limit > 500) limit = 500;
    if (offset < 0) offset = 0;
    return this.searchTransport.get(
      "/api/v1/entities/list",
      {
        limit: String(limit),
        offset: String(offset),
      });
  }

  /**
   * Create or update a named entity (idempotent by name).
   *
   * @param name    - Entity name (required).
   * @param options - Optional type, summary, and attributes.
   * @returns The entity ID.
   */
  async upsertEntity(
    name: string,
    options?: UpsertEntityOptions): Promise<{ id: number }> {
    const payload: Record<string, unknown> = { name };
    if (options?.entityType) payload.entity_type = options.entityType;
    if (options?.summary) payload.summary = options.summary;
    if (options?.attributes) payload.attributes = options.attributes;

    return this.searchTransport.post<{ id: number }>("/api/v1/entities", payload);
  }

  /**
   * BFS traversal of entity relationships.
   *
   * Starting from an entity, discovers connected entities through
   * typed edges (works_at, knows, part_of, etc.).
   *
   * Junior Tip [omit `depth` and the SERVER owns the default — it is 1]:
   * `handler/entity.go:280` sets `depth := 1` before reading `?depth`; live on
   * 2026-09-14 an omitted depth answered `{"depth":1,"node_count":1}` and
   * `?depth=2` answered `{"depth":2,"node_count":2}` — the value CHANGES the
   * graph. So no `depth` param is sent when the caller did not ask, rather than
   * restating `1` on the wire: an SDK hardcoding today's server default keeps
   * returning the old graph on the day the server changes it. Python used to
   * default to 2 and silently returned a graph the server would not have.
   *
   * @param entityId - The starting entity ID.
   * @param depth    - How many hops. Omit for the server default of 1 (max 5).
   */
  async entityGraph(
    entityId: number,
    depth?: number): Promise<EntityGraphResult> {
    const params: Record<string, string> = {};
    if (depth !== undefined) params.depth = String(depth);

    return this.searchTransport.get<EntityGraphResult>(
      `/api/v1/entities/${entityId}/graph`,
      Object.keys(params).length > 0 ? params : undefined);
  }

  /**
   * Get the full temporal history of an entity's relationships.
   *
   * Shows ALL edges including invalidated ones, ordered by event time.
   * Use to understand how an entity's context evolved over time.
   *
   * @param entityId - The entity ID.
   */
  async entityTimeline(
    entityId: number): Promise<EntityTimelineResult> {
    return this.searchTransport.get<EntityTimelineResult>(
      `/api/v1/entities/${entityId}/timeline`,
      undefined);
  }

  /**
   * Create or update a typed relationship between two entities.
   *
   * @param sourceId - Source entity ID.
   * @param targetId - Target entity ID.
   * @param relation - Relationship type (e.g. "works_at", "knows").
   * @param options  - Optional event time, confidence, source record ID.
   */
  async upsertEntityEdge(
    sourceId: number,
    targetId: number,
    relation: string,
    options?: UpsertEntityEdgeOptions): Promise<void> {
    const payload: Record<string, unknown> = {
      source_id: sourceId,
      target_id: targetId,
      relation,
    };
    if (options?.eventTime) payload.event_time = options.eventTime;
    if (options?.confidence !== undefined)
      payload.confidence = options.confidence;
    if (options?.sourceRecordId !== undefined)
      payload.source_record_id = options.sourceRecordId;

    await this.searchTransport.post("/api/v1/entities/edges", payload);
  }

  /**
   * Link a memory record to an entity (cross-layer connection).
   *
   * @param recordId - Memory record ID.
   * @param entityId - Entity ID.
   * @param role     - Optional role description.
   */
  async linkRecordEntity(
    recordId: number,
    entityId: number,
    role?: string): Promise<void> {
    const payload: Record<string, unknown> = {
      record_id: recordId,
      entity_id: entityId,
    };
    if (role) payload.role = role;

    await this.searchTransport.post("/api/v1/entities/link", payload);
  }

  /**
   * Get entities linked to a specific memory record.
   *
   * @param recordId - The record ID.
   */
  async getRecordEntities(
    recordId: number): Promise<EntityRecord[]> {
    const data = await this.searchTransport.get<{ entities?: EntityRecord[] }>(
      `/api/v1/records/${recordId}/entities`,
      undefined);
    return data.entities ?? [];
  }
}
