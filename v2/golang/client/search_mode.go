package client

// search_mode.go — the ADR-0031 search-budget knob and the cross-VERSION guard
// that keeps it from lying.
//
// Domain: everything about `mode` — the three legal values, the client-side
// validation that rejects a fourth, and the response-side detector that proves
// the server actually honoured what we asked for.

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

// The three retrieval budgets the server accepts (ADR-0012, carried onto the
// wire by ADR-0031 Stage 2). They mirror server handler.SearchMode* exactly.
const (
	// SearchModeFast skips the query embedding entirely — the NOW legs (FTS5 +
	// SimHash) answer alone. Cheapest and never waits on the embedder.
	SearchModeFast = "fast"
	// SearchModeBalanced embeds under a budget and degrades to the NOW legs when
	// the embedder is slow or down. This is the server default for an empty mode.
	SearchModeBalanced = "balanced"
	// SearchModeSemantic promises strict semantics: if the embedding cannot be
	// resolved the server answers 503/504 instead of quietly returning lexical
	// results. "Semantics or nothing."
	SearchModeSemantic = "semantic"
)

// validateSearchMode rejects any value outside the three legal budgets.
//
// Junior Tip [why the SDK is STRICTER than the server here, 2026-09-05]: the
// server's normalizeSearchMode (handler/search_mode.go) lowercases the value
// and falls back to "balanced" for anything it does not recognise — deliberately,
// so gRPC and REST can never disagree on an unknown mode. That fallback is right
// for a SERVER (two ports, one behaviour) and wrong for an SDK: a caller who
// typed WithSearchMode("semantik") wants to hear about the typo, not to silently
// receive balanced results while believing they asked for strict semantics.
// Failing here costs one error; failing at the server costs a wrong answer
// nobody can observe. Python validates its own mode enums the same way.
func validateSearchMode(requestedMode string) (string, error) {
	normalizedMode := normalizeSearchMode(requestedMode)
	switch normalizedMode {
	case "", SearchModeFast, SearchModeBalanced, SearchModeSemantic:
		return normalizedMode, nil
	default:
		// Echo what the CALLER typed, never the normalised form: a caller who
		// wrote "SEMANITC" must see "SEMANITC" back, or the error names a string
		// they never wrote and the typo becomes harder to find, not easier.
		return "", newValidationError(
			"INVALID_PARAM: 'mode' %q is not supported; use %q, %q or %q",
			requestedMode, SearchModeFast, SearchModeBalanced, SearchModeSemantic)
	}
}

// normalizeSearchMode folds the caller's spelling of `mode` into the exact form
// the server compares against: surrounding whitespace trimmed, ASCII lowered.
//
// Junior Tip [why leniency here and not strictness elsewhere, 2026-09-05]: the
// three SDKs disagreed on this for one release — Python normalised, so
// mode="SEMANTIC" and " semantic " worked; Go and TypeScript rejected both with
// INVALID_PARAM. One knob, three behaviours, is exactly what the parity round
// exists to kill, and the direction of the fix is not symmetric: making Go and
// TypeScript lenient only ACCEPTS MORE, so no caller that works today can
// break, while tightening Python would break every caller already passing
// "Semantic". Leniency wins on that argument alone.
//
// Junior Tip [why the normalised value is what goes on the WIRE]: the SDK reads
// retrieval.mode back and compares it to what it asked for
// (verifySearchKnobsHonoured). If the request carried "SEMANTIC" and the server
// echoed "semantic", that comparison would fail against a perfectly current
// server and the SDK would throw SERVER_TOO_OLD at a server that did exactly
// what it was told. Normalise once, before the request leaves, and both sides
// of the comparison speak the same alphabet.
func normalizeSearchMode(requestedMode string) string {
	return strings.ToLower(strings.TrimSpace(requestedMode))
}

// validateSemanticTimeoutMs rejects a NEGATIVE per-request semantic budget.
//
// The message is byte-identical to the Python and TypeScript SDKs, and the
// error is the same kind an unknown mode produces (*APIError, StatusCode 400,
// clientSide, so Kind() is KindInvalidRequest and Retryable() is false).
//
// Junior Tip [why 0 is legal and -1 is not, 2026-09-05]: 0 is the SENTINEL for
// "I never set this knob" — buildSearchPayload omits the key entirely and the
// server applies its own 700ms budget. A negative number is not a sentinel and
// not a budget; it is a caller bug (an unchecked subtraction of two deadlines
// is how it usually arrives). Before this check, Go and TypeScript let it fall
// through the `> 0` omit-gate and vanish WITHOUT a word, so the caller believed
// they had capped the embedder and had in fact restored the default. Python
// already refused it. Silent divergence between SDKs on the same knob is the
// exact defect ADR-0031 parity exists to remove, so all three now refuse it.
func validateSemanticTimeoutMs(requestedSemanticTimeoutMs int) error {
	if requestedSemanticTimeoutMs < 0 {
		return newValidationError(
			"INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0")
	}
	return nil
}

// sdkWarningLogger is the ONE writer for every warning this SDK emits.
//
// Junior Tip [why not the package-level log.Printf, 2026-09-06]: the standard
// logger carries LstdFlags, so the line came out as
// "2026/09/06 00:01:18 anhurdb-sdk: warning: ..." while the TypeScript SDK
// prints the same sentence through console.warn with NO prefix at all. The
// warning TEXT is a cross-SDK contract (search_mode_messages.go) pinned by
// exact-equality tests in all three SDKs — but a timestamp glued to the front
// of it is what an operator actually greps, so the "byte for byte" promise was
// false in the only place it can be observed. A dedicated logger also stops the
// SDK from inheriting whatever flags, prefix or output the HOST application set
// on the standard logger, which would make the line differ per application.
var sdkWarningLogger = log.New(os.Stderr, "", 0)

// alreadyWarnedKnobs remembers which knobs this PROCESS has already complained
// about, guarded by its own mutex because a Memory is safe to share across
// goroutines and two concurrent searches would otherwise race on the map.
//
// Junior Tip [why dedup, and why it may never become a switch]: the probe that
// motivated this ran three identical searches against one pre-ADR-0031 server
// and counted lines — Go printed 3, TypeScript 1, Python 1. One knob, three
// cadences, is the same divergence class the parity round exists to remove, and
// an operator running a search loop against an old server got one line per
// query until the real signal drowned. ADR-0031 is explicit that this detection
// may not become an environment variable or a constructor option: it is derived
// from a response the server already sends, and a switch would let someone turn
// it off and be back to a degradation nobody can observe. Printing once per
// process is the most silencing this SDK is allowed to do.
//
// Junior Tip [why the key is the knob NAME and never its value]: TypeScript
// keys its `alreadyWarnedKnobs` set on "mode" / "semantic_timeout_ms" /
// "debug_signals" / "mode_shared_all", so a loop that varies the budget still
// warns once. Keying on the value here would print a fresh line for every
// distinct semantic_timeout_ms, and the three SDKs would once again warn with
// three different cadences from the same input.
var (
	alreadyWarnedKnobsMutex sync.Mutex
	alreadyWarnedKnobs      = map[string]struct{}{}
)

// warnKnobOnce writes one warning line the FIRST time a knob is reported
// ignored in this process, and stays silent for that knob afterwards.
//
// formattedMessage arrives already formatted so this helper never treats a
// caller string as a format string — a message containing a stray %s would
// otherwise print "%!s(MISSING)" into a contract-pinned sentence.
func warnKnobOnce(knobName, formattedMessage string) {
	alreadyWarnedKnobsMutex.Lock()
	_, warnedBefore := alreadyWarnedKnobs[knobName]
	if !warnedBefore {
		alreadyWarnedKnobs[knobName] = struct{}{}
	}
	alreadyWarnedKnobsMutex.Unlock()
	if warnedBefore {
		return
	}
	sdkWarningLogger.Print(warningPrefix + formattedMessage)
}

// resetSearchKnobWarnings forgets which warnings were already printed.
//
// Test seam only — the mirror of TypeScript's resetSearchKnobWarnings. It is
// unexported on purpose: a caller able to clear the ledger could re-enable the
// per-query spam this dedup exists to remove. Any test that ASSERTS on warning
// output must call it first, or an earlier test in the same binary may have
// already consumed the one line it is waiting for.
func resetSearchKnobWarnings() {
	alreadyWarnedKnobsMutex.Lock()
	defer alreadyWarnedKnobsMutex.Unlock()
	alreadyWarnedKnobs = map[string]struct{}{}
}

// verifySearchKnobsHonoured is the cross-VERSION guard demanded by ADR-0031's
// 2026-09-05 amendment ("Restrição de VERSÃO CRUZADA").
//
// Junior Tip [an additive proto field is safe for the PARSER, not for the
// PROMISE]: `mode`, `semantic_timeout_ms` and `debug_signals` were added as new
// fields. A server that predates them does not reject them — it drops them into
// unknownFields and answers 200. So a caller who asked for strict semantics
// against an old server gets lexical results, no error, and no way to tell.
// The honest detector is the RESPONSE, never the request: a current server
// always resolves `retrieval.mode` to one of the three budgets, so an empty or
// mismatched mode coming back means the field was ignored on the way in.
//
// Severity follows the ADR: mode=semantic FAILS LOUD (the promise "semantics or
// nothing" was broken and the result set is a lie), while the other two only
// WARN (they degrade without misrepresenting which records matched).
//
// requestedScope is required because of a real exception measured in the server:
// for scope=shared_all the REST handler builds its RetrievalMeta by hand
// (handler/record_search_shared_all.go) and deliberately leaves Mode empty —
// two legs, no single honest mode. A current server therefore returns mode=""
// for shared_all, which is indistinguishable from an old server ignoring the
// field. Failing loud there would reject a perfectly healthy modern server on
// every shared_all query, so that one case warns instead.
func verifySearchKnobsHonoured(cfg *searchConfig, retrieval *RetrievalMeta, requestedScope string) error {
	knobsAsked := cfg.searchMode != "" || cfg.semanticTimeoutMs > 0 || cfg.debugSignals
	if !knobsAsked {
		return nil
	}

	serverEchoedMode := ""
	if retrieval != nil {
		serverEchoedMode = retrieval.Mode
	}

	if cfg.searchMode == SearchModeSemantic {
		if requestedScope == searchScopeSharedAll {
			warnKnobOnce("mode_shared_all", warnModeSharedAllUnconfirmable)
			return nil
		}
		if serverEchoedMode != SearchModeSemantic {
			return newValidationError(errServerTooOldForSemanticMode, serverEchoedMode)
		}
		return nil
	}

	// fast / balanced / unset: only the soft knobs are left to check, and they
	// are only unverifiable when the server echoed no mode at all.
	if serverEchoedMode != "" || requestedScope == searchScopeSharedAll {
		return nil
	}
	if cfg.searchMode != "" {
		warnKnobOnce("mode", fmt.Sprintf(warnModeIgnored, cfg.searchMode))
	}
	if cfg.semanticTimeoutMs > 0 {
		warnKnobOnce("semantic_timeout_ms", fmt.Sprintf(warnSemanticTimeoutIgnored, cfg.semanticTimeoutMs))
	}
	if cfg.debugSignals {
		warnKnobOnce("debug_signals", warnDebugSignalsIgnored)
	}
	return nil
}
