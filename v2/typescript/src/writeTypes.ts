/**
 * AnhurDB TypeScript SDK — the WRITE path's shapes: `add()`, `create()`,
 * `createSession()`, the batch status update, and the two request bodies the
 * SDK sends (`/api/v1/ingest`, `/api/v1/records`).
 *
 * One domain in one sentence: what a caller passes to store a memory and what
 * the SDK answers back. Split out of `types.ts` (681 lines, house cut ~300) in
 * 4.0.0; `types.ts` re-exports everything here, so no import path changed.
 */

import type { MemoryType } from "./coreTypes.js";

// ── add() ────────────────────────────────────────────────────

/** Options for `Memory.add()`. */
export interface AddOptions {
  /** Importance rating 1-10 (default 5). */
  score?: number;
  /** Memory type (default "episodic"). */
  type?: MemoryType;
  /**
   * Write path: `ingest` (POST /api/v1/ingest — default) or `regular`
   * (POST /api/v1/records as episodic). Session-first servers require
   * {@link Memory.createSession} before either path succeeds.
   */
  mode?: "ingest" | "regular";
  /**
   * Caller-supplied metadata.
   *
   * three SDKs expose the identical capability — `add(text, {score, type,
   * metadata})`. The SDK merges it into the canonical
   * `{"container_tag": "..."}` envelope before sending so it never
   * overwrites the container tag (see the 2026-05-22 metadata corruption
   * incident). Server stores `metadata` as a JSON string.
   */
  metadata?: Record<string, unknown>;
  /**
   * Pins the SESSION (uuid) the ingested record lands in. The tenant comes from
   * the API key; the session is the caller's unit of conversation.
   *
   * Omit to use the client's current sessionUuid (from constructor, newSession,
   * or createSession). Writes require createSession on session-first servers.
   */
  sessionId?: string;
}

/** Options for `Memory.createSession()`. */
export interface CreateSessionOptions {
  /** Session uuid to register. Omit and the server generates one. */
  sessionId?: string;
  /** Optional session-level metadata copied onto every record in this session. */
  metadata?: Record<string, unknown>;
}

/** A single record descriptor returned inside `AddResult`. */
export interface AddRecordSummary {
  id: number;
  type: MemoryType;
  summary: string;
}

/** Value returned by `Memory.add()`. */
export interface AddResult {
  /** Session UUID that groups this memory. */
  sessionId: string;
  /** Records created by the server. */
  records: AddRecordSummary[];
  /**
   * The door that actually wrote: `"ingest"` = `POST /api/v1/ingest`
   * (pipeline), `"records"` = `POST /api/v1/records` (direct, verbatim).
   *
   * Junior Tip [renamed in 4.0.0 — was `"cloud" | "oss"`]: the old values named
   * a server KIND, from the days `add()` fell back to `/records` when an "OSS"
   * server answered 404 on `/ingest`. That fallback is gone (the route is
   * mounted unconditionally, `server/router.go:135`), so the value now names
   * the door, which is what a caller debugging a write needs to know.
   */
  mode: "ingest" | "records";
}

// ── Batch Operations ─────────────────────────────────────────

/**
 * Result from batch status update — `PATCH /api/v1/records/mark-consolidated`.
 *
 * Junior Tip [`updated_count` never existed — 2026-09-14]: this interface used
 * to declare exactly one field, `updated_count: number`, and the handler has
 * never sent it. `handler/record_batch.go:212` is the only success path and it
 * writes a single literal: `{"message": "marked consolidated"}`. So every
 * caller writing `if (result.updated_count === ids.length)` was comparing
 * `undefined` to a number — a check that is always false, on a type that
 * promised the number was there. The server does not tell you how many rows
 * it touched on this route; the honest type says so, and a caller who needs
 * the count must re-read the records.
 *
 * The exported NAME is kept (it is public API via `index.ts`): changing the
 * body is the fix, deleting the name would be a second, pointless break.
 */
export interface BatchUpdateResult {
  /** Server ack, currently the literal `"marked consolidated"`. */
  message: string;
}

// ── create() full-fidelity ───────────────────────────────────

/**
 * Options for `Memory.create()` — the full-fidelity record-create surface.
 *
 * POST /api/v1/records (NOT the ingest pipeline), so every field is persisted
 * exactly as supplied — unlike `add()`, whose ingest path owns its own
 * type/score. The optional field set is identical in all three SDKs:
 * `type, score, status, related_ids, valid_from, valid_until, metadata`.
 * All optional; `type` defaults to "episodic" and `score` to 5.
 *
 * Junior Tip [the session is NOT in here any more — 3.0.0, 2026-09-14]: this
 * interface used to carry `sessionUuid` AND its alias `sessionId`, and
 * `create()` fell back to the client's ambient session when both were absent.
 * Three ways to name one thing, plus an invisible fourth. A caller who forgot
 * the option did not get an error — the record landed in whatever session the
 * Memory instance happened to be holding, which is the kind of misfile nobody
 * notices until a search comes back with someone else's turn in it. The
 * session is now the FIRST POSITIONAL ARGUMENT of `create()`, exactly as in
 * Go's `Create(ctx, sessionUUID, content, opts...)`: `POST /api/v1/records`
 * cannot succeed without a session and a body, so both are required, and
 * forgetting one is a compile error instead of a silent misfile.
 */
export interface CreateOptions {
  /** Memory type (default "episodic"). Written verbatim. */
  type?: MemoryType;
  /** Importance rating 1-10 (default 5). Written verbatim. */
  score?: number;
  /**
   * Record status, written verbatim (default "saved").
   *
   * Present for parity with Go's `WithCreateStatus` and Python's
   * `CreateRequest.status` — the three SDKs expose ONE optional field set on
   * this endpoint: type, score, status, related_ids, valid_from, valid_until,
   * metadata. TypeScript was the only arm that could not set it.
   */
  status?: string;
  /** Parent/sibling record IDs to attach as `related_ids`. */
  relatedIds?: number[];
  /** RFC3339 UTC start of the record's validity window (`valid_from`). */
  validFrom?: string;
  /** RFC3339 UTC end of the record's validity window (`valid_until`). */
  validUntil?: string;
  /** Caller-supplied metadata, merged under the canonical container_tag. */
  metadata?: Record<string, unknown>;
}

// ── Internal / wire-level types ──────────────────────────────

/**
 * Payload sent to POST /api/v1/ingest.
 *
 * Wire contract: `content` + `container_tag` + required `session_id`.
 * Create the session first via POST /api/v1/sessions. Callers that pin
 * score/type/metadata use `/api/v1/records` instead — matching Go and Python.
 */
export interface IngestPayload {
  content: string;
  container_tag: string;
  /** Session UUID from POST /api/v1/sessions — required on every ingest. */
  session_id: string;
}

/** Payload sent to POST /api/v1/records (pinned `add()`, `create()`, `createInSession()`). */
export interface RecordPayload {
  uuid: string;
  type: string;
  dimension: number;
  prefix: string;
  weight: number;
  score: number;
  vector: string;
  related_ids: number[];
  main_ids: number[];
  consolidate_id: number;
  metadata: string;
  summary: string;
  content: string;
  consolidated: boolean;
  status: string;
  /**
   * RFC3339 UTC start of the temporal validity window. Omitted when unset.
   * persist a bitemporal window verbatim, matching Go/Python `create`.
   */
  valid_from?: string;
  /** RFC3339 UTC end of the temporal validity window. Omitted when unset. */
  valid_until?: string;
}

