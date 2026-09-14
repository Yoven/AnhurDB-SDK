/**
 * AnhurDB TypeScript SDK — what the two GRAPH-WALK endpoints ANSWER.
 *
 * `POST /api/v1/walk` (BFS) and `POST /api/v1/walk/semantic` (Dijkstra / A*).
 * Its own file because `types.ts` is long past the ~300-line house cut and
 * house law forbids growing a file already over it.
 *
 * GROUND TRUTH: `AnhurDB/server/handler/record_search_graph.go`. The BFS
 * route writes `{nodes, edges, truncated}` at :220-222; the semantic route
 * writes `{nodes, edges}` at :337-340 — no `truncated`. Both were re-proved
 * live on 2026-09-14 against `https://anhurdb.yoven.ai` with `seed_id: 18`.
 *
 * @module
 */

import type { MemoryRecord } from "./types.js";

/**
 * One edge of a walk: a directed pair of record ids and nothing else.
 *
 * Junior Tip [why there is no `type` on an edge — 2026-09-14]: this shape used
 * to declare `type: string`. Both handlers build the edge list as an anonymous
 * Go struct with exactly two fields, `Source` and `Target`
 * (`record_search_graph.go:102-105` and :326-329), and the second one carries
 * a comment saying the two-key shape is the frozen public contract. A caller
 * reading `edge.type` got `undefined` with no compiler complaint, because the
 * type promised a key the wire has never carried.
 */
export interface WalkEdge {
  source: number;
  target: number;
}

/**
 * Result of a graph walk starting from a given record.
 *
 * Junior Tip [`nodes` are FULL records, not a projection]: the BFS handler
 * accumulates `map[int64]*model.Record` (`record_search_graph.go:101`) and
 * marshals those records whole. Live, each node carried all 14 record keys
 * (`archived, consolidated, created_at, id, main_ids, metadata, related_ids,
 * score, status, summary, type, updated_at, uuid, weight`). The old
 * four-field `{id, type, summary, weight}` shape was not wrong on the wire —
 * it was wrong in the type, hiding ten real fields from every caller and from
 * autocompletion. `MemoryRecord` is the same record shape every other read
 * endpoint returns, so a walk node now composes with the rest of the SDK.
 */
export interface WalkResult {
  nodes: MemoryRecord[];
  edges: WalkEdge[];
  /**
   * True when the walk stopped early on `max_nodes` / the depth budget.
   *
   * Junior Tip [OPTIONAL on purpose, and the spec's literal `truncated:
   * boolean` would have been a phantom]: only `POST /api/v1/walk` sends this
   * key (`record_search_graph.go:222`). `POST /api/v1/walk/semantic` writes a
   * two-key map and never sends it (:337-340) — confirmed live the same day:
   * `/walk` answered `[edges, nodes, truncated]`, `/walk/semantic` answered
   * `[edges, nodes]`. Both routes return THIS type, so declaring it required
   * would promise a key that half the callers can never receive. Read it as
   * `result.truncated === true`: `undefined` means "the semantic route did not
   * say", which is not the same claim as "nothing was cut".
   */
  truncated?: boolean;
}
