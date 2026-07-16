# AnhurDB memory for Claude Code

Give Claude Code a **persistent, sovereign long-term memory** backed by [AnhurDB](https://anhur.yoven.ai).

- **Auto-recall** — at the start of every session, your AnhurDB profile (decisions, facts,
  preferences, recent topics) is injected into Claude's context. It wakes up remembering.
- **Auto-persist** — after every turn (and at session end) the new conversation is saved to AnhurDB
  (one memory per turn). AnhurDB's **Smart Units** then distill it into typed memories —
  `fact` / `preference` / `decision` / `risk` / `task` / `emotion` — whenever Smart Units are enabled
  on your AnhurDB (see [Structured memory](#structured-memory-smart-units) below).
- **No silent loss at the boundary** — if AnhurDB is unreachable when a turn ends, the turn is
  queued to disk and retried on the next session start. A crash risks at most the in-flight turn.
- **The key never touches the transcript** — it lives only in `ANHUR_API_KEY` (env), sent as the
  `X-API-Key` header. This honors AnhurDB's auth model: a master key for services, **one API key
  per tenant** — nothing else.

The engine is a **single static Go binary that dogfoods the official AnhurDB Go SDK**
(`github.com/Yoven/AnhurDB-SDK/v2/golang/v2`) — so it inherits the SDK's HTTP transport and error
handling, and has **zero runtime dependencies** (no python, no jq, no curl, and **no Go toolchain to
install** — prebuilt binaries ship for macOS and Linux). The plugin also registers the AnhurDB
**MCP tools** for explicit recall/store during a session.

## Requirements

- A running AnhurDB reachable at `ANHUR_URL` (local docker-compose, or a hosted deployment like
  `https://anhurdb.yoven.ai`).
- A **per-tenant** AnhurDB API key — an `anhur_…` token, **not** the master key. The same key the
  MCP tools accept.
- macOS (arm64/amd64) or Linux (arm64/amd64). Windows via WSL.
- For **structured memory** (decisions/facts/emotions, not just raw turns), your AnhurDB must have
  **Smart Units** enabled (its cognitive layer; on by default on hosted plans). Without them every
  turn is still saved, but nothing is distilled. See [Structured memory](#structured-memory-smart-units).

> No Go toolchain is required to **use** the plugin — the marketplace ships a prebuilt binary per
> platform. Go 1.24+ is only for [development](#development).

## Install

### 1. Add the marketplace and install the plugin

In Claude Code:

```
/plugin marketplace add Yoven/AnhurDB-SDK
/plugin install anhurdb-memory@anhur
```

The `anhur` marketplace manifest is at the repo root, so the GitHub `owner/repo` shorthand works — no
clone needed. (Working from a local checkout instead? `/plugin marketplace add .` from the repo root.)

(The `anhur` marketplace also offers `anhurdb-memory-hermes` — the same engine pointed at a separate
tenant/container, for a second, isolated agent identity.)

A committed wrapper (`bin/anhur-claude-memory`) auto-selects the right prebuilt binary for your
OS/arch, so there is **nothing to build**.

### 2. Configure the environment

The hooks `source $HOME/.anhur-claude-memory/env` before running, so that file (mode `0600`, **outside
any repo**) is the canonical place for your config. Create it:

```bash
install -m 700 -d "$HOME/.anhur-claude-memory"
umask 177
cat > "$HOME/.anhur-claude-memory/env" <<'EOF'
export ANHUR_API_KEY="anhur_…your_tenant_key…"
export ANHUR_URL="https://anhurdb.yoven.ai"   # or http://localhost:8000 for local
export ANHUR_CONTAINER="claude-ltm"           # your memory profile — pick once, keep stable
EOF
```

- **Never commit the key.** It lives only in this file and is sent as the `X-API-Key` header.
- **`ANHUR_CONTAINER` is your memory profile — choose it once and keep it stable.** The API key
  selects your *tenant*; `ANHUR_CONTAINER` names the memory profile **within** it that recall reads
  from. Change it later and recall stops surfacing what was saved under the old name — nothing is
  lost (it's still there under the old name), it just isn't re-surfaced.

Optional variables (see `.env.example`): `ANHUR_STATE_DIR` (queue/log location, default
`~/.anhur-claude-memory`), `ANHUR_RECALL_LIMIT` (facts surfaced at recall, default 8), `ANHUR_ARCHIVE`
(verbatim transcript archive, default on), and **`ANHUR_MCP_URL`** — the AnhurDB MCP server the bundled
tools connect to (default `http://localhost:8090/mcp`; point it at your hosted MCP endpoint if you use
one).

> **MCP tools on the Desktop app:** `${ANHUR_MCP_URL:-…}` in `.mcp.json` is expanded by the Claude
> Code **CLI** but **not** the macOS **Desktop app** — there the literal `${ANHUR_MCP_URL}` is sent and
> the MCP tools fail to connect. On Desktop, edit `.mcp.json` to a hardcoded URL. This only affects the
> optional MCP tools; the core recall/persist loop (which talks to `ANHUR_URL` via the SDK, not MCP) is
> unaffected either way.

### 3. Start a new session

On startup your AnhurDB memory is injected as an `<anhur-memory>` block; after each turn it persists.
That's it.

## Verify it works (without waiting for a session)

The same binary the hooks run can be driven by hand:

```bash
. "$HOME/.anhur-claude-memory/env"

# Recall: prints your <anhur-memory> block.
"$HOME/.claude/plugins/cache/anhur/anhurdb-memory"/*/bin/anhur-claude-memory recall </dev/null
```

Diagnostics (never the key) go to `$ANHUR_STATE_DIR/plugin.log` (default
`~/.anhur-claude-memory/plugin.log`).

**Verify the *full* loop — not just storage.** `recall` proves reading; to confirm AnhurDB is also
distilling turns into typed memories, save a sentence with a clear decision/fact, wait a few seconds
(Smart Units are asynchronous), then recall:

```bash
curl -s -X POST "$ANHUR_URL/api/v1/ingest" -H "X-API-Key: $ANHUR_API_KEY" -H 'Content-Type: application/json' \
  -d '{"content":"Decision: we ship in June. Fact: the build uses Go 1.24.","container_tag":"'"$ANHUR_CONTAINER"'"}'
# wait a few seconds, then recall — the decision/fact should appear in the <anhur-memory> block.
```

If it stays empty, Smart Units aren't enabled on your AnhurDB — see
[Structured memory](#structured-memory-smart-units).

## How the memory loop works

```
SessionStart ─▶ recall  ─▶ flush any turns queued from a previous offline moment
                        └▶ read your profile, inject the <anhur-memory> block
   …turns…
Stop (each)  ─▶ persist ─▶ save the new turn  (on failure: queue to disk, retry next start)
SessionEnd   ─▶ persist ─▶ final flush of any remaining turns
```

Each saved turn becomes a memory in AnhurDB. From there AnhurDB's **Smart Units** distill it into
typed memories, keep them current, and retire contradicted facts so recall stays accurate over time.

## Structured memory (Smart Units)

Saving your turns is only half of it. Turning them into typed memories you can recall — `fact`,
`preference`, `decision`, `risk`, `task`, `emotion` — is done by AnhurDB's **Smart Units (SUs)**, its
cognitive layer. The plugin saves every turn no matter what; the Smart Units distill it.

**This is the most common "why is my memory empty?" surprise.** If Smart Units aren't enabled, your
turns are still saved safely, but recall stays thin — few or no Decisions/Facts in the block, because
nothing has been distilled yet.

- Enable Smart Units on your AnhurDB (hosted plans have them on by default).
- Distillation is **asynchronous** — a saved turn becomes typed memories a short while later.
- **Nothing is lost while you wait** — your raw turns are durable; the Smart Units catch up.

## Honest limitations

- **`SessionEnd` does not fire on a hard crash / `kill -9`.** The per-turn `Stop` hook is the durable
  path; `SessionEnd` is only a final flush. Worst-case loss is the single in-flight turn.
- **`SessionEnd` may not provide the transcript path.** `Stop` does; `SessionEnd` falls back to the
  documented transcript location, best-effort. Rely on `Stop` for durability.
- **Structured memories aren't instant.** Turns are saved immediately; typed facts/decisions appear a
  short while later, after the Smart Units distill them.
- **Hooks aren't retried by Claude Code.** That's why persistence is queued to disk and retried by
  `recall` on the next start.

## Development

Build and iterate locally (needs **Go 1.24+**):

```bash
cd v2/plugins/claude
make build     # native binary → bin/anhur-claude-memory-<os>-<arch>
make deploy    # build + sync the wrapper and this binary into the installed plugin cache
./test_e2e.sh  # end-to-end against the live AnhurDB in ~/.anhur-claude-memory/env
```

The plugin dogfoods the SDK via the monorepo `replace ../../golang` in `go.mod`. For a standalone
build outside this repo, drop that `replace` and `go get github.com/Yoven/AnhurDB-SDK/v2/golang/v2`.

### Releasing

Delivery is **prebuilt per-platform binaries committed in `bin/`**, selected at runtime by the
`bin/anhur-claude-memory` wrapper — so a marketplace install needs no toolchain. Distribution is the
`anhur` marketplace git repo, not a package registry.

1. Bump `version` in `.claude-plugin/plugin.json` (semver).
2. `make release-binaries` — cross-compiles darwin/linux × amd64/arm64 (reproducible via `-trimpath`).
   Build with **go 1.24.4** to match CI's freshness gate.
3. Commit the refreshed `bin/` + the version bump; merge to `main`.

`.github/workflows/release-plugin.yml` then gates on `go vet` + unit tests + a reproducible-build
freshness check (committed binaries must equal a fresh build), tags `plugins/claude/v<version>`, and
publishes a GitHub Release with the four binaries attached.

**Release order (SDK coupling):** the plugin builds against the SDK through the local `replace`, so
cut a plugin release from a commit where the SDK is already at its intended version — tag the SDK
first (`Release Go SDK`), then the plugin.

## Security

The API key is read from `ANHUR_API_KEY` and sent only as the `X-API-Key` header by the SDK. It is
never echoed to stdout/stderr, written to the plugin log, or placed in the transcript. Use a
per-tenant key scoped to exactly the memory this agent should see.
