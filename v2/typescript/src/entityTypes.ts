/**
 * AnhurDB TypeScript SDK — the entity knowledge graph (Layer 2) shapes.
 *
 * One domain in one sentence: named real-world things (people, organisations,
 * concepts), their typed temporal edges, and the options to write them.
 * Server truth: `AnhurDB/server/handler/entity.go` (the wire field is
 * `entity_type`, never `type` — `entityToResponse`, :89). Split out of
 * `types.ts` (681 lines, house cut ~300) in 4.0.0; re-exported there.
 */

/**
 * A named entity in the AnhurDB knowledge graph (Layer 2).
 *
 * Entities are real-world objects (people, organisations, concepts) linked to
 * memory records. Wire field is `entity_type` — not `record.type`
 * (episodic / fact / decision). The cross-layer link is the “tag”.
 */
export interface EntityRecord {
  id: number;
  name: string;
  entity_type: string;
  summary?: string;
  attributes?: Record<string, unknown>;
  dimension?: number;
  first_seen?: string;
  last_seen?: string;
  mention_count?: number;
  weight?: number;
}

/**
 * A typed, temporal relationship between two entities.
 *
 * Examples: "works_at", "knows", "part_of", "created_by".
 */
export interface EntityEdge {
  source_id: number;
  target_id: number;
  relation: string;
  event_time?: string;
  valid_until?: string;
  confidence?: number;
  source_record_id?: number;
}

/** Options for `Memory.upsertEntity()`. */
export interface UpsertEntityOptions {
  entityType?: string;
  summary?: string;
  attributes?: Record<string, unknown>;
}

/** Options for `Memory.upsertEntityEdge()`. */
export interface UpsertEntityEdgeOptions {
  eventTime?: string;
  confidence?: number;
  sourceRecordId?: number;
}

/** Result from entity graph traversal. */
export interface EntityGraphResult {
  entity_id: number;
  depth: number;
  nodes: Array<{
    entity: EntityRecord;
    edges?: EntityEdge[];
  }>;
  node_count: number;
}

/** Result from entity timeline query. */
export interface EntityTimelineResult {
  entity: EntityRecord;
  timeline: EntityEdge[];
  record_ids?: number[];
}
