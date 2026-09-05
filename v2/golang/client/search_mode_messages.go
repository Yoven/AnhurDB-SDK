package client

// search_mode_messages.go — the operator-facing text of the ADR-0031 knobs.
//
// Domain, in one sentence: the exact words the SDK says when a search knob was
// mistyped, ignored, or could not be confirmed.
//
// Junior Tip [why these strings live alone, in a file of their own, 2026-09-05]:
// they are a CROSS-SDK CONTRACT, not prose. The same sentence has to come out of
// the Go SDK (here), the TypeScript SDK (`src/searchModeMessages.ts`) and the
// Python SDK (`anhurdb/client/search_mode_messages.py`), byte for byte, because
// an operator reading a log line or a support ticket must never have to work out
// which SDK wrote it before they can search for the message.
//
// That contract has now broken TWICE in the same shape. The first parity round
// pinned the REQUEST-validation strings (INVALID_PARAM) and left the
// RESPONSE-verification strings one function further down, so SERVER_TOO_OLD and
// the four warnings drifted apart again — different wording AND different
// quoting style. Naming each string, in one file per SDK, is what makes the next
// drift visible in a diff instead of invisible in a log.
//
// Junior Tip [how to change one of these safely]: change it in all THREE files in
// the same commit, and update the three tests that compare with EXACT equality
// (Go `TestServerTooOldMessageIsThePinnedCrossSDKText`, TypeScript
// `search_knobs.test.ts`, Python `test_search_mode.py`). A substring assertion is
// what let these drift the first time — never weaken one to make a change pass.

const (
	// warningPrefix tags every line this SDK writes to the log, so an operator
	// grepping a mixed application log can isolate the SDK's own voice. The
	// TypeScript SDK prints the same prefix through console.warn and the Python
	// SDK carries it inside the warnings.warn message, so the three emitted
	// lines match character for character, not merely in substance.
	warningPrefix = "anhurdb-sdk: warning: "

	// errServerTooOldForSemanticMode is the ONE hard failure of the set:
	// mode="semantic" is a promise ("strict semantics or an error"), and a server
	// that ignored it answered 200 with results that may be purely lexical.
	// Takes one argument: the mode the server echoed back ("" when it echoed
	// none, which is itself the tell).
	errServerTooOldForSemanticMode = `SERVER_TOO_OLD: requested mode="semantic" but the server answered ` +
		`retrieval.mode=%q — this server predates ADR-0031 and IGNORED the mode field, so these ` +
		`results came from the balanced budget and may be purely lexical. Upgrade the server, or ` +
		`drop to mode="balanced" and read retrieval.degraded yourself.`

	// warnModeSharedAllUnconfirmable covers the one scope where absence proves
	// nothing: handler/record_search_shared_all.go builds its RetrievalMeta by
	// hand and deliberately leaves Mode empty, because two merged legs have no
	// single honest mode. A CURRENT server therefore looks exactly like an
	// ancient one here, so throwing would reject healthy servers.
	warnModeSharedAllUnconfirmable = `mode="semantic" cannot be CONFIRMED on scope="shared_all" — the ` +
		`server never echoes retrieval.mode for a two-leg merge, so a server too old for ADR-0031 ` +
		`looks identical to a current one here. Use a single scope to get the strict-semantic ` +
		`guarantee verified.`

	// warnModeIgnored fires for fast/balanced against a server that echoed no
	// mode at all. Takes one argument: the mode that was asked for. It is a
	// warning and not an error because fast/balanced do not promise semantics —
	// the caller gets balanced results, which is a real answer, just not the
	// budget they chose.
	warnModeIgnored = `this AnhurDB server ignored mode=%q (it predates ADR-0031) and ran its own ` +
		`balanced pipeline; the results are balanced results.`

	// warnSemanticTimeoutIgnored takes one argument: the budget that was sent.
	warnSemanticTimeoutIgnored = `this AnhurDB server ignored semantic_timeout_ms=%d (it predates ` +
		`ADR-0031); the server's own semantic budget (700ms) was used instead.`

	// warnDebugSignalsIgnored explains an ABSENCE, which is why it exists at all:
	// without it, empty Signals and empty LegScores read as "nothing matched"
	// when the truth is "the server never understood the request".
	warnDebugSignalsIgnored = `this AnhurDB server ignored debug_signals (it predates ADR-0031); ` +
		`per-hit signals and leg_scores are absent, not empty.`

	// warnLegScoresDroppedByRetrievalForm is Go-only, and it exists because Go
	// alone cannot widen a return type without breaking every caller.
	// SearchWithRetrieval returns ([]SearchResult, *RetrievalMeta, error) — a
	// tuple with no room for leg_scores — while TypeScript searchWithRetrieval
	// and Python search_with_retrieval return ONE envelope that carries all
	// three. Takes one argument: how many leg summaries are being dropped, so
	// the line only appears when something real was actually lost.
	warnLegScoresDroppedByRetrievalForm = `debug_signals was set and the server returned %d leg_scores ` +
		`entries, but SearchWithRetrieval's ([]SearchResult, *RetrievalMeta, error) tuple has no ` +
		`room for them. Call SearchWithSignals instead — it returns the same single envelope that ` +
		`TypeScript searchWithRetrieval and Python search_with_retrieval return.`
)
