/**
 * AnhurDB TypeScript SDK — the ADR-0031 search-budget knob and the
 * cross-VERSION guard that keeps it from lying.
 *
 * Domain, in one sentence: which retrieval budget did the caller ask for, and
 * did the server actually honour it?
 *
 * Junior Tip [why this left searchRequest.ts, 2026-09-05]: searchRequest.ts had
 * reached 348 lines against this project's ~300-line cut. The seam is not
 * arbitrary — it restores the shape the other two SDKs already have
 * (`client/search_mode.go`, `anhurdb/client/search_mode.py`), so a reader
 * looking for "how does mode work here?" finds the same filename in all three.
 * What is left in searchRequest.ts is assembly and parsing: it builds a body
 * and reads a response. What lives here is a PROMISE and its verification,
 * which is a different sentence.
 *
 * Junior Tip [why the validators and the verifier share one file]: they are two
 * halves of one contract. `validateSearchMode` refuses a mode the caller
 * mistyped on the way OUT; `checkSearchKnobsHonored` refuses a mode the server
 * ignored on the way BACK. Splitting them would let one half be updated — a new
 * mode, a new knob — while the other silently kept the old vocabulary, and a
 * mode the SDK accepts but never verifies is exactly the silent degradation
 * ADR-0031 exists to make impossible.
 */

import { AnhurError } from "./types.js";
import type { SearchMode, SearchOptions, SearchScope } from "./searchTypes.js";
import type { RetrievalMeta } from "./searchResults.js";
import {
  WARNING_PREFIX,
  WARN_DEBUG_SIGNALS_IGNORED,
  WARN_MODE_SHARED_ALL_UNCONFIRMABLE,
  serverTooOldForSemanticModeMessage,
  warnModeIgnoredMessage,
  warnSemanticTimeoutIgnoredMessage,
} from "./searchModeMessages.js";

/**
 * Every mode the server understands (ADR-0012 / ADR-0031), in the order a
 * human reads them: cheapest first.
 *
 * Mirrors `handler.SearchModeFast/Balanced/Semantic`. Kept as a runtime array
 * — not only a TypeScript union — because the validation below has to reject
 * a value that arrived from untyped JavaScript, where the union is gone.
 */
export const SEARCH_MODES: readonly SearchMode[] = ["fast", "balanced", "semantic"];

/**
 * Reject an unknown search mode CLIENT-SIDE instead of letting the server
 * silently reinterpret it.
 *
 * Junior Tip [why failing here is not pedantry]: `handler.normalizeSearchMode`
 * folds ANY unrecognised string into `balanced` — deliberately, so that gRPC
 * and REST cannot disagree about a typo. Correct for the server, disastrous
 * for a caller: `mode: "semanitc"` would come back 200 with lexical hits and
 * `retrieval.mode === "balanced"`, which is indistinguishable from an old
 * server ignoring the field. The SDK is the only layer that still knows the
 * caller wrote a typo, so the SDK is where it has to be caught.
 *
 * @param requestedMode - The raw value the caller put in `options.mode`.
 * @returns The same value, once proven to be a real mode.
 * @throws {AnhurError} `INVALID_PARAM: ...` on anything else.
 */
export function validateSearchMode(requestedMode: string): SearchMode | "" {
  // A non-string can only arrive from untyped JavaScript, and it is a caller bug
  // exactly like a typo is — Python refuses it for the same reason. Refuse it
  // here rather than let `.trim()` throw a TypeError nobody can act on.
  if (typeof requestedMode !== "string") {
    throw unsupportedSearchModeError(requestedMode);
  }
  const normalizedMode = normalizeSearchMode(requestedMode);
  if (normalizedMode === "") {
    return "";
  }
  if (!SEARCH_MODES.includes(normalizedMode as SearchMode)) {
    throw unsupportedSearchModeError(requestedMode);
  }
  return normalizedMode as SearchMode;
}

/**
 * Build the ONE cross-SDK message for a mode this SDK refuses.
 *
 * Junior Tip [why the offending value is echoed UN-normalised]: a caller who
 * typed `"SEMANITC"` must see `"SEMANITC"` back. "must be one of: ..." only
 * repeats the rule they already read in the docs; echoing the value names the
 * typo they actually made, which is the only new information available. Go and
 * Python echo the raw value for the same reason.
 */
function unsupportedSearchModeError(offendingValue: unknown): AnhurError {
  return new AnhurError(
    `INVALID_PARAM: 'mode' "${String(offendingValue)}" is not supported; use "fast", "balanced" or "semantic"`,
    undefined,
    "invalid_request",
  );
}

/**
 * Fold the caller's spelling of `mode` into the exact form the server compares
 * against: surrounding whitespace trimmed, ASCII lowered.
 *
 * Junior Tip [why leniency here and not strictness elsewhere, 2026-09-05]: the
 * three SDKs disagreed on this for one release — Python normalised, so
 * `mode: "SEMANTIC"` and `" semantic "` worked; Go and TypeScript rejected both
 * with `INVALID_PARAM`. One knob, three behaviours, is exactly what the parity
 * round exists to kill, and the direction of the fix is not symmetric: making
 * Go and TypeScript lenient only ACCEPTS MORE, so no caller that works today
 * can break, while tightening Python would break every caller already passing
 * `"Semantic"`. Leniency wins on that argument alone.
 *
 * Junior Tip [why the normalised value is what goes on the WIRE]: the SDK reads
 * `retrieval.mode` back and compares it to what it asked for
 * ({@link checkSearchKnobsHonored}). If the request carried `"SEMANTIC"` and
 * the server echoed `"semantic"`, that comparison would fail against a
 * perfectly current server and the SDK would throw `SERVER_TOO_OLD` at a server
 * that did exactly what it was told. Normalise once, before the request leaves,
 * and both sides of the comparison speak the same alphabet.
 *
 * A non-string folds to `""` instead of crashing on `.trim()`, so this helper is
 * safe to call on the response path too, where the value has already been
 * validated and a throw would be the wrong reaction.
 */
export function normalizeSearchMode(requestedMode: unknown): string {
  if (typeof requestedMode !== "string") {
    return "";
  }
  return requestedMode.trim().toLowerCase();
}

/**
 * Reject a NEGATIVE per-request semantic budget CLIENT-SIDE.
 *
 * Junior Tip [why 0 is legal and -1 is not]: `0` is the SENTINEL for "I never
 * set this knob" — the payload builder omits the key entirely and the server
 * applies its own 700ms budget. A negative number is neither a sentinel nor a
 * budget; it is a caller bug, usually an unchecked subtraction of two
 * deadlines. Until 2026-09-05 both this SDK and the Go SDK let it fall through
 * the omit-unless-set gate and DISAPPEAR without a word, so the caller believed
 * they had capped the embedder while they had in fact restored the default.
 * Python already refused it. The message and the error kind below are
 * byte-identical to the other two SDKs on purpose: an operator reading a log
 * line should never have to know which SDK wrote it.
 *
 * Also rejects a non-integer (`1.5`, `NaN`, `Infinity`): the field is an int32
 * on the wire, and untyped JavaScript is where a float actually arrives.
 *
 * @param requestedSemanticTimeoutMs - The raw value from `options.semanticTimeoutMs`.
 * @returns The same value, once proven to be a legal budget.
 * @throws {AnhurError} `INVALID_PARAM: ...` on a negative or non-integer value.
 */
export function validateSemanticTimeoutMs(requestedSemanticTimeoutMs: number): number {
  if (!Number.isInteger(requestedSemanticTimeoutMs) || requestedSemanticTimeoutMs < 0) {
    throw new AnhurError(
      "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0",
      undefined,
      "invalid_request",
    );
  }
  return requestedSemanticTimeoutMs;
}

/**
 * Knobs already warned about in this process, so a search loop against an old
 * server produces one line, not one line per query.
 *
 * Junior Tip [why a module-level set and not a config knob]: ADR-0031 is
 * explicit that this detection may not become an environment variable or a
 * constructor option — it is derived from a response the server already
 * sends, and adding a switch would let an operator turn the warning off and
 * be back to a degradation nobody can observe. Deduplicating is the most
 * silencing the SDK is allowed to do.
 */
const alreadyWarnedKnobs = new Set<string>();

/** Test seam: forget which warnings were already printed. Not public API. */
export function resetSearchKnobWarnings(): void {
  alreadyWarnedKnobs.clear();
}

function warnOnce(knobName: string, message: string): void {
  if (alreadyWarnedKnobs.has(knobName)) return;
  alreadyWarnedKnobs.add(knobName);
  console.warn(`${WARNING_PREFIX}${message}`);
}

/**
 * Decide whether the server honoured the ADR-0031 knobs, from the RESPONSE.
 *
 * Junior Tip [an additive proto field is safe for the PARSER, not for the
 * MEANING]: `mode`, `semantic_timeout_ms` and `debug_signals` were added as
 * new proto3 / JSON fields. An AnhurDB that predates ADR-0031 Stage 2 does
 * not error on them — it drops them into unknown fields and runs `balanced`
 * with its own 700 ms budget, then answers **200** with lexical results. A
 * caller that asked for strict semantics is now holding degraded results and
 * a success code. The request cannot detect this; only the response can.
 *
 * The detector is `retrieval.mode`: a Stage-2 server ALWAYS fills it, because
 * `normalizeSearchMode` always resolves to one of the three. Missing block or
 * mismatching mode ⇒ the field was ignored.
 *
 * The reaction is graded on purpose:
 * - `mode: "semantic"` is a PROMISE ("semantics or an error") — breaking it
 *   changes the result set while still returning 200, so the SDK throws.
 * - `semanticTimeoutMs` and `debugSignals` degrade without lying about which
 *   records came back, so a warning is the honest ceiling.
 *
 * Junior Tip [why `scope=shared_all` is exempt from the throw — a real server
 * behaviour, not a soft spot]: `handler/record_search_shared_all.go` builds
 * that scope's `RetrievalMeta` BY HAND and deliberately leaves `Mode` empty,
 * because two merged legs have no single honest mode (only the resolved
 * weights and the shared degrade verdict are echoed). So a perfectly CURRENT
 * server answers `retrieval.mode === ""` for every `shared_all` query, which
 * is byte-identical to an ancient server ignoring the field. Throwing there
 * would reject healthy servers on every `searchShared`; the mode IS honoured
 * server-side (ADR-0031 Stage 2, §4 of the amendment), it simply is not
 * echoed. We warn and say how to get the guarantee verified. The Go SDK
 * carries the same exception, for the same measured reason.
 *
 * @param options        - The options the caller passed to this very request.
 * @param retrieval      - The `retrieval` block, `undefined` when absent.
 * @param requestedScope - The scope actually put on the wire.
 * @throws {AnhurError} when `mode: "semantic"` was asked and not honoured.
 */
export function checkSearchKnobsHonored(
  options: SearchOptions | undefined,
  retrieval: RetrievalMeta | undefined,
  requestedScope: SearchScope,
): void {
  if (options === undefined) return;
  const serverMode = retrieval?.mode ?? "";
  // A server that answers with a mode is a server that read the field.
  const serverUnderstandsModes = serverMode !== "";
  // Compare the NORMALISED request against the server's echo. The request was
  // already normalised on the way out, so `mode: "SEMANTIC"` must not read here
  // as a different mode from the `"semantic"` the server sends back — that
  // mismatch would throw SERVER_TOO_OLD at a perfectly current server.
  const requestedMode = normalizeSearchMode(options.mode);

  if (requestedMode === "semantic" && serverMode !== "semantic") {
    if (requestedScope === "shared_all") {
      warnOnce("mode_shared_all", WARN_MODE_SHARED_ALL_UNCONFIRMABLE);
      return;
    }
    throw new AnhurError(
      serverTooOldForSemanticModeMessage(serverMode),
      undefined,
      "invalid_request",
    );
  }

  // shared_all can never carry the detector — a CURRENT server answers with an
  // empty mode there — so absence proves nothing and the soft knobs cannot be
  // judged either. Go and Python both stop here for the same measured reason;
  // warning anyway would train operators to ignore the line.
  if (requestedScope === "shared_all") return;
  if (serverUnderstandsModes) return;

  if (requestedMode !== "") {
    warnOnce("mode", warnModeIgnoredMessage(requestedMode));
  }
  if (options.semanticTimeoutMs !== undefined && options.semanticTimeoutMs > 0) {
    warnOnce("semantic_timeout_ms", warnSemanticTimeoutIgnoredMessage(options.semanticTimeoutMs));
  }
  if (options.debugSignals) {
    warnOnce("debug_signals", WARN_DEBUG_SIGNALS_IGNORED);
  }
}
