/**
 * Zero-dependency HTTP client for AnhurDB.
 *
 * Uses the native `fetch` API available in Node 18+ and all modern
 * browsers / runtimes (Deno, Bun, Cloudflare Workers).
 *
 * Security hardening:
 *   - ``X-API-Key`` header for auth (matches server middleware).
 *   - Redirect following disabled to prevent credential leakage
 *     (mitigates CVE-2026-34518 class of attacks).
 *   - Response body capped at 100 MB to prevent memory exhaustion.
 *   - Header injection protection: tenant_id validated against CRLF.
 *   - Error messages never include the full API key.
 */

import {
  AnhurAuthError,
  AnhurConnectionError,
  AnhurError,
  AnhurQueryError,
} from "./types.js";

/** HTTP methods the client supports. */
type Method = "GET" | "POST" | "PATCH" | "DELETE";

/** Options forwarded to `fetch()`. */
interface RequestOptions {
  method: Method;
  path: string;
  body?: unknown;
  params?: Record<string, string>;
}

/** Maximum response body size: 100 MB. */
const MAX_RESPONSE_SIZE = 100 * 1024 * 1024;

/**
 * Number of attempts (initial + retries) for idempotent writes that hit a
 * transient cluster condition.
 *
 * Junior Tip [retry parity, 2026-06]: 3 total attempts matches the Go/Python
 * SDK retry budget. We ONLY retry writes whose effect is idempotent at the
 * application layer (POST/PATCH of the same record payload) and ONLY for the
 * two known-transient signatures below — never blanket-retry, or a genuine
 * 500 storm turns into a 3x amplification.
 */
const WRITE_RETRY_ATTEMPTS = 3;

/** Base backoff in milliseconds; doubles each attempt (100, 200, 400...). */
const RETRY_BACKOFF_BASE_MS = 100;

/**
 * Decide whether an error response is a transient cluster condition that a
 * retry can plausibly clear.
 *
 *   - HTTP 500 with a "not_leader" body: the contacted node was not the Raft
 *     leader at that instant; a moment later a leader is elected / known.
 *   - Any status with an "episodic anchor" body: the anchor record this write
 *     depends on was created microseconds earlier and the read that validates
 *     it raced ahead of replication — read-your-writes catches up on retry.
 */
function isTransientWriteError(status: number, bodyText: string): boolean {
  const lower = bodyText.toLowerCase();
  if (status === 500 && lower.includes("not_leader")) return true;
  if (lower.includes("episodic anchor")) return true;
  return false;
}

/** Sleep helper for backoff between retries. */
function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

/** Regex for safe HTTP header values (printable ASCII only). */
const HEADER_SAFE = /^[\x20-\x7e]*$/;

/**
 * Validate a string is safe for use as an HTTP header value.
 *
 * Rejects CR, LF, null bytes, and other control characters that could
 * enable HTTP header injection (response splitting).
 */
function validateHeaderValue(value: string, name: string): void {
  if (value && !HEADER_SAFE.test(value)) {
    throw new Error(
      `${name} contains invalid characters for HTTP header. ` +
        "Only printable ASCII (0x20-0x7E) is allowed.",
    );
  }
}

/**
 * Lightweight HTTP wrapper around `fetch`.
 *
 * - Sets ``X-API-Key`` and ``X-Tenant-ID`` headers on every request.
 * - Deserialises JSON responses automatically.
 * - Maps HTTP error codes to typed AnhurDB exceptions.
 * - Disables redirect following (credential leak protection).
 * - Caps response size at 100 MB (memory exhaustion protection).
 */
export class HttpClient {
  private readonly baseUrl: string;
  private readonly headers: Record<string, string>;

  constructor(baseUrl: string, apiKey: string, tenantId?: string) {
    // Validate inputs against header injection.
    validateHeaderValue(apiKey, "apiKey");
    if (tenantId) validateHeaderValue(tenantId, "tenantId");

    this.baseUrl = baseUrl.replace(/\/+$/, "");
    this.headers = {
      "Content-Type": "application/json",
      "X-API-Key": apiKey,
      "User-Agent": "AnhurSDK-TypeScript/2.1",
    };
    if (tenantId) {
      this.headers["X-Tenant-ID"] = tenantId;
    }
  }

  // ── Public helpers ───────────────────────────────────────────

  /** Send a GET request. */
  async get<T = unknown>(
    path: string,
    params?: Record<string, string>,
  ): Promise<T> {
    return this.request<T>({ method: "GET", path, params });
  }

  /** Send a POST request with a JSON body. */
  async post<T = unknown>(path: string, body: unknown): Promise<T> {
    return this.request<T>({ method: "POST", path, body });
  }

  /** Send a PATCH request with a JSON body. */
  async patch<T = unknown>(path: string, body: unknown): Promise<T> {
    return this.request<T>({ method: "PATCH", path, body });
  }

  /** Send a DELETE request. */
  async delete(path: string): Promise<void> {
    await this.request<void>({ method: "DELETE", path });
  }

  // ── Core request method ──────────────────────────────────────

  private async request<T>(opts: RequestOptions): Promise<T> {
    // Junior Tip [retry, 2026-06]: only POST/PATCH are retried, and only on
    // the transient signatures in isTransientWriteError. GET is safe to retry
    // too in principle, but the two transient conditions we target
    // (not_leader on a write, missing anchor for a dependent write) are
    // write-path only, so we scope the budget to writes to avoid masking
    // genuine read failures. DELETE is left single-shot (idempotency of a
    // hard delete on retry is server-dependent).
    const retryable = opts.method === "POST" || opts.method === "PATCH";
    const maxAttempts = retryable ? WRITE_RETRY_ATTEMPTS : 1;

    let lastError: unknown;
    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      try {
        return await this.attempt<T>(opts);
      } catch (err: unknown) {
        lastError = err;
        const transient =
          err instanceof TransientWriteError && attempt < maxAttempts;
        if (!transient) {
          // Unwrap the carrier so callers see the real typed AnhurError.
          if (err instanceof TransientWriteError) throw err.cause;
          throw err;
        }
        // Exponential backoff: 100ms, 200ms, ...
        await delay(RETRY_BACKOFF_BASE_MS * 2 ** (attempt - 1));
      }
    }
    // Unreachable in practice; satisfies the type checker.
    throw lastError;
  }

  /**
   * Perform a single HTTP attempt. Throws a `TransientWriteError` (wrapping
   * the typed error) when the response matches a retryable cluster condition,
   * so the retry loop in `request` can distinguish it from a permanent error.
   */
  private async attempt<T>(opts: RequestOptions): Promise<T> {
    let url = `${this.baseUrl}${opts.path}`;

    if (opts.params) {
      const qs = new URLSearchParams(opts.params).toString();
      if (qs) url += `?${qs}`;
    }

    const init: RequestInit = {
      method: opts.method,
      headers: { ...this.headers },
      // SECURITY: Disable redirects to prevent X-API-Key header
      // leaking to external origins on 3xx (CVE-2026-34518 class).
      redirect: "error",
    };

    if (opts.body !== undefined) {
      init.body = JSON.stringify(opts.body);
    }

    let response: Response;
    try {
      response = await fetch(url, init);
    } catch (err: unknown) {
      // SECURITY: Do not include full URL in error (could be logged).
      const message =
        err instanceof Error ? err.message : String(err);
      throw new AnhurConnectionError(
        `Failed to connect to AnhurDB: ${message}`,
      );
    }

    // Map HTTP error codes to typed exceptions.
    // SECURITY: Error messages include status but not API key.
    if (!response.ok) {
      const bodyText = await response
        .text()
        .then((t) => t.slice(0, 500))
        .catch(() => "");

      let typedError: AnhurError;
      if (response.status === 401 || response.status === 403) {
        typedError = new AnhurAuthError(
          `Authentication failed (HTTP ${response.status})`,
        );
      } else if (response.status === 400 || response.status === 422) {
        typedError = new AnhurQueryError(
          `Invalid request (HTTP ${response.status}): ${bodyText}`,
        );
      } else if (response.status === 404) {
        typedError = new AnhurQueryError(
          `Resource not found (HTTP 404): ${opts.path}`,
        );
      } else {
        typedError = new AnhurError(
          `Server error (HTTP ${response.status}): ${bodyText}`,
        );
      }

      // Wrap transient conditions so the retry loop can act on them while
      // preserving the original typed error for the final throw.
      if (isTransientWriteError(response.status, bodyText)) {
        throw new TransientWriteError(typedError);
      }
      throw typedError;
    }

    // SECURITY: Cap response size to prevent memory exhaustion.
    const text = await response.text();
    if (text.length > MAX_RESPONSE_SIZE) {
      throw new AnhurError(
        `Response exceeds maximum size (${MAX_RESPONSE_SIZE / (1024 * 1024)} MB)`,
      );
    }

    // 204 No Content or empty body — return empty object.
    if (!text) return {} as T;

    try {
      return JSON.parse(text) as T;
    } catch {
      return { message: text.slice(0, 1000) } as T;
    }
  }
}

/**
 * Internal carrier used to flag a retryable transient cluster error between
 * `attempt` and the retry loop in `request`. Never surfaces to callers — the
 * loop always unwraps `cause` (the real typed AnhurError) before throwing.
 */
class TransientWriteError extends Error {
  readonly cause: AnhurError;
  constructor(cause: AnhurError) {
    super(cause.message);
    this.name = "TransientWriteError";
    this.cause = cause;
  }
}
