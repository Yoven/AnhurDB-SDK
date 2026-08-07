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
- **A memory that stops saving says so** — a background monitor reads the engine's own log during
  the session and reports failures to Claude as they happen, instead of letting them accumulate in a
  file nobody opens. Three skills (`memory-health`, `memory-blackout`, `memory-recall`) turn that into
  an answer. See [Diagnosing](#diagnosing-is-the-memory-alive).
- **The key never touches the transcript** — it lives only in `ANHUR_API_KEY` (env), sent as the
  `X-API-Key` header. This honors AnhurDB's auth model: a master key for services, **one API key
  per tenant** — nothing else.

Underneath, this is **one static Go binary and three hooks** — `recall` on `SessionStart`, `persist`
on `Stop` and `SessionEnd`. That is the entire mechanism; the plugin is how it gets installed,
updated, and shipped, and [installing it](#option-a--the-plugin-recommended) is all you should need to
do.

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

**Install the plugin.** [Option A](#option-a--the-plugin-recommended) is the supported path and the
one to use. [Option B](#option-b--direct-one-binary-three-hooks) wires the same engine by hand — it
exists for setups that cannot take a marketplace (locked-down machines, config management), not as a
way to avoid the plugin. Both produce the **same memory**: same binary, same hooks, same records.

| | [Plugin](#option-a--the-plugin-recommended) | [Direct](#option-b--direct-one-binary-three-hooks) |
|---|---|---|
| What you manage | one install | one binary + three hooks |
| Needs a Go toolchain | no — prebuilt binaries ship | yes |
| Bundles the MCP tools | yes | no (add `.mcp.json` yourself) |
| Updates | `/plugin update` | you rebuild |

> **If you develop this plugin, run this plugin.** It is tempting to wire the binary directly and skip
> the marketplace — it has fewer moving parts, and the maintainers are the ones who know how. Do not.
> The people working on it are the only ones exercising it before customers do; take them off it and
> nobody is running what ships. That is not hypothetical: this plugin once stopped loading entirely and
> went unnoticed for hours precisely because its failure was silent and nobody was watching from the
> outside. Worse, running both at once double-writes every turn and fragments the memory.

### Option A — the plugin (recommended)

In Claude Code:

```
/plugin marketplace add Yoven/AnhurDB-SDK
/plugin install anhurdb-memory@anhur
```

The `anhur` marketplace manifest is at the repo root, so the GitHub `owner/repo` shorthand works — no
clone needed. A committed wrapper (`bin/anhur-claude-memory`) auto-selects the right prebuilt binary
for your OS/arch, so there is **nothing to build**. This also registers the AnhurDB MCP tools via the
bundled `.mcp.json`.

Install at **user scope** unless you have a reason not to: this is a *long-term memory*, and scoping it
to one repository means it forgets everywhere else.

(The `anhur` marketplace also offers `anhurdb-memory-hermes` — the same engine pointed at a separate
tenant/container, for a second, isolated agent identity.)

Then [configure the environment](#configure-the-environment) and start a new session.

#### Working on the plugin itself

Point the marketplace at your clone (`/plugin marketplace add /path/to/AnhurDB-SDK`) and know the two
rules that bite:

- **An install is a snapshot.** The plugin is *copied* into `~/.claude/plugins/cache/` at install time.
  Editing the source changes nothing about what runs. To ship a change: `make release-binaries`, **bump
  `version` in `.claude-plugin/plugin.json`** (the version is what invalidates the cache), reinstall.
- **A `directory` marketplace reads the live worktree.** Its `.claude-plugin/marketplace.json` must
  exist at the registered path on every load. Move or rename it — an ordinary refactor — and the
  marketplace goes dangling, the plugin stops loading, and **every hook silently stops firing**. No
  error reaches the session; the memory just quietly stops. That exact failure happened here and went
  unnoticed for hours. Diagnose it with `claude plugin list` (look for `✘ failed to load`).

Prefer your memory to track *releases* rather than your working tree? Install from GitHub
(`Yoven/AnhurDB-SDK`) — then a rebase can never touch it.

### Option B — direct: one binary, three hooks

For machines that cannot take a marketplace. Same engine, wired by hand — **not** a way to skip the
plugin (see the warning above).

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
- **Never point a hook at a path inside a git worktree.** A rebase, rename, or branch switch then
  breaks the memory silently. That is why step 1 copies the binary out of the repo.

**4. Want the MCP tools too?** They are independent of the memory loop — add an `.mcp.json` to your
project (or `~/.claude.json`):

```json
{ "mcpServers": { "anhurdb": { "type": "http", "url": "https://anhurdb.yoven.ai/mcp" } } }
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

## What ships in the plugin

Every component lives at the **plugin root**; only `plugin.json` goes inside `.claude-plugin/`
([plugin structure](https://code.claude.com/docs/en/plugins#plugin-structure-overview)).

| Path | What it is | Why it exists |
| --- | --- | --- |
| `.claude-plugin/plugin.json` | Manifest: name, version, monitor path | `version` is what invalidates an installed cache — bump it or nothing ships |
| `hooks/hooks.json` | `SessionStart` → `recall`, `Stop` + `SessionEnd` → `persist` | the memory loop itself |
| `bin/` | the engine: a wrapper + one prebuilt binary per OS/arch | a marketplace install needs no Go toolchain |
| `.mcp.json` | the AnhurDB MCP server (`ANHUR_MCP_URL`) | explicit tool access for *you*, not for the model (see below) |
| `monitors/monitors.json` | one background monitor: `anhur-memory-health` | makes a memory that stopped saving **visible during the session** |
| `scripts/anhur-memory-watch.sh` | the monitor: POSIX `sh`, no key, no network | reports on the engine **without going through it** |
| `scripts/anhur-memory-watch_test.sh` | offline tests for the monitor (`sh scripts/anhur-memory-watch_test.sh`) | the blackout happened in untested code; this part is tested |
| `skills/memory-health/` | `/anhurdb-memory:memory-health` | "is my memory alive right now?", with evidence |
| `skills/memory-blackout/` | `/anhurdb-memory:memory-blackout` | the outage runbook: five failure classes, one fix each |
| `skills/memory-recall/` | `/anhurdb-memory:memory-recall` | re-read the memory profile mid-session |

Deliberately **not** shipped:

- **`agents/`** — a subagent would need its own reason to exist; diagnosis is a procedure, and a
  procedure is a skill.
- **`settings.json`** — only the `agent` and `subagentStatusLine` keys are supported there, and this
  plugin ships no agent, so the file would be an empty gesture.
- **`commands/`** — the legacy flat-file form of skills. New plugins use `skills/`.
- **Topic-scoped semantic recall as a skill** — every `mcp__anhurdb__*` tool takes `api_key` as a
  **required argument**, and this plugin persists the transcript *into AnhurDB*: a key in the model's
  context would be stored in the memory that key protects. A query subcommand on the engine (which
  holds the key and never prints it) is the honest way to add it.

Context cost of the surface: about **560 tokens** per session for the three skill descriptions
(`claude plugin details anhurdb-memory` prints the current number). The monitor costs nothing until
it has something to say.

### The background monitor

`monitors/monitors.json` declares one monitor, started automatically whenever the plugin is active
in an interactive session. Each line it prints on stdout is delivered to Claude as a notification
mid-session ([monitors reference](https://code.claude.com/docs/en/plugins-reference#monitors)).

It watches `$ANHUR_STATE_DIR/plugin.log` (default `~/.anhur-claude-memory/plugin.log`) and speaks
in exactly three situations:

1. **Failures since the last successful AnhurDB call** — summarised in one line at session start.
   Failures *older* than the last success are history and stay quiet, so a healed outage does not
   re-announce itself forever.
2. **A new failure while you work** — the first of its kind speaks immediately, then every 25th
   repeat (capped at 40 notifications per session, so a loop cannot flood the context).
3. **Silence that means something** — the engine running for 48h without a single successful call,
   or no `plugin.log` at all within the first four minutes of a session.

It is deliberately quiet otherwise: the `<anhur-memory>` block *is* the proof that recall worked, so
a heartbeat notification would only cost context. Tunables, if you need them: `ANHUR_WATCH_SCAN_LINES`,
`ANHUR_WATCH_STALE_MINUTES`, `ANHUR_WATCH_POLL_SECONDS`, `ANHUR_WATCH_REPEAT_EVERY`,
`ANHUR_WATCH_MAX_NOTIFICATIONS`, `ANHUR_WATCH_MISSING_LOG_SECONDS`, `ANHUR_WATCH_STARTUP_DELAY_SECONDS`.

> **Those variables — and `ANHUR_STATE_DIR` — must be in the environment Claude Code itself runs in,
> not in `~/.anhur-claude-memory/env`.** The monitor never reads that file: it holds the API key, and
> a watchdog has no business opening it. It resolves the state directory exactly the way the engine
> does (`$ANHUR_STATE_DIR`, else `$HOME/.anhur-claude-memory`), so relocating the state dir *only*
> inside the env file leaves both of them looking at the same wrong place — which is the safe way to
> be wrong.

> **The monitor cannot report its own absence.** If the plugin fails to load, Claude Code never starts
> the monitor either — silence then means nothing at all. That class is covered by
> [check 3](#3-the-block-reached-the-model--the-only-check-that-proves-the-loop) alone.

### The skills

| Skill | Ask it when | What it does |
| --- | --- | --- |
| `/anhurdb-memory:memory-health` | "is your memory working?", "are you saving this?", or a monitor notification arrived | six checks, cheapest first, ending in a verdict: `ALIVE` / `DEGRADED` / `DEAD` |
| `/anhurdb-memory:memory-blackout` | the memory block never arrived, or health said DEGRADED/DEAD | walks the five failure classes with a discriminator for each, and says what was actually lost |
| `/anhurdb-memory:memory-recall` | "what do you remember about X", after a long session | re-runs the engine's recall and reports the profile, including any backlog warning |

All three are read-only, and all three are forbidden from printing the API key: they inspect the env
file's permissions and variable *names*, never its values.

## Verify it works

Three checks, in ascending order of what they actually prove. **Only the third proves the memory
reaches the model** — the first two are necessary but routinely fooled anyone who stopped there.

### 1. The plugin loads, and its engine can reach AnhurDB

```bash
claude plugin list          # anhurdb-memory@anhur must say: Status ✔ enabled
```

`✘ failed to load` means no hook is registered and the memory is dead — the most common cause is a
dangling `directory` marketplace. Then run the same binary the hooks run:

```bash
# pick the newest installed copy — the cache keeps every version you have installed, so a bare
# glob would expand to several paths and the shell would pass them to the binary as arguments
engine=$(ls -1d "$HOME"/.claude/plugins/cache/anhur/anhurdb-memory/*/bin/anhur-claude-memory 2>/dev/null | sort -V | tail -n 1)
"$engine" recall </dev/null
```

Should print your `<anhur-memory>` block and exit 0. (The engine reads
`~/.anhur-claude-memory/env` itself since 2026-07-30 — no `source` needed, and sourcing a file
whose lines lack `export` never worked anyway. That is [class C](#the-five-ways-it-breaks).) Diagnostics (never the key) go to
`$ANHUR_STATE_DIR/plugin.log` (default `~/.anhur-claude-memory/plugin.log`).

### 2. The hooks actually fire

The log cannot tell you this on its own — a line there proves only that *something* ran the binary,
and running it by hand (as in check 1) looks identical to a hook running it. Correlate with a
**session start**:

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
SESSION=$(curl -s -X POST "$ANHUR_URL/api/v1/sessions" -H "X-API-Key: $ANHUR_API_KEY" -H 'Content-Type: application/json' -d '{}' | jq -r .session_id)
curl -s -X POST "$ANHUR_URL/api/v1/ingest" -H "X-API-Key: $ANHUR_API_KEY" -H 'Content-Type: application/json' \
  -d '{"content":"Decision: we ship in June. Fact: the build uses Go 1.24.","container_tag":"'"$ANHUR_CONTAINER"'","session_id":"'"$SESSION"'"}'
# wait a few seconds, then recall — the decision/fact should appear in the <anhur-memory> block.
```

If it stays empty, Smart Units aren't enabled on your AnhurDB — see
[Structured memory](#structured-memory-smart-units).

## Diagnosing: is the memory alive?

> **Why this section exists.** Between 2026-07-18 and 2026-07-30 this plugin saved **nothing**: 743
> hook invocations, 743 skips, 12,8 days, every run exiting 0 with empty stdout and empty stderr. The
> proof was sitting in `plugin.log` the entire time — `ANHUR_API_KEY not set — skipping`, 743 times.
> Nobody read it, because nobody knew this was the thing to read. Two changes came out of that: the
> engine now [fails loud](#what-each-artifact-proves-and-what-it-does-not), and the monitor above
> reads the log so a human doesn't have to. This section is the rest of the answer — how to check,
> and what each check is worth.

### The 60-second check

Ask Claude:

```
/anhurdb-memory:memory-health
```

It runs the checks below in order and reports `ALIVE`, `DEGRADED`, or `DEAD` with the evidence.
If the verdict isn't `ALIVE`, `/anhurdb-memory:memory-blackout` triages *why*.

By hand, the same thing:

```bash
claude plugin list | grep -A3 anhurdb-memory          # 1. is the plugin even loaded?
tail -n 15 ~/.anhur-claude-memory/plugin.log          # 2. what does the engine say? (UTC stamps)
ls -1 ~/.anhur-claude-memory/queue/*.txt | wc -l      # 3. anything stuck? (queued ≠ lost)
ls -1 ~/.anhur-claude-memory/queue/quarantine/*.txt | wc -l   # 4. anything a human must resolve?
```

**Never `cat` `~/.anhur-claude-memory/env`.** Check its mode with `ls -l`, and list the variable
*names* with:

```bash
grep -o '^[[:space:]]*\(export \)\?[A-Za-z_][A-Za-z0-9_]*=' ~/.anhur-claude-memory/env \
  | sed 's/^[[:space:]]*//; s/^export //; s/=$//' | sort -u
```

Do **not** use `cut -d= -f1`: on a line with no `=` — a stray comment, a merge leftover — `cut`
echoes the whole line, and if that line happens to hold a secret it goes straight into the
transcript. The pattern above only ever emits text it matched as a variable *name*.

Whatever you print in a session is persisted into AnhurDB and archived verbatim on disk — that is
precisely where an API key must never be.

### The five ways it breaks

| Class | Discriminator | Meaning |
| --- | --- | --- |
| **A. Plugin not loaded** | `claude plugin list` shows `✘ failed to load` or nothing, and no new log line at session start | no hook is registered; nothing recalls, nothing saves. Usually a dangling `directory` marketplace |
| **B. Hooks not firing** | plugin enabled, but a new session adds no log line, while running the binary by hand does | wrong binary path/arch, lost exec bit, or a hand-wired hook fighting the plugin |
| **C. Key not readable** | `ANHUR_API_KEY not set … MEMORY IS NOT BEING SAVED` in the log | the 2026-07 blackout. Transcripts are still archived to disk; recall is dead until fixed |
| **D. AnhurDB unreachable/rejecting** | `profile failed`, `CreateSession failed`, `flush still failing`, and `queue/` growing | degraded, **not** lost: every persist and every session start retries the queue |
| **E. Green but empty** | successful calls in the log, thin `<anhur-memory>` block | `ANHUR_CONTAINER` changed (old memories are under the old name), or Smart Units are off |

### What each artifact proves, and what it does not

| Artifact | Proves | Does **not** prove |
| --- | --- | --- |
| `plugin.log` has a fresh line | *something* executed the engine | that a **hook** did — running it by hand looks identical |
| The engine prints a block by hand | key, URL and AnhurDB are all good | that any of it reached the model |
| A monitor notification arrived | the plugin loaded, the monitor runs, and it found a failure | — |
| **No** monitor notification | either nothing is wrong, **or** the plugin never loaded | that the memory is healthy |
| The `<anhur-memory>` block in Claude's context | the whole loop closed: AnhurDB → hook → context → model | that *writes* are landing (ask about the queue for that) |

The last row is the only complete proof, and only the model can report it — a hook that never fires
leaves no trace anywhere else. That is [check 3](#3-the-block-reached-the-model--the-only-check-that-proves-the-loop),
and it is worth running after any change to the plugin, the marketplace, or the env file.

### Nothing is lost while it is broken

- **Queued chunks** (`~/.anhur-claude-memory/queue/*.txt`) are retried on every persist and every
  session start, back into their original session. A stuck queue is announced at the top of the next
  `<anhur-memory>` block, not just in the log.
- **Quarantined chunks** (`queue/quarantine/`) are the exception: their originating session could not
  be proven, and writing them elsewhere would merge two conversations — *1 Claude session = 1 AnhurDB
  session* is inviolable. They are never retried automatically and need a human.
- **The verbatim archive** (`~/.anhur-claude-memory/archive/<session>.jsonl`) is written on every
  persist, and — since 2026-07-30 — even when the key is missing. It is the lossless copy. Treat it
  as a recovery source, not as something to print: it holds full tool output.
- **Reads fail before writes do.** `persist` advances a per-session cursor, so once the hooks are
  restored the next run backfills every turn it missed. A dead hook costs you recall immediately, but
  not the record.

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

## What happens when AnhurDB is unreachable

Nothing is lost, and — just as important — nothing is written locally that did not have to be.

```
persist ─▶ try AnhurDB, up to 3 attempts          (a 200 ms leader election never touches disk)
        │  ├─ delivered ────────────────────────▶ done, no local state at all
        │  └─ refused (401 / 409 / 422) ────────▶ stop early: the server answered, retrying
        │                                          would only spend your turn to hear the same
        └─ no usable response after 3 ──────────▶ queue.db  (state: pending)

next persist / next SessionStart
        └─ drain oldest first ─▶ delivered ─────▶ row deleted; when the queue empties, VACUUM
                              └▶ failed ────────▶ back to pending, with the reason and a
                                                  doubling backoff (capped at 30 min)
```

**`~/.anhur-claude-memory/queue.db`** — the queue, with explicit state per item: `pending`,
`sending`, `quarantined`, plus `retry_count`, `last_error` and `next_attempt_at`. This replaced
a scheme that encoded everything in the *file name*, where a chunk failing for ten days looked
exactly like one queued a second ago.

Rules worth knowing, because each one is a scar:

- **A failure blocks only its own session.** Inside a conversation the order *is* the
  conversation, so turn 7 never overtakes a stuck turn 5. Across conversations nothing is
  blocked — one session hitting its record cap used to freeze the entire queue.
- **There is no retry limit.** "Tried too many times" must never become "threw it away".
  A dead backend costs one attempt per session per drain, not one per item.
- **Quarantine is for chunks with no provable session** — never for an outage. They are kept
  whole, never sent, and reported in every `<anhur-memory>` block until you decide. Writing
  one under a guessed session id would merge two conversations, which is unrecoverable.
- **If `queue.db` itself cannot be opened**, the turn is written as a plain `.txt` under
  `queue/` and migrated in on the next run. The floor never depends on the thing above it.
- **The verbatim transcript archive is independent of all of this** and is written before any
  of it, so even a total failure of both queues leaves a lossless copy on disk.

The Hermes Agent provider implements the identical rule in Python, and
[`../tests/parity`](../tests/parity) fails if the two ever drift.

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
- **A plugin that never loads cannot report itself.** If the plugin fails to load, the session starts
  without memory *and* without the monitor — no error, no empty block, no log line, because nothing
  was executed. The plugin log can never rule this out either: it only ever proves *something* ran the
  binary, not that a hook did. This is the one failure mode neither the no-silent-loss queue nor the
  monitor covers — the queue protects writes once the engine is running, and the monitor only runs
  when the plugin loaded. Guard against it with
  [check 3](#3-the-block-reached-the-model--the-only-check-that-proves-the-loop): ask the model
  whether it got the block. Writes are safer than reads here: `persist` advances a per-session cursor,
  so once the hooks are restored the next run backfills every turn it missed — a dead hook costs you
  recall immediately, but not the record.
- **The monitor is quiet by design, and quiet is ambiguous.** It reports failures, staleness, and a
  log that never appears; it says nothing when things are fine, so silence alone is not evidence of
  health. It also runs only in interactive CLI sessions (the Monitor mechanism is unavailable on
  Bedrock, Vertex/Agent Platform and Foundry, and when `DISABLE_TELEMETRY` or
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is set) — on those hosts the memory still works and the
  in-session alarm simply does not exist.
- **Notifications are capped.** At most 40 per session, with repeats of one failure kind collapsed to
  every 25th occurrence. A pathological loop cannot flood the context — but it also means the log
  remains the complete record.

## Development

Build and iterate locally (needs **Go 1.24+**):

```bash
cd v2/plugins/claude
make build                              # native binary → bin/anhur-claude-memory-<os>-<arch>
./test_e2e.sh                           # end-to-end against the live AnhurDB in ~/.anhur-claude-memory/env
sh scripts/anhur-memory-watch_test.sh   # the monitor's own suite — offline, no key, no network
claude plugin validate . --strict       # manifest + component paths
claude plugin details anhurdb-memory    # what the installed copy actually exposes, and its token cost
make deploy                             # push this build into the installed plugin's cache (see below)
```

The monitor suite is offline on purpose: it builds synthetic `plugin.log` fixtures — including the
743-skip blackout — and asserts what the monitor says about each. **When `plugins/core/core.go` adds
or renames a failure log line, update `failure_pattern` in `scripts/anhur-memory-watch.sh` in the same
commit**, or the alarm goes quiet for that failure without anything failing.

The engine lives in the shared `plugins/core` package so `claude` and `hermes` never drift — fix a
bug once, both get it. `go.mod` carries two `replace` directives (`../core` and `../../golang`), so
the plugin builds from within this monorepo.

**Run the plugin while you work on it.** Its failures are silent by nature, so the people building it
are the only ones who will catch them before customers do. Do not wire the binary by hand to avoid the
marketplace — you would take the product out of test and, if you leave both wired, double-write every
turn.

**`make deploy` is a debugging shortcut, not a way to ship.** It hand-copies your fresh binary over the
installed plugin's cached one, which is handy for a tight edit-run loop — but it *masks* the fact that
the cache is a **snapshot**: every other file in it (manifest, `.mcp.json`, README) stays stale, and it
only truly refreshes on a version bump or a reinstall. Trusting `deploy` is how the installed cache sat
a month behind the source. To validate what a user will actually get, bump `version` and reinstall.

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

The surface added around the engine keeps that property:

- **The monitor never opens the env file.** It reads only `plugin.log`, which the engine writes
  without ever including the key (it logs the key's *source* — `file` / `environment` / `missing` —
  never its value). It makes no network call and holds no credential.
- **The skills inspect the env file's mode and variable NAMES only**, with a `grep -o` that can only
  emit text matching `NAME=`. They are instructed never to `cat` it and never to echo
  `$ANHUR_API_KEY` — because whatever a session prints is persisted into AnhurDB *and* archived
  verbatim under `~/.anhur-claude-memory/archive/`.
- **The bundled MCP tools are not usable by the model, on purpose.** Every `mcp__anhurdb__*` tool
  takes `api_key` as a required argument; supplying one would write the key into the transcript that
  this plugin saves into the memory the key protects. They are there for *you*, from your own client.
- **Treat the archive as sensitive.** It is the verbatim transcript, tool output included. It is a
  recovery source, not something to print into a session.
