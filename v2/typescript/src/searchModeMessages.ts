/**
 * The operator-facing text of the ADR-0031 search knobs.
 *
 * Domain, in one sentence: the exact words the SDK says when a search knob was
 * mistyped, ignored, or could not be confirmed.
 *
 * Junior Tip [why these strings live alone, in a module of their own,
 * 2026-09-05]: they are a CROSS-SDK CONTRACT, not prose. The same sentence has
 * to come out of the Go SDK (`client/search_mode_messages.go`), this SDK, and
 * the Python SDK (`anhurdb/client/search_mode_messages.py`), byte for byte,
 * because an operator reading a log line or a support ticket must never have to
 * work out which SDK wrote it before they can search for the message.
 *
 * That contract has now broken TWICE in the same shape. The first parity round
 * pinned the REQUEST-validation strings (`INVALID_PARAM`) and left the
 * RESPONSE-verification strings one function further down, so `SERVER_TOO_OLD`
 * and the warnings drifted apart again — different wording AND different quoting
 * style. Naming each string, in one module per SDK, is what makes the next drift
 * visible in a diff instead of invisible in a log.
 *
 * Junior Tip [how to change one of these safely]: change it in all THREE files
 * in the same commit, and update the three tests that compare with EXACT
 * equality (Go `TestServerTooOldMessageIsThePinnedCrossSDKText`, this SDK's
 * `search_knobs.test.ts`, Python's `test_search_mode.py`). A substring assertion
 * is what let these drift the first time — never weaken one to make a change
 * pass.
 */

/**
 * Tags every line this SDK writes to the console, so an operator grepping a
 * mixed application log can isolate the SDK's own voice. Go prints the same
 * prefix through `log.Printf` and Python carries it inside the `warnings.warn`
 * message, so the three emitted lines match character for character.
 */
export const WARNING_PREFIX = "anhurdb-sdk: warning: ";

/**
 * The ONE hard failure of the set: `mode: "semantic"` is a promise ("strict
 * semantics or an error"), and a server that ignored it answered 200 with
 * results that may be purely lexical.
 *
 * @param servedMode - The mode the server echoed back; `""` when it echoed
 *   none, which is itself the tell that it predates ADR-0031.
 */
export function serverTooOldForSemanticModeMessage(servedMode: string): string {
  return (
    `SERVER_TOO_OLD: requested mode="semantic" but the server answered ` +
    `retrieval.mode="${servedMode}" — this server predates ADR-0031 and IGNORED the mode field, so these ` +
    `results came from the balanced budget and may be purely lexical. Upgrade the server, or ` +
    `drop to mode="balanced" and read retrieval.degraded yourself.`
  );
}

/**
 * The one scope where absence proves nothing:
 * `handler/record_search_shared_all.go` builds its `RetrievalMeta` by hand and
 * deliberately leaves `Mode` empty, because two merged legs have no single
 * honest mode. A CURRENT server therefore looks exactly like an ancient one
 * here, so throwing would reject healthy servers.
 */
export const WARN_MODE_SHARED_ALL_UNCONFIRMABLE =
  `mode="semantic" cannot be CONFIRMED on scope="shared_all" — the ` +
  `server never echoes retrieval.mode for a two-leg merge, so a server too old for ADR-0031 ` +
  `looks identical to a current one here. Use a single scope to get the strict-semantic ` +
  `guarantee verified.`;

/**
 * Fires for `fast`/`balanced` against a server that echoed no mode at all. It
 * is a warning and not an error because those two do not promise semantics —
 * the caller gets balanced results, which is a real answer, just not the budget
 * they chose.
 *
 * @param requestedMode - The mode that was asked for, already normalised.
 */
export function warnModeIgnoredMessage(requestedMode: string): string {
  return (
    `this AnhurDB server ignored mode="${requestedMode}" (it predates ADR-0031) and ran its own ` +
    `balanced pipeline; the results are balanced results.`
  );
}

/**
 * @param semanticTimeoutMs - The budget that was put on the wire.
 */
export function warnSemanticTimeoutIgnoredMessage(semanticTimeoutMs: number): string {
  return (
    `this AnhurDB server ignored semantic_timeout_ms=${semanticTimeoutMs} (it predates ` +
    `ADR-0031); the server's own semantic budget (700ms) was used instead.`
  );
}

/**
 * Explains an ABSENCE, which is why it exists at all: without it, empty
 * `signals` and empty `legScores` read as "nothing matched" when the truth is
 * "the server never understood the request".
 */
export const WARN_DEBUG_SIGNALS_IGNORED =
  `this AnhurDB server ignored debug_signals (it predates ADR-0031); ` +
  `per-hit signals and leg_scores are absent, not empty.`;
