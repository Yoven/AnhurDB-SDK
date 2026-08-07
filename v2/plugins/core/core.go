// Package core is the shared engine behind the AnhurDB memory plugins for Claude Code.
//
// It dogfoods the official AnhurDB Go SDK (github.com/Yoven/AnhurDB-SDK/v2/golang/v2) instead of poking the
// REST API by hand — so it inherits the SDK's HTTP transport and error handling,
// client contract we ship to users. It compiles into a SINGLE static binary per plugin, which
// means the hook has ZERO runtime dependencies (no python, no jq, no curl).
//
// Two subcommands, wired to Claude Code hooks:
//
//	<binary> recall    # SessionStart: flush any queued writes, then print the agent's AnhurDB
//	                   # profile so Claude Code injects it as context.
//	<binary> persist   # Stop / SessionEnd: drain the queue, then ingest the transcript delta
//	                   # since the last run; on failure, queue to disk.
//
// Design principle (mirrors AnhurDB's #1 rule — no silent loss): a turn we cannot persist is
// queued to disk and recovered by the next persist or the next session start — whichever comes
// first — never dropped. Every error path exits 0 so a memory backend that is down can never block
// or crash the agent's session.
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
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

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
	// Junior Tip [a chave ausente agora GRITA, e o arquivo é salvo antes —
	// 2026-07-30]: esta guarda produziu 743 skips consecutivos em 12,8 dias com
	// exit 0, stdout vazio e stderr vazio. Duas coisas mudaram, e as duas são o
	// ponto:
	//
	//  1. A mensagem vai também para o STDERR, que o Claude Code mostra. Um plugin
	//     que não pode cumprir sua única função tem de ser visível — "never crash
	//     the session" continua valendo (segue saindo com 0), mas silêncio total
	//     não é a mesma coisa que não travar.
	//  2. O ARQUIVO LOSSLESS é salvo ANTES de desistir. Ele existe justamente para
	//     ser a rede quando a rede falha, e estava atrás deste mesmo return: a
	//     salvaguarda morria junto com aquilo que ela deveria salvaguardar. Com o
	//     arquivo em disco, uma chave ausente vira atraso recuperável, não perda.
	if cfg.apiKey == "" {
		diagnostic := "ANHUR_API_KEY not set (env file: " + cfg.envFilePath
		if cfg.envFileErr != nil {
			diagnostic += ", unreadable: " + cfg.envFileErr.Error()
		} else {
			diagnostic += ", " + strconv.Itoa(cfg.envFileVars) + " vars loaded"
		}
		diagnostic += ") — MEMORY IS NOT BEING SAVED"
		logLine(cfg, diagnostic)
		fmt.Fprintln(os.Stderr, "anhur-memory: "+diagnostic)

		// A transcrição vai para o disco mesmo sem chave: recuperável depois.
		// Mesma resolução de sessão/caminho que cmdPersist usa, para o arquivo
		// cair com o mesmo nome que teria caído se a chave estivesse presente.
		if len(args) > 1 && args[1] == "persist" && cfg.archive {
			var stdinInput hookInput
			_ = json.NewDecoder(os.Stdin).Decode(&stdinInput)
			transcriptPath := resolveTranscript(stdinInput)
			if transcriptPath == "" {
				logLine(cfg, "archive without key: transcript not found")
			} else {
				sessionID := stdinInput.SessionID
				if sessionID == "" {
					sessionID = "anon-" + filepath.Base(transcriptPath)
				}
				archiveTranscript(cfg, sessionID, transcriptPath)
				logLine(cfg, "transcript archived despite missing key — recoverable (session="+sessionID+")")
			}
		}
		return
	}
	logLine(cfg, "config: key source="+cfg.keySource+" env file="+cfg.envFilePath+
		" vars="+strconv.Itoa(cfg.envFileVars))

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
	backlog := flushQueue(ctx, cfg, mem)
	archive := inspectArchiveHealth(cfg)

	profile, err := mem.Profile(ctx)
	if err != nil {
		// Junior Tip [o aviso NÃO pode ficar atrás do portão que ele protege, 2026-07-31]:
		// esta linha era um `return` seco. O efeito: quando o AnhurDB está fora — que é
		// EXATAMENTE quando existe backlog e quando saber do backlog importa — o bloco
		// inteiro era suprimido, e com ele o único canal que alcança o usuário. O aviso
		// morria junto com a coisa sobre a qual ele avisava. É a terceira vez que esta
		// mesma forma aparece nesta base (o arquivo lossless atrás da guarda de chave, o
		// arquivo atrás do early-return de config, e agora este), por isso ela virou
		// regra: uma rede de segurança nunca depende daquilo que ela protege.
		//
		// Continua verdade que o recall NUNCA bloqueia o início da sessão: se não há
		// nada a avisar, formatMemory devolve vazio e nada é impresso.
		logLine(cfg, "recall: profile failed (AnhurDB unreachable?): "+err.Error())
		if degradedBlock := formatMemory(cfg, nil, backlog, archive); degradedBlock != "" {
			fmt.Println(degradedBlock)
			logLine(cfg, fmt.Sprintf("recall: profile failed but wrote a warnings-only block to stdout (bytes=%d)", len(degradedBlock)))
		}
		return
	}
	block := formatMemory(cfg, profile, backlog, archive)
	if block != "" {
		fmt.Println(block)
		// Junior Tip [say only what we can prove, 2026-07-16]: this used to read "injected memory
		// block". It could not know that. Writing to stdout is ALL this process does; whether the
		// block reaches the model depends on Claude Code having loaded the hook at all — which this
		// process cannot observe. That one word cost real debugging time: with the engine unloaded
		// and recalling nothing, every manual run still logged "injected", so the log read healthy
		// while the memory was dead. A log line must describe the action taken, never an outcome
		// owned by someone else.
		logLine(cfg, fmt.Sprintf("recall: wrote memory block to stdout (bytes=%d)", len(block)))
	}
}

// formatMemory renders the ProfileResult into the <anhur-memory> block. Sections with no items are
// omitted so we never inject empty headers. An unpersisted backlog is announced FIRST — see below.

// ── persist ──────────────────────────────────────────────────────────────────

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
// the header is absent — callers must create_session before persisting on
// session-first servers.
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

// sanitize keeps a session id safe as a filename.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}
