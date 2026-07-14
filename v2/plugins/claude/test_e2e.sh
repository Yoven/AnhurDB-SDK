#!/usr/bin/env bash
#
# test_e2e.sh — end-to-end validation of the AnhurDB Claude Code memory plugin.
#
# Exercises the REAL built binary against the LIVE AnhurDB backend named in
# ~/.anhur-claude-memory/env (the same config the session hooks use), covering
# every path that matters for "is my memory plugin healthy?":
#
#   1. preflight   — env vars present, binary built, cache binary not stale
#   2. auth        — the API key authenticates against the host (X-API-Key)
#   3. recall      — `recall` runs clean, no auth failure, drains queue
#   4. persist     — `persist` writes a marker turn and it ROUND-TRIPS back
#   5. no-loss     — a write during a DB outage is QUEUED, then FLUSHED on recovery
#   6. unit        — the pure-logic Go unit tests still pass
#
# Isolation: the whole run points ANHUR_STATE_DIR at a throwaway temp dir, so it
# NEVER touches the user's live queue / cursors / plugin.log. It DOES write a
# couple of real records to the tenant (that is the point of an E2E), each tagged
# with a unique per-run id and DELETED again in cleanup.
#
# Junior Tip [why drive the binary, not just the SDK, 2026-07-14]: the value
# paths (cursor delta, transcript extraction, queue-on-failure, flush-on-recall)
# live in the binary + filesystem, not in a single SDK call. Testing the shipped
# artifact the hooks actually run is the only way to catch a stale/broken deploy.
#
# Usage:  ./test_e2e.sh [--build] [--keep]
#   --build  force `make build` before testing (default: build only if bin missing)
#   --keep   skip deletion of the test records (leave them for manual inspection)
#
# Exit code: 0 = all critical checks passed, 1 = at least one failed.

set -u

# ── locations ────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_BIN="$SCRIPT_DIR/bin/anhur-claude-memory"          # canonical build target (source of truth)
# The binary actually exercised. Defaults to the repo build; set PLUGIN_BIN to point
# the whole E2E at a different binary — e.g. the deployed cache binary the hooks run.
BIN_UNDER_TEST="${PLUGIN_BIN:-$REPO_BIN}"
ENV_FILE="$HOME/.anhur-claude-memory/env"
# The binary the Claude Code hooks ACTUALLY invoke lives in the plugin cache.
# We test the repo bin (source of truth) but compare against this to catch the
# recurring "cache binary went stale after a build" gotcha.
CACHE_BIN="$HOME/.claude/plugins/cache/anhur/anhurdb-memory/0.1.0/bin/anhur-claude-memory"

FORCE_BUILD=0
KEEP_RECORDS=0
for arg in "$@"; do
  case "$arg" in
    --build) FORCE_BUILD=1 ;;
    --keep)  KEEP_RECORDS=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

# ── reporting ────────────────────────────────────────────────────────────────
PASS_COUNT=0
FAIL_COUNT=0
WARN_COUNT=0
pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf '  \033[32m✓ PASS\033[0m  %s\n' "$1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); printf '  \033[31m✗ FAIL\033[0m  %s\n' "$1"; }
warn() { WARN_COUNT=$((WARN_COUNT + 1)); printf '  \033[33m! WARN\033[0m  %s\n' "$1"; }
info() { printf '         %s\n' "$1"; }
phase() { printf '\n\033[1m▸ %s\033[0m\n' "$1"; }

# ── config: source the LIVE env, then isolate state to a temp dir ────────────
if [[ ! -f "$ENV_FILE" ]]; then
  echo "FATAL: $ENV_FILE not found — plugin is not configured." >&2
  exit 1
fi
# shellcheck disable=SC1090
source "$ENV_FILE"
: "${ANHUR_API_KEY:?ANHUR_API_KEY missing from env}"
: "${ANHUR_URL:?ANHUR_URL missing from env}"
: "${ANHUR_CONTAINER:?ANHUR_CONTAINER missing from env}"
REAL_URL="$ANHUR_URL"   # remember the good URL; phase 5 swaps ANHUR_URL to force a queue

TEST_STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/anhur-plugin-e2e.XXXXXX")"
export ANHUR_STATE_DIR="$TEST_STATE_DIR"   # <- isolation: nothing below touches the live state dir

# Junior Tip [asdf shim needs a pinned Go version, 2026-07-14]: `go` here is an
# asdf shim that refuses to run in a directory with no pinned version ("No version
# is set for command go"). The core/ package pins none, so we pin the newest
# installed golang for THIS process via the env var asdf honours. Harmless when go
# is a real (non-asdf) install — it simply ignores the variable.
if command -v asdf >/dev/null 2>&1 && [[ -z "${ASDF_GOLANG_VERSION:-}" ]]; then
  export ASDF_GOLANG_VERSION="$(asdf list golang 2>/dev/null | sed 's/[* ]//g' | grep -E '^[0-9]' | tail -1)"
fi

# Unique per-run id. Because persist embeds the session id in every chunk header
# ("Claude Code session <id> — ..."), a run-unique session id makes our records
# findable AND cleanable with zero dependence on LLM summarisation or FTS lag.
RUN_ID="selftest-$(date -u +%Y%m%dT%H%M%SZ)-$$"
TEST_SESSION="plugin-$RUN_ID"
MARKER="PLUGIN_SELFTEST_MARKER_$RUN_ID"
# Single-token sentinels for the thinking + tool_result blocks. They must NOT share any FTS
# token with MARKER/RUN_ID (which ARE persisted), otherwise a cortex-feed search would match
# the legit text and falsely report a leak. Pure alphanumeric = one FTS token; pid = unique.
THINK_SENTINEL="xthinkprobe$$"
TOOLRES_SENTINEL="xtoolresprobe$$"

# ── HTTP helpers (key from env, never echoed) ────────────────────────────────
# api_status URL_PATH → prints the HTTP status code of a GET.
api_status() {
  curl -sS -o /dev/null -w '%{http_code}' -m 15 "$REAL_URL$1" -H "X-API-Key: $ANHUR_API_KEY" 2>/dev/null
}
# recent_json LIMIT → prints the /recent response body.
recent_json() {
  curl -sS -m 15 "$REAL_URL/api/v1/recent?limit=$1" -H "X-API-Key: $ANHUR_API_KEY" 2>/dev/null
}
# search_json QUERY → prints the /search/global response body.
search_json() {
  curl -sS -m 15 -X POST "$REAL_URL/api/v1/search/global" \
    -H "X-API-Key: $ANHUR_API_KEY" -H "Content-Type: application/json" \
    -d "{\"text\":\"$1\",\"limit\":10}" 2>/dev/null
}

# ── cleanup: delete every record whose header carries our RUN_ID, drop temp dir ─
cleanup() {
  if [[ "$KEEP_RECORDS" -eq 0 ]]; then
    # Find records by the session id embedded in each chunk's summary/header.
    local ids
    ids="$(recent_json 50 | jq -r --arg needle "$TEST_SESSION" \
      '.records[]? | select((.summary // "") | contains($needle)) | .id' 2>/dev/null)"
    if [[ -n "${ids:-}" ]]; then
      local deleted=0
      while read -r rid; do
        [[ -z "$rid" ]] && continue
        # Junior Tip [best-effort, non-fatal, 2026-07-14]: cleanup must never mask a
        # real test result. A failed delete is reported, not fatal — the leftover id
        # is printed so it can be removed by hand.
        if curl -sS -o /dev/null -m 15 -X DELETE "$REAL_URL/api/v1/records/$rid" \
             -H "X-API-Key: $ANHUR_API_KEY" 2>/dev/null; then
          deleted=$((deleted + 1))
        else
          printf '  cleanup: could NOT delete test record id=%s (remove manually)\n' "$rid" >&2
        fi
      done <<< "$ids"
      printf '\ncleanup: deleted %s test record(s) for %s\n' "$deleted" "$RUN_ID"
    fi
  else
    printf '\ncleanup: --keep set; leaving test records for session %s\n' "$TEST_SESSION"
  fi
  rm -rf "$TEST_STATE_DIR"
}
trap cleanup EXIT

# ── synthetic transcript with the marker in the FIRST user turn ──────────────
# The transcript carries three fidelity probes tied to $RUN_ID: the text MARKER (kept by
# both the episodic feed and the archive), a thinking block + a tool_result (dropped by the
# episodic feed, MUST survive verbatim in the archive).
make_transcript() {
  local path="$1"
  cat > "$path" <<EOF
{"type":"user","message":{"content":"$MARKER please remember this self-test fact: the validation sky is teal ($RUN_ID)."}}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"$THINK_SENTINEL internal reasoning that must NOT reach the cortex but MUST be archived"},{"type":"text","text":"Acknowledged — recording the teal validation-sky self-test fact ($RUN_ID)."},{"type":"tool_use","name":"Bash","input":{"command":"echo verifying $RUN_ID"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"$TOOLRES_SENTINEL full untruncated tool output the episodic feed drops"}]}}
EOF
}

# run_persist SESSION_ID TRANSCRIPT_PATH → feeds the hook stdin JSON and returns
# the binary's exit code; the binary logs sent/queued into $ANHUR_STATE_DIR/plugin.log.
run_persist() {
  printf '{"session_id":"%s","transcript_path":"%s","cwd":"%s"}' "$1" "$2" "$SCRIPT_DIR" \
    | "$BIN_UNDER_TEST" persist
}

echo "AnhurDB plugin E2E — host=$REAL_URL container=$ANHUR_CONTAINER run=$RUN_ID"
echo "isolated state dir: $TEST_STATE_DIR"

# ══ PHASE 1: preflight ═══════════════════════════════════════════════════════
phase "1. preflight"
if [[ "$FORCE_BUILD" -eq 1 || ! -x "$BIN_UNDER_TEST" ]]; then
  info "building binary (make build)…"
  if ( cd "$SCRIPT_DIR" && make build >/dev/null 2>&1 ); then pass "binary built"; else fail "make build failed"; fi
fi
if [[ -x "$BIN_UNDER_TEST" ]]; then pass "binary under test present ($BIN_UNDER_TEST)"; else fail "binary missing — run with --build"; fi
# Staleness: the hooks run CACHE_BIN. A byte-diff vs the repo build is a FALSE alarm —
# a different Go compiler emits different bytes from IDENTICAL source. The real question
# is whether the deployed binary is OLDER than any source file (someone edited source but
# never rebuilt+redeployed). Compare mtimes, not bytes.
if [[ -x "$CACHE_BIN" ]]; then
  newest_src="$(find "$SCRIPT_DIR/cmd" "$SCRIPT_DIR/../core" \( -name '*.go' -o -name 'go.mod' \) -exec stat -c '%Y' {} + 2>/dev/null | sort -n | tail -1)"
  cache_mtime="$(stat -c '%Y' "$CACHE_BIN" 2>/dev/null)"
  if [[ -n "$newest_src" && -n "$cache_mtime" && "$newest_src" -le "$cache_mtime" ]]; then
    pass "deployed cache binary is up to date with source"
  else
    warn "deployed cache binary is OLDER than source — rebuild + copy repo bin → $CACHE_BIN"
  fi
else
  warn "cache binary not found at $CACHE_BIN (plugin may be installed elsewhere)"
fi

# ══ PHASE 2: auth ════════════════════════════════════════════════════════════
phase "2. auth — API key against $REAL_URL"
# /profile: no key → 401, bad key → 403, good key → 400 "tag required" or 200 with ?tag.
status_with_key="$(api_status "/api/v1/profile?tag=$ANHUR_CONTAINER")"
status_no_key="$(curl -sS -o /dev/null -w '%{http_code}' -m 15 "$REAL_URL/api/v1/profile" 2>/dev/null)"
if [[ "$status_no_key" == "401" ]]; then pass "auth middleware live (no key → 401)"; else warn "no-key probe returned $status_no_key (expected 401)"; fi
case "$status_with_key" in
  200) pass "API key authenticates (profile → 200)" ;;
  400) pass "API key authenticates (profile → 400, past auth layer)" ;;
  401|403) fail "API key REJECTED (profile → $status_with_key) — key/host wrong" ;;
  000) fail "host unreachable ($REAL_URL) — DNS/connectivity" ;;
  *) warn "profile returned unexpected status $status_with_key" ;;
esac

# ══ PHASE 3: recall ══════════════════════════════════════════════════════════
phase "3. recall — hook path, isolated state"
recall_out="$("$BIN_UNDER_TEST" recall 2>/dev/null)"; recall_rc=$?
recall_log="$TEST_STATE_DIR/plugin.log"
if [[ "$recall_rc" -eq 0 ]]; then pass "recall exited 0 (never blocks the session)"; else fail "recall exited $recall_rc"; fi
if grep -q "authentication failed" "$recall_log" 2>/dev/null; then
  fail "recall logged an AUTH FAILURE (see $recall_log)"
elif grep -q "profile failed" "$recall_log" 2>/dev/null; then
  fail "recall could not reach the backend (profile failed)"
elif grep -q "injected memory block" "$recall_log" 2>/dev/null; then
  pass "recall authenticated and injected the <anhur-memory> block"
else
  pass "recall ran clean (no error logged)"
fi
if [[ "$recall_out" == *"<anhur-memory"* ]]; then info "recall emitted an <anhur-memory> block to stdout"; fi

# ══ PHASE 4: persist round-trip ══════════════════════════════════════════════
phase "4. persist — write a marker turn and read it back"
TRANSCRIPT="$TEST_STATE_DIR/transcript.jsonl"
make_transcript "$TRANSCRIPT"
persist_out="$(run_persist "$TEST_SESSION" "$TRANSCRIPT" 2>/dev/null)"
persist_log_line="$(grep 'persist:' "$TEST_STATE_DIR/plugin.log" | tail -1)"
info "persist log: ${persist_log_line:-<none>}"
if [[ "$persist_log_line" == *"sent=1"* && "$persist_log_line" == *"queued=0"* ]]; then
  pass "persist wrote the turn to the live backend (sent=1 queued=0)"
elif [[ "$persist_log_line" == *"queued=1"* ]]; then
  fail "persist could not reach the backend — turn was QUEUED (sent=0 queued=1)"
else
  fail "persist produced no sent/queued result (see $TEST_STATE_DIR/plugin.log)"
fi
# Round-trip: poll /recent (fresh SQLite, no FTS lag) for our session id in a chunk header.
info "polling /recent for the persisted record (read-your-writes)…"
found_id=""
for attempt in 1 2 3 4 5 6 7 8; do
  found_id="$(recent_json 30 | jq -r --arg needle "$TEST_SESSION" \
    '.records[]? | select((.summary // "") | contains($needle)) | .id' 2>/dev/null | head -1)"
  [[ -n "$found_id" ]] && break
  sleep 2
done
if [[ -n "$found_id" ]]; then
  pass "round-trip: persisted record is retrievable (id=$found_id)"
else
  fail "round-trip: persisted record NOT found in /recent within ~16s"
fi
# Secondary (soft): confirm the search pipeline also indexes it.
search_count="$(search_json "$MARKER" | jq -r '.count // 0' 2>/dev/null)"
if [[ "${search_count:-0}" -ge 1 ]]; then
  pass "search/global indexed the marker (count=$search_count)"
else
  warn "search/global has not indexed the marker yet (count=${search_count:-0}) — FTS/enrichment lag, not necessarily a fault"
fi

# ══ PHASE 5: lossless archive ════════════════════════════════════════════════
phase "5. archive — full verbatim transcript preserved"
ARCHIVE_FILE="$TEST_STATE_DIR/archive/$TEST_SESSION.jsonl"
if [[ -f "$ARCHIVE_FILE" ]]; then
  pass "archive written ($ARCHIVE_FILE)"
else
  fail "no archive file at $ARCHIVE_FILE (ANHUR_ARCHIVE broken?)"
fi
if [[ -f "$ARCHIVE_FILE" ]] && cmp -s "$ARCHIVE_FILE" "$TRANSCRIPT"; then
  pass "archive is byte-identical to the source transcript"
else
  fail "archive differs from the source transcript (not verbatim)"
fi
# Fidelity: the archive KEEPS what the episodic feed drops (thinking + tool_result).
if grep -q "$THINK_SENTINEL" "$ARCHIVE_FILE" 2>/dev/null && grep -q "$TOOLRES_SENTINEL" "$ARCHIVE_FILE" 2>/dev/null; then
  pass "archive kept thinking + tool_result verbatim (cortex feed drops them)"
else
  fail "archive missing thinking/tool_result — not the full record"
fi
# Cross-check the separation: those blocks must NOT be in what the cortex received. The
# sentinels are single unique tokens, so a hit here is a real leak, not tokenizer overlap.
leak_think="$(search_json "$THINK_SENTINEL" | jq -r '.count // 0' 2>/dev/null)"
leak_tool="$(search_json "$TOOLRES_SENTINEL" | jq -r '.count // 0' 2>/dev/null)"
if [[ "${leak_think:-0}" -eq 0 && "${leak_tool:-0}" -eq 0 ]]; then
  pass "thinking + tool_result did NOT leak into the cortex feed — separation holds"
else
  warn "leak into cortex feed (thinking=$leak_think tool_result=$leak_tool) — is ANHUR_INCLUDE_TOOLS=all?"
fi

# ══ PHASE 6: no-silent-loss queue ════════════════════════════════════════════
phase "6. no-silent-loss — queue on outage, flush on recovery"
QUEUE_DIR="$TEST_STATE_DIR/queue"
OUTAGE_SESSION="${TEST_SESSION}-outage"
OUTAGE_TRANSCRIPT="$TEST_STATE_DIR/transcript-outage.jsonl"
make_transcript "$OUTAGE_TRANSCRIPT"
# Force a write failure by pointing at a refused port with a tight timeout.
# Junior Tip [export+restore, not a prefix, 2026-07-14]: `VAR=x func` persists the
# assignment after a bash FUNCTION returns (unlike an external command), which would
# leave ANHUR_URL broken for the recovery step below. So we export explicitly and
# restore REAL_URL before recalling. The api_* helpers use $REAL_URL directly, so
# read-back/cleanup keep working even while ANHUR_URL points at the dead port.
queue_before="$(find "$QUEUE_DIR" -name '*.txt' 2>/dev/null | wc -l | tr -d ' ')"
export ANHUR_URL="http://127.0.0.1:1" ANHUR_HTTP_TIMEOUT=3
run_persist "$OUTAGE_SESSION" "$OUTAGE_TRANSCRIPT" >/dev/null 2>&1
export ANHUR_URL="$REAL_URL"; unset ANHUR_HTTP_TIMEOUT
queue_after="$(find "$QUEUE_DIR" -name '*.txt' 2>/dev/null | wc -l | tr -d ' ')"
if [[ "$queue_after" -gt "$queue_before" ]]; then
  pass "backend outage → chunk QUEUED to disk (not dropped)"
else
  fail "outage did not queue a chunk (no-silent-loss path may be broken)"
fi
# Recover: recall against the real URL must flush the queued chunk.
"$BIN_UNDER_TEST" recall >/dev/null 2>&1
queue_final="$(find "$QUEUE_DIR" -name '*.txt' 2>/dev/null | wc -l | tr -d ' ')"
if grep -q "flushed queued chunk" "$TEST_STATE_DIR/plugin.log" 2>/dev/null && [[ "$queue_final" -eq 0 ]]; then
  pass "recovery → queued chunk FLUSHED, queue empty"
else
  fail "queued chunk not flushed on recovery (queue=$queue_final remaining)"
fi

# ══ PHASE 7: unit tests ══════════════════════════════════════════════════════
phase "7. unit — pure-logic Go tests (core package)"
CORE_DIR="$SCRIPT_DIR/../core"
if [[ -d "$CORE_DIR" ]] && command -v go >/dev/null 2>&1; then
  if ( cd "$CORE_DIR" && go test ./... >/tmp/anhur-e2e-gotest.log 2>&1 ); then
    pass "go test ./... green ($(grep -oE 'ok[[:space:]]+\S+' /tmp/anhur-e2e-gotest.log | head -1))"
  else
    fail "go test ./... failed (see /tmp/anhur-e2e-gotest.log)"
  fi
else
  warn "skipping unit tests (core dir or go toolchain unavailable)"
fi

# ── summary ──────────────────────────────────────────────────────────────────
printf '\n\033[1m─── summary ───\033[0m\n'
printf 'PASS=%d  FAIL=%d  WARN=%d\n' "$PASS_COUNT" "$FAIL_COUNT" "$WARN_COUNT"
if [[ "$FAIL_COUNT" -eq 0 ]]; then
  printf '\033[32mPLUGIN OK — all critical checks passed.\033[0m\n'
  exit 0
else
  printf '\033[31mPLUGIN NOT OK — %d critical check(s) failed.\033[0m\n' "$FAIL_COUNT"
  exit 1
fi
