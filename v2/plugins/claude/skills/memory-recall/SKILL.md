---
description: Re-read the AnhurDB long-term memory profile mid-session and report what it holds — decisions, facts, preferences, recent topics, open tasks — plus any unpersisted backlog. Use when the user asks what you remember, when the memory block is stale after a long session, or to confirm a memory landed after it was saved.
when_to_use: Triggered by "what do you remember about this", "check your memory", "refresh your memory", "did that get saved", or when a decision from an earlier session needs to be re-read rather than guessed.
---

# Recall the AnhurDB memory profile

Re-run the memory engine's own recall and report what comes back.

```bash
"${CLAUDE_SKILL_DIR}/../../bin/anhur-claude-memory" recall </dev/null
```

`${CLAUDE_SKILL_DIR}` resolves to this skill's directory inside the installed plugin, so
`../../bin/` is *this* copy's engine — the exact binary the hooks run. Do not substitute a
`~/.claude/plugins/cache/.../*/bin/…` glob: it matches every cached version at once and the
shell would hand the extra paths to the binary as arguments.

That is the same command the `SessionStart` hook runs. It prints an `<anhur-memory>` block:
Decisions, Facts, Preferences, Recent topics, Open tasks, and the record/session counts.

Then:

1. **Report what is there**, grouped as the block groups it. Quote it; do not paraphrase a
   decision into something softer than what was recorded.
2. **Report what the block warns about.** If it opens with an *Unpersisted backlog* or
   *Quarantined chunks* section, tell the user in this reply — those sections exist because
   a log file reaches nobody. Quarantined chunks never resolve themselves.
3. **Report failure honestly — and do not read the exit code.** The engine ALWAYS exits 0
   (core.go:172-179). That is the exact shape of the 2026-07 blackout: 743 consecutive
   failures, every one of them a clean exit. Exit status proves nothing here.

   So when no `<anhur-memory>` block is printed, never report "empty memory". Read
   `tail -n 3 ~/.anhur-claude-memory/plugin.log` and say which line explains it:
   `recall: profile failed` means AnhurDB is unreachable; `ANHUR_API_KEY not set` means the key
   could not be read. Then offer `/anhurdb-memory:memory-health`.

   A non-zero exit means something else entirely: the `bin/` wrapper found no binary for this
   OS/arch. An empty memory and an unreachable memory look identical to the user, and must
   never be reported the same way.

## Two side effects, both intended

- **It drains the queue.** Recall flushes any turns queued from an offline moment before it
  reads the profile. That is the designed retry path, not a surprise — mention it only if
  the block reports a backlog that then disappears.
- **The command lands in the transcript, the output does not.** With the default
  `ANHUR_INCLUDE_TOOLS=calls`, the episodic feed records tool *calls* and drops tool
  *results* (`plugins/core/core.go`, `contentText`), so recalled memory is not written back
  into memory as a copy of itself. Set `ANHUR_INCLUDE_TOOLS=all` and it would be.

## What this skill deliberately does not do

**It cannot search your memory by topic.** Recall returns the *profile* — the distilled
decisions and facts — not an arbitrary semantic query. Topic search exists in AnhurDB and is
exposed by the `mcp__anhurdb__*` tools, but every one of those tools takes `api_key` as a
**required argument**, and the key is deliberately kept out of the model's context: this
plugin persists the transcript into AnhurDB, so a key in your context would end up stored in
the very memory the key protects.

Do not work around that by curling the API with the key inline, and do not ask the user to
paste their key into the chat. If topic-scoped recall is genuinely needed, the honest fix is
a query subcommand on the engine (which holds the key and never prints it) — a change to the
binary, not to this skill.

If the goal is "what did we decide about X", the practical answer today is: read the
Decisions and Facts from the block above, and say plainly when they do not cover X.
