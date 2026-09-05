/**
 * AnhurDB TypeScript SDK
 *
 * Dead-simple memory for AI agents — with the full power of a
 * cognitive knowledge graph when you need it.
 *
 * @example
 * ```ts
 * import { Memory, sessionsAll } from "anhurdb";
 *
 * const mem = new Memory({ apiKey: "anhur_xxx", url: "https://anhurdb.yoven.ai" });
 * const sessionId = await mem.createSession();
 * await mem.add("I'm a data scientist at Google", { mode: "ingest", sessionId });
 * const results = await mem.search("what does this user do?", sessionsAll());
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
// Version of this SDK, as sent in the `User-Agent` header.
export { SDK_VERSION, USER_AGENT } from "./version.js";
// Session filter (ADR-0014) — every search takes a mandatory `sessions`.
export {
  MAX_SESSION_FILTER_UUIDS,
  SESSION_WILDCARD,
  normalizeSessions,
  sessionsAll,
} from "./sessionFilter.js";
export type {
  // Constructor
  MemoryOptions,
  MemoryType,
  MemoryStatus,
  // Core methods
  AddOptions,
  AddResult,
  AddRecordSummary,
  CreateSessionOptions,
  CreateOptions,
  SearchOptions,
  SearchPayload,
  SearchScope,
  SearchResult,
  SearchHitSignals,
  RelatedNode,
  RetrievalMeta,
  // ADR-0031 search controls and signals
  SearchMode,
  LegScoreSummary,
  SearchWithRetrievalResult,
  ProfileResult,
  // Extended
  MemoryRecord,
  WalkResult,
  WalkTarget,
  WalkSemanticOptions,
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
  // Delete file (whole ingested document)
  DeleteFileOptions,
  DeleteFileResult,
  // Failure classification carried by every AnhurError
  AnhurErrorKind,
} from "./types.js";
// Query-string shape accepted by HttpClient's read verbs — needed by anyone
// who types a wrapper around `HttpClient.get`/`delete`.
export type { QueryParams } from "./client.js";
export {
  AnhurError,
  AnhurAuthError,
  AnhurQueryError,
  AnhurConnectionError,
  // Thrown by `waitForUpload`; it was unreachable, so callers could not
  // `instanceof` the one error the upload wait can produce.
  AnhurUploadWaitTimeout,
} from "./types.js";
