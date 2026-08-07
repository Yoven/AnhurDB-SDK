# Install — AnhurDB memory for Claude Code

Five steps, then one command that **proves** it works. Nothing here is optional except step 6.

If you only remember one thing: an install is a **snapshot**, and the only proof that the memory
works is a record written and read back. Both are covered below.

- [0. Before you start](#0-before-you-start)
- [1. Create the config file](#1-create-the-config-file)
- [2. Add the marketplace](#2-add-the-marketplace)
- [3. Install the plugin](#3-install-the-plugin)
- [4. Start a new session](#4-start-a-new-session)
- [5. Prove it works](#5-prove-it-works)
- [6. Updating — the snapshot ritual](#6-updating--the-snapshot-ritual)
- [7. When the verifier says MEMORY IS DEAD](#7-when-the-verifier-says-memory-is-dead)
- [8. Uninstall](#8-uninstall)

---

## 0. Before you start

| You need | Notes |
| --- | --- |
| Claude Code CLI | `claude --version` must work. |
| macOS or Linux, arm64 or amd64 | Windows via WSL. A prebuilt engine ships for each; **no Go toolchain is required to use the plugin**. |
| An AnhurDB endpoint | Default `https://anhurdb.yoven.ai`. |
| A **per-tenant** API key | An `anhur_…` token — **not** the master key. Same key the AnhurDB MCP tools accept. |

Go 1.24+ is needed only to *develop* the plugin (step 6).

> **The key never gets printed.** Not by the engine, not by the verifier, not by you. Everything
> printed inside a Claude Code session is persisted into AnhurDB *and* archived verbatim on disk.
> When you need to inspect the config file, list variable **names** only — see
> [step 1](#inspecting-the-file-safely).

---

## 1. Create the config file

The engine reads `~/.anhur-claude-memory/env` **itself** — it does not depend on a shell sourcing
it. Create it before installing, so the very first hook run has a key:

```bash
install -m 700 -d "$HOME/.anhur-claude-memory"
( umask 177; cat > "$HOME/.anhur-claude-memory/env" <<'EOF'
export ANHUR_API_KEY=anhur_…your_tenant_key…
export ANHUR_URL=https://anhurdb.yoven.ai
export ANHUR_CONTAINER=claude-ltm
EOF
)
ls -l "$HOME/.anhur-claude-memory/env"     # must print -rw------- (0600)
```

Three things that are not style preferences:

- **Keep the `export` prefix.** The engine parses the file directly and does not need it, but this
  file gets copied, edited and re-templated by people and scripts. An artifact that works only one
  way is an artifact that repairs itself. Between 2026-07-18 and 2026-07-30 a rewrite of this file
  dropped the `export` keywords while the hooks still did `. env`; POSIX `.` without `export`
  creates a *shell* variable that the child process never inherits, so the engine read an empty key
  and logged `not set — skipping` **743 times over 12,8 days, exiting 0 with empty stderr**. The
  engine reads the file now, but the habit stays.
- **`ANHUR_CONTAINER` is your memory profile — choose it once and keep it stable.** The API key
  picks your *tenant*; the container names the profile **within** it that recall reads from. Change
  it later and recall stops surfacing what was saved under the old name. Nothing is lost — it is
  still there, under the old name — it just is not re-surfaced.
- **Mode `0600`, outside every git worktree.** The key lives only here.

Optional variables are documented in [`.env.example`](.env.example).

### Inspecting the file safely

```bash
grep -o '^[[:space:]]*\(export \)\?[A-Za-z_][A-Za-z0-9_]*=' ~/.anhur-claude-memory/env \
  | sed 's/^[[:space:]]*//; s/^export //; s/=$//' | sort -u
```

Never `cat` it. Never `cut -d= -f1` it either: on a line with no `=` — a stray comment, a merge
leftover — `cut` echoes the **whole line**, and if that line holds a secret it goes straight into
the transcript.

---

## 2. Add the marketplace

Pick **one** source.

**From GitHub** (your memory then tracks releases, and a local rebase can never touch it):

```bash
claude plugin marketplace add Yoven/AnhurDB-SDK
```

**From a local clone** (what you want if you work on the plugin):

```bash
claude plugin marketplace add /path/to/AnhurDB-SDK      # the REPO ROOT, not v2/plugins/claude
```

The manifest is `.claude-plugin/marketplace.json` **at the repo root**; the `anhur` marketplace
declares both `anhurdb-memory` (this plugin) and `anhurdb-memory-hermes`.

Check it registered:

```bash
claude plugin marketplace list
#   ❯ anhur
#     Source: Directory (/path/to/AnhurDB-SDK)
```

> ⚠ **A `directory` marketplace reads the live worktree on every load.** If
> `.claude-plugin/marketplace.json` ever stops existing at the registered path — a rename, a move,
> a branch switch — the marketplace goes dangling, the plugin stops loading, and **every hook
> silently stops firing**. No error reaches the session; the memory just quietly stops. That exact
> failure happened here on 2026-07-16 and went unnoticed for hours. Treat that path as a production
> interface. The [verifier](#5-prove-it-works) checks it on every run.

---

## 3. Install the plugin

```bash
claude plugin install anhurdb-memory@anhur --scope user
```

**User scope, unless you have a specific reason not to.** This is a *long-term memory*: scope it to
one repository and it forgets everywhere else.

Verify:

```bash
claude plugin list
#   ❯ anhurdb-memory@anhur
#     Version: 0.2.0
#     Scope: user
#     Status: ✔ enabled
```

`✘ failed to load` means no hook is registered and the memory is dead — go back to step 2.

Installing also registers the bundled AnhurDB **MCP tools** via `.mcp.json`. They are independent
of the memory loop; the loop talks to `ANHUR_URL` through the SDK, not through MCP.

---

## 4. Start a new session

**Hooks are registered at session start.** The install takes effect in the *next* session, not the
one you typed it in. Quit Claude Code and start it again.

On that new session you should see an `<anhur-memory>` block arrive as context. On a brand-new
memory it will be nearly empty — that is expected; it fills in as you work.

---

## 5. Prove it works

From a clone (always available):

```bash
sh v2/plugins/claude/scripts/anhur-memory-verify.sh
```

From the installed snapshot (available once you have installed a version that ships this script):

```bash
verifier=$(ls -1 "$HOME"/.claude/plugins/cache/anhur/anhurdb-memory/*/scripts/anhur-memory-verify.sh \
  2>/dev/null | sort -V | tail -n 1)
sh "$verifier"
```

> Pick the newest copy explicitly. The cache keeps **every** version you have ever installed, so a
> bare `…/*/scripts/…` glob expands to several paths and the shell hands the extras to `sh` as
> arguments. On this machine that glob currently matches four directories.

Either invocation tests the **installed** plugin — the verifier never grades the source tree, only
compares against it.

It runs six checks against the **installed** plugin and prints one verdict:

```
[1/6] INSTALLATION — is the plugin installed, enabled, and current?
  [ OK ] installed: anhurdb-memory@anhur version 0.2.0 -> …/cache/anhur/anhurdb-memory/0.2.0
  [ OK ] loaded and enabled according to 'claude plugin list'
  [ OK ] marketplace 'anhur' resolves: …/AnhurDB-SDK/.claude-plugin/marketplace.json
  [ OK ] snapshot matches source (version 0.2.0, identical linux/amd64 engine)
  [ OK ] hooks declared: SessionStart->recall, Stop/SessionEnd->persist (…/hooks/hooks.json)
  [ OK ] engine wrapper is executable: …/bin/anhur-claude-memory

[2/6] CONFIG — can the engine READ the key? (proved by running it)
  [ OK ] engine fails LOUD on a missing key (stderr carries 'MEMORY IS NOT BEING SAVED')
  [ OK ] engine read the key from /home/you/.anhur-claude-memory/env (key source=file, 4 vars loaded)
  [ OK ] env file permissions are owner-only (-rw-------)

[3/6] CONNECTIVITY — is https://anhurdb.yoven.ai up, and is the key accepted?
  [ OK ] endpoint healthy: {"mode":"datacenter","name":"anhurdb","status":"healthy"}
  [ OK ] key accepted (HTTP 200 on /api/v1/profile)

[4/6] WRITE — persist one real record through the real engine
  [ OK ] AnhurDB accepted the write (chunks=1 sent=1 queued=0, session=anhur-verify-…)
  [ OK ] verbatim transcript archive written (the lossless copy is working)

[5/6] READ-BACK — find that record again (this is the check that matters)
  [ OK ] read the record back verbatim — the write/read cycle is CLOSED

[6/6] STATE — anything stuck, and what does the engine's own log say?
  [ OK ] queue is empty — no turn is waiting to reach AnhurDB
  [ OK ] archive directory is writable: /home/you/.anhur-claude-memory/archive
  [ OK ] no unresolved failures in /home/you/.anhur-claude-memory/plugin.log
  [ OK ] the engine ran in the last 60 min (something invoked it — a hook, or you)

----------------------------------------------------------------------
VERDICT: MEMORY IS ALIVE — wrote a record and read it back, just now.
```

| Exit code | Verdict | What to do |
| --- | --- | --- |
| `0` | `MEMORY IS ALIVE` | Nothing. A record was written and read back seconds ago. |
| `1` | `MEMORY IS DEAD — <reason>` | Fix the `[FAIL]` lines top to bottom; each one prints the command that fixes it. Re-run. |
| `2` | `ALIVE, WITH N WARNING(S)` / `LOCAL CHECKS PASSED` (`--offline`) / `NOT PROVED` | The cycle either has caveats or was not tested. Read the `[WARN]` lines. |

Useful flags: `--offline` (skip all HTTP; the cycle is then **not** proved and the verdict says so)
and `--help`.

**What step 4 writes.** One small record, into container `claude-ltm-verify` and a fresh session
`anhur-verify-<utc>-<rand>` — never your real container, never an existing session. Recall filters
strictly by container, so a probe record can never appear in your memory block. The verifier prints
exactly what it created.

**What the verifier cannot prove.** That the `<anhur-memory>` block reaches the **model**. No
external process can observe that. Only the model can report it:

```bash
claude -p "Without using any tools: did you receive an <anhur-memory> block? \
If yes, quote its first line. If no, say NO BLOCK." </dev/null
```

Run that once after installing, and again after any change to the plugin, the marketplace, or the
env file. It is the one check that closes the loop `AnhurDB → hook → context → model`.

---

## 6. Updating — the snapshot ritual

**An install is a copy.** `claude plugin install` snapshots the plugin into
`~/.claude/plugins/cache/anhur/anhurdb-memory/<version>/`. The hooks run *that copy*. Editing the
source afterwards changes **nothing** about what runs — and because the copy still looks perfectly
installed, everything except a byte comparison reports success.

**The version is the cache key.** `claude plugin update` re-copies when the version in
`.claude-plugin/plugin.json` has moved. Same version means the cache may legitimately stay put. So:

```bash
cd v2/plugins/claude
make release-binaries                       # cross-compiles all four shipped platforms into bin/
$EDITOR .claude-plugin/plugin.json          # bump "version" — THIS is what invalidates the cache
claude plugin update anhurdb-memory@anhur   # re-snapshot
# restart Claude Code, then:
sh scripts/anhur-memory-verify.sh           # must report the new version and MEMORY IS ALIVE
```

The verifier reports both failure shapes explicitly:

- `installed snapshot is version 0.2.0, source is 0.3.0 — you are NOT running your source`
- `SAME version (0.2.0) but a DIFFERENT engine binary is installed — the snapshot is stale`
  ← this is the one that costs hours; it is what "I fixed it and nothing changed" actually looks
  like.

Two more notes:

- The release build is **reproducible** (`-trimpath -buildvcs=false`), so committed binaries and a
  fresh build are byte-identical. That is what makes the verifier's checksum comparison meaningful.
- `make deploy` overwrites just the engine binary inside the installed cache. It is a fast
  dev-loop shortcut and it **masks** a missing version bump — the cache then holds one plugin
  version with another version's engine. Use it while iterating; never end a change with it.

> **If you develop this plugin, run this plugin.** Wiring the binary into `settings.json` by hand
> looks simpler and removes the only person exercising the shipped install path. Worse, running
> both at once double-writes every turn and fragments the memory.

---

## 7. When the verifier says MEMORY IS DEAD

Each `[FAIL]` line prints its own fix. The mapping, in the order the checks run:

| Verifier says | Meaning | Fix |
| --- | --- | --- |
| `is NOT installed` | no entry in `installed_plugins.json` | `claude plugin install anhurdb-memory@anhur --scope user` |
| `FAILED TO LOAD` | usually a dangling `directory` marketplace | restore `.claude-plugin/marketplace.json` at the registered path, then `claude plugin marketplace update anhur` |
| `installed but DISABLED` | no hook is registered | `claude plugin enable anhurdb-memory@anhur` |
| `marketplace 'anhur' is DANGLING` | the registered path has no manifest | re-add the marketplace at the right path (step 2) |
| `engine stayed SILENT with no key` | the installed engine predates the 2026-07-30 fail-loud fix | rebuild + bump + update (step 6) |
| `engine CANNOT read a key` | the file is missing, unreadable, or has no `ANHUR_API_KEY` line | follow the printed command — it **appends**, it never overwrites your file |
| `cannot reach <url>` | DNS, TLS, proxy, VPN, or the server is down | `curl -v $ANHUR_URL/api/v1/health` |
| `key REJECTED (HTTP 401)` | wrong, revoked, or another deployment's key | issue a fresh per-tenant key, rewrite the env file |
| `write REFUSED — queued to disk` | AnhurDB rejected the write; the turn is **queued, not lost** | fix the cause above; the queue drains on the next persist or session start |
| `wrote the record but CANNOT read it back` | the write path works and the read path does not | the printed `curl` reproduces it |

Warnings worth acting on:

- `N turn(s) queued on disk` — not lost. Retried on every persist and every session start, back into
  their original session.
- `N chunk(s) QUARANTINED` — **never retried**, needs a human. Their originating session could not be
  proven, and writing them anywhere else would merge two conversations. *1 Claude session = 1 AnhurDB
  session* is inviolable.
- `transcript … but plugin.log did NOT move` — the hooks may not be firing. Start a new session and
  check that a fresh line appears in `~/.anhur-claude-memory/plugin.log`.

Inside a session, the same triage is available as skills: `/anhurdb-memory:memory-health` and
`/anhurdb-memory:memory-blackout`. A background monitor also turns new failure lines into
notifications — see the [README](README.md#the-background-monitor).

---

## 8. Uninstall

```bash
claude plugin disable anhurdb-memory@anhur      # stop the hooks, keep everything installed
claude plugin uninstall anhurdb-memory@anhur    # remove the snapshot
```

Neither touches your data: your memories stay in AnhurDB, and `~/.anhur-claude-memory` keeps the
queue, the cursors, the log and the verbatim archive. Delete that directory only if you also want
the local archive gone — **it holds full transcripts, including tool output.**

---

## Where things live

| Path | What |
| --- | --- |
| `~/.anhur-claude-memory/env` | your config + key (`0600`) |
| `~/.anhur-claude-memory/plugin.log` | the engine's own diagnostics, UTC, never the key |
| `~/.anhur-claude-memory/queue/` | turns that could not be sent yet (retried) |
| `~/.anhur-claude-memory/queue/quarantine/` | chunks a human must resolve (never retried) |
| `~/.anhur-claude-memory/archive/<session>.jsonl` | the lossless verbatim transcript copy |
| `~/.claude/plugins/cache/anhur/anhurdb-memory/<version>/` | the installed snapshot the hooks run |

See the [README](README.md) for how the loop works, what each artifact proves, and the honest
limitations.
