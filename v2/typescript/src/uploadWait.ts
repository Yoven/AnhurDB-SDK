/**
 * AnhurDB TypeScript SDK — the upload-wait STATE MACHINE.
 *
 * One responsibility: given a way to read an upload's status, block until that
 * upload reaches a terminal state, a timeout, or a real error. It lives outside
 * `memory.ts` because it is the only piece of that file with non-trivial
 * temporal logic — three clocks (poll interval, read-your-writes grace, overall
 * deadline) whose interaction is what breaks — and because `memory.ts` is far
 * past the house cut and must not grow to hold it.
 *
 * The status reader is injected rather than imported so the machine can be
 * exercised on its own; the transport contract it depends on is exactly one
 * thing — that a missing upload arrives as an `AnhurError` carrying
 * `statusCode === 404`.
 */

import { AnhurError, AnhurUploadWaitTimeout } from "./types.js";
import type { UploadStatusResult } from "./types.js";

/** Timings of {@link pollUploadUntilTerminal}. All optional. */
export interface UploadWaitOptions {
  /** Give up after this long. Default 120s. */
  timeoutMs?: number;
  /** Sleep between polls. Default 5s. */
  intervalMs?: number;
  /** How long an HTTP 404 still counts as "not visible yet". Default 30s. */
  notFoundGraceMs?: number;
}

/** Statuses the server uses to say "this upload will not change again". */
const TERMINAL_STATUSES = ["completed", "saved", "done", "failed"];

/**
 * Poll `readStatus` until the upload reaches a terminal state.
 *
 * Junior Tip [por que 404 vira "pendente" no começo — medido 2026-08-07]: as
 * leituras do AnhurDB são load-balanced; logo após o POST de upload um follower
 * que ainda não aplicou a entrada devolve 404 legítimo por alguns segundos
 * (read-your-writes). Dentro de `notFoundGraceMs` o 404 é espera; DEPOIS dela
 * ele re-lança — um id inválido não pode virar espera infinita (falhar alto,
 * nunca engolir).
 *
 * Junior Tip [this grace depends on `statusCode`, not on the message]: the test
 * below is `thrown.statusCode === 404`. An error built without its status
 * classifies as `"transport"` and never matches, so the whole grace window
 * silently stops existing and a merely-not-yet-replicated upload is reported as
 * a hard failure. That is why `clientResponse.ts` refuses to construct an HTTP
 * error without its status — this function is the consumer that pays for it.
 *
 * Returns the final payload — INCLUDING `status="failed"` (a failed ingest is
 * terminal data the caller must inspect, not a transport error). Throws the
 * original `AnhurQueryError` when the 404 persists beyond the grace window, and
 * `AnhurUploadWaitTimeout` when no terminal status arrives in time.
 *
 * @param readStatus - Reads the current status (one HTTP GET per call).
 * @param uploadId   - Only used to name the upload in the timeout message.
 * @param options    - Timings; see {@link UploadWaitOptions}.
 */
export async function pollUploadUntilTerminal(
  readStatus: () => Promise<UploadStatusResult>,
  uploadId: number,
  options?: UploadWaitOptions,
): Promise<UploadStatusResult> {
  const timeoutMs = options?.timeoutMs ?? 120_000;
  const intervalMs = options?.intervalMs ?? 5_000;
  const notFoundGraceMs = options?.notFoundGraceMs ?? 30_000;

  const startedAt = Date.now();
  let lastStatus = "never-seen";
  for (;;) {
    try {
      const statusPayload = await readStatus();
      const statusText = String(statusPayload?.status ?? "").toLowerCase();
      // Junior Tip [terminal state comes from `status`, and only from it]: this
      // also tested `Boolean(statusPayload.error)`, a field the handler NEVER
      // emits (see UploadStatusResult). Dead code that reads like a safety net
      // is worse than no net — it invites the next reader to report failures
      // through a channel nobody is listening on.
      if (
        statusPayload?.completed === true ||
        TERMINAL_STATUSES.includes(statusText)
      ) {
        return statusPayload;
      }
      if (statusText) lastStatus = statusText;
    } catch (thrown) {
      const isTransientNotFound =
        thrown instanceof AnhurError &&
        thrown.statusCode === 404 &&
        Date.now() - startedAt < notFoundGraceMs;
      if (!isTransientNotFound) throw thrown;
      lastStatus = "not-found-yet";
    }

    if (Date.now() - startedAt + intervalMs > timeoutMs) {
      throw new AnhurUploadWaitTimeout(
        `upload ${uploadId} not terminal after ${timeoutMs}ms (last=${lastStatus})`,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}
