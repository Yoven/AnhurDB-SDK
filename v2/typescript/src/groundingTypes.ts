/**
 * AnhurDB TypeScript SDK — what a record's NEIGHBOURHOOD looks like: the
 * topology context (`getContext`) and the grounding BFS (`getGrounding`).
 *
 * One domain in one sentence: where a record came from and what surrounds it.
 * Split out of `types.ts` (681 lines, house cut ~300) in 4.0.0; `types.ts`
 * re-exports everything here.
 */

import type { MemoryRecord } from "./coreTypes.js";

/** Topology context around a specific record. */
export interface ContextResult {
  target: MemoryRecord;
  neighbors: MemoryRecord[];
}


// ── get_grounding ────────────────────────────────────────────

/** The target record of a grounding lookup. */
export interface GroundingTarget {
  id: number;
  type: string;
  summary: string;
  uuid: string;
}

/**
 * An anchor (source episodic record) discovered during grounding BFS.
 *
 * `content` is omitempty and holds ONLY whitelisted keys
 * ("user"/"assistant"/"full_text"); it is null/absent when the .gz is missing
 * or unparseable. `session_position` is omitempty (absent when unknown).
 */
export interface GroundingAnchor {
  id: number;
  type: string;
  uuid: string;
  summary: string;
  content?: Record<string, string>;
  hops_from_target: number;
  session_position?: number;
}

/** A consolidation node discovered during grounding BFS. */
export interface GroundingConsolidation {
  id: number;
  uuid: string;
  summary: string;
  hops_from_target: number;
}

/**
 * Response from GET /api/v1/records/{id}/grounding.
 *
 * arrays (may be empty []). `found_count = len(anchors) + len(consolidations)`.
 * The cap flags are omitempty and use the JSON keys `anchors_capped` /
 * `consolidations_capped`.
 */
export interface GroundingResult {
  target: GroundingTarget;
  anchors: GroundingAnchor[];
  consolidations: GroundingConsolidation[];
  depth_used: number;
  max_depth: number;
  found_count: number;
  anchors_capped?: boolean;
  consolidations_capped?: boolean;
}
