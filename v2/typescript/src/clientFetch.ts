/**
 * AnhurDB TypeScript SDK — ONE HTTP attempt: send it, bound it in time, and
 * turn a network failure or a non-2xx status into the typed error.
 *
 * One domain in one sentence: everything between "the request is built" and
 * "here is the response text", shared by the JSON verbs and the multipart
 * upload. Split out of `client.ts` (330 lines, house cut ~300) in 4.0.0, where
 * `request()` and `postMultipart()` each carried a private copy of the same
 * timeout/error mapping — the exact shape `clientResponse.ts` already removed
 * once for the status mapping.
 *
 * Transport is a transparent pipe: exactly one HTTP attempt per call, then the
 * typed error goes back to the caller. No client-side retry loop, and no
 * application-level anchor seeding (a 422 "episodic anchor" surfaces as a typed
 * `AnhurQueryError`; the SDK never fabricates a synthetic episodic record).
 */

import { AnhurConnectionError } from "./errors.js";
import { readErrorBodyExcerpt, typedErrorForResponse } from "./clientResponse.js";

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

/**
 * Perform exactly one `fetch` and return the SUCCESSFUL response.
 *
 * Adds the two settings every request must carry — `redirect: "error"` and the
 * timeout signal — so no verb can forget one of them.
 *
 * Junior Tip [why redirects are an error]: following a redirect would replay
 * the `X-API-Key` header to whatever host the `Location` names.
 *
 * Junior Tip [why both "TimeoutError" and "AbortError"]: `fetch()` has NO
 * default timeout; `AbortSignal.timeout` rejects with a DOMException named
 * "TimeoutError" (some runtimes say "AbortError"). Both become a timeout-kind
 * `AnhurConnectionError`. SECURITY: never quote the full URL (it could be
 * logged); `requestPath` is quoted only in the 404 message.
 *
 * @param url         - Full request URL (base + path + query string).
 * @param requestPath - Path only, for error messages.
 * @param init        - Method, headers and body; redirect/signal are set here.
 * @throws {AnhurConnectionError} the request never got an answer.
 * @throws {AnhurError} the typed error for a non-2xx status.
 */
export async function fetchOnce(
  url: string,
  requestPath: string,
  init: RequestInit,
): Promise<Response> {
  let response: Response;
  try {
    response = await fetch(url, {
      ...init,
      redirect: "error",
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
  } catch (err: unknown) {
    if (
      err instanceof Error &&
      (err.name === "TimeoutError" || err.name === "AbortError")
    ) {
      throw new AnhurConnectionError(
        `Failed to connect to AnhurDB: request timeout (${REQUEST_TIMEOUT_MS / 1000}s)`,
        undefined,
        "timeout",
      );
    }
    const message = err instanceof Error ? err.message : String(err);
    throw new AnhurConnectionError(`Failed to connect to AnhurDB: ${message}`);
  }

  if (!response.ok) {
    // SECURITY: error messages include the status, never the API key.
    throw typedErrorForResponse(
      response.status,
      requestPath,
      await readErrorBodyExcerpt(response),
    );
  }
  return response;
}

/**
 * Parse a successful body as JSON.
 *
 * An empty body (204 / no content) is `{}`; a non-JSON body is wrapped as
 * `{ message: <first 1000 chars> }` so a plain-text acknowledgement is still
 * readable. Endpoints whose payload IS text use the raw-text read instead.
 */
export function parseJsonBody<T>(bodyText: string): T {
  if (!bodyText) return {} as T;
  try {
    return JSON.parse(bodyText) as T;
  } catch {
    return { message: bodyText.slice(0, 1000) } as T;
  }
}
