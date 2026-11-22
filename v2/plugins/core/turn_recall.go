package core

// turn_recall.go — o recall POR TURNO: do hook UserPromptSubmit até um bloco
// <anhur-memory-turn> no contexto do modelo. Um domínio: "a cada prompt, trazer
// da memória o que casa com ELE e ainda não foi mostrado nesta sessão".
// O estado de dedupe mora em turn_recall_state.go; a renderização do bloco e do
// JSON de saída em turn_recall_render.go. Regra do corte (~300, CLAUDE.md).
//
// Contrato do Claude Code verificado em https://code.claude.com/docs/en/hooks
// (lido em 2026-11-22):
//   - stdin traz session_id, prompt, hook_event_name (+ campos comuns);
//   - exit 0 + JSON {"hookSpecificOutput":{"hookEventName":"UserPromptSubmit",
//     "additionalContext":"..."}} injeta o texto junto ao prompt;
//   - exit 2 BLOQUEIA o prompt — por isso Run sempre sai 0;
//   - timeout/crash = erro não bloqueante, saída descartada, prompt segue;
//   - additionalContext acima de 10.000 caracteres vira arquivo + prévia.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/client"
)

// turnHookInput is the subset of the UserPromptSubmit stdin payload this hook reads.
type turnHookInput struct {
	SessionID     string `json:"session_id"`
	Prompt        string `json:"prompt"`
	HookEventName string `json:"hook_event_name"`
}

// turnRecallLimits bundles every bound the per-turn recall enforces. It is a value,
// not package state, so tests can shrink the budget without touching globals.
type turnRecallLimits struct {
	// budget is the wall-clock cap on the whole search round trip (DNS + TLS + server).
	budget time.Duration
	// searchLimit is the top-k asked of AnhurDB; maxInjected is how many NEW records
	// may reach the block. The gap absorbs records already shown this session.
	searchLimit int
	maxInjected int
	// itemMaxRunes caps one rendered memory line; blockMaxRunes caps the whole block.
	itemMaxRunes  int
	blockMaxRunes int
	// A prompt shorter than minPromptRunes OR with fewer than minPromptWords words is
	// skipped: "ok", "continue", "sim, pode" carry no retrievable intent.
	minPromptRunes int
	minPromptWords int
	// queryMaxRunes truncates the text sent as the search query.
	queryMaxRunes int
}

// defaultTurnRecallLimits returns the production bounds.
//
// Junior Tip [why the budget is 3 s and not 1.5 s, measured 2026-11-22]: from this
// workstation against the hosted API, a fresh-process POST /api/v1/search (top-k 6)
// measured min 1.95 s / p50 2.27 s / p90 2.98 s in the server-default "balanced" mode
// and min 1.62 s / p50 1.96 s / p90 3.35 s in "fast" (n=20 each; GET /health ≈ 0.22 s,
// so the network is not the cost). A 1.5 s cap would have failed open on 40 of 40
// calls — a hook that never injects anything, while still charging 1.5 s per prompt.
// 3 s covers ~p90 of balanced. Balanced (the default, so no mode knob is sent) is
// kept over fast because prompts are natural language and fast drops the semantic
// leg while saving only ~0.3 s at p50. Re-measure before lowering this.
//
// Junior Tip [these are constants, not env vars]: house rule — a new knob is debt and
// needs the owner's ask. If a bound has to move, change it here, with a measurement.
func defaultTurnRecallLimits() turnRecallLimits {
	return turnRecallLimits{
		budget:         3 * time.Second,
		searchLimit:    6,
		maxInjected:    4,
		itemMaxRunes:   280,
		blockMaxRunes:  2000,
		minPromptRunes: 15,
		minPromptWords: 3,
		queryMaxRunes:  500,
	}
}

// cmdTurnRecall is the UserPromptSubmit hook. It searches AnhurDB with the prompt and
// writes a JSON hookSpecificOutput carrying ONLY records this session has not seen.
//
// Junior Tip [fail open, always]: this hook sits in front of EVERY prompt. Any failure
// — unreadable stdin, a short prompt, a slow or down AnhurDB, a corrupt state file —
// ends with nothing on stdout and exit 0 (Run guarantees the exit code). Exit 2 would
// BLOCK the user's prompt, and a memory outage must never cost the user a turn.
func cmdTurnRecall(parentContext context.Context, cfg config, memory *client.Memory,
	hookStdin io.Reader, hookStdout io.Writer, limits turnRecallLimits) {
	startedAt := time.Now()
	var input turnHookInput
	if decodeError := json.NewDecoder(hookStdin).Decode(&input); decodeError != nil {
		logLine(cfg, "turn-recall: unreadable hook input, injecting nothing: "+decodeError.Error())
		return
	}

	queryText, skipReason := shapeTurnQuery(input.Prompt, limits)
	if skipReason != "" {
		logLine(cfg, "turn-recall: skipped ("+skipReason+")")
		return
	}

	// Junior Tip [the deadline is OURS, not the HTTP client's]: the SDK client carries
	// ANHUR_HTTP_TIMEOUT (15 s) for persist, which is far too long to hold a prompt.
	// A child context bounds this one call; cancelling it aborts the in-flight request.
	searchContext, cancelSearch := context.WithTimeout(parentContext, limits.budget)
	defer cancelSearch()
	searchResults, searchError := memory.Search(searchContext, queryText, client.SessionsAll(),
		client.WithLimit(limits.searchLimit))
	searchElapsed := time.Since(startedAt).Round(time.Millisecond)
	if searchError != nil {
		logLine(cfg, fmt.Sprintf("turn-recall: search failed open after %s (budget %s): %v",
			searchElapsed, limits.budget, searchError))
		return
	}

	seenSet := loadTurnSeenSet(cfg, input.SessionID)
	candidates := selectNewTurnRecords(searchResults, seenSet, limits.maxInjected)
	block, renderedCount := renderTurnBlock(cfg.container, candidates, limits)
	if renderedCount == 0 {
		logLine(cfg, fmt.Sprintf("turn-recall: nothing new to inject (hits=%d, elapsed=%s)",
			len(searchResults), searchElapsed))
		return
	}

	hookOutput, marshalError := marshalTurnOutput(block)
	if marshalError != nil {
		logLine(cfg, "turn-recall: cannot encode hook output: "+marshalError.Error())
		return
	}
	if _, writeError := hookStdout.Write(hookOutput); writeError != nil {
		logLine(cfg, "turn-recall: cannot write hook output: "+writeError.Error())
		return
	}

	// Junior Tip [mark seen AFTER writing, never before]: if the state were saved first
	// and the process were then killed (hook timeout), those records would be marked
	// "shown" without ever reaching the model — silently gone for the rest of the
	// session. Writing first means the worst case is showing a record twice. A
	// duplicate costs a few tokens; a loss costs the memory its purpose.
	for _, candidate := range candidates[:renderedCount] {
		seenSet.add(candidate.dedupeKeys...)
	}
	if saveError := saveTurnSeenSet(cfg, input.SessionID, seenSet); saveError != nil {
		logLine(cfg, "turn-recall: dedupe state not saved (records may repeat): "+saveError.Error())
	}
	logLine(cfg, fmt.Sprintf("turn-recall: wrote %d record(s) to stdout (hits=%d, bytes=%d, elapsed=%s)",
		renderedCount, len(searchResults), len(hookOutput), searchElapsed))
}

// shapeTurnQuery turns the raw prompt into a search query, or returns a non-empty
// skipReason when the prompt is not worth a round trip.
//
// Junior Tip [why skip at all]: every skipped prompt saves the user the whole search
// latency. "ok", "go on", "sim" retrieve noise at best, and a bare slash command such
// as "/compact" is an instruction to Claude Code, not a question for the memory.
func shapeTurnQuery(prompt string, limits turnRecallLimits) (queryText string, skipReason string) {
	collapsed := strings.Join(strings.Fields(prompt), " ")
	if collapsed == "" {
		return "", "empty prompt"
	}
	if strings.HasPrefix(collapsed, "/") && !strings.Contains(collapsed, " ") {
		return "", "bare slash command"
	}
	if utf8.RuneCountInString(collapsed) < limits.minPromptRunes {
		return "", "prompt too short"
	}
	if len(strings.Fields(collapsed)) < limits.minPromptWords {
		return "", "too few words"
	}
	// Truncate WITHOUT the ellipsis truncateRunes appends: the query is sent to the
	// search engine, not shown to anyone, and "…" would only be one more token to match.
	if promptRunes := []rune(collapsed); limits.queryMaxRunes > 0 && len(promptRunes) > limits.queryMaxRunes {
		collapsed = string(promptRunes[:limits.queryMaxRunes])
	}
	return collapsed, ""
}
