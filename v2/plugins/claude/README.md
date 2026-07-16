# AnhurDB memory for Claude Code

Give Claude Code a **persistent, sovereign long-term memory** backed by [AnhurDB](https://anhur.yoven.ai).

- **Auto-recall** — at the start of every session, your AnhurDB profile (decisions, facts,
  preferences, recent topics) is injected into Claude's context. It wakes up remembering.
- **Auto-persist** — after every turn (and at session end) the new conversation is saved to AnhurDB
  (one memory per turn). AnhurDB's **Smart Units** then distill it into typed memories —
  `fact` / `preference` / `decision` / `risk` / `task` / `emotion` — whenever Smart Units are enabled
  on your AnhurDB (see [Structured memory](#structured-memory-smart-units) below).
- **No silent loss at the boundary** — if AnhurDB is unreachable when a turn ends, the turn is
  queued to disk and retried on the next persist or session start, whichever comes first. A crash
  risks at most the in-flight turn.
- **The key never touches the transcript** — it lives only in `ANHUR_API_KEY` (env), sent as the
  `X-API-Key` header. This honors AnhurDB's auth model: a master key for services, **one API key
  per tenant** — nothing else.

Underneath, this is **one static Go binary and three hooks** — `recall` on `SessionStart`, `persist`
on `Stop` and `SessionEnd`. That is the entire mechanism; everything else in this directory is
packaging for people who would rather not build it themselves. You can skip all of it and
[wire the binary directly](#option-a--direct-one-binary-three-hooks).

The binary **dogfoods the official AnhurDB Go SDK** (`github.com/Yoven/AnhurDB-SDK/v2/golang/v2`), so
it inherits the SDK's HTTP transport and error handling, and has **zero runtime dependencies** — no
python, no jq, no curl, and **no Go toolchain** if you install from the marketplace (prebuilt binaries
ship for macOS and Linux). The marketplace route also registers the AnhurDB **MCP tools** for explicit
recall/store during a session.

## Requirements

- An AnhurDB endpoint — `https://anhurdb.yoven.ai`.
- A **per-tenant** AnhurDB API key — an `anhur_…` token, **not** the master key. The same key the
  MCP tools accept.
- macOS (arm64/amd64) or Linux (arm64/amd64). Windows via WSL.
- For **structured memory** (decisions/facts/emotions, not just raw turns), your AnhurDB must have
  **Smart Units** enabled (its cognitive layer; on by default on hosted plans). Without them every
  turn is still saved, but nothing is distilled. See [Structured memory](#structured-memory-smart-units).

> No Go toolchain is required to **use** the plugin — the marketplace ships a prebuilt binary per
> platform. Go 1.24+ is only for [development](#development) or the direct install below.

## Install

There are two ways in, and they produce the **same memory** — the same binary, the same hooks, the
same records. They differ only in how much machinery sits between you and the engine:

| | [Direct](#option-a--direct-one-binary-three-hooks) | [Marketplace](#option-b--marketplace-plugin) |
|---|---|---|
| What you manage | one binary + three hooks | a marketplace, an install, a cache, a scope |
| Needs a Go toolchain | yes (or grab a release binary) | no — prebuilt binaries ship |
| Bundles the MCP tools | no (add `.mcp.json` yourself) | yes |
| Updates | you rebuild | `/plugin update` |
| Ways it can silently break | essentially none | dangling marketplace, stale cache, wrong scope |

**Pick Direct if you hack on this repo.** Pick Marketplace if you just want the memory and never
intend to touch the source.

### Option A — direct: one binary, three hooks

Nothing to register, nothing to cache, nothing to go stale. A hook either runs or it doesn't.

**1. Build and install the engine.** It lands on a stable path *outside* any git worktree:

```bash
cd v2/plugins/claude
make install                      # → ~/.local/bin/anhur-claude-memory
# PREFIX=/usr/local make install  # to put it elsewhere
```

**2. Configure the environment** — see [Configure](#configure-the-environment) below. Do this first;
the hooks source that file.

**3. Wire the three hooks** into `~/.claude/settings.json` (user scope = memory in every project,
which is what you want for a *long-term memory*; use a project's `.claude/settings.json` to scope it
to one repo):

```jsonc
{
  "hooks": {
    "SessionStart": [
      { "matcher": "startup|resume|clear|compact",
        "hooks": [{ "type": "command", "timeout": 20,
          "command": ". $HOME/.anhur-claude-memory/env 2>/dev/null; $HOME/.local/bin/anhur-claude-memory recall" }] }
    ],
    "Stop": [
      { "hooks": [{ "type": "command", "timeout": 30,
          "command": ". $HOME/.anhur-claude-memory/env 2>/dev/null; $HOME/.local/bin/anhur-claude-memory persist" }] }
    ],
    "SessionEnd": [
      { "hooks": [{ "type": "command", "timeout": 45,
          "command": ". $HOME/.anhur-claude-memory/env 2>/dev/null; $HOME/.local/bin/anhur-claude-memory persist" }] }
    ]
  }
}
```

That is the whole integration. Details that matter:

- **`matcher` is a regex** — `startup|resume|clear|compact` covers all four SessionStart sources
  (fresh start, `--resume`, `/clear`, `/compact`). Omit `matcher` entirely and it also matches all.
  `Stop` and `SessionEnd` take no matcher.
- **Absolute paths, not `$PATH`.** Hooks run under a non-interactive shell that may not have
  `~/.local/bin` on `PATH`.
- **`. $HOME/.anhur-claude-memory/env`** is what loads the API key. `2>/dev/null` swallows the
  shell's error on stderr if that file is missing.
- **Never point a hook at a path inside a git worktree.** See [the trap](#the-trap-worktree-paths).

**4. Want the MCP tools too?** They are independent of the memory loop — add an `.mcp.json` to your
project (or `~/.claude.json`):

```json
{ "mcpServers": { "anhurdb": { "type": "http", "url": "https://anhurdb.yoven.ai/mcp" } } }
```

### Option B — marketplace plugin

In Claude Code:

```
/plugin marketplace add Yoven/AnhurDB-SDK
/plugin install anhurdb-memory@anhur
```

The `anhur` marketplace manifest is at the repo root, so the GitHub `owner/repo` shorthand works — no
clone needed. A committed wrapper (`bin/anhur-claude-memory`) auto-selects the right prebuilt binary
for your OS/arch, so there is **nothing to build**. This route also registers the AnhurDB MCP tools
via the bundled `.mcp.json`.

(The `anhur` marketplace also offers `anhurdb-memory-hermes` — the same engine pointed at a separate
tenant/container, for a second, isolated agent identity.)

Then configure the environment below and start a new session.

#### The trap: worktree paths

**Do not install this plugin from a `directory` marketplace pointing at a clone you actively develop
in** (`/plugin marketplace add .` from your working copy). A `directory` marketplace is read **in
place**: its `.claude-plugin/marketplace.json` must exist at the registered path on every load. Rename
or move that manifest — an ordinary refactor — and the marketplace goes dangling, the plugin stops
loading, and **every hook silently stops firing**. No error reaches the session; the memory just
quietly stops. That exact failure happened here, and it went unnoticed for hours.

If you develop this repo, use [Option A](#option-a--direct-one-binary-three-hooks): a binary at a
fixed path cannot be broken by a rebase. If you want to dogfood the real customer path, install from
GitHub (`Yoven/AnhurDB-SDK`) so your memory tracks *releases*, not your working tree.

Diagnose a suspected load failure with:

```bash
claude plugin list                        # look for: Status ✘ failed to load
claude plugin validate .                  # validate a marketplace/plugin manifest
```

### Configure the environment

The hooks `source $HOME/.anhur-claude-memory/env` before running, so that file (mode `0600`, **outside
any repo**) is the canonical place for your config. Create it:

```bash
install -m 700 -d "$HOME/.anhur-claude-memory"
umask 177
cat > "$HOME/.anhur-claude-memory/env" <<'EOF'
export ANHUR_API_KEY="anhur_…your_tenant_key…"
export ANHUR_URL="https://anhurdb.yoven.ai"
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
(verbatim transcript archive, default on), and **`ANHUR_MCP_URL`** — the MCP endpoint the bundled tools
connect to (default `https://anhurdb.yoven.ai/mcp`).

> **MCP tools on the Desktop app:** `${ANHUR_MCP_URL:-…}` in `.mcp.json` is expanded by the Claude
> Code **CLI** but **not** the macOS **Desktop app** — there the literal `${ANHUR_MCP_URL}` is sent and
> the MCP tools fail to connect. On Desktop, edit `.mcp.json` to a hardcoded URL. This only affects the
> optional MCP tools; the core recall/persist loop (which talks to `ANHUR_URL` via the SDK, not MCP) is
> unaffected either way.

### Start a new session

Hooks are registered **at session start**, so the setup takes effect in the *next* session, not the
one you configured it in. Open a new session: your AnhurDB memory arrives as an `<anhur-memory>`
block, and every turn persists from then on. That's it.

## Verify it works

Three checks, in ascending order of what they actually prove. **Only the third proves the memory
reaches the model** — the first two are necessary but routinely fooled anyone who stopped there.

### 1. The engine runs and can reach AnhurDB

```bash
. "$HOME/.anhur-claude-memory/env"
"$HOME/.local/bin/anhur-claude-memory" recall </dev/null    # direct install
```

Should print your `<anhur-memory>` block and exit 0. Diagnostics (never the key) go to
`$ANHUR_STATE_DIR/plugin.log` (default `~/.anhur-claude-memory/plugin.log`).

### 2. The hooks actually fire

The log cannot tell you this on its own — a line there proves only that *something* ran the binary,
and running it by hand looks identical to a hook running it. Correlate with a **session start**:

```bash
tail -3 ~/.anhur-claude-memory/plugin.log
# then start a NEW session, and tail again — a fresh `recall:` line must appear
# with a timestamp matching that session start. No new line = the hook never fired.
```

> **Why this matters.** A hook that never fires is invisible: no error, no warning, no empty block —
> the session simply proceeds without memory, exactly as if you had none. Absence of the block is the
> only symptom, and it is easy to miss. Nothing in the log says "I was not called".

### 3. The block reached the model — the only check that proves the loop

Ask the model something it can only answer from the block:

```bash
claude -p "Without using any tools: did you receive an <anhur-memory> block? \
If yes, quote its first Decision. If no, say NO BLOCK." </dev/null
```

If it quotes your memory back, the loop is closed: AnhurDB → hook → context → model. If it says
`NO BLOCK`, the hook isn't wired or isn't firing (go back to check 2) — regardless of what the log
claims.

### 4. Optional — confirm the cognitive layer is distilling

`recall` proves reading; to confirm AnhurDB is also distilling turns into typed memories, save a
sentence with a clear decision/fact, wait a few seconds (Smart Units are asynchronous), then recall:

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
Stop (each)  ─▶ persist ─▶ drain any queued turns, then save the new turn
                        └▶ (on failure: queue to disk)
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
  every subsequent `persist` and by `recall` at the next start.
- **A hook that never runs fails silently, and nothing here can warn you.** If the hook isn't wired,
  or the plugin fails to load, the session just starts without memory — no error, no empty block, no
  log line, because the engine was never executed. The plugin log can never rule this out either: it
  only ever proves *something* ran the binary, not that a hook did. This is the one failure mode the
  no-silent-loss queue does **not** cover — the queue protects writes once the engine is running; it
  cannot protect a process that is never started. Guard against it with
  [check 3](#3-the-block-reached-the-model--the-only-check-that-proves-the-loop): ask the model
  whether it got the block. Writes are safer than reads here: `persist` advances a per-session cursor,
  so once the hooks are restored the next run backfills every turn it missed — a dead hook costs you
  recall immediately, but not the record.

## Development

Build and iterate locally (needs **Go 1.24+**):

```bash
cd v2/plugins/claude
make build     # native binary → bin/anhur-claude-memory-<os>-<arch>  (inside the worktree)
make install   # build + copy to ~/.local/bin  (stable path, for the direct setup)
./test_e2e.sh  # end-to-end against the live AnhurDB in ~/.anhur-claude-memory/env
```

The engine lives in the shared `plugins/core` package so `claude` and `hermes` never drift — fix a
bug once, both get it. `go.mod` carries two `replace` directives (`../core` and `../../golang`), so
the plugin builds from within this monorepo.

**If you run your own memory off this repo, use `make install`, not `make deploy`.** `make deploy`
hand-copies the fresh binary into an installed plugin's cache; it exists for the marketplace route,
but it also *masks* the fact that the cache is a snapshot — every other file in it (manifest,
`.mcp.json`, README) stays stale, and the cache only ever refreshes on a version bump or a reinstall.
`make install` has no cache to desynchronise.

### Releasing

Delivery is **prebuilt per-platform binaries committed in `bin/`**, selected at runtime by the
`bin/anhur-claude-memory` wrapper — so a marketplace install needs no toolchain. Distribution is the
`anhur` marketplace git repo, not a package registry.

1. Bump `version` in `.claude-plugin/plugin.json` (semver). **This is load-bearing, not bookkeeping:**
   the version is what invalidates an installed cache. Ship a change without bumping it and
   `/plugin update` has nothing to compare — existing installs keep running the old copy forever, and
   nothing reports a problem.
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
