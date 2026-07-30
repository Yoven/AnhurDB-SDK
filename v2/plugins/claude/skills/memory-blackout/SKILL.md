---
description: Diagnose an AnhurDB memory outage — no <anhur-memory> block at session start, turns not being saved, recall came back empty, or a memory-health check that did not return ALIVE. Walks the five failure classes in order and names the one fix for each.
when_to_use: Triggered by "my memory disappeared", "you forgot everything", "the memory stopped working", "no memory block", "nothing is being saved", or after /anhurdb-memory:memory-health reports DEGRADED or DEAD.
---

# Memory blackout runbook

A memory outage has exactly five shapes. They are ordered here by how cheap they are to
rule out, and each has a **discriminator**: one observation that only that class produces.
Work down the list, state which class matched, and stop at the first match.

> **Rule 0 — never print the API key.** Do not `cat` `~/.anhur-claude-memory/env`, do not
> echo `$ANHUR_API_KEY`. Everything printed here lands in the transcript, and the transcript
> is saved into AnhurDB and archived verbatim on disk. Inspect names and permissions only.

> **This is why blackouts last.** On 2026-07-30 it was found that this plugin had saved
> **nothing for 12,8 days**: 743 hook runs, 743 skips, every one exiting 0 with empty stdout
> and empty stderr. The evidence was in `plugin.log` the whole time. Nothing here is
> hypothetical — each class below has actually happened.

## Class A — the plugin is not loaded

Nothing runs: no recall, no persist, no log line. The session simply proceeds without memory.

**Discriminator**: `claude plugin list` does not show `anhurdb-memory@anhur` as enabled, **and**
`plugin.log` has no new line at this session's start.

```bash
claude plugin list 2>/dev/null | grep -B2 -A3 anhurdb-memory
tail -n 5 ~/.anhur-claude-memory/plugin.log
```

Causes, most common first:
- **A dangling `directory` marketplace.** That source reads the *live worktree*: rename or
  move `.claude-plugin/marketplace.json` and the plugin stops loading (`failed to load:
  cache-miss`). This happened on 2026-07-16 and went unnoticed for hours.
- **The install is a snapshot.** Editing the plugin source changes nothing about what runs.
  A change ships only with `make release-binaries` + a bumped `version` in
  `.claude-plugin/plugin.json` + a reinstall; the version is what invalidates the cache.

Fix: re-add the marketplace at its real path, reinstall the plugin, start a new session.

## Class B — the plugin loads, but the hooks do not fire

**Discriminator**: `claude plugin list` says enabled, `plugin.log` gets **no** new line at a
session start, but running the binary by hand *does* append lines.

```bash
tail -n 3 ~/.anhur-claude-memory/plugin.log     # note the newest timestamp
# start a NEW Claude Code session, then:
tail -n 3 ~/.anhur-claude-memory/plugin.log     # a fresh recall: line must have appeared
```

Check the wiring the hooks depend on:

```bash
ls -l "$HOME"/.claude/plugins/cache/anhur/anhurdb-memory/*/bin/anhur-claude-memory
file "$HOME"/.claude/plugins/cache/anhur/anhurdb-memory/*/bin/anhur-claude-memory-* 2>/dev/null | head -n 5
```

Causes: the binary lost its executable bit, the wrapper picked the wrong OS/arch build, or
the plugin and a hand-wired hook in `~/.claude/settings.json` are fighting. **Never run
both** — duplicate hooks double-write every turn and fragment the memory.

## Class C — the hooks fire, but the key is not readable

This is the 2026-07-18 → 2026-07-30 blackout.

**Discriminator**: `plugin.log` contains, at the timestamp of a session start or a turn:

```
ANHUR_API_KEY not set (env file: …) — MEMORY IS NOT BEING SAVED
```

```bash
grep -c 'ANHUR_API_KEY not set' ~/.anhur-claude-memory/plugin.log
ls -l ~/.anhur-claude-memory/env
# variable NAMES only: -o prints just the matched "NAME=", so a malformed line with no '='
# (a stray pasted key) can never reach the transcript. Plain `cut -d=` would echo it whole.
grep -o '^[[:space:]]*\(export \)\?[A-Za-z_][A-Za-z0-9_]*=' ~/.anhur-claude-memory/env | sed 's/^[[:space:]]*//; s/^export //; s/=$//' | sort -u
```

What to look for in the env file, without printing values:
- the file exists and is mode `-rw-------`;
- `ANHUR_API_KEY` is among the names;
- no CRLF line endings (`file ~/.anhur-claude-memory/env` should not say "CRLF");
- the value is not wrapped in stray quotes inside quotes.

**History worth knowing.** The original failure was subtler than a missing key: the hooks did
`. $HOME/.anhur-claude-memory/env` and the file had lost its `export` prefixes. In POSIX `sh`,
sourcing without `export` creates a *shell* variable that the child process does not inherit —
so the binary read an empty key, logged "skipping", and exited 0. The binary now **reads the
env file itself** (`plugins/core/envfile.go`), which is why a missing key today produces a
loud stderr message and the transcript is archived to disk before the run gives up.

Recovery: fix the key, then start a new session. Nothing dialogue-side is lost — `persist`
advances a per-session cursor, so the next run backfills the turns it missed, and the full
verbatim transcripts sat in `~/.anhur-claude-memory/archive/` the whole time.

## Class D — key fine, AnhurDB unreachable or rejecting

**Discriminator**: `plugin.log` shows `profile failed`, `CreateSession failed`, or
`flush still failing`, and `~/.anhur-claude-memory/queue/` is growing.

```bash
grep -E 'profile failed|CreateSession failed|flush still failing' ~/.anhur-claude-memory/plugin.log | tail -n 5
ls -1 ~/.anhur-claude-memory/queue/*.txt 2>/dev/null | wc -l
```

Read the error text at the end of those lines:
- `failed to connect` → endpoint down or `ANHUR_URL` wrong (check the name is present in the
  env file; check the endpoint from a browser or `curl -I`).
- `authentication failed: invalid API key` → the key was rotated or belongs to another
  deployment.
- `HTTP 409 … session has reached the maximum of N records` → a permanent rejection that will
  retry forever. It needs a decision, not patience.

**Queued turns are not lost.** Every `persist` and every session start retries them, oldest
first, back into their original session. Say that plainly — the user's first fear is loss.

## Class E — everything is green, but recall is empty

**Discriminator**: the log shows successful calls (`sent=1`, `wrote memory block to stdout`)
yet the `<anhur-memory>` block is thin or empty.

Two causes, both benign:
- **The container changed.** `ANHUR_CONTAINER` names the memory profile *within* the tenant,
  and recall reads strictly from it. Change the name and old memories stop surfacing — they
  are still there, under the old name.
- **Smart Units are off.** Turns are saved regardless, but distillation into
  fact/decision/preference is AnhurDB's cognitive layer. Without it, recall stays thin.

## What is never recoverable automatically

`~/.anhur-claude-memory/queue/quarantine/` holds chunks whose originating session could not
be proven. They are **never** persisted automatically: writing them into another
conversation's session would merge two conversations, and *1 Claude session = 1 AnhurDB
session* is inviolable. Report the count and the oldest file; only a human decides where
they belong.

The verbatim archive in `~/.anhur-claude-memory/archive/<session>.jsonl` is the lossless
copy of every session. Treat it as a recovery source, **not** as something to print: it
contains full tool output and may include secrets.

## Report

State: the class that matched, the evidence line that proved it, the fix, and what — if
anything — was actually lost. If the class is C or D, say explicitly that queued turns are
retried and that the archive on disk is intact.
