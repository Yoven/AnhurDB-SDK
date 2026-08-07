# Cross-language parity harness — Go engine vs Python provider

One command, one question: **do the two implementations of the memory rules still behave
the same way?**

```sh
cd AnhurDB-SDK/v2/plugins/tests/parity
./run_parity.sh
```

No API key, no network, no AnhurDB, no plugin installed. Everything runs in temp
directories; your `~/.anhur-claude-memory` and `~/.anhur-hermes-memory` are never touched
or read.

Exit codes — "did not run" must never look like "passed":

| code | meaning |
| ---- | ------- |
| `0`  | parity OK: every scenario behaves identically in both implementations |
| `1`  | PARITY BROKEN: at least one scenario diverges (the report names it and says what to do) |
| `2`  | the comparison could not be made (no Go toolchain, a probe crashed, no observations) — nothing was proven |

---

## Why this exists

The same three rules are implemented **twice**, in two languages:

| rule | Go — `plugins/core/` | Python — `plugins/hermes-agent/` |
| ---- | -------------------- | -------------------------------- |
| config loading (`export` optional, quotes, comments, a malformed line must not kill the next key, environment wins) | `envfile.go` `loadEnvFileInto`, `core.go` `loadConfig` | `config.py` `read_env_file`, `load_plugin_config` |
| on-disk queue (chronological order, quarantine for anything with no provable session, never retried) | `core.go` `flushQueue`, `quarantineChunk` | `memory_queue.py` `DurableTurnQueue.drain` |
| chunking (nothing truncated, nothing lost) | `core.go` `splitIntoChunks`, `hardSplitRunes` | `transcript.py` `split_into_chunks` |

Two implementations of one rule drift, and they drift **in silence** — the same shape as
the 2026-07-18 → 2026-07-30 blackout, where the Claude plugin ran 743 times, skipped 743
times and exited `0` every single time. It is also the rule the project already applies to
the three SDKs: Go, Python and TypeScript ship every method and every bugfix together.

**Measured proof that this harness is not redundant** (2026-07-30): with a one-line drift
injected into `hermes-agent/transcript.py` (effective chunk size halved on the Python side
only — lossless, so nothing crashes), the provider's own suite reported `54 passed`, the Go
suite reported `ok`, and only this harness failed:

```
[DIVERGE ] 11_chunk_100kb_loses_nothing  (chunk, 6 fields compared)
    !! field `chunk_count`
    expected : 9
    go       : 9
    python   : 14   <-- differs
```

A second injection — deleting a queued item whose send had just failed (the classic
ACK-first data loss) — was caught the same way, on `every_item_accounted_for` and on
`pending_after`. Both injections were reverted; both files verified byte-identical
afterwards by sha256.

---

## Shape, and why it is this shape

```
run_parity.sh          the one command: prerequisites → Go probe → Python probe → verdict
scenarios/*.json       one file per scenario: the inputs, the expected observation,
                       and what to do when it fails.  THE FIXTURES ARE THE SPEC.
fixtures/*.txt         shared input files (a 161 KB ASCII transcript, a multibyte one)
python_probe.py        runs the scenarios against the real hermes-agent modules
compare_parity.py      the only component that decides pass/fail
../../core/parity_probe_test.go    the Go arm (see below)
```

Three components instead of one program, because the shape is **forced**: the Go rules are
unexported, so they can only be observed from a Go test *inside* `core/`; the Python rules
live in a package the Hermes host loads from a file path. They cannot share a process. So
each side only **observes** into a JSON file and a third component **compares** — which
also means a divergence is reported once, by one component, in one voice, and each step
fails with its own name (`[2/4] observing the Go engine`) instead of a stack trace you have
to interpret.

**The Go probe lives in `core/parity_probe_test.go`, not here.** `loadConfig`, `flushQueue`
and `splitIntoChunks` are unexported; Go cannot reach another directory's unexported
identifiers. The alternative would be re-implementing them next to the harness and
comparing a copy against a copy — a parity check that proves nothing about the shipped
binary, which is the worst possible outcome for a drift detector. It is a `_test.go` file,
so it never links into either plugin binary, and it skips unless the two `ANHUR_PARITY_*`
variables are set (a skip can never be mistaken for a pass: the runner fails loudly if the
observation file it asked for does not appear).

**The Go arm wears the *hermes* identity** (`hermes-ltm`, `.anhur-hermes-memory`). The state
dir name and container are plugin *identity*, not rule — the claude build says `claude-ltm`
— so probing with the hermes identity keeps the harness comparing rules instead of flagging
an intended difference every run.

---

## Reading a scenario file

Every scenario classifies **every** observed field into exactly one of three buckets. A
field that is observed but not classified is a hard failure: an unclassified field is a
blind spot, and a blind spot looks exactly like a green run.

- **`expect`** — both implementations must produce exactly this value.
- **`divergent`** — a known, deliberate disagreement, with both sides' current values
  written down plus the decision it is waiting on. Each side must still match its declared
  value, so the difference cannot quietly grow, shrink or flip. Printed in **every** run so
  it never fades out of view.
- **`not_compared`** — representation-only differences (units, file formats), each with a
  written reason. Printed in every run too.

The secret hygiene rule: API keys are compared as `sha256(value)[:12]`, never as values.
The fixtures use fake keys, but somebody will eventually point this harness at a real env
file, and a harness that *can* print a key *will* print one.

---

## Current state (2026-07-30)

`VERDICT: PARITY BROKEN — 1 of 13 scenarios diverge`

**`06_config_empty_env_var_must_not_mask_file`** — a real defect, found by this harness on
its first run. With `ANHUR_API_KEY` exported as an empty string (a wrapper script, a
systemd unit with an empty `Environment=`), the Go plugin resolves **no key at all** and
disables memory, while a perfectly good key sits in its own env file. Python resolves the
file's key. Go is inconsistent with *itself* here: `core.go` `envOr()` — used for
`ANHUR_URL`, `ANHUR_CONTAINER`, `ANHUR_ARCHIVE_DIR` — already falls back when a variable is
empty; only the API key does not. The fix is in the scenario's `on_failure`, one condition
in `core/envfile.go`. This scenario turns green with no other change.

Two **declared divergences** are also printed on every run and need a decision, not a
patch:

- `09_queue_failure_mid_drain` / `09b_queue_backend_down` — Go keeps trying every queued
  item after a failure; Python stops at the first one. Go's cost: a later turn lands in
  AnhurDB *ahead* of an older one that is still stuck (permanent misordering), plus one
  HTTP timeout per queued item inside a hook Claude Code will kill. Python's cost:
  head-of-line blocking — one poison item stops every later turn from ever being persisted.
  Recommended resolution: stop on a **transport** failure (timeout/5xx), skip-and-continue
  on a **permanent** rejection (4xx) — and apply it to both, in one commit, updating the
  scenario in the same change.

---

## Adding a scenario

1. Drop a `.json` file in `scenarios/` with `name`, `rule` (`config` | `queue` | `chunk`),
   `why`, `given`, `expect`, and `on_failure`. The `name` must match the file name.
2. Run `./run_parity.sh`. If a probe reports a field you did not classify, the run fails
   and tells you which field — that is the design, not an inconvenience.

Do **not** add a scenario whose expected values you copied from whatever the code happens
to do today without understanding why. The fixtures are the specification; a fixture that
merely records current behaviour teaches the next person nothing when it goes red.
