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

import { buildRequestHeaders } from "./clientHeaders.js";
// One HTTP attempt (timeout, redirect guard, typed error) lives in
// clientFetch.ts; response → typed error in clientResponse.ts. Both the JSON
// verbs and the multipart upload go through the SAME functions: when the two
// paths each owned a private copy, one of them dropped the HTTP status and
// turned every 404 into a "retryable transport failure".
import { fetchOnce, parseJsonBody } from "./clientFetch.js";
import { readCappedText } from "./clientResponse.js";

/** HTTP methods the client supports. */
type Method = "GET" | "POST" | "PATCH" | "DELETE";

/**
 * Query-string arguments accepted by the read verbs.
 *
 * Junior Tip [why a pair array is allowed alongside the record]: the ADR-0014
 * `sessions` filter is multi-valued and MUST reach the server as a repeated key
 * (`?sessions=a&sessions=b`). A `Record<string, string>` cannot express a
 * repeated key, and joining the uuids with a separator would break the day a
 * session id contains that character. `URLSearchParams` accepts either shape
 * as-is, so widening the type costs nothing and every existing record call site
 * keeps working untouched.
 */
export type QueryParams = Record<string, string> | Array<[string, string]>;

/** Options forwarded to `fetch()`. */
interface RequestOptions {
  method: Method;
  path: string;
  body?: unknown;
  params?: QueryParams;
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
    this.baseUrl = baseUrl.replace(/\/+$/, "");
    // Header construction AND its injection guard live in clientHeaders.ts —
    // see that file's Junior Tips for why the rule sits beside the map it
    // protects, and why this file could not simply grow another three lines.
    this.headers = buildRequestHeaders(apiKey, tenantId);
  }

  // ── Public helpers ───────────────────────────────────────────

  /**
   * Send a GET request.
   */
  async get<T = unknown>(
    path: string,
    params?: QueryParams,
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
    params?: QueryParams,
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
    // Intentionally no Content-Type — FormData sets the multipart boundary.
    const response = await fetchOnce(`${this.baseUrl}${path}`, path, {
      method: "POST",
      headers: { ...this.headers },
      body: form,
    });
    // SECURITY: the 100 MB cap applies to uploads too — see readCappedText.
    return parseJsonBody<T>(await readCappedText(response));
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

  /**
   * Send a DELETE request, optionally with query parameters, and return the
   * parsed body.
   *
   * Junior Tip [por que um DELETE que devolve corpo]: `DELETE
   * /api/v1/records/{id}` responde vazio, mas `DELETE /api/v1/records/by-file`
   * responde a CONTAGEM do que foi apagado — e essa contagem é a resposta ao
   * usuário ("apaguei 511"), não um detalhe. O default `T = void` mantém os
   * chamadores antigos intactos.
   *
   * Junior Tip [por que query string e não corpo]: corpo em DELETE é descartado
   * por vários proxies e pelo `fetch` do navegador. Os parâmetros viajam na URL
   * para que os três SDKs falem o mesmo dialeto do servidor.
   */
  async delete<T = void>(path: string, params?: QueryParams): Promise<T> {
    return this.request<T>({ method: "DELETE", path, params });
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
      const queryString = new URLSearchParams(opts.params).toString();
      if (queryString) url += `?${queryString}`;
    }

    const headers: Record<string, string> = { ...this.headers };
    const init: RequestInit = { method: opts.method, headers };
    if (opts.body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(opts.body);
    }

    const response = await fetchOnce(url, opts.path, init);
    // SECURITY: Cap response size to prevent memory exhaustion.
    const text = await readCappedText(response);

    // Raw-text mode: return the verbatim body (e.g. record content is
    // text/plain). Must come BEFORE JSON parsing so content is never
    // wrapped/truncated. Empty body → empty string.
    if (opts.rawText) {
      return (text ?? "") as unknown as T;
    }
    return parseJsonBody<T>(text);
  }
}
