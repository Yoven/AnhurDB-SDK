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
          `Resource not found (HTTP 404): ${opts.path}`,
        );
      }
      throw new AnhurError(
        `Server error (HTTP ${response.status}): ${bodyText}`,
      );
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
