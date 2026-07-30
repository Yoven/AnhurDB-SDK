# AnhurDB memory provider for the Hermes Agent

Sovereign long-term memory for [Hermes Agent](https://github.com/hermes-agent),
backed by AnhurDB. It is a **memory provider plugin** (`MemoryProvider`), not a
Claude Code plugin — the Hermes Agent has its own plugin format and would never
load the hook-based layout that lives in `../hermes/`.

What it does, per session:

| Host call | What this plugin does |
|---|---|
| `is_available()` | Answers honestly, and says **out loud** why not (no key, no SDK, no config). |
| `initialize(session_id)` | Registers **that** session id in AnhurDB, drains the disk queue, fetches the profile. |
| `system_prompt_block()` | Injects `<anhur-memory>`: decisions, facts, preferences, recent topics. |
| `prefetch(query)` | Bounded hybrid recall for the upcoming turn, as `<anhur-recall>`. |
| `sync_turn(user, assistant)` | Persists the turn under its session. On failure it goes to a **durable disk queue** and is retried on the next turn. |
| `get_tool_schemas()` | `anhurdb_recall` and `anhurdb_search` for explicit lookups. |
| `backup_paths()` | Declares the state dir so `hermes backup` captures un-persisted turns. |

**The inviolable rule: 1 Hermes session = 1 AnhurDB session.** The Hermes
session id is used verbatim as the AnhurDB session id. A turn whose session
cannot be proven is *quarantined*, never attributed to a neighbouring
conversation.

---

## Install

### 1. Install the AnhurDB Python SDK into the Hermes environment

The plugin dogfoods the official SDK (`anhurdb`), and Hermes runs from its own
virtualenv, so the SDK must be importable **there**:

```bash
~/.hermes/hermes-agent/venv/bin/pip install -e /path/to/AnhurDB-SDK/v2/python
```

If you skip this, the plugin still loads and reports the exact command above
through `is_available()` — it never fails silently.

### 2. Install the plugin

```bash
# From a git repository (clones into ~/.hermes/plugins/<manifest name>):
hermes plugins install <git-url-of-this-plugin>

# Or, from this checkout — copy or symlink it:
cp -r /path/to/AnhurDB-SDK/v2/plugins/hermes-agent ~/.hermes/plugins/anhurdb
```

> **The directory name IS the provider id.** Hermes keys memory providers by
> the directory under `~/.hermes/plugins/` (`plugins/memory/__init__.py`
> → `_iter_provider_dirs` uses `child.name`), and `memory.provider` in
> `~/.hermes/config.yaml` must match it. Install it as **`anhurdb`** — not as
> `hermes-agent`, which is only the folder name inside this SDK repo.
>
> User-installed memory providers live in `~/.hermes/plugins/<name>/`, flat.
> The `plugins/memory/<name>/` layout is for providers **bundled** inside the
> hermes-agent repo.

### 3. Configure the key

Either through the wizard:

```bash
hermes memory setup      # pick "anhurdb"; the key is written to ~/.hermes/.env (0600)
```

…or by writing the plugin's own file (see `.env.example`):

```bash
mkdir -p ~/.anhur-hermes-memory && chmod 700 ~/.anhur-hermes-memory
cp .env.example ~/.anhur-hermes-memory/env && chmod 600 ~/.anhur-hermes-memory/env
$EDITOR ~/.anhur-hermes-memory/env
```

Both paths work, and **the process environment always wins** over the file.
Use a **per-tenant** key, never the master key.

### 4. Activate

`hermes memory setup` writes it for you; manually it is:

```yaml
# ~/.hermes/config.yaml
memory:
  provider: anhurdb
```

Start a new session. Confirm with `hermes memory status`.

---

## Verify it actually works

A plugin that *looks* installed and stores nothing is this project's number-one
failure mode. Check, in order:

```bash
hermes memory status                     # provider: anhurdb
tail -f ~/.anhur-hermes-memory/plugin.log  # config + per-session lines
HERMES_PLUGINS_DEBUG=1 hermes chat       # verbose plugin discovery on stderr
ls ~/.anhur-hermes-memory/queue           # EMPTY = everything reached AnhurDB
```

The plugin log records the key's **source** (`environment` / `file` /
`missing`) on every session — never the key itself. That single line is what
would have made the 12.8-day blackout of the sibling Claude Code plugin
diagnosable on day one.

A non-empty `queue/` is not data loss: those turns are retried on every
subsequent turn, and the model is told about them in its context block so it
can tell you. A non-empty `queue/quarantine/` **is** something only you can
fix: those turns have no provable session, so they are never written anywhere.

---

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `ANHUR_API_KEY` | — | **Required.** Tenant API key. Never logged. |
| `ANHUR_URL` | SDK `DEFAULT_CLOUD_URL` | AnhurDB endpoint. |
| `ANHUR_CONTAINER` | `hermes-ltm` | Memory profile within the tenant. Keep it stable. |
| `ANHUR_STATE_DIR` | `~/.anhur-hermes-memory` | Queue, quarantine, plugin log. |
| `ANHUR_ENV_FILE` | `<state dir>/env` | Alternative config file location. |
| `ANHUR_HTTP_TIMEOUT` | `15` | Seconds per write call (background). |
| `ANHUR_PREFETCH_TIMEOUT` | `5` | Seconds per recall call (inline in the turn). |
| `ANHUR_RECALL_LIMIT` | `8` | Items per profile section / recall hits. |
| `ANHUR_MAX_CHUNK_CHARS` | `24000` | Size of each persisted chunk. |

---

## Design notes (and what breaks if you "simplify" them)

**The safety net never depends on what it protects.** The disk queue needs no
API key, no network, and no SDK. A turn is queued even when the plugin is
completely unconfigured — because the 2026-07-30 post-mortem found the previous
safety net sitting *behind* the same early-return that disabled writing, so it
died together with the thing it was supposed to save.

**Nothing third-party is imported at module level.** Hermes' memory loader
executes every `*.py` in the plugin directory and, if one raises, logs it at
`debug` and moves on — the plugin then simply "does not appear". An
`import anhurdb` at the top of a module would turn *"SDK not installed"* into
*"plugin vanished"*. The SDK is imported inside functions so its absence
becomes a sentence in `is_available()` instead.

**Writes are pinned to an explicit session id.** The SDK's `create_session`
mutates the client's own registered session as a side effect, and one runtime
serves turns from different sessions (gateway group chats). Every write passes
`session_id=` explicitly, so no turn can inherit its neighbour's session.

**`on_session_switch` is not optional.** Hermes rotates `session_id` on
`/resume`, `/branch`, `/reset`, `/new`, and on context compression, *without*
recreating the provider. A provider that caches the session only in
`initialize` keeps writing the new conversation into the old session — two
conversations merged into one record. That hook is the defence.

**Drain before write.** Queued turns are replayed *before* the current turn is
sent, so recovered memories keep their original order, and the drain stops at
the first failure instead of burning N × timeout inside the user's turn.

**Deliberate divergence from the Claude Code plugin:** only the rendered
dialogue (`USER:` / `ASSISTANT:`) is persisted. The raw `messages` list with
tool calls and tool results is available and intentionally **not** stored —
the AnhurDB extraction pipeline is calibrated on clean dialogue. The sibling
plugin's `ANHUR_INCLUDE_TOOLS=calls` behaviour has no equivalent here yet.

---

## Tests

```bash
cd v2/plugins/hermes-agent
python -m pytest tests/ -q
```

The suite loads the plugin exactly the way Hermes does (synthetic package,
per-file `spec_from_file_location`) and runs the provider end-to-end against a
fake AnhurDB HTTP server through the **real** `anhurdb` SDK — so an SDK
transport regression (like the 2026-07-30 truncated-response bug that made
search return `[]` with no error) fails here rather than in someone's memory.

`tests/test_host_contract.py` goes further: when a real Hermes Agent install is
present (`~/.hermes/hermes-agent`, or `HERMES_AGENT_DIR`), it runs the plugin
inside **that** runtime — its discovery heuristic, its `MemoryProvider` ABC,
its `MemoryManager` fan-out — and asserts a turn lands under the right session.
It skips cleanly when no install is found, and never touches the operator's
real Hermes home or AnhurDB state directory.
