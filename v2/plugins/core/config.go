package core

// config.go — a IDENTIDADE e a configuração do plugin: o que difere entre claude
// e hermes (Config), o que vem do ambiente (config/loadConfig) e os helpers de
// env. Um domínio: "quem eu sou e como fui configurado". O carregamento do
// arquivo env (a lição do blackout) mora em envfile.go. Regra do corte (CLAUDE.md).

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
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
	// archive + archiveDir control the LOSSLESS verbatim transcript archive (ANHUR_ARCHIVE,
	// ANHUR_ARCHIVE_DIR). This is the complete counterpart to the filtered episodic feed:
	// the cortex sees clean dialogue, the archive keeps the full transcript (thinking + tool
	// I/O included) so nothing is ever dropped. See archiveTranscript.
	archive    bool
	archiveDir string

	// Diagnóstico do carregamento de configuração — ver envfile.go.
	// Junior Tip [nunca guardam o VALOR da chave]: keySource diz de ONDE a chave
	// veio (environment | file | missing) para o log. O blackout de 12,8 dias foi
	// invisível porque nada registrava isso: a mesma invocação podia funcionar ou
	// pular dependendo do shell que a lançou, e o log não distinguia os casos.
	keySource   string
	envFilePath string
	envFileVars int
	envFileErr  error
}

// loadConfig reads the runtime config from the environment, falling back to the plugin identity's
// defaults for the two values that differ between plugins (state dir + container).
func loadConfig(plugin Config) config {
	stateDir := envOr("ANHUR_STATE_DIR", filepath.Join(homeDir(), plugin.StateDirName))

	// Junior Tip [ler o próprio arquivo, não confiar no shell — 2026-07-30]: os
	// hooks fazem `. <stateDir>/env`, mas sourcing sem `export` cria variável de
	// SHELL que o processo filho não herda. Foi assim que o plugin passou 12,8
	// dias com 743 skips consecutivos e zero persists, em silêncio. Carregar o
	// arquivo aqui torna o binário autocontido: funciona sob qualquer executor de
	// hook, em qualquer shell, e o carregamento vira código testável. Ver
	// envfile.go para o formato aceito e a regra de precedência.
	// Junior Tip [vazio não conta como "veio do ambiente", 2026-07-31]: usar só o
	// booleano do LookupEnv aqui faria o log dizer `key source=environment` para uma
	// chave que na verdade veio do ARQUIVO — porque a variável existia, vazia. O log
	// de diagnóstico apontaria para o lugar errado exatamente na investigação em que
	// ele é a única pista, e mandaria quem depura procurar no shell uma chave que
	// está no disco. Ver a Junior Tip gêmea em envfile.go.
	apiKeyWasAlreadyInEnvironment := strings.TrimSpace(os.Getenv("ANHUR_API_KEY")) != ""
	envFilePath := resolveEnvFilePath(stateDir)
	injectedCount, envFileErr := loadEnvFileInto(envFilePath)

	resolvedAPIKey := os.Getenv("ANHUR_API_KEY")
	loaded := config{
		apiKey: resolvedAPIKey,
		// keySource / envFileNote alimentam o log de diagnóstico — NUNCA o valor
		// da chave. Foi a precedência do ambiente herdado do terminal que produziu
		// 187 sucessos intercalados e mascarou a falha; sem registrar a fonte, o
		// próximo diagnóstico volta a ser adivinhação.
		keySource:   apiKeySource(apiKeyWasAlreadyInEnvironment, resolvedAPIKey),
		envFilePath: envFilePath,
		envFileVars: injectedCount,
		envFileErr:  envFileErr,
		// Junior Tip [the SDK owns the default, 2026-07-17]: this used to default to
		// http://localhost:8000 — a dead port on every machine not running a dev stack, so a key
		// set without ANHUR_URL dialled nothing, recall injected nothing, and the process still
		// exited 0 (an unreachable default is indistinguishable from an empty memory). Rather than
		// hardcode the hosted URL here, take client.DefaultCloudURL: this plugin dogfoods the SDK,
		// and duplicating the constant would leave the plugin pointing at a stale endpoint the day
		// the SDK moves. Local development sets ANHUR_URL explicitly.
		url:           envOr("ANHUR_URL", client.DefaultCloudURL),
		container:     envOr("ANHUR_CONTAINER", plugin.DefaultContainer),
		stateDir:      stateDir,
		httpTimeout:   time.Duration(envInt("ANHUR_HTTP_TIMEOUT", 15)) * time.Second,
		recallLimit:   envInt("ANHUR_RECALL_LIMIT", 8),
		maxChunkChars: envInt("ANHUR_MAX_CHUNK_CHARS", 24000),
		includeTools:  strings.ToLower(envOr("ANHUR_INCLUDE_TOOLS", "calls")),
		// Junior Tip [default ON, 2026-07-14]: the archive is a capability, so it defaults
		// TRUE in code (disabled only by the literal "false"), matching the project rule that
		// capabilities are on-by-default rather than gated behind a flag nobody sets.
		archive:    os.Getenv("ANHUR_ARCHIVE") != "false",
		archiveDir: envOr("ANHUR_ARCHIVE_DIR", filepath.Join(stateDir, "archive")),
	}
	return loaded
}

func (cfg config) queueDir() string  { return filepath.Join(cfg.stateDir, "queue") }
func (cfg config) cursorDir() string { return filepath.Join(cfg.stateDir, "cursor") }
func (cfg config) logPath() string   { return filepath.Join(cfg.stateDir, "plugin.log") }

// quarantineDir holds queued chunks whose originating conversation session could not be
// identified. Chunks in here are kept intact but NEVER retried — see quarantineChunk.
// It lives INSIDE queueDir on purpose: flushQueue skips directories, so a quarantined chunk
// can never be picked up again by the normal retry loop.
func (cfg config) quarantineDir() string { return filepath.Join(cfg.queueDir(), "quarantine") }

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
