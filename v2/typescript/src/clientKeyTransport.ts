/**
 * The single round trip of the client-key surface.
 *
 * One domain: send ONE request authenticated with a CLIENT key to
 * `/api/v1/client/*` and return the parsed 2xx object or the typed error.
 * `ClientAPI` (`clientKeyApi.ts`) builds every method on top.
 *
 * Security, same as the data-plane transport (`clientFetch.ts`): redirects are
 * an error, the response is capped at 100 MB, the key never appears in a
 * message, the URL never appears in a transport error.
 *
 * Junior Tip [never X-Tenant-ID with a client key]: a client key is not bound
 * to a tenant — the tenant travels in the BODY. Sending X-Tenant-ID would
 * invite a future server change to act on a tenant the body did not name, so
 * this transport never sets that header.
 *
 * Junior Tip [no retry here]: a 503 `not_leader` is reported
 * (`AnhurClientAPIError.isNotLeader`) and the caller decides; a hidden retry
 * would repeat a non-idempotent mint the caller cannot see.
 */

import { AnhurConnectionError, AnhurError } from "./errors.js";
import { MAX_RESPONSE_SIZE } from "./clientResponse.js";
import { validateHeaderValue } from "./clientHeaders.js";
import { USER_AGENT } from "./version.js";
import { DEFAULT_CLOUD_URL } from "./memory.js";
import { AnhurClientAPIError, unexpectedResponse } from "./clientKeyErrors.js";
import type { ClientAPIOptions } from "./clientKeyTypes.js";

const DEFAULT_TIMEOUT_MS = 30_000;

/** A parsed 2xx JSON object. */
export type JsonObject = Record<string, unknown>;

/** Holds the client key and sends one request at a time (stateless, safe to share). */
export class ClientKeyTransport {
  readonly baseUrl: string;
  private readonly clientKey: string;
  private readonly timeoutMs: number;

  constructor(options: ClientAPIOptions) {
    if (!options?.clientKey) throw new Error("clientKey is required.");
    validateHeaderValue(options.clientKey, "clientKey");
    this.clientKey = options.clientKey;
    this.baseUrl = (options.url ?? DEFAULT_CLOUD_URL).replace(/\/+$/, "");
    this.timeoutMs = options.timeoutMs && options.timeoutMs > 0 ? options.timeoutMs : DEFAULT_TIMEOUT_MS;
  }

  /**
   * Send ONE request; return the 2xx JSON object.
   *
   * @throws {AnhurClientAPIError} any non-2xx status (code = `body.error`).
   * @throws {AnhurClientAPIResponseError} a 2xx body that is not a JSON object.
   * @throws {AnhurConnectionError} no answer (timeout, transport, redirect).
   */
  protected async send(method: string, path: string, body?: JsonObject): Promise<JsonObject> {
    let response: Response;
    try {
      response = await fetch(this.baseUrl + path, {
        method,
        headers: {
          "X-API-Key": this.clientKey,
          "User-Agent": USER_AGENT,
          "Content-Type": "application/json",
        },
        body: body === undefined ? undefined : JSON.stringify(body),
        redirect: "error",
        signal: AbortSignal.timeout(this.timeoutMs),
      });
    } catch (err: unknown) {
      if (err instanceof Error && (err.name === "TimeoutError" || err.name === "AbortError")) {
        throw new AnhurConnectionError(
          `Failed to connect to AnhurDB: request timeout (${this.timeoutMs / 1000}s)`,
          undefined,
          "timeout",
        );
      }
      const message = err instanceof Error ? err.message : String(err);
      throw new AnhurConnectionError(`Failed to connect to AnhurDB: ${message}`);
    }
    const bodyText = await response.text();
    if (bodyText.length > MAX_RESPONSE_SIZE) {
      throw new AnhurError(`Response exceeds maximum size (${MAX_RESPONSE_SIZE / (1024 * 1024)} MB)`);
    }
    if (!response.ok) {
      throw AnhurClientAPIError.fromBody(response.status, method, path, bodyText);
    }
    let parsed: unknown = null;
    try {
      parsed = JSON.parse(bodyText);
    } catch {
      parsed = null;
    }
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
      throw unexpectedResponse(method, path, "response is not a JSON object");
    }
    return parsed as JsonObject;
  }
}
