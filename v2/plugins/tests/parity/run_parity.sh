#!/bin/sh
# run_parity.sh — ONE command that proves the Go plugin engine and the Python memory
# provider still implement the same three rules.
#
#     ./run_parity.sh
#
# It needs no API key, no network and no AnhurDB: it exercises config loading, the
# on-disk queue and chunking directly, in temp directories. Nothing here touches
# ~/.anhur-claude-memory or ~/.anhur-hermes-memory.
#
# WHY THIS EXISTS (2026-07-30): the same three rules are implemented twice, in two
# languages — core/ (Go, shipped as anhur-claude-memory and anhur-hermes-memory) and
# hermes-agent/ (Python, loaded by the Hermes Agent). Two implementations of one rule
# drift, and they drift in SILENCE: the same class of failure as the 12,8-day blackout,
# where the plugin ran 743 times, skipped 743 times and exited 0 every time. This is the
# same rule the project already applies to the three SDKs (Go/Python/TS ship every method
# and every bugfix together).
#
# Junior Tip [why a shell runner around two probes instead of one program]: the two
# implementations cannot run in one process — one is a Go package whose rules are
# unexported (so they can only be observed from a Go test inside that package), the other
# is a Python package the Hermes host loads from a file path. So the shape is forced:
# each side OBSERVES into a JSON file, and a third component COMPARES. The upside is that
# each part fails in its own way, and this script names the step that failed instead of
# leaving you to interpret a stack trace.
#
# Exit codes — "did not run" must never look like "passed":
#   0  parity OK
#   1  PARITY BROKEN (the comparator explains which scenario and what to do)
#   2  the check could not be made (missing toolchain, probe crashed, no observations)

set -eu

harness_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
scenarios_directory="$harness_directory/scenarios"
plugins_directory=$(CDPATH= cd -- "$harness_directory/../.." && pwd)
core_directory="$plugins_directory/core"
provider_directory="$plugins_directory/hermes-agent"

observations_directory=$(mktemp -d "${TMPDIR:-/tmp}/anhur-parity-XXXXXX")
go_observations="$observations_directory/go.json"
python_observations="$observations_directory/python.json"

fail_setup() {
    printf '\n%s\n' "=============================================================================="
    printf 'VERDICT: COULD NOT COMPARE — %s\n' "$1"
    printf '\n%s\n' "WHAT TO DO: $2"
    printf 'Nothing was proven about parity. This is not a pass.\n'
    printf '%s\n' "=============================================================================="
    exit 2
}

printf 'AnhurDB memory plugins — parity harness\n'
printf '  scenarios    : %s\n' "$scenarios_directory"
printf '  go engine    : %s\n' "$core_directory"
printf '  python provider: %s\n' "$provider_directory"
printf '  observations : %s\n\n' "$observations_directory"

# ── step 1: prerequisites ────────────────────────────────────────────────────
printf '[1/4] checking prerequisites\n'
[ -d "$scenarios_directory" ] || fail_setup \
    "the scenarios directory is missing ($scenarios_directory)" \
    "restore AnhurDB-SDK/v2/plugins/tests/parity/scenarios/ — the fixtures ARE the specification."
[ -d "$core_directory" ] || fail_setup \
    "the Go engine directory is missing ($core_directory)" \
    "run this script from inside the AnhurDB-SDK checkout."
[ -d "$provider_directory" ] || fail_setup \
    "the Python provider directory is missing ($provider_directory)" \
    "run this script from inside the AnhurDB-SDK checkout."
command -v go >/dev/null 2>&1 || fail_setup \
    "the Go toolchain is not on PATH" \
    "install Go (this repo pins it in .tool-versions: asdf install golang 1.24.4)."
command -v python3 >/dev/null 2>&1 || fail_setup \
    "python3 is not on PATH" \
    "install Python 3.9 or newer; the harness itself needs only the standard library."
printf '      go: %s | python3: %s\n' "$(cd "$core_directory" && go version 2>/dev/null | cut -d' ' -f3)" "$(python3 --version 2>&1 | cut -d' ' -f2)"

# ── step 2: the Go arm ───────────────────────────────────────────────────────
# `cd` into core/ on purpose: the module lives there, and .tool-versions pins the Go
# version per directory (asdf resolves it from the working directory).
printf '[2/4] observing the Go engine (core/parity_probe_test.go)\n'
if ! (cd "$core_directory" && \
      ANHUR_PARITY_SCENARIOS="$scenarios_directory" \
      ANHUR_PARITY_OUT="$go_observations" \
      go test -run TestParityProbe -count=1 ./... 2>&1) ; then
    fail_setup \
        "the Go probe did not complete (output above)" \
        "fix the Go build/test first: cd $core_directory && go test ./..."
fi
[ -s "$go_observations" ] || fail_setup \
    "the Go probe wrote no observations" \
    "check that TestParityProbe exists in $core_directory/parity_probe_test.go and did not skip."

# ── step 3: the Python arm ───────────────────────────────────────────────────
printf '[3/4] observing the Python provider (hermes-agent)\n'
printf '      (a loud QUARANTINED line here is EXPECTED: scenario 10 feeds the queue an item\n'
printf '       with no provable session on purpose, and the provider is supposed to shout)\n'
if ! python3 "$harness_directory/python_probe.py" "$scenarios_directory" "$python_observations" ; then
    fail_setup \
        "the Python probe did not complete (output above)" \
        "fix the provider import first: cd $provider_directory/tests && python3 -m pytest"
fi
[ -s "$python_observations" ] || fail_setup \
    "the Python probe wrote no observations" \
    "re-run: python3 $harness_directory/python_probe.py $scenarios_directory /tmp/py.json"

# ── step 4: the verdict ──────────────────────────────────────────────────────
printf '[4/4] comparing\n'
set +e
python3 "$harness_directory/compare_parity.py" "$scenarios_directory" "$go_observations" "$python_observations"
comparison_status=$?
set -e

printf '\nobservations kept for inspection:\n  %s\n  %s\n' "$go_observations" "$python_observations"
exit "$comparison_status"
