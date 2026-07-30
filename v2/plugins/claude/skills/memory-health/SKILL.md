---
description: Check whether Claude Code's AnhurDB long-term memory is actually alive right now — plugin loaded, hooks firing, key readable, AnhurDB reachable, nothing stuck on disk. Use when the user asks "is your memory working?", "are you saving this?", "did you remember X?", when an anhur-memory failure notification arrives, or before relying on this session being remembered.
when_to_use: Triggered by "is my memory working", "are you saving this conversation", "check the memory", "anhur memory status", by any anhur-memory notification during the session, or before a long session whose contents must survive.
---

# Memory health check

Answer one question with evidence: **is this session being remembered?**

Report a verdict — `ALIVE`, `DEGRADED`, or `DEAD` — and the evidence for it. Never say
"looks fine" without naming the check that proved it.

## Rule 0 — the key must never reach the transcript

The API key lives only in `~/.anhur-claude-memory/env` (mode `0600`). Everything you print
here ends up in the transcript, and the transcript is persisted into AnhurDB and archived
verbatim on disk.

- **Never** `cat`, `head`, `tail`, `grep -v`, or otherwise print that file.
- **Never** echo `$ANHUR_API_KEY`.
- To check the file, check its *existence, mode and variable NAMES* only — as below.

## The checks, cheapest first

Run these with Bash. Each one proves something different; say which.

**1. Is the plugin loaded at all?**

```bash
claude plugin list 2>/dev/null | grep -A3 anhurdb-memory
```

`✘ failed to load` (or absent) = **DEAD**: no hook is registered, so nothing is being saved
and nothing was recalled. Usual cause is a dangling `directory` marketplace. Stop here and
go to `/anhurdb-memory:memory-blackout`.

**2. What does the engine's own log say?**

```bash
tail -n 15 ~/.anhur-claude-memory/plugin.log
```

Timestamps are **UTC** (RFC3339). Compare the newest line against `date -u` and against
when this session started.

- A line at this session's start (`recall:` …) = the SessionStart hook fired.
- `ANHUR_API_KEY not set … MEMORY IS NOT BEING SAVED` = **DEAD** (key not readable).
- `profile failed`, `CreateSession failed`, `flush still failing` = **DEGRADED** (AnhurDB
  unreachable or rejecting; turns are queued, not lost).
- `ERROR: … quarantined` = **DEGRADED and not self-healing** — needs a human.
- Nothing newer than hours/days ago while sessions have been running = hooks are not firing.

**3. Is anything stuck on disk?**

```bash
ls -1 ~/.anhur-claude-memory/queue/*.txt 2>/dev/null | wc -l
ls -1 ~/.anhur-claude-memory/queue/quarantine/*.txt 2>/dev/null | wc -l
```

- Queue > 0: turns that never reached AnhurDB. **They are not lost** — every `persist` and
  every session start retries them. Report the number and that it should drain by itself.
- Quarantine > 0: chunks whose originating session could not be proven. These are **never**
  retried automatically, by design: writing them anywhere else would merge two conversations
  (1 Claude session = 1 AnhurDB session, inviolable). Only a human can resolve them — say so
  explicitly.

**4. Is the configuration readable?** (names and permissions only — never values)

```bash
ls -l ~/.anhur-claude-memory/env
grep -c . ~/.anhur-claude-memory/env
grep -o '^[[:space:]]*\(export \)\?[A-Za-z_][A-Za-z0-9_]*=' ~/.anhur-claude-memory/env | sed 's/^[[:space:]]*//; s/^export //; s/=$//' | sort -u
```

The `grep -o` form prints only text matching `NAME=` — a malformed line with no `=` (a stray
paste of a key, for instance) cannot slip through into the transcript. Never use plain
`cut -d= -f1`: on a line without `=`, `cut` echoes the whole line.

Expect mode `-rw-------`, and `ANHUR_API_KEY` among the names. The binary reads this file
itself, so `export` is optional (see `/anhurdb-memory:memory-blackout` for why that line
exists at all).

**5. Live probe — does the engine reach AnhurDB?** (network call; ask before running it)

```bash
"${CLAUDE_SKILL_DIR}/../../bin/anhur-claude-memory" recall </dev/null | head -n 20
```

That path is *this* installed copy of the plugin — deterministic. Do not reach for
`~/.claude/plugins/cache/anhur/anhurdb-memory/*/bin/…`: the glob matches every cached version
and the shell would pass the extra paths to the binary as arguments.

Prints an `<anhur-memory>` block = key, endpoint and AnhurDB are all good.

**Prints no block: do NOT read the exit code, and do NOT call it an empty memory.** The engine
always exits 0 (core.go:172-179) — 743 consecutive failures in the 2026-07 blackout all exited
cleanly, which is exactly why nobody noticed. Read `tail -n 3 ~/.anhur-claude-memory/plugin.log`
and name the line that explains it: `recall: profile failed` = AnhurDB unreachable;
`ANHUR_API_KEY not set` = the key could not be read. A non-zero exit means something else
entirely — the `bin/` wrapper found no binary for this OS/arch.

Two things to state honestly when you use it:
- It **flushes the queue** as a side effect (by design — it drains the backlog).
- It proves the *engine* works. It does **not** prove a hook ran it, and it does not prove
  the block reached you. Running it by hand looks identical in the log to a hook running it.

**6. The only check that proves the loop closed.**

Look at your own context for this session: is there an `<anhur-memory>` block in it?

State plainly whether you received one. This is evidence no log file can produce — a hook
that never fires leaves no trace at all — and you are the only witness to it.

## Verdict

| Verdict | Evidence |
| --- | --- |
| `ALIVE` | plugin enabled, a `recall:` line at this session's start, empty queue, block present in context |
| `DEGRADED` | engine running but queue/quarantine non-empty, or AnhurDB failing — say what is stuck, and that queued turns are retried, not lost |
| `DEAD` | plugin not loaded, key missing, or no log line for this session — nothing is being saved right now |

Finish with the single next action. If the verdict is not `ALIVE`, that action is
`/anhurdb-memory:memory-blackout`, which triages *why*.
