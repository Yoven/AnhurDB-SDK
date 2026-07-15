#!/usr/bin/env bash
# generate-changelog.sh — build CHANGELOG / release notes for one SDK language.
#
# Usage:
#   ./.github/scripts/generate-changelog.sh <python|typescript|golang> [to_ref] [--write]
#
# Examples:
#   ./.github/scripts/generate-changelog.sh python HEAD
#   ./.github/scripts/generate-changelog.sh typescript v2/typescript/v2.0.1 --write
#   ./.github/scripts/generate-changelog.sh golang HEAD --write
#
# Junior Tip [why per-SDK, not monorepo-wide]: each language ships on its own
# cadence (PyPI / npm / go tag). Release notes must only list commits that
# touched that SDK's tree, otherwise a Go-only fix would pollute the Python
# changelog and confuse downstream consumers.

set -euo pipefail

SDK="${1:-}"
TO_REF="${2:-HEAD}"
WRITE_MODE="false"

if [[ "${3:-}" == "--write" ]]; then
  WRITE_MODE="true"
fi

if [[ -z "$SDK" ]]; then
  echo "usage: $0 <python|typescript|golang> [to_ref] [--write]" >&2
  exit 1
fi

case "$SDK" in
  python)
    SDK_DIR="v2/python"
    TAG_PREFIX="v2/python/v"
    TITLE="Python SDK"
    ;;
  typescript)
    SDK_DIR="v2/typescript"
    TAG_PREFIX="v2/typescript/v"
    TITLE="TypeScript SDK"
    ;;
  golang)
    SDK_DIR="v2/golang"
    TAG_PREFIX="v2/golang/v"
    TITLE="Go SDK"
    ;;
  *)
    echo "unknown sdk: $SDK (expected python, typescript, or golang)" >&2
    exit 1
    ;;
esac

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

LATEST_TAG="$(git tag -l "${TAG_PREFIX}*" | sort -V | tail -n1 || true)"
if [[ -z "$LATEST_TAG" ]]; then
  FROM_REF="$(git rev-list --max-parents=0 HEAD | tail -n1)"
  VERSION="2.0.0"
else
  FROM_REF="$LATEST_TAG"
  VERSION="${LATEST_TAG#${TAG_PREFIX}}"
fi

NEXT_VERSION="$(echo "$VERSION" | awk -F. -v OFS=. '{$NF += 1 ; print}')"
GENERATED_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

OUTPUT_FILE="${SDK_DIR}/CHANGELOG.md"
TEMP_NOTES="$(mktemp)"

{
  echo "# ${TITLE} Changelog"
  echo
  echo "## Unreleased (${NEXT_VERSION})"
  echo
  echo "_Generated at ${GENERATED_AT} from \`${FROM_REF:-root}\` → \`${TO_REF}\`_"
  echo
  if git rev-parse --verify "${FROM_REF}^{commit}" >/dev/null 2>&1; then
    git log --no-merges --pretty=format:'- %s (%h)' "${FROM_REF}..${TO_REF}" -- "${SDK_DIR}" ".github/scripts/generate-changelog.sh" ".github/workflows/release-${SDK}.yml" || true
  else
    git log --no-merges --pretty=format:'- %s (%h)' "${TO_REF}" -- "${SDK_DIR}" ".github/scripts/generate-changelog.sh" ".github/workflows/release-${SDK}.yml" || true
  fi
  echo
  echo
  echo "## ${VERSION}"
  echo
  echo "- Initial v2 release: unified \`Memory\` API parity across Python, TypeScript, and Go."
  echo "- Open Beta default endpoint: \`https://anhurdb.yoven.ai\`."
  echo "- Full MCP-aligned surface: search, query AST, manifests, entities, uploads, temporal versioning."
  echo
} > "$TEMP_NOTES"

cat "$TEMP_NOTES"

if [[ "$WRITE_MODE" == "true" ]]; then
  cp "$TEMP_NOTES" "$OUTPUT_FILE"
  echo "wrote ${OUTPUT_FILE}" >&2
fi

rm -f "$TEMP_NOTES"
