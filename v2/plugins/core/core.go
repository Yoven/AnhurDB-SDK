// Package core is the shared engine behind the AnhurDB memory plugins for Claude Code.
//
// It dogfoods the official AnhurDB Go SDK (github.com/anhurdb/sdk-go/v2) instead of poking the
// REST API by hand — so it inherits the SDK's retries, read-your-writes plumbing, and the same
// client contract we ship to users. It compiles into a SINGLE static binary per plugin, which
// means the hook has ZERO runtime dependencies (no python, no jq, no curl).
//
// Two subcommands, wired to Claude Code hooks:
//
//	<binary> recall    # SessionStart: flush any queued writes, then print the agent's AnhurDB
//	                   # profile so Claude Code injects it as context.
//	<binary> persist   # Stop / SessionEnd: ingest the transcript delta since the last run;
//	                   # on failure, queue to disk and retry on the next recall.
//
// Design principle (mirrors AnhurDB's #1 rule — no silent loss): a turn we cannot persist is
// queued to disk and recovered on the next session start, never dropped. Every error path exits 0
// so a memory backend that is down can never block or crash the agent's session.
//
// Junior Tip [why a shared core, 2026-07-07]: the `claude` and `hermes` plugins are the SAME engine
// pointed at DIFFERENT memory identities (state dir + container + tenant key). Instead of copying
// ~600 lines into each — which is exactly the kind of drift the SDK-parity rule exists to prevent —
// the engine lives here ONCE and each plugin ships a thin main that calls Run with its own Config.
// The claude plugin's Config reproduces the old hardcoded defaults byte-for-byte, so its behaviour
// (the user's LIVING long-term memory) is unchanged by this extraction.
package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/anhurdb/sdk-go/v2/client"
)

// Config is the per-plugin identity — the ONLY thing that differs between the `claude` and `hermes`
// builds. Everything else in this package is shared byte-for-byte. Each plugin's thin main passes
// its own Config to Run.
//
// Junior Tip [keep this tiny + all-required, 2026-07-07]: these three fields are the exact seam
// between the two plugins. Adding non-identity knobs here would blur the line between "plugin
// identity" and "runtime tuning" (which is env-driven via loadConfig). If it can be tuned per-run,
// it belongs in an ANHUR_* env var, not here.
type Config struct {
	// StateDirName is the DEFAULT directory (under $HOME) for the queue, cursors, and log when
	// ANHUR_STATE_DIR is unset. claude: ".anhur-claude-memory"; hermes: ".anhur-hermes-memory".
	StateDirName string
	// DefaultContainer is the DEFAULT container tag (the SDK's WithUserID) when ANHUR_CONTAINER is
	// unset. It names the memory profile within the tenant. claude: "claude-ltm"; hermes: "hermes-ltm".
	DefaultContainer string
	// BinaryName is the executable name, used only in the usage diagnostic line.
	BinaryName string
}

// config holds everything sourced from the environment. The API key lives ONLY here and is handed
// straight to the SDK as the X-API-Key header — it is never printed, logged, or written anywhere.
type config struct {
	apiKey        string
	url           string
	container     string
	stateDir      string
	httpTimeout   time.Duration
	recallLimit   int
	maxChunkChars int
	// includeTools governs how tool blocks are persisted (ANHUR_INCLUDE_TOOLS):
	//   "none"  — text only (the original behaviour).
	//   "calls" — text + a COMPACT tool_use line; tool_result skipped (default).
	//   "all"   — the above + a TRUNCATED tool_result.
	includeTools string
}

// loadConfig reads the runtime config from the environment, falling back to the plugin identity's
// defaults for the two values that differ between plugins (state dir + container).
func loadConfig(plugin Config) config {
	stateDir := envOr("ANHUR_STATE_DIR", filepath.Join(homeDir(), plugin.StateDirName))
	return config{
		apiKey:        os.Getenv("ANHUR_API_KEY"),
		url:           envOr("ANHUR_URL", "http://localhost:8000"),
		container:     envOr("ANHUR_CONTAINER", plugin.DefaultContainer),
		stateDir:      stateDir,
		httpTimeout:   time.Duration(envInt("ANHUR_HTTP_TIMEOUT", 15)) * time.Second,
		recallLimit:   envInt("ANHUR_RECALL_LIMIT", 8),
		maxChunkChars: envInt("ANHUR_MAX_CHUNK_CHARS", 24000),
		includeTools:  strings.ToLower(envOr("ANHUR_INCLUDE_TOOLS", "calls")),
	}
}

func (cfg config) queueDir() string  { return filepath.Join(cfg.stateDir, "queue") }
func (cfg config) cursorDir() string { return filepath.Join(cfg.stateDir, "cursor") }
func (cfg config) logPath() string   { return filepath.Join(cfg.stateDir, "plugin.log") }

// newMemory builds the SDK client. WithUserID sets the container tag the SDK reads/writes under;
// WithURL points at the AnhurDB HTTP endpoint; WithTimeout bounds every call.
func newMemory(cfg config) *client.Memory {
	return client.NewMemory(cfg.apiKey,
		client.WithURL(cfg.url),
		client.WithUserID(cfg.container),
		client.WithTimeout(cfg.httpTimeout),
	)
}

// Run is the plugin entrypoint. args is the process argv (os.Args); plugin is the caller's identity.
//
// Junior Tip [never crash the session]: a panic in a hook must not surface to Claude Code as a
// failed hook. Recover, log, and exit 0 no matter what. The session always proceeds.
func Run(args []string, plugin Config) {
	cfg := loadConfig(plugin)
	defer func() {
		if r := recover(); r != nil {
			logLine(cfg, fmt.Sprintf("panic recovered: %v", r))
		}
		os.Exit(0)
	}()

	_ = os.MkdirAll(cfg.queueDir(), 0o755)
	_ = os.MkdirAll(cfg.cursorDir(), 0o755)

	if len(args) < 2 {
		logLine(cfg, "usage: "+plugin.BinaryName+" <recall|persist>")
		return
	}
	if cfg.apiKey == "" {
		logLine(cfg, "ANHUR_API_KEY not set — skipping")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.httpTimeout+5*time.Second)
	defer cancel()
	mem := newMemory(cfg)

	switch args[1] {
	case "recall":
		cmdRecall(ctx, cfg, mem)
	case "persist":
		cmdPersist(ctx, cfg, mem)
	default:
		logLine(cfg, "unknown subcommand: "+args[1])
	}
}

// ── recall ───────────────────────────────────────────────────────────────────

// cmdRecall flushes any queued writes from a prior session, then prints the agent's AnhurDB
// profile to stdout. Claude Code injects stdout from a SessionStart hook into the model context.
func cmdRecall(ctx context.Context, cfg config, mem *client.Memory) {
	flushQueue(ctx, cfg, mem)

	profile, err := mem.Profile(ctx)
	if err != nil {
		logLine(cfg, "recall: profile failed (AnhurDB unreachable?): "+err.Error())
		return // inject nothing — never block startup
	}
	block := formatMemory(cfg, profile)
	if block != "" {
		fmt.Println(block)
		logLine(cfg, fmt.Sprintf("recall: injected memory block (bytes=%d)", len(block)))
	}
}

// formatMemory renders the ProfileResult into the <anhur-memory> block. Sections with no items are
// omitted so we never inject empty headers.
func formatMemory(cfg config, profile *client.ProfileResult) string {
	var builder strings.Builder
	builder.WriteString(`<anhur-memory backend="AnhurDB" container="` + cfg.container + `">` + "\n")
	builder.WriteString("You (Claude) have persistent long-term memory in AnhurDB. This is what you remember — trust it, build on it, and keep it accurate (use supersede when a fact changes).\n")

	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		if len(items) > cfg.recallLimit {
			items = items[:cfg.recallLimit]
		}
		builder.WriteString("\n## " + title + "\n")
		for _, item := range items {
			builder.WriteString("- " + item + "\n")
		}
	}

	section("Decisions", stringList(profile.Static, "decisions"))
	section("Facts", stringList(profile.Static, "facts"))
	section("Preferences", stringList(profile.Static, "preferences"))
	section("Recent topics", stringList(profile.Dynamic, "recent_topics"))
	section("Open tasks", stringList(profile.Dynamic, "recent_tasks"))

	total := numField(profile.Stats, "total_records")
	sessions := numField(profile.Stats, "sessions")
	builder.WriteString(fmt.Sprintf("\n(%d records across %d sessions. The MCP tools mcp__anhurdb__* let you recall/store more during this session.)\n", total, sessions))
	builder.WriteString("</anhur-memory>")
	return builder.String()
}

// ── persist ──────────────────────────────────────────────────────────────────

// hookInput is the JSON Claude Code pipes to a hook on stdin. Stop hooks include transcript_path;
// SessionEnd may not, so we fall back to the documented transcript location using session_id+cwd.
type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
}

// cmdPersist ingests the transcript lines added since the last run (cursor-based), so each Stop
// persists only the new turn. On a write failure the chunk is queued to disk for retry.
func cmdPersist(ctx context.Context, cfg config, mem *client.Memory) {
	var input hookInput
	_ = json.NewDecoder(os.Stdin).Decode(&input)

	// Junior Tip [drain the backlog on EVERY persist, 2026-07-08]: flushQueue used to
	// run ONLY on cmdRecall (SessionStart). In a long-running session that queued a
	// chunk during a transient DB-down (e.g. a stack restart), the backlog then sat
	// on disk until the NEXT session start — could be hours. Persist runs every few
	// minutes, so flushing here first drains queued chunks opportunistically the
	// moment the DB is reachable again. Order matters: drain the OLD backlog before
	// adding this turn's chunk, so recovered memories keep their original ordering.
	flushQueue(ctx, cfg, mem)

	path := resolveTranscript(input)
	if path == "" {
		logLine(cfg, "persist: transcript not found (session="+input.SessionID+")")
		return
	}
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = "anon-" + filepath.Base(path)
	}

	lines, err := readLines(path)
	if err != nil {
		logLine(cfg, "persist: cannot read transcript: "+err.Error())
		return
	}
	cursorFile := filepath.Join(cfg.cursorDir(), sanitize(sessionID))
	last := readCursor(cursorFile)
	if len(lines) <= last {
		return // nothing new since the last persist
	}

	text := extractConversation(cfg, lines[last:])
	// Advance the cursor unconditionally: these lines are now processed (every chunk
	// below is either sent or queued — never dropped).
	writeCursor(cursorFile, len(lines))
	if strings.TrimSpace(text) == "" {
		return // the new lines were only tool noise we don't persist (or empty)
	}

	// Junior Tip [no-silent-loss chunking, 2026-06-20]: a long delta is split into
	// maxChunkChars-sized pieces and EACH is persisted (queued on failure), instead of
	// truncating to the last maxChunkChars and dropping the rest. Every chunk stays a
	// search-/extraction-friendly size and nothing is lost.
	chunks := splitIntoChunks(text, cfg.maxChunkChars)
	sent, queued := 0, 0
	for i, body := range chunks {
		label := ""
		if len(chunks) > 1 {
			label = fmt.Sprintf(" [part %d/%d]", i+1, len(chunks))
		}
		chunk := fmt.Sprintf("Claude Code session %s — conversation excerpt%s (%s):\n%s",
			sessionID, label, time.Now().UTC().Format(time.RFC3339), body)
		// Junior Tip [tenant + session, 2026-07-08]: pin the record to THIS
		// conversation's session (sessionID), NOT the container. The tenant comes
		// from the API key; each Claude Code conversation is its own session, so
		// consolidation produces one consolidated per conversation anchored at its
		// first episodic. Recall still scopes to the whole tenant.
		if _, addErr := mem.Add(ctx, chunk, client.WithSessionID(sessionID)); addErr != nil {
			queueChunk(cfg, chunk) // DB down → queue; recall flushes it next start (no silent loss)
			queued++
		} else {
			sent++
		}
	}
	logLine(cfg, fmt.Sprintf("persist: lines %d-%d (session=%s, chunks=%d sent=%d queued=%d)",
		last+1, len(lines), sessionID, len(chunks), sent, queued))
}

// resolveTranscript returns the transcript path, preferring the one the hook provided and falling
// back to the documented location built from cwd + session_id.
func resolveTranscript(input hookInput) string {
	if input.TranscriptPath != "" && fileExists(input.TranscriptPath) {
		return input.TranscriptPath
	}
	if input.SessionID != "" && input.Cwd != "" {
		munged := strings.ReplaceAll(input.Cwd, "/", "-")
		guess := filepath.Join(homeDir(), ".claude", "projects", munged, input.SessionID+".jsonl")
		if fileExists(guess) {
			return guess
		}
	}
	return ""
}

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

// queueSeq gives every queued chunk a process-unique suffix so multiple chunks
// persisted within the same clock tick never collide onto one filename.
var queueSeq uint64

func queueChunk(cfg config, content string) {
	// Junior Tip [collision-proof + fail-loud, 2026-07-04]: the old name was
	// second-granular time + pid, so two chunks queued in the same second (a NORMAL
	// multi-chunk persist — cmdPersist loops over chunks) produced the SAME path and
	// os.WriteFile silently overwrote all-but-the-last = silent memory loss, violating
	// this file's own "#1 rule — no silent loss" (header). And a WriteFile error was
	// swallowed (only the success path logged), so a failed queue write vanished with no
	// trace while the transcript moved on. Now the name is unique per write (nanosecond +
	// pid + atomic seq) and a write failure fails LOUD (logged, not silent).
	seq := atomic.AddUint64(&queueSeq, 1)
	name := fmt.Sprintf("%s-%d-%d.txt", time.Now().UTC().Format("20060102T150405.000000000"), os.Getpid(), seq)
	path := filepath.Join(cfg.queueDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		logLine(cfg, fmt.Sprintf("queue write FAILED for chunk (data at risk): %v", err))
		return
	}
	logLine(cfg, "queued chunk -> "+path)
}

// flushQueue retries every queued chunk; successful ones are removed, the rest stay for next time.
func flushQueue(ctx context.Context, cfg config, mem *client.Memory) {
	entries, err := os.ReadDir(cfg.queueDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		path := filepath.Join(cfg.queueDir(), entry.Name())
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		// Recover into the ORIGINAL conversation's session: the session id is
		// embedded in the chunk header ("Claude Code session <id> — ..."), so a
		// queued chunk drains back into the same session it came from instead of
		// collapsing into the container. Empty extraction → server default.
		if _, addErr := mem.Add(ctx, string(content), client.WithSessionID(sessionFromChunk(string(content)))); addErr == nil {
			_ = os.Remove(path)
			logLine(cfg, "flushed queued chunk "+path)
		} else {
			// Junior Tip [log the addErr, 2026-07-03]: this line used to swallow the
			// error, so a PERMANENT rejection (e.g. HTTP 409 "session has reached the
			// maximum of 500 records") looked identical to a transient DB-down retry —
			// the queue sat "still failing" for 10 days before anyone saw the 409.
			// The queue must never drop chunks, but it must FAIL LOUD about why.
			logLine(cfg, fmt.Sprintf("flush still failing for %s (retry next start): %v", path, addErr))
		}
	}
}

// ── small helpers ────────────────────────────────────────────────────────────

func readCursor(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func writeCursor(path string, n int) { _ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0o600) }

func readLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024) // tolerate long JSONL lines
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// sessionFromChunk pulls the conversation session id out of a persisted chunk's
// header ("Claude Code session <id> — conversation excerpt..."). Returns "" when
// the header is absent, in which case the server keeps its container_tag default.
func sessionFromChunk(chunk string) string {
	const marker = "Claude Code session "
	start := strings.Index(chunk, marker)
	if start < 0 {
		return ""
	}
	rest := chunk[start+len(marker):]
	if end := strings.IndexAny(rest, " \n\t"); end >= 0 {
		return rest[:end]
	}
	return ""
}

// logLine appends a timestamped diagnostic to the plugin log. It NEVER includes the API key.
func logLine(cfg config, msg string) {
	file, err := os.OpenFile(cfg.logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339), msg)
}

// stringList coerces profile section[key] (interface{} → []interface{}) into a []string.
func stringList(section map[string]interface{}, key string) []string {
	rawList, ok := section[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rawList))
	for _, item := range rawList {
		if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
			out = append(out, str)
		}
	}
	return out
}

// numField reads a numeric stat that JSON decoded as float64.
func numField(stats map[string]interface{}, key string) int {
	if f, ok := stats[key].(float64); ok {
		return int(f)
	}
	return 0
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// sanitize keeps a session id safe as a filename.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}
