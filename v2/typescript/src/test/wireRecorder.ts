/**
 * A `fetch` stub that RECORDS what the SDK put on the wire and replays a
 * scripted body back.
 *
 * WHY: most parity defects in this SDK were not "the code threw" — they were
 * "the type described a response the server never sends" or "a query parameter
 * the caller asked for never left the process". Neither is visible from a
 * return value alone; both are obvious the moment you look at the REQUEST the
 * SDK made and the exact keys it read back. Every test in the 3.0.0 parity
 * round uses this, so a future reader can see the wire, not a mock's opinion.
 */

/** One request the SDK made, as the transport saw it. */
export interface RecordedRequest {
  url: URL;
  method: string;
  /** Parsed JSON body when there was one, otherwise undefined. */
  body?: unknown;
}

/** Result of a recorded run: what went out, and what the call returned. */
export interface RecordedRun<T> {
  requests: RecordedRequest[];
  value: T;
}

/**
 * Run `call`, answering every HTTP request with `responseBody` (HTTP 200).
 *
 * `globalThis.fetch` is restored in a `finally` so one failing assertion
 * cannot leak a stub into the next test file — a leaked stub turns unrelated
 * suites green for the wrong reason, which is worse than the failure it hides.
 *
 * @param responseBody - JSON value every request receives.
 * @param call         - The SDK call under test.
 */
export async function recordWire<T>(
  responseBody: unknown,
  call: () => Promise<T>,
): Promise<RecordedRun<T>> {
  const originalFetch = globalThis.fetch;
  const requests: RecordedRequest[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const rawBody = init?.body;
    requests.push({
      url: new URL(String(input)),
      method: init?.method ?? "GET",
      body: typeof rawBody === "string" ? JSON.parse(rawBody) : undefined,
    });
    return new Response(JSON.stringify(responseBody), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;
  try {
    const value = await call();
    return { requests, value };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

/**
 * Run `call` with a fetch that never answers — it throws on the FIRST request.
 *
 * Used to prove a guard fires BEFORE any network I/O. Asserting only that the
 * promise rejects cannot tell a local guard from a server rejection; asserting
 * that zero requests were made can.
 */
export async function expectNoRequest(
  call: () => Promise<unknown>,
): Promise<{ requestCount: number; thrown: unknown }> {
  const originalFetch = globalThis.fetch;
  let requestCount = 0;
  globalThis.fetch = (async () => {
    requestCount += 1;
    return new Response("{}", { status: 200 });
  }) as typeof fetch;
  let thrown: unknown;
  try {
    await call();
  } catch (err: unknown) {
    thrown = err;
  } finally {
    globalThis.fetch = originalFetch;
  }
  return { requestCount, thrown };
}
