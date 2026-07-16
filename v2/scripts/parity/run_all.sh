#!/usr/bin/env bash
# Cross-SDK live parity probe — Go / Python / TypeScript against one AnhurDB.
#
# Required env:
#   ANHUR_API_KEY  — tenant or master key
# Optional:
#   ANHUR_URL      — default https://anhurdb.yoven.ai
#
# Usage:
#   export ANHUR_API_KEY=...
#   bash AnhurDB-SDK/v2/scripts/parity/run_all.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
export ANHUR_URL="${ANHUR_URL:-https://anhurdb.yoven.ai}"

if [[ -z "${ANHUR_API_KEY:-}" ]]; then
  echo "FAIL: ANHUR_API_KEY is required" >&2
  exit 1
fi

echo "=== PARITY URL=${ANHUR_URL} ==="

echo ""
echo "=== GO ==="
(cd "$ROOT/golang" && go run ./scripts/parity_probe/)

echo ""
echo "=== PYTHON ==="
python3 "$ROOT/scripts/parity/probe_python.py"

echo ""
echo "=== TYPESCRIPT ==="
(cd "$ROOT/typescript" && npx --yes tsx "$ROOT/scripts/parity/probe_typescript.ts")

echo ""
echo "=== ALL SDKs PASSED ==="
