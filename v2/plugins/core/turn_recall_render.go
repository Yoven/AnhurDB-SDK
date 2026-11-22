package core

// turn_recall_render.go — a VOZ do recall por turno: o bloco <anhur-memory-turn>
// e o envelope JSON que o Claude Code lê do stdout. Um domínio: "como o que foi
// lembrado neste turno chega ao modelo, e em quanto espaço". Regra do corte.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// turnHookOutput is the documented UserPromptSubmit JSON output (hooks reference,
// "UserPromptSubmit decision control"). Only additionalContext is set: the hook never
// blocks, so decision/reason are deliberately absent.
type turnHookOutput struct {
	HookSpecificOutput turnHookSpecificOutput `json:"hookSpecificOutput"`
}

// turnHookSpecificOutput carries the event name (required by Claude Code for any
// hookSpecificOutput) and the context string it wraps in a system reminder.
type turnHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// marshalTurnOutput encodes the block as the ONE JSON object stdout may hold.
//
// Junior Tip [nothing else on stdout]: Claude Code parses stdout as JSON only when it
// is a single object with nothing around it; one stray line turns the whole output
// into a non-blocking error and the context is lost. That is why the hook command in
// hooks.json sends the env-file sourcing's stdout to /dev/null.
//
// SetEscapeHTML(false) keeps "<anhur-memory-turn>" literal instead of "\u003c...":
// both decode to the same string, but the literal form is greppable in a debug log.
func marshalTurnOutput(block string) ([]byte, error) {
	var outputBuffer bytes.Buffer
	encoder := json.NewEncoder(&outputBuffer) // Encode appends the trailing newline
	encoder.SetEscapeHTML(false)
	encodeError := encoder.Encode(turnHookOutput{
		HookSpecificOutput: turnHookSpecificOutput{
			HookEventName:     "UserPromptSubmit",
			AdditionalContext: block,
		},
	})
	if encodeError != nil {
		return nil, encodeError
	}
	return outputBuffer.Bytes(), nil
}

// renderTurnBlock renders candidates into the <anhur-memory-turn> block, adding lines in
// rank order until the next one would cross blockMaxRunes. renderedCount says how many
// candidates made it in; only those may be marked as seen.
//
// Junior Tip [why the cap is far below Claude Code's 10,000]: above 10,000 characters
// Claude Code swaps the text for a file path plus a 2,000-character preview the model
// is never asked to open. But the real budget is the user's context: this block rides
// on EVERY qualifying prompt, so 2,000 runes (~500 tokens) per turn is already a
// noticeable tax over a long session. Records cut by the cap stay unmarked and can
// surface on a later prompt.
//
// Junior Tip [factual phrasing, not commands]: the hooks reference warns that text
// framed as out-of-band system instructions can trip the model's prompt-injection
// defences, which then surfaces it to the user instead of using it. The header states
// what the lines are; it does not order the model around.
func renderTurnBlock(container string, candidates []turnCandidate, limits turnRecallLimits) (block string, renderedCount int) {
	if len(candidates) == 0 {
		return "", 0
	}
	header := `<anhur-memory-turn backend="AnhurDB" container="` + container + `">` + "\n" +
		"Long-term memories from AnhurDB that match this prompt and were not shown earlier in this session. " +
		"When two of them disagree, the more recent date is the current one.\n"
	footer := "</anhur-memory-turn>"

	var builder strings.Builder
	builder.WriteString(header)
	usedRunes := utf8.RuneCountInString(header) + utf8.RuneCountInString(footer)
	for _, candidate := range candidates {
		line := renderTurnLine(candidate, limits.itemMaxRunes)
		lineRunes := utf8.RuneCountInString(line)
		if usedRunes+lineRunes > limits.blockMaxRunes {
			break
		}
		builder.WriteString(line)
		usedRunes += lineRunes
		renderedCount++
	}
	if renderedCount == 0 {
		return "", 0
	}
	builder.WriteString(footer)
	return builder.String(), renderedCount
}

// renderTurnLine renders one memory as "- [type #id, YYYY-MM-DD] text\n".
//
// Junior Tip [the date is load-bearing]: the live tenant holds "healthy_deadline is
// 8 minutes" next to a later "the owner decided the healthy_deadline is 15 minutes".
// Without dates the model cannot tell which one is current; with them it can.
func renderTurnLine(candidate turnCandidate, itemMaxRunes int) string {
	var labelParts []string
	if candidate.recordType != "" {
		labelParts = append(labelParts, candidate.recordType)
	}
	if candidate.recordID != 0 {
		labelParts = append(labelParts, "#"+strconv.Itoa(candidate.recordID))
	}
	label := strings.Join(labelParts, " ")
	if candidate.createdAt != nil {
		createdDate := candidate.createdAt.UTC().Format("2006-01-02")
		if label == "" {
			label = createdDate
		} else {
			label += ", " + createdDate
		}
	}

	text := candidate.text
	if itemMaxRunes > 1 && utf8.RuneCountInString(text) > itemMaxRunes {
		text = truncateRunes(text, itemMaxRunes-1) // appends "…": itemMaxRunes in total
	}
	if label == "" {
		return "- " + text + "\n"
	}
	return "- [" + label + "] " + text + "\n"
}
