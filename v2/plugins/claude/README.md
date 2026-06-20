# AnhurDB memory for Claude Code

Give Claude Code a **persistent, sovereign long-term memory** backed by [AnhurDB](https://anhur.cloud).

- **Auto-recall** — at the start of every session, your AnhurDB profile (decisions, facts,
  preferences, recent topics) is injected into Claude's context. It wakes up remembering.
- **Auto-persist** — after every turn (and at session end) the new conversation is ingested into
  AnhurDB, where the cognitive pipeline extracts facts/entities/relations automatically.
- **No silent loss at the boundary** — if AnhurDB is unreachable when a turn ends, the turn is
  queued to disk and retried on the next session start. A crash risks at most the in-flight turn.
- **The key never touches the transcript** — it lives only in `ANHUR_API_KEY` (env), sent as the
  `X-API-Key` header. This honors AnhurDB's auth model: a master key for services, **one API key
  per tenant** — nothing else.

The engine is a **single static Go binary that dogfoods the official AnhurDB Go SDK**
(`github.com/anhurdb/sdk-go/v2`) — so it inherits the SDK's retries and read-your-writes plumbing,
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
   `source`). **Never commit the key.**

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

## How the memory loop works

```
SessionStart ─▶ recall  ─▶ flush any queued chunks (retry)  ─▶ SDK Profile(container)
                                                             └▶ print <anhur-memory> to stdout (injected)
   …turns…
Stop (each)  ─▶ persist ─▶ read transcript lines since cursor ─▶ SDK Add(excerpt)  [→ /api/v1/ingest]
                                                              └▶ on failure: queue to disk, advance cursor
SessionEnd   ─▶ persist ─▶ final flush of any remaining lines
```

`Add` (no pinned score/type) routes to AnhurDB's ingest path, so the cognitive pipeline then
extracts entities/relations, consolidates, decays, and (via `supersede`) keeps contradicted facts
out of recall — the memory stays accurate over time.

## Honest limitations

- **`SessionEnd` does not fire on a hard crash / `kill -9`.** The per-turn `Stop` hook is the
  durable path; `SessionEnd` is only a final flush. Worst-case loss is the single in-flight turn.
- **`SessionEnd` may not provide the transcript path.** `Stop` does; `SessionEnd` falls back to the
  documented transcript location, best-effort. Rely on `Stop` for durability.
- **Extracted facts have the freshness of the pipeline.** Text you state is ingested immediately;
  the structured facts appear after the (async, LLM-bound) extraction runs.
- **Hooks aren't retried by Claude Code.** That's why persistence is queued to disk and retried by
  `recall` on the next start, rather than relying on the hook to retry.

## Building / distributing

`make build` uses `CGO_ENABLED=0` for a fully static binary. For a standalone build outside this
monorepo, drop the `replace` in `go.mod` and `go get github.com/anhurdb/sdk-go/v2`, then `make build`.

## Security

The API key is read from `ANHUR_API_KEY` and sent only as the `X-API-Key` header by the SDK. It is
never echoed to stdout/stderr, written to the plugin log, or placed in the transcript. Use a
per-tenant key scoped to exactly the memory this agent should see.
