/**
 * AnhurDB TypeScript SDK — the core vocabulary: memory types, record
 * statuses, constructor options and the full record shape.
 *
 * One domain in one sentence: the words every other type file builds on — what
 * a record IS (`MemoryRecord`), which cognitive type and lifecycle status it can
 * carry, and how a `Memory` is constructed. Split out of `types.ts` (681 lines,
 * house cut ~300) in 4.0.0; `types.ts` re-exports everything here, so no import
 * path changed.
 */

// ── Memory types (cognitive epistemology) ────────────────────

/**
 * Cognitive memory types defined by the AnhurDB epistemology (13 types).
 *
 * The authority is AnhurCore's `core.yaml`, mirrored by the server's
 * `schema.MemoryTypes` and the MCP `list_types` tool — not this union. Because
 * this is a closed union, a type the server serves but that is missing here is a
 * COMPILE ERROR for the caller, not a runtime warning: it is the strictest of
 * the three SDKs, so it drifts loudest. Keep Go, Python, TypeScript and
 * `core.yaml` in step, in the same change.
 */
export type MemoryType =
  | "episodic"
  | "fact"
  | "preference"
  | "decision"
  | "task"
  | "risk"
  | "reasoning"
  | "idea"
  | "emotion"
  | "consolidated"
  | "hub"
  | "file"
  /** Macro-theme backbone: a hub of hubs (ADR-0005). */
  | "router";

/**
 * Lifecycle status of a cognitive record.
 *
 * No `"pending"` (removed 4.0.0): it was a pre-v2 record status the server no
 * longer writes. `"pending"` survives only as an ExtractionStatus on the ingest
 * path (`server/service/ingest.go:233`) — a different field, not this one.
 */
export type MemoryStatus =
  | "saved"
  | "consolidated"
  | "archived"
  | "decayed"
  | "processing"
  | "completed"
  | "linked"
  | "hubbed"
  | "failed"
  | "pending_judge"
  | "failed_judge"
  | "failed_consolidation";

// ── Constructor options ──────────────────────────────────────

/** Options passed to `new Memory(...)`. */
export interface MemoryOptions {
  /** AnhurDB API key (required). */
  apiKey: string;

  /**
   * Server URL. Defaults to `https://anhurdb.yoven.ai`.
   * Pass `http://localhost:8000` for a self-hosted server.
   */
  url?: string;

  /**
   * Optional user identifier. Used as `container_tag` for grouping
   * memories. When omitted the SDK derives a stable tag from a hash
   * of the API key.
   */
  userId?: string;

  /**
   * Optional tenant ID for multi-tenant deployments.
   * Sent as `X-Tenant-ID` header on every request.
   */
  tenantId?: string;
}

// ── Extended Memory types ────────────────────────────────────

/**
 * A full record as returned by the AnhurDB API.
 *
 * exact JSON keys the server serialises (internal fields are `json:"-"` and
 * never appear). The optional fields are omitempty server-side: `content` only
 * appears on history endpoints; `superseded_by`/`valid_from`/`valid_until`
 * only when set.
 */
export interface MemoryRecord {
  id: number;
  uuid: string;
  type: string;
  summary: string;
  status: string;
  weight: number;
  score: number;
  created_at: string;
  updated_at: string;
  /** Raw JSON-as-string metadata envelope (e.g. `{"container_tag":"..."}`). */
  metadata?: string;
  related_ids?: number[];
  main_ids?: number[];
  consolidated?: boolean;
  archived?: boolean;
  /** Present only on history endpoints (the .gz body); omitted elsewhere. */
  content?: string;
  superseded_by?: number;
  valid_from?: string;
  valid_until?: string;
}

// POST /api/v1/walk and /walk/semantic. Shapes live in `walkTypes.ts`
