# AnhurDB memory for Hermes

Give a second, isolated agent identity — **Hermes** — a **persistent, sovereign long-term memory**
backed by [AnhurDB](https://anhur.yoven.ai).

This plugin is the **same engine** as the default [`anhurdb-memory`](../claude) plugin (they share
`github.com/anhurdb/anhur-memory-core` so they never drift). The only difference is **identity**: the
Hermes plugin reads/writes under its OWN tenant (via `ANHUR_API_KEY`), its own container
(`hermes-ltm`), and its own on-disk state dir (`~/.anhur-hermes-memory`). Install both side-by-side to
keep two separate memories that never mix.

- **Auto-recall** — at the start of every session, the Hermes AnhurDB profile (decisions, facts,
  preferences, recent topics) is injected into context. It wakes up remembering.
- **Auto-persist** — after every turn (and at session end) the new conversation is saved to AnhurDB
  (one memory per turn). AnhurDB's **Smart Units** then distill it into typed memories —
  `fact` / `preference` / `decision` / `risk` / `task` / `emotion` — whenever Smart Units are enabled
  on your AnhurDB (see [Structured memory](#structured-memory-smart-units) below).
- **No silent loss at the boundary** — if AnhurDB is unreachable when a turn ends, the turn is
  queued to disk and retried on the next persist or session start, whichever comes first. A crash risks at most the in-flight turn.
- **The key never touches the transcript** — it lives only in `ANHUR_API_KEY` (env), sent as the
  `X-API-Key` header. This honors AnhurDB's auth model: a master key for services, **one API key
  per tenant** — nothing else.

The engine is a **single static Go binary that dogfoods the official AnhurDB Go SDK**
(`github.com/Yoven/AnhurDB-SDK/v2/golang/v2`) — so it inherits the SDK's HTTP transport and error handling,
and has **zero runtime dependencies** (no python, no jq, no curl). The plugin also registers the
AnhurDB **MCP tools** for explicit recall/store during a session.

## What's in here

```
plugins/hermes/
├── .claude-plugin/plugin.json    # plugin manifest (name: anhurdb-memory-hermes)
├── .mcp.json                     # registers the AnhurDB MCP server (https://anhurdb.yoven.ai/mcp)
├── hooks/hooks.json              # SessionStart→recall, Stop+SessionEnd→persist
├── cmd/anhur-hermes-memory/      # thin main → core.Run with the Hermes identity
│   └── main.go
├── go.mod / Makefile             # build → bin/anhur-hermes-memory (static)
├── bin/anhur-hermes-memory       # built binary (run `make build`)
└── .env.example                  # configuration template

plugins/core/                     # the SHARED engine (imported by claude + hermes)
```

## Prerequisites

- An AnhurDB endpoint at `ANHUR_URL` — `https://anhurdb.yoven.ai`.
- **Go 1.24+** to build the binary once (the built binary itself needs nothing at runtime).
- A **per-tenant** AnhurDB API key for the Hermes tenant (not the master key) — the same key the MCP
  tools accept.
- For **structured memory** (decisions/facts/emotions — not just raw turns), your AnhurDB must have
  **Smart Units** enabled (its cognitive layer; on by default on the hosted plans). Without them,
  every turn is still saved, but nothing is distilled from it. See
  [Structured memory](#structured-memory-smart-units).

## Install

1. **Build the binary** (once):
   ```bash
   cd plugins/hermes && make build      # → bin/anhur-hermes-memory (static)
   ```

2. **Configure** the environment (see `.env.example`). At minimum:
   ```bash
   export ANHUR_API_KEY="anhur_…your_hermes_tenant_key…"
   export ANHUR_URL="https://anhurdb.yoven.ai"
   export ANHUR_CONTAINER="hermes-ltm"
   ```
   Put these where they reach the Claude Code process (shell profile, or a gitignored file you
   `source`). **Never commit the key.** The hooks `source $HOME/.anhur-hermes-memory/env` before
   running the binary, so that 0600 file (outside the repo) is the canonical place for these vars.

   > **`ANHUR_CONTAINER` is your memory profile — choose it once and keep it stable.** The API key
   > selects your *tenant*; `ANHUR_CONTAINER` names the memory profile **within** it that recall
   > reads from. If you change it later, recall stops surfacing what was saved under the old name —
   > nothing is lost (it's still there under the old name), it just isn't re-surfaced. So pick a
   > stable value now.

3. **Enable the plugin.** Register this directory as a Claude Code plugin. The hooks reference the
   binary via `${CLAUDE_PLUGIN_ROOT}/bin/anhur-hermes-memory`; if your Claude Code build exposes the
   plugin root under a different variable, adjust `hooks/hooks.json`.

4. **Start a new session.** On startup the Hermes AnhurDB memory is injected; after each turn it
   persists.

## Verify it works (without waiting for a session)

```bash
export ANHUR_API_KEY="…" ANHUR_URL="https://anhurdb.yoven.ai" ANHUR_CONTAINER="hermes-ltm"

# Recall: prints your <anhur-memory> block.
./bin/anhur-hermes-memory recall </dev/null

# Persist: feed it a fake Stop payload pointing at a JSONL transcript.
echo '{"session_id":"test","transcript_path":"/path/to/a.jsonl"}' | ./bin/anhur-hermes-memory persist
```

Diagnostics (never the key) go to `$ANHUR_STATE_DIR/plugin.log` (default `~/.anhur-hermes-memory/plugin.log`).

## How the memory loop works

```
SessionStart ─▶ recall  ─▶ flush any turns queued from a previous offline moment
                        └▶ read your profile, inject the <anhur-memory> block
   …turns…
Stop (each)  ─▶ persist ─▶ save the new turn  (on failure: queue to disk; every later persist retries)
SessionEnd   ─▶ persist ─▶ final flush of any remaining turns
```

Each saved turn becomes a memory in AnhurDB. From there AnhurDB's **Smart Units** distill it into
typed memories, keep them current, and retire contradicted facts so recall stays accurate over time —
see [Structured memory](#structured-memory-smart-units).

## Structured memory (Smart Units)

Saving your turns is only half of it. Turning them into typed memories you can recall — `fact`,
`preference`, `decision`, `risk`, `task`, `emotion` — is done by AnhurDB's **Smart Units (SUs)**, its
cognitive layer. The plugin saves every turn no matter what; the Smart Units distill it.

**This is the most common "why is my memory empty?" surprise.** If Smart Units aren't enabled on your
AnhurDB, your turns are still saved safely, but recall stays thin — you'll see few or no
Decisions/Facts in the `<anhur-memory>` block, because nothing has been distilled yet.

- Enable Smart Units on your AnhurDB (hosted plans have them on by default).
- Distillation is **asynchronous** — a saved turn becomes typed memories a short while later, not
  instantly. Recall right after saving may not show them yet; check again shortly.
- **Nothing is lost while you wait** — your raw turns are durable in AnhurDB, and the Smart Units
  catch up.

## Relationship to the `anhurdb-memory` (Claude) plugin

Both plugins are the exact same engine (`plugins/core`). The seam between them is a three-field
`core.Config` in each plugin's `cmd/.../main.go`:

| Field              | claude                    | hermes                    |
| ------------------ | ------------------------- | ------------------------- |
| `StateDirName`     | `.anhur-claude-memory`    | `.anhur-hermes-memory`    |
| `DefaultContainer` | `claude-ltm`              | `hermes-ltm`              |
| `BinaryName`       | `anhur-claude-memory`     | `anhur-hermes-memory`     |

Everything else — the recall/persist loop, the no-silent-loss disk queue, tool-block handling, chunk
splitting — is shared, so a fix to either plugin's behaviour is made once in `plugins/core`.

## Building / distributing

`make build` uses `CGO_ENABLED=0` for a fully static binary. For a standalone build outside this
monorepo, drop the `replace` directives in `go.mod` and `go get` the core + SDK modules, then
`make build`.

## Security

The API key is read from `ANHUR_API_KEY` and sent only as the `X-API-Key` header by the SDK. It is
never echoed to stdout/stderr, written to the plugin log, or placed in the transcript. Use a
per-tenant key scoped to exactly the memory this agent should see.
