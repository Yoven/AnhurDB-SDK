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
 *   - 30s per-request timeout so a stalled server can never hang the caller
 *     (parity with the Python/Go SDKs).
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
  /**
   * When true, the raw response body is returned verbatim as a string instead
   * of being JSON-parsed. Mirrors the Python SDK's `raw_text=True`.
   *
   * Junior Tip [parity-fix 2026-06-11]: endpoints como GET /records/{id}/content
   * respondem text/plain (não JSON). Sem isto, JSON.parse falhava e o fallback
   * embrulhava em `{message: text.slice(0,1000)}`, então readContent extraía
   * `data.content` (undefined) e devolvia "" — PERDA TOTAL do conteúdo no TS,
   * enquanto Go e Python devolviam o corpo cru.
   */
  rawText?: boolean;
  /**
   * Optional read-your-writes (RYW) barrier. When set to a positive Raft index
   * (the `raftIndex` a caller received from its own prior write), the
   * `X-Anhur-Min-Index` request header is sent so the server blocks this read
   * until the contacted node has applied that index for the tenant.
   *
   * Junior Tip [parity 2026-06-17, verified against server/middleware/min_index.go]:
   * this is an HTTP request HEADER, not a query param or body field. Reads
   * WITHOUT it keep the default eventually-consistent, load-balanced behaviour
   * at zero cost. The Go SDK exposes the same capability via WithMinIndex, the
   * Python SDK via `min_index=`. Undefined / 0 → header omitted.
   */
  minIndex?: number;
}

/** Maximum response body size: 100 MB. */
const MAX_RESPONSE_SIZE = 100 * 1024 * 1024;

/**
 * Request header that opts a single read into read-your-writes consistency.
 * The server's MinIndexBarrier middleware blocks the read until the node's
 * local applied Raft index for the tenant reaches the supplied value. Kept in
 * one place so it matches the Go/Python SDKs and the server byte-for-byte.
 */
const HEADER_MIN_INDEX = "X-Anhur-Min-Index";

/**
 * Per-request timeout in milliseconds.
 *
 * Junior Tip [security/availability, parity 2026-06]: native fetch() has NO
 * default timeout, so a stalled server would hang the caller's promise forever.
 * 30_000ms (30s) matches the Python SDK (requests timeout=30) and the Go SDK
 * (http.Client.Timeout = 30s) so all three SDKs fail at the same boundary.
 */
const REQUEST_TIMEOUT_MS = 30_000;

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

  /**
   * Send a GET request.
   *
   * `minIndex`, when set to a positive Raft index (the `raftIndex` from a prior
   * write), adds the read-your-writes barrier header so the server blocks until
   * the node has applied that index. Omit / 0 for the default eventually-
   * consistent read.
   */
  async get<T = unknown>(
    path: string,
    params?: Record<string, string>,
    minIndex?: number,
  ): Promise<T> {
    return this.request<T>({ method: "GET", path, params, minIndex });
  }

  /**
   * Send a GET request and return the RAW response body as a string, without
   * JSON parsing. Use for text/plain endpoints (e.g. record content).
   * Mirrors the Python SDK's `raw_text=True` and the Go SDK's raw-bytes read.
   *
   * `minIndex` works exactly as in {@link get} — optional read-your-writes.
   */
  async getText(
    path: string,
    params?: Record<string, string>,
    minIndex?: number,
  ): Promise<string> {
    return this.request<string>({
      method: "GET",
      path,
      params,
      rawText: true,
      minIndex,
    });
  }

  /**
   * Send a POST request with a JSON body. This is the WRITE entry point — it
   * never sends the RYW barrier (a write PRODUCES the raft index, it does not
   * consume one). Read-shaped POST endpoints use {@link postRead} instead.
   */
  async post<T = unknown>(path: string, body: unknown): Promise<T> {
    return this.request<T>({ method: "POST", path, body });
  }

  /**
   * Send a POST request for a READ-shaped endpoint (global search, graph walk,
   * batch-content) with an optional `minIndex` read-your-writes barrier. The
   * server's MinIndexBarrier middleware wraps the whole API, so it honours
   * `X-Anhur-Min-Index` on POST reads too. Kept distinct from {@link post} so a
   * write can never accidentally send a barrier.
   */
  async postRead<T = unknown>(
    path: string,
    body: unknown,
    minIndex?: number,
  ): Promise<T> {
    return this.request<T>({ method: "POST", path, body, minIndex });
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

    // Per-request headers: clone the session defaults, then add the optional
    // RYW barrier. A falsy minIndex (undefined / 0) leaves the header off so
    // the default eventually-consistent read is preserved.
    const headers: Record<string, string> = { ...this.headers };
    if (opts.minIndex) {
      headers[HEADER_MIN_INDEX] = String(opts.minIndex);
    }

    const init: RequestInit = {
      method: opts.method,
      headers,
      // SECURITY: Disable redirects to prevent X-API-Key header
      // leaking to external origins on 3xx (CVE-2026-34518 class).
      redirect: "error",
      // SECURITY / AVAILABILITY: abort the request after 30s so a stalled
      // server can never hang the caller forever. fetch() has NO default
      // timeout. 30s matches the Python SDK (timeout=30) and the Go SDK
      // (http.Client{Timeout: 30 * time.Second}) so all three behave
      // identically. On timeout the promise rejects with an AbortError, which
      // we translate to AnhurConnectionError below.
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    };

    if (opts.body !== undefined) {
      init.body = JSON.stringify(opts.body);
    }

    let response: Response;
    try {
      response = await fetch(url, init);
    } catch (err: unknown) {
      // A 30s timeout surfaces as a DOMException with name "TimeoutError"
      // (some runtimes use "AbortError"). Map both to a clear connection error
      // so callers get a precise, actionable message instead of a generic
      // abort. SECURITY: never include the full URL (could be logged).
      if (
        err instanceof Error &&
        (err.name === "TimeoutError" || err.name === "AbortError")
      ) {
        throw new AnhurConnectionError(
          `Failed to connect to AnhurDB: request timeout (${
            REQUEST_TIMEOUT_MS / 1000
          }s)`,
        );
      }
      const message = err instanceof Error ? err.message : String(err);
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

    // Raw-text mode: return the verbatim body (e.g. record content is
    // text/plain). Must come BEFORE the empty-body and JSON.parse branches so
    // content is never wrapped/truncated. Empty body → empty string.
    if (opts.rawText) {
      return (text ?? "") as unknown as T;
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
