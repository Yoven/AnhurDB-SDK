# AnhurDB memory for Claude Code

Give Claude Code a **persistent, sovereign long-term memory** backed by [AnhurDB](https://anhur.cloud).

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
(`github.com/anhurdb/sdk-go/v2`) — so it inherits the SDK's HTTP transport and error handling,
and has **zero runtime dependencies** (no python, no jq, no curl). The plugin also registers the
AnhurDB **MCP tools** for explicit recall/store during a session.

## What's in here

```
plugins/claude/
├── .claude-plugin/plugin.json    # plugin manifest
├── .mcp.json                     # registers the AnhurDB MCP server (anhur-mcp, SSE :8090)
├── hooks/hooks.json              # SessionStart→recall, Stop+SessionEnd→persist
├── cmd/anhur-claude-memory/      # the hook engine (Go, uses sdk-go/v2)
│   └── main.go
├── go.mod / Makefile             # build → bin/anhur-claude-memory (static)
├── bin/anhur-claude-memory       # built binary (run `make build`)
└── .env.example                  # configuration template
```

## Prerequisites

- A running AnhurDB stack reachable at `ANHUR_URL` (the local docker-compose, or your deployment).
- **Go 1.24+** to build the binary once (the built binary itself needs nothing at runtime).
- A **per-tenant** AnhurDB API key (not the master key) — the same key the MCP tools accept.
- For **structured memory** (decisions/facts/emotions — not just raw turns), your AnhurDB must have
  **Smart Units** enabled (its cognitive layer; on by default on the hosted plans). Without them,
  every turn is still saved, but nothing is distilled from it. See
  [Structured memory](#structured-memory-smart-units).

## Install

1. **Build the binary** (once):
   ```bash
   cd plugins/claude && make build      # → bin/anhur-claude-memory (static)
   ```

2. **Configure** the environment (see `.env.example`). At minimum:
   ```bash
   export ANHUR_API_KEY="anhur_…your_tenant_key…"
   export ANHUR_URL="http://localhost:8000"
   export ANHUR_CONTAINER="claude-ltm"
   ```
   Put these where they reach the Claude Code process (shell profile, or a gitignored file you
   `source`). **Never commit the key.** The hooks `source $HOME/.anhur-claude-memory/env` before
   running the binary, so that 0600 file (outside the repo) is the canonical place for these vars.

   > **`ANHUR_CONTAINER` is your memory profile — choose it once and keep it stable.** The API key
   > selects your *tenant*; `ANHUR_CONTAINER` names the memory profile **within** it that recall
   > reads from. If you change it later, recall stops surfacing what was saved under the old name —
   > nothing is lost (it's still there under the old name), it just isn't re-surfaced. So pick a
   > stable value now.

3. **Enable the plugin.** Register this directory as a Claude Code plugin. The hooks reference the
   binary via `${CLAUDE_PLUGIN_ROOT}/bin/anhur-claude-memory`; if your Claude Code build exposes the
   plugin root under a different variable, adjust `hooks/hooks.json`.

4. **Start a new session.** On startup your AnhurDB memory is injected; after each turn it persists.

## Verify it works (without waiting for a session)

```bash
export ANHUR_API_KEY="…" ANHUR_URL="http://localhost:8000" ANHUR_CONTAINER="claude-ltm"

# Recall: prints your <anhur-memory> block.
./bin/anhur-claude-memory recall </dev/null

# Persist: feed it a fake Stop payload pointing at a JSONL transcript.
echo '{"session_id":"test","transcript_path":"/path/to/a.jsonl"}' | ./bin/anhur-claude-memory persist
```

Diagnostics (never the key) go to `$ANHUR_STATE_DIR/plugin.log` (default `~/.anhur-claude-memory/plugin.log`).

**Verify the *full* loop — not just storage.** `recall`/`persist` only prove the turn is being saved.
To confirm AnhurDB is also distilling it into typed memories, save a sentence with a clear
decision/fact, wait a few seconds (Smart Units work asynchronously), then recall:

```bash
# 1. save one memory (the same way the plugin does)
curl -s -X POST "$ANHUR_URL/api/v1/ingest" -H "X-API-Key: $ANHUR_API_KEY" -H 'Content-Type: application/json' \
  -d '{"content":"Decision: we will ship in June. Fact: the build uses Go 1.24.","container_tag":"'"$ANHUR_CONTAINER"'"}'

# 2. wait a few seconds, then recall — the decision/fact should now be in the <anhur-memory> block:
./bin/anhur-claude-memory recall </dev/null
```

If step 2 stays empty, Smart Units aren't enabled on your AnhurDB — see
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
typed memories, keep them current, and retire contradicted facts so recall stays accurate over time —
see [Structured memory](#structured-memory-smart-units).

## Structured memory (Smart Units)

Saving your turns is only half of it. Turning them into typed memories you can recall — `fact`,
`preference`, `decision`, `risk`, `task`, `emotion` — is done by AnhurDB's **Smart Units (SUs)**, its
cognitive layer. The plugin saves every turn no matter what; the Smart Units distill it.

**This is the most common "why is my memory empty?" surprise.** If Smart Units aren't enabled on your
AnhurDB, your turns are still saved safely, but recall stays thin — you'll see few or no
Decisions/Facts in the `<anhur-memory>` block, because nothing has been distilled yet.

- Enable Smart Units on your AnhurDB (see your AnhurDB setup guide; hosted plans have them on by
  default).
- Distillation is **asynchronous** — a saved turn becomes typed memories a short while later, not
  instantly. Recall right after saving may not show them yet; check again shortly.
- **Nothing is lost while you wait** — your raw turns are durable in AnhurDB, and the Smart Units
  catch up.

## Honest limitations

- **`SessionEnd` does not fire on a hard crash / `kill -9`.** The per-turn `Stop` hook is the
  durable path; `SessionEnd` is only a final flush. Worst-case loss is the single in-flight turn.
- **`SessionEnd` may not provide the transcript path.** `Stop` does; `SessionEnd` falls back to the
  documented transcript location, best-effort. Rely on `Stop` for durability.
- **Structured memories aren't instant.** Your turns are saved immediately; the typed
  facts/decisions appear a short while later, after the Smart Units distill them.
- **Hooks aren't retried by Claude Code.** That's why persistence is queued to disk and retried by
  `recall` on the next start, rather than relying on the hook to retry.

## Building / distributing

`make build` uses `CGO_ENABLED=0` for a fully static binary. For a standalone build outside this
monorepo, drop the `replace` in `go.mod` and `go get github.com/anhurdb/sdk-go/v2`, then `make build`.

## Security

The API key is read from `ANHUR_API_KEY` and sent only as the `X-API-Key` header by the SDK. It is
never echoed to stdout/stderr, written to the plugin log, or placed in the transcript. Use a
per-tenant key scoped to exactly the memory this agent should see.
