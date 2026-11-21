/**
 * AnhurDB TypeScript SDK — Memory client.
 *
 * Single entry point for the REST API. Core methods: `add`, `search`,
 * `profile`. The same class also exposes CRUD, batch ops, entities, uploads,
 * graph walk, query AST, manifests, and temporal versioning.
 *
 * Junior Tip [one class, many files]: `Memory` is ONE public class, but its
 * methods live in per-domain abstract base classes chained by `extends`, each
 * in its own file under the house ~300-line cut. Read the chain bottom-up:
 *
 *   search.ts      MemorySearchApi    hybrid search (owns the 3 accessors)
 *   recordsApi.ts  MemoryRecordsApi   by-id reads/writes (scope-aware)
 *   filesApi.ts    MemoryFilesApi     upload, upload status, delete-by-file
 *   lookupApi.ts   MemoryLookupApi    type listing, smart search, AST query
 *   manifestApi.ts MemoryManifestApi  recent, manifests, list_chat, tallies
 *   graphApi.ts    MemoryGraphApi     walks and record links
 *   entitiesApi.ts MemoryEntitiesApi  entity knowledge graph (Layer 2)
 *   sessionsApi.ts MemorySessionsApi  current session + session listings
 *   writeApi.ts    MemoryWriteApi     add / create / createInSession
 *   memory.ts      Memory             construction, identity, profile, health
 *
 * Only `Memory` holds state (the transport and the {@link MemoryIdentity});
 * every base class borrows it through the protected accessors below, so no
 * domain file can quietly acquire ownership of a connection or a session.
 *
 * @module
 */

import { HttpClient } from "./client.js";
import { MemoryIdentity } from "./memoryIdentity.js";
import { fetchProfile } from "./profile.js";
import type { MemoryOptions, ProfileResult } from "./types.js";
import { MemoryWriteApi } from "./writeApi.js";

/** Default cloud endpoint. Self-hosted users pass `url` explicitly. */
const DEFAULT_CLOUD_URL = "https://anhurdb.yoven.ai";

/**
 * Dead-simple memory interface for AnhurDB.
 *
 * Handles session management and container tagging; `add()` writes through
 * `/api/v1/ingest` by default and through `/api/v1/records` when the caller
 * pins type/score/metadata (see {@link MemoryWriteApi.add}).
 */
export class Memory extends MemoryWriteApi {
  private readonly client: HttpClient;
  private readonly identity: MemoryIdentity;

  /**
   * Create a new Memory instance.
   *
   * @param options - Connection options (apiKey is required).
   *
   * @example
   * ```ts
   * // Cloud
   * const mem = new Memory({ apiKey: "anhur_xxx" });
   *
   * // Self-hosted
   * const mem = new Memory({
   *   url: "http://localhost:8000",
   *   apiKey: "my-key",
   * });
   * ```
   */
  constructor(options: MemoryOptions) {
    super();
    if (!options.apiKey) {
      throw new Error("apiKey is required");
    }

    const baseUrl = options.url ?? DEFAULT_CLOUD_URL;
    this.client = new HttpClient(baseUrl, options.apiKey, options.tenantId);
    // Container tag: explicit userId or derived (async) from the apiKey.
    this.identity = new MemoryIdentity(options.apiKey, options.userId);
  }

  // ── Public properties ────────────────────────────────────────

  /**
   * Current session UUID.
   *
   * If no userId was provided, containerTag is derived async from the
   * API key hash. The sessionUuid depends on containerTag, so it may be
   * empty until the derivation resolves. Use `getSessionId()` for safety.
   */
  get sessionId(): string {
    return this.identity.sessionUuid;
  }

  /** Async-safe session ID getter (waits for tag derivation). */
  async getSessionId(): Promise<string> {
    await this.identity.ready;
    return this.identity.sessionUuid;
  }

  /**
   * Container tag: the key every memory of this user/agent is grouped under.
   *
   * It is `userId` when one was passed, otherwise `mem-` plus the first 12 hex
   * digits of SHA-256(apiKey). Parity with Go `Memory.ContainerTag()` and
   * Python `Memory.container_tag`.
   *
   * Same caveat as {@link sessionId}: with no `userId` this reads
   * `"mem-init"` until the async derivation lands. Use
   * {@link getContainerTag} when the real value matters.
   */
  get containerTag(): string {
    return this.identity.containerTag;
  }

  /** Async-safe container tag getter (waits for tag derivation). */
  async getContainerTag(): Promise<string> {
    await this.identity.ready;
    return this.identity.containerTag;
  }

  // ── Bridge to the domain base classes ────────────────────────
  //
  // Junior Tip [why these are getters and not shared fields]: the base classes
  // hold NO state — they borrow the transport, the readiness promise, the
  // current session and the identity through these accessors. Keeping them
  // read-only and explicit is what stops a domain file from quietly acquiring
  // ownership of a connection it did not build.

  /** @internal */
  protected get searchTransport(): HttpClient {
    return this.client;
  }

  /** @internal */
  protected get searchTagReady(): Promise<void> {
    return this.identity.ready;
  }

  /** @internal */
  protected get searchCurrentSession(): string {
    return this.identity.sessionUuid;
  }

  /** @internal */
  protected get memoryIdentity(): MemoryIdentity {
    return this.identity;
  }

  // ── profile() — get user/agent profile ──────────────────────

  /**
   * Get the memory profile for a container tag (user/agent).
   *
   * Static facts, dynamic state and aggregate statistics. The call — including
   * WHY an unknown tag is an empty 200 and never an exception, and why an
   * empty tag never reaches the wire — lives in `profile.ts`.
   *
   * @param containerTag - Tag to profile. Defaults to this client's own.
   * @example
   * ```ts
   * const other = await mem.profile("some-other-agent");
   * ```
   */
  async profile(containerTag?: string): Promise<ProfileResult> {
    await this.identity.ready;
    return fetchProfile(
      this.client,
      containerTag?.trim() || this.identity.containerTag);
  }

  /**
   * Check server health via `GET /api/v1/health`.
   *
   * Junior Tip [parity]: matches Python `health` and Go `Health`.
   */
  async health(): Promise<Record<string, unknown>> {
    return this.client.get<Record<string, unknown>>("/api/v1/health");
  }

  /**
   * Forget a specific memory or trigger cognitive decay.
   *
   * Not yet implemented — placeholder for the decay API.
   * Use `delete()` for hard removal or `update(id, { status: "archived" })`
   * for soft delete.
   *
   * @param memoryId - If provided, forget this specific memory.
   * @throws Always throws until the API is available.
   */
  async forget(memoryId?: number): Promise<void> {
    void memoryId;
    throw new Error(
      "forget() is not yet available. " +
        "Use delete() for hard removal or update(id, {status: 'archived'}) " +
        "for soft delete.");
  }

  /** String representation for logging / debugging. */
  toString(): string {
    return `Memory(containerTag=${this.identity.containerTag}, session=${this.identity.sessionUuid})`;
  }
}
