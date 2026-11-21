#!/usr/bin/env bash
# Cross-SDK live parity probe — Go / Python / TypeScript against one AnhurDB,
# then one PASS/FAIL matrix (step × SDK).
#
# Required env:
#   ANHUR_API_KEY    — tenant or master key
# Optional:
#   ANHUR_URL        — default https://anhurdb.yoven.ai
#   ANHUR_TENANT_ID  — shared tenant for all three probes (default: each probe
#                      mints its own throwaway sdk-<lang>-parity-<unix> tenant)
#
# Usage:
#   export ANHUR_API_KEY=...
#   bash AnhurDB-SDK/v2/scripts/parity/run_all.sh
#
# Exit status: 0 only when every probe exited 0 AND every matrix row is
# identical across the three SDKs with no FAIL and no MISSING cell (a row of
# SKIP on all three is identical, so it does not fail the run).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
export ANHUR_URL="${ANHUR_URL:-https://anhurdb.yoven.ai}"

if [[ -z "${ANHUR_API_KEY:-}" ]]; then
  echo "FAIL: ANHUR_API_KEY is required" >&2
  exit 1
fi

LOG_DIR="$(mktemp -d)"
trap 'rm -rf "$LOG_DIR"' EXIT

echo "=== PARITY URL=${ANHUR_URL} ==="

# Junior Tip [do not hijack the exit code]: each probe runs to its own log via
# tee, and PIPESTATUS[0] is read on the very next line — `$?` there would be
# tee's status (always 0) and a crashed probe would look green. No `set -e`:
# one SDK failing must not stop the other two from producing their column.
echo ""
echo "=== GO ==="
(cd "$ROOT/golang" && go run ./scripts/parity_probe/) 2>&1 | tee "$LOG_DIR/go.log"
GO_EXIT=${PIPESTATUS[0]}

echo ""
echo "=== PYTHON ==="
python3 "$ROOT/scripts/parity/probe_python.py" 2>&1 | tee "$LOG_DIR/python.log"
PYTHON_EXIT=${PIPESTATUS[0]}

echo ""
echo "=== TYPESCRIPT ==="
(cd "$ROOT/typescript" && npx --yes tsx "$ROOT/scripts/parity/probe_typescript.ts") 2>&1 | tee "$LOG_DIR/typescript.log"
TYPESCRIPT_EXIT=${PIPESTATUS[0]}

echo ""
echo "=== PARITY MATRIX ==="
# Every `RESULT sdk=<sdk> op="<op>" status=<PASS|FAIL|SKIP> ...` line becomes a
# cell. Rows keep first-seen order; an op one SDK never printed is MISSING.
# Junior Tip [count with awk, not grep -c]: ugrep's `grep -c` prints nothing
# and exits 1 on zero matches — the mismatch count must come from awk itself.
MISMATCH_COUNT=$(awk -v out="$LOG_DIR/matrix.txt" '
  /^RESULT sdk=/ {
    sdk = ""; op = ""; status = ""
    if (match($0, /sdk=[a-z]+/))         sdk = substr($0, RSTART + 4, RLENGTH - 4)
    if (match($0, /op="[^"]*"/))         op = substr($0, RSTART + 4, RLENGTH - 5)
    if (match($0, /status=[A-Z]+/))      status = substr($0, RSTART + 7, RLENGTH - 7)
    if (sdk == "" || op == "" || status == "") next
    if (!(op in seen)) { seen[op] = 1; order[++rows] = op }
    key = op SUBSEP sdk
    # One op printed twice by one SDK: a FAIL anywhere wins over a PASS.
    if (!(key in cell) || status == "FAIL") cell[key] = status
  }
  END {
    split("go python typescript", sdks, " ")
    printf "%-34s %-8s %-8s %-10s %s\n", "STEP", "go", "python", "typescript", "VERDICT" > out
    mismatches = 0
    for (row = 1; row <= rows; row++) {
      op = order[row]; line = sprintf("%-34s", op)
      first = ""; verdict = "OK"
      for (column = 1; column <= 3; column++) {
        key = op SUBSEP sdks[column]
        value = (key in cell) ? cell[key] : "MISSING"
        line = line sprintf(" %-" (column == 3 ? 10 : 8) "s", value)
        if (column == 1) first = value
        if (value == "FAIL" || value == "MISSING" || value != first) verdict = "MISMATCH"
      }
      if (verdict != "OK") mismatches++
      print line " " verdict > out
    }
    if (rows == 0) { print "(no RESULT lines parsed)" > out; mismatches = 1 }
    print mismatches
  }' "$LOG_DIR/go.log" "$LOG_DIR/python.log" "$LOG_DIR/typescript.log")
cat "$LOG_DIR/matrix.txt"

echo ""
echo "probe exit codes: go=${GO_EXIT} python=${PYTHON_EXIT} typescript=${TYPESCRIPT_EXIT}"
if [[ "$MISMATCH_COUNT" != "0" || $GO_EXIT -ne 0 || $PYTHON_EXIT -ne 0 || $TYPESCRIPT_EXIT -ne 0 ]]; then
  echo "=== PARITY FAILED: ${MISMATCH_COUNT} mismatched step(s) ==="
  exit 1
fi
echo "=== ALL SDKs PASSED ==="
