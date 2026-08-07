# Install — AnhurDB memory provider for the Hermes Agent

This plugin gives the Hermes Agent persistent long-term memory in AnhurDB:
every turn is persisted automatically, past sessions are recalled before each
turn, and a turn that cannot be delivered is queued on disk instead of being
dropped.

Install takes four steps. The fifth — **verify** — is not optional: it is the
only thing that proves memory is actually working.

```bash
python3 verify.py
```

---

## 0. This replaces `v2/plugins/hermes/`, which never worked

`AnhurDB-SDK/v2/plugins/hermes/` was written in **Claude Code plugin format** —
`.claude-plugin/plugin.json`, `hooks/hooks.json` with `SessionStart`/`Stop`
commands and `${CLAUDE_PLUGIN_ROOT}` — and shipped Go binaries.

The Hermes Agent does not read any of that. It discovers memory providers by
scanning `$HERMES_HOME/plugins/<name>/__init__.py` for the strings
`MemoryProvider` or `register_memory_provider`
(`hermes-agent/plugins/memory/__init__.py:_is_memory_provider_dir`). The old
directory has **no `__init__.py` at all**, so the host's own check answers:

```
_is_memory_provider_dir(v2/plugins/hermes) = False
```

It could never have been loaded by the Hermes Agent, in any configuration.
Worse, its hooks used `. $HOME/.anhur-hermes-memory/env` — the exact sourcing
pattern that caused the 12.8-day silent blackout in the Claude Code plugin
(743 hook invocations, 743 skips, exit 0, empty stderr).

**Do not install `v2/plugins/hermes/` into Hermes.** This directory
(`v2/plugins/hermes-agent/`) is a real Python `MemoryProvider` and is the only
supported one. The two are unrelated code with unrelated formats.

---

## 1. Install the AnhurDB Python SDK into the Hermes environment

The plugin talks to AnhurDB through the official Python SDK. It is **not** in
`plugin.yaml`'s `pip_dependencies` (it is not resolvable from a public index in
every environment), so install it explicitly, into the interpreter that runs
`hermes`:

```bash
~/.hermes/hermes-agent/venv/bin/python -m pip install -e \
  /path/to/AnhurDB-SDK/v2/python
```

Without it the plugin still loads, and `is_available()` returns `False` with the
reason spelled out — the host then drops the provider with a single DEBUG line.
That is a silent failure from the outside, which is why step 5 exists.

`aiohttp` is declared in `plugin.yaml` and is installed for you by
`hermes memory setup`.

---

## 2. Find the directory Hermes actually scans

**This is the trap that costs the most time.** Hermes profiles are fully
independent `HERMES_HOME` directories. With a non-default profile active, the
CLI redirects `HERMES_HOME` to `<root>/profiles/<name>/`, and
`~/.hermes/plugins/` is **never scanned**:

```bash
cat ~/.hermes/active_profile     # e.g. "architect"  ->  NOT the default home
hermes profile list              # every profile, and which one is active
```

| `active_profile` | Directory Hermes scans |
| --- | --- |
| absent, or `default` | `~/.hermes/plugins/` |
| `architect` | `~/.hermes/profiles/architect/plugins/` |

`verify.py` resolves this exactly the way `hermes_cli/main.py` does and prints
both lines at the top — `HERMES_HOME` and `scanned dir`. When in doubt, run it
first and install into the `scanned dir` it prints.

A plugin installed in the wrong home is not an error for Hermes. The directory
simply does not exist, nothing is logged, and memory is silently absent. That is
the same failure shape as the incident this plugin was written to end.

---

## 3. Install the plugin

The **directory name is the provider id** — not the `name:` field in
`plugin.yaml`. Hermes keys providers by `child.name`
(`plugins/memory/__init__.py:_iter_provider_dirs`), so the directory must be
called `anhurdb`.

### Symlink (recommended for a checkout you keep updated)

```bash
HOME_DIR=~/.hermes/profiles/architect      # use YOUR scanned dir from step 2
mkdir -p "$HOME_DIR/plugins"
ln -s /path/to/AnhurDB-SDK/v2/plugins/hermes-agent "$HOME_DIR/plugins/anhurdb"
```

A symlink is always current. A copy is a **snapshot**: edit the source and the
installed plugin keeps running the old code until you re-copy. `verify.py`
fingerprints both and warns when they diverge.

### Copy (for a machine without the repo)

```bash
cp -r /path/to/AnhurDB-SDK/v2/plugins/hermes-agent "$HOME_DIR/plugins/anhurdb"
rm -rf "$HOME_DIR/plugins/anhurdb/tests" "$HOME_DIR/plugins/anhurdb/__pycache__"
```

### `hermes plugins install`

`hermes plugins install` only accepts a **Git URL or `owner/repo` shorthand** —
it clones into `$HERMES_HOME/plugins/` (`hermes_cli/plugins_cmd.py:_install_plugin_core`).
It cannot install from a local path. Use symlink or copy for a local checkout.

---

## 4. Configure the key and activate the provider

### The easy way

```bash
hermes memory setup        # pick "anhurdb" in the picker
```

The wizard installs `plugin.yaml`'s pip dependencies, prompts for the three
fields this plugin declares, writes them to `$HERMES_HOME/.env`, and sets
`memory.provider: anhurdb` in `$HERMES_HOME/config.yaml`.

| Field | Env var | Secret |
| --- | --- | --- |
| AnhurDB tenant API key | `ANHUR_API_KEY` | yes |
| Endpoint | `ANHUR_URL` | no |
| Memory profile (container tag) | `ANHUR_CONTAINER` | no |

### The manual way

Activation — without this key the plugin is installed but **inert**, and Hermes
runs on built-in memory without saying anything:

```yaml
# $HERMES_HOME/config.yaml
memory:
  provider: anhurdb
```

Credentials, either in `$HERMES_HOME/.env` (loaded into the process environment
at CLI boot by `hermes_cli/env_loader.load_hermes_dotenv`) or in the plugin's own
file `~/.anhur-hermes-memory/env`:

```bash
mkdir -p ~/.anhur-hermes-memory && chmod 700 ~/.anhur-hermes-memory
cat > ~/.anhur-hermes-memory/env <<'EOF'
export ANHUR_API_KEY=anhur_your_tenant_key
export ANHUR_URL=https://anhurdb.yoven.ai
export ANHUR_CONTAINER=hermes-ltm
EOF
chmod 600 ~/.anhur-hermes-memory/env
```

The process environment always wins over the file. See `.env.example` for every
optional variable. The plugin reads this file itself — it never relies on a
shell having sourced it, which is precisely the bug that caused the blackout.

Only **one** external memory provider runs at a time. Setting `memory.provider`
to `anhurdb` replaces whatever was there.

---

## 5. Prove it works

```bash
python3 verify.py
```

It re-runs itself under the Hermes interpreter, resolves the same `HERMES_HOME`
the CLI would, and checks five things, each with its own verdict:

1. **DISCOVERY** — is the plugin in the scanned directory, does it pass the
   host's 8 KB text scan, and does `config.yaml` select it?
2. **LOADING** — does the host's own loader return a provider? Import failures
   that Hermes hides in `logger.debug` are printed here as failures.
3. **CONFIG** — was a key resolved, and from where (`environment` / `file` /
   `missing`)? The value is never printed.
4. **CYCLE** — writes one probe turn and reads it back out of AnhurDB.
5. **STATE** — queue and quarantine on disk.

The last line is the answer:

```
VERDICT: WORKING — a turn was written to AnhurDB and read back.
VERDICT: BROKEN — 1 failure(s). First: step 3/5 CONFIG ... | fix: ...
VERDICT: UNPROVEN — ... --no-network skipped the only step that proves memory works.
```

`--no-network` skips everything that talks to AnhurDB and can therefore never
report `WORKING` — an unproven write is not a working write.

The full run writes **one** turn into a dedicated `hermes-verify-<random>`
session of your configured container and reads it back. It never touches a real
conversation: one Hermes session equals one AnhurDB session, always.

Exit code is `0` for `WORKING` and `1` otherwise, for scripting. **Read the
line, not the code** — the incident that motivated this plugin consisted
entirely of a process exiting `0` while doing nothing.

### Common verdicts

| Verifier says | Fix |
| --- | --- |
| `not installed: .../plugins/anhurdb does not exist` | You installed into the wrong `HERMES_HOME` — see step 2. |
| `memory.provider is not set` | `hermes memory setup`, or add the `memory:` block from step 4. |
| `memory.provider is 'X', not 'anhurdb'` | Another provider owns the slot; only one runs. |
| `load_memory_provider(...) returned None` | An import inside the plugin failed; the host log lines printed below it name the module. |
| `the AnhurDB Python SDK is not importable` | Step 1. |
| `no API key` | Step 4. |
| `the turn did NOT reach AnhurDB: N queued` | Server unreachable or refusing. The turn is safe on disk and retried; the failing line from `plugin.log` is printed. |
| `N quarantined turn(s)` | Turns that arrived with no session id. They are never written automatically — only a human can say which conversation they belong to. |
| `installed copy DIFFERS from this source` | You installed a copy and then edited the source. Re-copy, or switch to a symlink. |

### Also useful

```bash
hermes memory status    # what the host thinks the active provider is
```

Diagnostics the plugin writes itself (never contain the API key):

```
~/.anhur-hermes-memory/plugin.log         # every problem, with a timestamp
~/.anhur-hermes-memory/queue/             # turns not yet delivered
~/.anhur-hermes-memory/queue/quarantine/  # turns with no provable session
```

The state directory is declared through `backup_paths()`, so `hermes backup`
includes it — a queued turn has not reached AnhurDB yet and must survive a
backup/restore cycle.

---

## Uninstall

```bash
hermes memory off                      # clears memory.provider in config.yaml
rm "$HERMES_HOME/plugins/anhurdb"
```

Leave `~/.anhur-hermes-memory/` alone until `verify.py` reports the queue and
quarantine empty — anything still there exists nowhere else.
