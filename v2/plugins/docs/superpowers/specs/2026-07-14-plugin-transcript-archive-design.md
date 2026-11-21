# Design — Durable verbatim transcript archive for the Claude memory plugin

- **Date:** 2026-07-14
- **Status:** Approved (pending spec review)
- **Scope:** `AnhurDB-SDK/v2/plugins/core` (+ `claude/Makefile`, `claude/test_e2e.sh`)

## Motivation

The plugin's `persist` path deliberately filters what it sends to AnhurDB: it keeps
user/assistant **text** and **compact tool calls** (input truncated to 200 chars), and
**drops** thinking blocks and tool results. That filter is correct for the cortex — the
extraction pipeline (LLM) should not be flooded with bash output or internal reasoning,
which is exactly what caused the documented `max_session_records=500` incident and Ollama
saturation.

The user wants a **complete verbatim record** of every session — thinking + full,
untruncated tool I/O — preserved durably and retrievably, **without** changing what the
cortex sees. In other words: keep the clean semantic feed as-is, and add a parallel
lossless archive.

Key fact that makes this cheap: Claude Code already writes the full verbatim transcript to
a JSONL file, and the `persist` hook already receives its path (`transcript_path` on stdin,
resolved by `resolveTranscript`). So the archive is a **file copy**, not a re-derivation.

Rejected alternative: uploading the transcript via `/api/v1/upload` — that triggers
`file_ingestor` (`file.uploaded` event), which chunks the file into **episodic** records
and feeds extraction. That defeats "don't feed the cortex."

## Goal / Non-goals

**Goal:** On every `persist` (Stop/SessionEnd), copy the complete session transcript to a
durable archive directory, atomically, keyed by session. Fidelity: byte-identical to the
transcript (thinking + tools + everything).

**Non-goals (YAGNI):**
- Indexing the archive into AnhurDB search/recall (that is Approach B — deferred; would
  require pipeline-exemption work across the agents, e.g. `entity_tagger` fires on
  `record.created` for any type).
- Compression, retention/pruning, encryption of the archive.
- Any change to the episodic/cortex feed.

## Design

### Component

New self-contained function in `core/core.go`:

```
func archiveTranscript(cfg config, sessionID, transcriptPath string)
```

Called from `cmdPersist` immediately after `sessionID` is resolved (right after the
`sessionID == ""` fallback, before `readLines`) so it runs on **every** persist,
independent of the episodic delta (a session with no new dialogue lines still refreshes the
archive). It reuses the transcript-path resolution `cmdPersist` already performs.

### Config (`loadConfig` / `config` struct)

Two new fields, defaults-ON, following the project's "capabilities default ON in code,
minimal flags" rule:

| Env var | Default | Meaning |
|---|---|---|
| `ANHUR_ARCHIVE` | `true` (disabled only by literal `false`) | master toggle |
| `ANHUR_ARCHIVE_DIR` | `<stateDir>/archive` | archive location; point at NFS / `ANHUR_STORAGE_PATH` for real durability |

`config` gains `archive bool` and `archiveDir string`; `loadConfig` reads
`ANHUR_ARCHIVE != "false"` and `envOr("ANHUR_ARCHIVE_DIR", filepath.Join(stateDir, "archive"))`.

### Data flow

1. If `!cfg.archive`, return immediately.
2. `os.MkdirAll(cfg.archiveDir, 0o700)`.
3. Destination: `<archiveDir>/<sanitize(sessionID)>.jsonl` — keyed by session id, so a
   re-copy **overwrites** the same file. The archive therefore always holds the latest
   complete transcript; at session end it is final. No delta/cursor logic → no
   loss/dup/ordering bugs.
4. **Atomic copy**: stream-copy (`io.Copy`) source → `<dest>.tmp` in the same directory,
   then `os.Rename(tmp, dest)` (atomic on one filesystem). Streaming (not `ReadFile`) so a
   large transcript is not held whole in memory.
5. File mode `0o600`, dir `0o700` — the archive holds verbatim secrets (matching the
   plugin's existing 0600 convention; the user explicitly accepts secrets in the archive).

### Error handling / isolation

`archiveTranscript` is best-effort and fully isolated: every failure path calls `logLine`
and returns. It **never** affects the episodic persist, the queue, or the exit code (the
hook still exits 0). The durable cortex feed is untouched. A failed `.tmp` write is cleaned
up (`os.Remove(tmp)`), consistent with the "fail loud in the log, never crash the session"
principle.

### Deploy friction fix (`claude/Makefile`)

New `deploy` target to end the recurring "rebuilt but the cache binary went stale" gotcha:

```
deploy: build
	# discover the installed cache binary (version-agnostic) and overwrite it
	cp $(BINARY) "$$(ls $(HOME)/.claude/plugins/cache/anhur/anhurdb-memory/*/bin/anhur-claude-memory | head -1)"
```

If no cache binary is found the `cp` fails loudly. After this, redeploy is one command
(`make deploy`) and the hooks never run a stale build.

## Testing

**Unit (`core/core_test.go`) — `TestArchiveTranscript`:**
- Write a temp transcript containing text + a `thinking` block + a `tool_result`.
- `archiveTranscript` with `cfg{archive:true, archiveDir:<temp>}` → dest exists,
  byte-identical to source, mode `0600`.
- Call again → still identical (idempotent overwrite).
- `cfg{archive:false}` → no file created.

**E2E (`claude/test_e2e.sh`) — new phase:**
- Extend the synthetic transcript with a `thinking` block and a `tool_result`.
- After `persist`, assert `$ANHUR_ARCHIVE_DIR/<session>.jsonl` exists, `cmp` byte-identical
  to the synthetic transcript, and `grep` confirms the **thinking + tool_result are
  present** in the archive — proving full fidelity, in contrast to the filtered episodic
  (which drops them). The archive dir defaults under the already-isolated temp state dir.

## Rollout

1. Implement `archiveTranscript` + config + tests.
2. `make deploy` (build + sync cache).
3. Optionally set `ANHUR_ARCHIVE_DIR` in `~/.anhur-claude-memory/env` to point at durable
   storage (NFS).
4. Validate with `./test_e2e.sh`.

Behaviour after this change is env-tunable only — no further code changes to use or tune the
archive.
