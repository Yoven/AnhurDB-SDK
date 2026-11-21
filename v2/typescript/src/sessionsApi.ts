/**
 * AnhurDB TypeScript SDK — session management: register, rotate and list the
 * conversations a `Memory` writes into.
 *
 * One domain in one sentence: own the "current session" (`POST
 * /api/v1/sessions`, local rotation) and read sessions back (stats, history,
 * clusters). Moved out of `memory.ts` (1532 lines, house cut ~300) in 4.0.0;
 * `Memory` extends this class, so the public surface is identical.
 *
 * Junior Tip [why this class may mutate and the others may not]: every other
 * base class of `Memory` only BORROWS the transport. This one also writes the
 * current session, through the {@link MemoryIdentity} that `Memory` owns and
 * hands over via `memoryIdentity`. Writes call `resolveWriteSessionId` here,
 * so the rule "no registered session, no write" lives in exactly one place.
 */

import { MemoryEntitiesApi } from "./entitiesApi.js";
import { mintSessionUuid, type MemoryIdentity } from "./memoryIdentity.js";
import { fetchAllSessionStats } from "./sessionStats.js";
import type { CreateSessionOptions, SessionStats } from "./types.js";

/** Session surface of `Memory`. Mutates only the identity it is handed. */
export abstract class MemorySessionsApi extends MemoryEntitiesApi {
  /** The container tag + current session owned by the concrete `Memory`. */
  protected abstract get memoryIdentity(): MemoryIdentity;

  /**
   * Register a write session via POST /api/v1/sessions.
   *
   * Session-first servers reject ingest and record writes until this succeeds.
   * Call once per conversation before {@link MemoryWriteApi.add}.
   *
   * Junior Tip [parity with Python/Go/MCP]: omit `sessionId` → server generates
   * a new UUID. To register a caller-chosen id (e.g. after {@link newSession}):
   * `await mem.createSession({ sessionId: await mem.newSession() })`.
   *
   * @returns The registered session id (also stored on the client).
   */
  async createSession(options?: CreateSessionOptions): Promise<string> {
    await this.memoryIdentity.ready;

    // Only send session_id when the caller explicitly chose one. Omitting it
    // matches Python create_session() / MCP create_session / REST {} body.
    const sessionIdToRegister = options?.sessionId?.trim() || "";
    const payload: Record<string, unknown> = {};
    if (sessionIdToRegister) {
      payload.session_id = sessionIdToRegister;
    }
    if (options?.metadata) {
      payload.metadata = options.metadata;
    }

    const data = await this.searchTransport.post<{ session_id?: string }>(
      "/api/v1/sessions",
      payload);

    const registeredSessionId = data.session_id ?? sessionIdToRegister;
    if (!registeredSessionId) {
      throw new Error("createSession: server returned empty session_id");
    }
    this.memoryIdentity.sessionUuid = registeredSessionId;
    this.memoryIdentity.sessionRegistered = true;
    return registeredSessionId;
  }

  /**
   * Generate a fresh local session id and register it (Python `open_session`).
   *
   * Equivalent to `createSession({ sessionId: await newSession() })`.
   */
  async openSession(options?: { metadata?: Record<string, unknown> }): Promise<string> {
    const localSessionId = await this.newSession();
    return this.createSession({
      sessionId: localSessionId,
      metadata: options?.metadata,
    });
  }

  /**
   * Start a new local session (generates a fresh UUID).
   *
   * Does NOT register the session on the server — call {@link createSession}
   * before writing on session-first deployments.
   *
   * @returns The new session ID.
   */
  async newSession(): Promise<string> {
    await this.memoryIdentity.ready;
    // `<container_tag>-<YYYYMMDD-HHMMSS UTC>-<6 random hex>`: the random suffix
    // stops two rotations in the same UTC second from colliding onto one
    // session, byte-for-byte with Python new_session and Go NewSession.
    this.memoryIdentity.sessionUuid = mintSessionUuid(this.memoryIdentity.containerTag);
    this.memoryIdentity.sessionRegistered = false;
    return this.memoryIdentity.sessionUuid;
  }

  /**
   * List ALL sessions with aggregate statistics, following server pagination.
   *
   * `/api/v1/sessions/stats` defaults to `limit=50` and reports the truncation
   * in `has_more`; this used to ignore it, so a 99-session tenant answered 50
   * and looked complete. Loop and brakes: `sessionStats.ts`.
   */
  async listSessions(): Promise<SessionStats[]> {
    return fetchAllSessionStats(this.searchTransport);
  }

  /**
   * Get paginated full-text history for a session.
   *
   * Returns actual message content, unlike `listSessions` which
   * returns metadata only.
   *
   * @param sessionUuid - The session UUID.
   * @param limit       - Max records per page (default 50).
   * @param offset      - Pagination offset (default 0).
   */
  async getSessionHistory(
    sessionUuid: string,
    limit?: number,
    offset?: number): Promise<unknown> {
    const params: Record<string, string> = {};
    if (limit !== undefined) params.limit = String(limit);
    if (offset !== undefined) params.offset = String(offset);

    return this.searchTransport.get(
      `/api/v1/sessions/${sessionUuid}/history`,
      Object.keys(params).length > 0 ? params : undefined);
  }

  /**
   * Get mathematically clustered topological groups for a session.
   *
   * @param sessionUuid - The session UUID.
   */
  async getSessionClusters(
    sessionUuid: string): Promise<unknown> {
    return this.searchTransport.get(
      `/api/v1/sessions/${sessionUuid}/clusters`,
      undefined);
  }

  /**
   * Resolve the session id for a write. An explicit override is passed
   * through; otherwise a prior createSession/openSession is required.
   */
  protected resolveWriteSessionId(explicitSessionId?: string): string {
    const trimmedExplicit = explicitSessionId?.trim() ?? "";
    if (trimmedExplicit) {
      return trimmedExplicit;
    }
    if (!this.memoryIdentity.sessionRegistered || !this.memoryIdentity.sessionUuid) {
      throw new Error(
        "session_id is required — create a session first (POST /api/v1/sessions)",
      );
    }
    return this.memoryIdentity.sessionUuid;
  }
}
