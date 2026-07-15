/**
 * Zero-dependency HTTP client for AnhurDB.
 *
 * Uses the native `fetch` API available in Node 18+ and all modern
 * browsers / runtimes (Deno, Bun, Cloudflare Workers).
 *
 * Security hardening:
 *   - ``X-API-Key`` header for auth (matches server middleware).
 *   - Redirect following disabled to prevent credential leakage
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
   * respondem text/plain (não JSON). Sem isto, JSON.parse falhava e o fallback
   * embrulhava em `{message: text.slice(0,1000)}`, então readContent extraía
   * `data.content` (undefined) e devolvia "" — PERDA TOTAL do conteúdo no TS,
   * enquanto Go e Python devolviam o corpo cru.
   */
  rawText?: boolean;
}

/** Maximum response body size: 100 MB. */
const MAX_RESPONSE_SIZE = 100 * 1024 * 1024;

/**
 * Per-request timeout in milliseconds.
 *
 * Default 30_000ms matches the Python SDK and the Go SDK so all three fail at
 * the same boundary. Override via ANHUR_REQUEST_TIMEOUT_MS for long-running
 * hosted paths (e.g. HEL1 E2E search auto-embed, which routinely exceeds 30s
 * under DeepInfra load while Go's e2e runner already uses 90s).
 */
const REQUEST_TIMEOUT_MS = (() => {
  const fromEnv = Number(process.env.ANHUR_REQUEST_TIMEOUT_MS || "");
  return Number.isFinite(fromEnv) && fromEnv > 0 ? fromEnv : 30_000;
})();

/*
 * Transport is a transparent pipe: exactly one HTTP attempt per call, then the
 * typed error is returned to the caller. No client-side retry loop.
 *
 * NOTE — there is ALSO no application-level anchor seeding. `createRecord` in
 * memory.ts makes exactly one request; a 422 "episodic anchor" surfaces as a
 * typed AnhurQueryError and is NEVER silently patched by fabricating a synthetic
 * episodic record.
 */

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
    // shared headers map — FormData uploads need the runtime to set
    // multipart/form-data with a boundary. JSON requests set it per-call.
    this.headers = {
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
   */
  async get<T = unknown>(
    path: string,
    params?: Record<string, string>,
  ): Promise<T> {
    return this.request<T>({ method: "GET", path, params });
  }

  /**
   * Send a GET request and return the RAW response body as a string, without
   * JSON parsing. Use for text/plain endpoints (e.g. record content).
   * Mirrors the Python SDK's `raw_text=True` and the Go SDK's raw-bytes read.
   */
  async getText(
    path: string,
    params?: Record<string, string>,
  ): Promise<string> {
    return this.request<string>({
      method: "GET",
      path,
      params,
      rawText: true,
    });
  }

  /**
   * Send a POST request with a JSON body. Read-shaped POST endpoints use
   * {@link postRead} instead.
   */
  async post<T = unknown>(path: string, body: unknown): Promise<T> {
    return this.request<T>({ method: "POST", path, body });
  }

  /**
   * Send a multipart/form-data POST (file upload).
   *
   * owns the boundary. Sticky application/json made AnhurDB return HTTP 400
   * "failed to parse multipart form".
   */
  async postMultipart<T = unknown>(path: string, form: FormData): Promise<T> {
    let url = `${this.baseUrl}${path}`;
    const headers: Record<string, string> = { ...this.headers };
    // Intentionally no Content-Type — FormData sets multipart boundary.

    let response: Response;
    try {
      response = await fetch(url, {
        method: "POST",
        headers,
        body: form,
        redirect: "error",
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });
    } catch (err: unknown) {
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

    if (!response.ok) {
      const bodyText = await response
        .text()
        .then((textBody) => textBody.slice(0, 500))
        .catch(() => "");
      if (response.status === 401 || response.status === 403) {
        throw new AnhurAuthError(
          `Authentication failed (HTTP ${response.status})`,
        );
      }
      if (response.status === 400 || response.status === 422) {
        throw new AnhurQueryError(
          `Invalid request (HTTP ${response.status}): ${bodyText}`,
        );
      }
      if (response.status === 404) {
        throw new AnhurQueryError(
          `Resource not found (HTTP 404): ${path}`,
        );
      }
      throw new AnhurError(
        `Server error (HTTP ${response.status}): ${bodyText}`,
      );
    }

    const text = await response.text();
    if (!text) return {} as T;
    try {
      return JSON.parse(text) as T;
    } catch {
      return { message: text.slice(0, 1000) } as T;
    }
  }

  /**
   * Send a POST request for a READ-shaped endpoint (global search, graph walk,
   * batch-content). Kept distinct from {@link post} so writes use a separate path.
   */
  async postRead<T = unknown>(
    path: string,
    body: unknown,
  ): Promise<T> {
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

  /**
   * Perform the single HTTP request and map the outcome to a typed result or a
   * typed AnhurDB error.
   *
   * Performs a single HTTP request. A connection failure / timeout becomes an
   * AnhurConnectionError; any HTTP error status becomes the matching typed
   * AnhurError and is thrown straight back to the caller.
   */
  private async request<T>(opts: RequestOptions): Promise<T> {
    let url = `${this.baseUrl}${opts.path}`;

    if (opts.params) {
      const qs = new URLSearchParams(opts.params).toString();
      if (qs) url += `?${qs}`;
    }

    const headers: Record<string, string> = { ...this.headers };

    const init: RequestInit = {
      method: opts.method,
      headers,
      // SECURITY: Disable redirects to prevent X-API-Key header
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
      headers["Content-Type"] = "application/json";
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

      // straight to the caller — no transient-detection and no retry wrapper.
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
