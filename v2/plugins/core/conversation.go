package core

// conversation.go — a TRANSFORMAÇÃO do transcript cru em texto persistível:
// filtro de ruído de ferramenta, chunking sem perda e os cortes rune-safe.
// Um domínio: "o que do transcript vira memória, e em que pedaços".
// Regra do corte (~300, CLAUDE.md).

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// transcriptLine is the minimal shape we read from each JSONL transcript entry. We
// need the role and the content blocks; how tool blocks are rendered is governed by
// cfg.includeTools (see contentText).
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// extractConversation pulls "ROLE: ..." lines from user/assistant entries, joining
// the rendered content parts (text + optional tool blocks per cfg.includeTools).
func extractConversation(cfg config, lines []string) string {
	var out strings.Builder
	for _, raw := range lines {
		var entry transcriptLine
		if json.Unmarshal([]byte(raw), &entry) != nil {
			continue
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}
		text := contentText(cfg, entry.Message.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out.WriteString("  " + strings.ToUpper(entry.Type) + ": " + text + "\n")
	}
	return out.String()
}

// contentText renders a message's content to plain text. Text blocks are always
// kept; tool blocks are governed by cfg.includeTools (ANHUR_INCLUDE_TOOLS):
//   - "none":  text only.
//   - "calls": text + a COMPACT tool_use line ("[tool: Name {input}]"); tool_result skipped.
//   - "all":   the above + a TRUNCATED tool_result ("[result: ...]") so the bulk of bash/
//     file output never floods the memory or the (already-saturated) extraction pipeline.
//
// thinking blocks are always skipped (internal reasoning, not memory).
func contentText(cfg config, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return asString
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Name    string          `json:"name"`    // tool_use
		Input   json.RawMessage `json:"input"`   // tool_use
		Content json.RawMessage `json:"content"` // tool_result
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case "tool_use":
			if cfg.includeTools == "calls" || cfg.includeTools == "all" {
				parts = append(parts, "[tool: "+block.Name+" "+truncateRunes(compactJSON(block.Input), 200)+"]")
			}
		case "tool_result":
			if cfg.includeTools == "all" {
				parts = append(parts, "[result: "+truncateRunes(blockText(block.Content), 500)+"]")
			}
		}
	}
	return strings.Join(parts, "\n")
}

// compactJSON collapses a JSON value to a single-line string (best-effort: returns
// the trimmed raw text if it is not re-marshalable).
func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v interface{}
	if json.Unmarshal(raw, &v) != nil {
		return strings.TrimSpace(string(raw))
	}
	out, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(out)
}

// blockText extracts plain text from a tool_result content field, which is either a
// bare string or an array of {type:"text", text:...} blocks.
func blockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return asString
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// truncateRunes cuts s to at most maxRunes runes (rune-safe), appending an ellipsis
// when truncated.
func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// splitIntoChunks splits text into pieces of at most maxChars bytes, preferring line
// boundaries so a turn stays intact. A single line longer than maxChars is hard-split
// on UTF-8 rune boundaries (never mid-rune). Junior Tip [no-silent-loss, 2026-06-20]:
// this replaces the old "truncate to the last maxChars" so a long delta is persisted
// in full across N chunks instead of dropping its head.
func splitIntoChunks(text string, maxChars int) []string {
	if maxChars <= 0 || len(text) <= maxChars {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		if len(line) > maxChars {
			flush()
			chunks = append(chunks, hardSplitRunes(line, maxChars)...)
			continue
		}
		if cur.Len()+len(line) > maxChars {
			flush()
		}
		cur.WriteString(line)
	}
	flush()
	return chunks
}

// hardSplitRunes splits s into pieces of at most maxBytes, cutting only on UTF-8 rune
// boundaries so a multi-byte rune is never split across pieces.
func hardSplitRunes(s string, maxBytes int) []string {
	var out []string
	for len(s) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = maxBytes // pathological (no rune boundary in range) — avoid an infinite loop
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

// ── no-silent-loss queue ─────────────────────────────────────────────────────
