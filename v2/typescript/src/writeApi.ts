/**
 * AnhurDB TypeScript SDK — the public write methods: `add`, `create`,
 * `createInSession`.
 *
 * One domain in one sentence: decide which door a write uses (ingest pipeline
 * or verbatim records) and which session it lands in; `writePath.ts` then
 * speaks to that door. Moved out of `memory.ts` (1532 lines, house cut ~300)
 * in 4.0.0; `Memory` extends this class, so the public surface is identical
 * apart from `AddResult.mode` (now `"ingest" | "records"`).
 */

import { MemorySessionsApi } from "./sessionsApi.js";
import { ingestText, pinsRecordsPath, writeRecord } from "./writePath.js";
import type { AddOptions, AddResult, CreateOptions, MemoryType } from "./types.js";

/** Write surface of `Memory`. Reads the identity; never rotates the session. */
export abstract class MemoryWriteApi extends MemorySessionsApi {
  /**
   * Store raw text via the ingest write path (default).
   *
   * Session-first servers require {@link MemorySessionsApi.createSession}
   * before add succeeds.
   *
   * Agent UX — pick the write path once:
   * - Raw chat/notes → plain `add(text)` or `{ mode: "ingest" }` →
   *   `POST /ingest` (episodic + async satellites; extraction LLM billed).
   *   MCP: `ingest_memory`. `AddResult.mode` is `"ingest"`.
   * - Direct episodic record → `{ mode: "regular" }` or {@link create} →
   *   `POST /records` (no extraction). MCP: `create_memory`.
   *   `AddResult.mode` is `"records"`.
   *
   * Trap: pinning `score`, `type`, or a non-empty `metadata` also forces
   * `/records` — the ingest body cannot carry them. There is NO fallback
   * between the doors: an error from `/ingest` (404 included) is thrown as is.
   *
   * @param text    - The text to remember.
   * @param options - Optional mode, score/type/metadata, or session override.
   * @returns A result containing the session ID and created records.
   *
   * @example
   * ```ts
   * await mem.createSession();
   * const result = await mem.add("I'm a data scientist at Google");
   * console.log(result.sessionId, result.records);
   * ```
   */
  async add(text: string, options?: AddOptions): Promise<AddResult> {
    if (!text) {
      throw new Error("text cannot be empty");
    }
    await this.memoryIdentity.ready;

    const resolvedSessionId = this.resolveWriteSessionId(options?.sessionId);
    const containerTag = this.memoryIdentity.containerTag;
    if (pinsRecordsPath(options)) {
      return writeRecord(this.searchTransport, {
        text,
        score: options?.score ?? 5,
        type: options?.type ?? "episodic",
        metadata: options?.metadata,
        sessionUuid: resolvedSessionId,
        containerTag,
      });
    }
    return ingestText(this.searchTransport, text, containerTag, resolvedSessionId);
  }

  /**
   * Create exactly one typed record via `POST /api/v1/records` (no extraction).
   *
   * Agent UX — write path: use when you already know `type` + content.
   * For raw text use plain {@link add} / MCP `ingest_memory` instead.
   * Every supplied field (type, score, related_ids, valid_from/until, metadata)
   * is persisted verbatim — no satellite LLM job.
   *
   * Empty session + derived type → HTTP 422 ("create an episodic first").
   * The SDK does not fabricate that anchor. MCP: `create_memory`.
   *
   * Junior Tip [the session is a REQUIRED first argument — 3.0.0, BREAKING]:
   * the old signature was `create(text, options?)`, taking the session from
   * an option or — when none was given — silently from whatever session this
   * instance happened to hold. That never prevented an error; it only decided,
   * invisibly, WHERE the record landed, so a forgotten option MISFILED a
   * record instead of failing. The session is positional and a blank one
   * throws, mirroring Go's `Create(ctx, sessionUUID, content, opts...)`.
   *
   * @param sessionUuid - Session UUID to place the record under (required).
   * @param text        - Record text (stored in summary + content).
   * @param options     - Full-fidelity fields (all optional).
   */
  async create(
    sessionUuid: string,
    text: string,
    options?: CreateOptions): Promise<AddResult> {
    if (!sessionUuid.trim()) {
      throw new Error("create: sessionUuid is required — create a session " +
        "first (POST /api/v1/sessions)");
    }
    if (!text) {
      throw new Error("text cannot be empty");
    }
    await this.memoryIdentity.ready;

    const type: MemoryType = options?.type ?? "episodic";
    return writeRecord(this.searchTransport, {
      text,
      score: options?.score ?? 5,
      type,
      metadata: options?.metadata,
      sessionUuid: sessionUuid.trim(),
      containerTag: this.memoryIdentity.containerTag,
      status: options?.status,
      relatedIds: options?.relatedIds,
      validFrom: options?.validFrom,
      validUntil: options?.validUntil,
    });
  }

  /**
   * Store `text` as an episodic record under a caller-owned session uuid.
   *
   * Python `create_in_session`. Same wire body as `create(sessionUuid, text)`
   * with every default (episodic, score 5, status "saved"). Session must be
   * registered via `createSession` first on session-first servers.
   *
   * Junior Tip [await the tag before writing]: on the auto-derived path the
   * tag reads the placeholder "mem-init" until the async SHA-256 resolves.
   * Writing before that would persist the placeholder in metadata and the
   * record would fall out of container-scoped search/profile.
   *
   * @param text        - Record text (stored in summary + content).
   * @param sessionUuid - Session UUID to place the record under (required).
   */
  async createInSession(text: string, sessionUuid: string): Promise<AddResult> {
    if (!sessionUuid) {
      throw new Error("createInSession: sessionUuid is required");
    }
    await this.memoryIdentity.ready;
    return writeRecord(this.searchTransport, {
      text,
      score: 5,
      type: "episodic",
      sessionUuid,
      containerTag: this.memoryIdentity.containerTag,
    });
  }
}
