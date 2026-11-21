/**
 * TypeScript type definitions for the AnhurDB SDK — the ONE import path.
 *
 * Every request/response shape lives in a domain file; this module only
 * re-exports them, so `import type { X } from "./types.js"` (and the public
 * `index.ts`) never has to know which file owns `X`:
 *
 *   - `coreTypes.ts`      memory types, statuses, `MemoryOptions`, `MemoryRecord`
 *   - `writeTypes.ts`     add/create/session options, `AddResult`, wire bodies
 *   - `searchTypes.ts` / `searchResults.ts` / `smartSearchResults.ts`  search
 *   - `queryTypes.ts`     AST query, manifest, list_chat
 *   - `walkTypes.ts`      graph walks
 *   - `groundingTypes.ts` topology context and grounding
 *   - `entityTypes.ts`    entity knowledge graph (Layer 2)
 *   - `profileTypes.ts`, `uploadTypes.ts`, `deleteFileTypes.ts`, `sessionStats.ts`
 *   - `errors.ts`         the error taxonomy (re-exported as VALUES, see below)
 *
 * Split in 4.0.0 (this file was 681 lines; house cut ~300).
 */

export type {
  MemoryOptions,
  MemoryRecord,
  MemoryStatus,
  MemoryType,
} from "./coreTypes.js";
export type {
  AddOptions,
  AddRecordSummary,
  AddResult,
  BatchUpdateResult,
  CreateOptions,
  CreateSessionOptions,
  IngestPayload,
  RecordPayload,
} from "./writeTypes.js";
// ── search() ─────────────────────────────────────────────────
//
// The hybrid-search wire contract lives in two domain files: `searchTypes.ts`
// (what a caller ASKS) and `searchResults.ts` (what the server ANSWERS). Both
// are re-exported below, so `import type { SearchOptions } from "./types.js"`
// keeps working verbatim.

export type {
  SearchMode,
  SearchOptions,
  SearchPayload,
  SearchScope,
} from "./searchTypes.js";
export type {
  LegScoreSummary,
  RelatedNode,
  RetrievalMeta,
  SearchHitSignals,
  SearchResult,
  SearchWithRetrievalResult,
  TypeListingResult,
} from "./searchResults.js";
// `GET /api/v1/search/smart` answers a different shape entirely — see
// smartSearchResults.ts for why it is not folded into SearchResult.
export type {
  SmartSearchHit,
  SmartSearchResponse,
} from "./smartSearchResults.js";

// ── profile() ────────────────────────────────────────────────
//
// GET /api/v1/profile answers three CLOSED blocks. The concrete interfaces
// live in `profileTypes.ts` (house 300-line cut) and are re-exported here
// because `types.ts` is the import path every module already uses.
export type {
  ProfileStatic,
  ProfileDynamic,
  ProfileStats,
  ProfileResult,
} from "./profileTypes.js";

export type {
  WalkEdge,
  WalkResult,
  WalkSemanticOptions,
  WalkTarget,
} from "./walkTypes.js";
export type {
  ContextResult,
  GroundingAnchor,
  GroundingConsolidation,
  GroundingResult,
  GroundingTarget,
} from "./groundingTypes.js";
export type {
  AstQuery,
  ListChatOptions,
  ListChatResult,
  ManifestGlobalOptions,
  ManifestResult,
  ManifestSessionOptions,
  QueryFilterCondition,
  QueryOperator,
  QueryPagination,
  QueryResult,
  QuerySortClause,
} from "./queryTypes.js";
export type {
  EntityEdge,
  EntityGraphResult,
  EntityRecord,
  EntityTimelineResult,
  UpsertEntityEdgeOptions,
  UpsertEntityOptions,
} from "./entityTypes.js";

// One row of GET /api/v1/sessions/stats. The shape lives beside the paging
// loop that consumes it, in `sessionStats.ts`; re-exported here unchanged.
export type { SessionStats } from "./sessionStats.js";

// ── File Upload ──────────────────────────────────────────────
//
// POST /api/v1/upload and GET /api/v1/upload/{id}/status. Both shapes live in
// `uploadTypes.ts` (house 300-line cut); re-exported here unchanged because
// `types.ts` is the import path every module and `index.ts` already use.
export type { UploadResult, UploadStatusResult } from "./uploadTypes.js";

// ── Delete file (whole ingested document) ────────────────────
// Moved to `deleteFileTypes.ts` (house 300-line cut, 2026-11-20, when the L2
// library added `legs` and `scope`); re-exported here so no import path changed.
export type {
  DeleteFileLeg,
  DeleteFileOptions,
  DeleteFileResult,
  FileSessionCandidate,
} from "./deleteFileTypes.js";


// ── Error types ──────────────────────────────────────────────
//
// The taxonomy itself moved to `errors.ts` (house 300-line cut). It is
// re-exported here, unchanged, because `types.ts` is the import path every
// module and the public `index.ts` already use.
//
// Junior Tip [why a re-export and not a rename]: `AnhurError` is thrown across
// a module boundary and matched with `instanceof`. Two copies of the class —
// one imported from `./types.js`, one from `./errors.js` — would be two
// DIFFERENT constructors, and `instanceof` between them is false. Keeping one
// definition and one re-export means every path in the SDK, and every caller,
// compares against the same class object.
export type { AnhurErrorKind } from "./errors.js";
export {
  kindForStatus,
  AnhurError,
  AnhurAuthError,
  AnhurQueryError,
  AnhurConnectionError,
  AnhurUploadWaitTimeout,
} from "./errors.js";
