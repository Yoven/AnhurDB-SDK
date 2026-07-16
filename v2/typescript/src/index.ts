/**
 * AnhurDB TypeScript SDK
 *
 * Dead-simple memory for AI agents — with the full power of a
 * cognitive knowledge graph when you need it.
 *
 * @example
 * ```ts
 * import { Memory } from "anhurdb";
 *
 * const mem = new Memory({ apiKey: "anhur_xxx" });
 * await mem.add("I'm a data scientist at Google");
 * const results = await mem.search("what does this user do?");
 *
 * // Entity knowledge graph
 * const entities = await mem.searchEntities("Google");
 * const graph = await mem.entityGraph(entities[0].id, 2);
 *
 * // Batch operations
 * const contents = await mem.batchReadContent([1, 2, 3]);
 *
 * // File upload
 * const upload = await mem.uploadFile("doc.pdf", base64Content);
 * const status = await mem.uploadStatus(upload.id);
 * ```
 *
 * @packageDocumentation
 */

export { Memory } from "./memory.js";
export { HttpClient } from "./client.js";
export { QueryBuilder } from "./query.js";
export type {
  // Constructor
  MemoryOptions,
  MemoryType,
  MemoryStatus,
  // Core methods
  AddOptions,
  AddResult,
  AddRecordSummary,
  CreateOptions,
  SearchOptions,
  SearchScope,
  SearchResult,
  ProfileResult,
  // Extended
  MemoryRecord,
  WalkResult,
  ContextResult,
  SessionStats,
  // AST query engine
  AstQuery,
  QueryOperator,
  QueryFilterCondition,
  QuerySortClause,
  QueryPagination,
  QueryResult,
  // Manifest / list_chat / count_by_type
  ManifestResult,
  ManifestGlobalOptions,
  ManifestSessionOptions,
  ListChatResult,
  ListChatOptions,
  // Grounding
  GroundingResult,
  GroundingTarget,
  GroundingAnchor,
  GroundingConsolidation,
  // Entity knowledge graph
  EntityRecord,
  EntityEdge,
  EntityGraphResult,
  EntityTimelineResult,
  UpsertEntityOptions,
  UpsertEntityEdgeOptions,
  // File upload
  UploadResult,
  UploadStatusResult,
  // Batch
  BatchUpdateResult,
} from "./types.js";
export {
  AnhurError,
  AnhurAuthError,
  AnhurQueryError,
  AnhurConnectionError,
} from "./types.js";
