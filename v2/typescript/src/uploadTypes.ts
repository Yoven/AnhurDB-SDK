/**
 * AnhurDB TypeScript SDK — what the two UPLOAD endpoints ANSWER.
 *
 * `POST /api/v1/upload` (202 Accepted) and `GET /api/v1/upload/{id}/status`.
 * Its own file because `types.ts` is long past the ~300-line house cut and
 * house law forbids growing a file already over it; upload is a real domain
 * with one handler (`AnhurDB/server/handler/upload.go`) owning both shapes.
 *
 * @module
 */

/**
 * Result of `POST /api/v1/upload` — the file was ACCEPTED, not processed.
 *
 * Mirrors the fixed nine-key map at `handler/upload.go:109-119` exactly. The
 * status code is 202: ingestion is asynchronous, so poll `uploadStatus()` /
 * `waitForUpload()` with `record_id` to learn what became of the file.
 *
 * Junior Tip [there is no `id`, and there never was — 2026-09-14]: this
 * interface used to declare `id?: number`, and the handler has never emitted
 * it. The key for polling is `record_id`. A phantom optional is the worst
 * kind of lie a type can tell: `upload.id ?? upload.record_id` compiles, reads
 * like a careful fallback, and the first branch is dead code that no test can
 * ever reach. Every field below is a key in that map; nothing in that map is
 * missing here. When you add one, open the handler first.
 *
 * Every field is optional because an OSS/older deployment may answer with a
 * subset — but a field absent from the handler does not belong here at all.
 */
export interface UploadResult {
  /** Human-readable ack, currently `"File accepted for processing"`. */
  message?: string;
  /** The file record's id — the value `uploadStatus()` takes. */
  record_id?: number;
  /** Session UUID the file was filed under (`result.SessionUUID`). */
  uuid?: string;
  /** Original filename as the server stored it. */
  filename?: string;
  /** MIME type the server settled on. */
  mime?: string;
  /** MIME type SNIFFED from the bytes; today the handler sends the same value. */
  mime_detected?: string;
  /** Extension the server derived (e.g. `"pdf"`). */
  extension?: string;
  /** Size of the accepted payload in bytes. */
  size_bytes?: number;
  /** Initial ingest status, e.g. `"processing"`. */
  status?: string;
}

/**
 * Result from upload status polling — `GET /api/v1/upload/{id}/status`.
 *
 * Mirrors `UploadHandler.UploadStatus` in
 * `AnhurDB/server/handler/upload.go` EXACTLY: the handler builds a fixed map of
 * seven keys and never adds another. Every key below exists there; nothing that
 * exists there is missing here.
 *
 * Junior Tip [a declared field the server never sends is worse than a missing
 * one]: this interface used to declare `filename`, `error`, `record_ids` and a
 * bare `id`. None of them are ever emitted — the handler answers
 * `record_id/uuid/status/type/summary/metadata/completed`. The cost was not
 * cosmetic: `waitForUpload` used `Boolean(payload.error)` as one of its
 * TERMINAL conditions, so that branch was unreachable code that read as a
 * safety net. A failed ingest is reported through `status`, and only through
 * `status`. When you add a field here, open the handler first; the type is a
 * claim about the server, not a wish list.
 */
export interface UploadStatusResult {
  /** The file record's id (server key is `record_id`, never `id`). */
  record_id?: number;
  /** Stable record uuid. */
  uuid?: string;
  /** "processing", "completed", "saved", or "failed". */
  status: string;
  /** Always `"file"` — the handler rejects anything else with HTTP 400. */
  type?: string;
  /** Server-computed: true when `status` is `completed` or `saved`. */
  completed?: boolean;
  summary?: string;
  /** Raw metadata JSON string, exactly as stored. */
  metadata?: string;
}
