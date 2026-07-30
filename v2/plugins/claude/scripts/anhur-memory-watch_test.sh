#!/bin/sh
# anhur-memory-watch_test.sh — offline tests for the background monitor.
#
# Run:  sh scripts/anhur-memory-watch_test.sh
#
# Junior Tip [test the alarm, not the alarmed system]: this suite never touches AnhurDB, a
# key, or the real ~/.anhur-claude-memory. It builds synthetic plugin.log fixtures — copied
# verbatim from lines the engine really writes — and asserts what the monitor says about
# them. The failure this monitor exists to catch (743 silent skips) is reproduced as
# fixture "blackout": if a future edit breaks the pattern, that test goes red instead of the
# memory going quiet for twelve days.

set -u

script_directory=$(cd "$(dirname "$0")" && pwd)
watch_script="$script_directory/anhur-memory-watch.sh"

tests_run=0
tests_failed=0

report_pass() {
    tests_run=$((tests_run + 1))
    printf '  ok   %s\n' "$1"
}

report_fail() {
    tests_run=$((tests_run + 1))
    tests_failed=$((tests_failed + 1))
    printf '  FAIL %s\n     expected: %s\n     actual:   %s\n' "$1" "$2" "$3"
}

assert_equals() {
    if [ "$2" = "$3" ]; then
        report_pass "$1"
    else
        report_fail "$1" "$2" "$3"
    fi
}

assert_contains() {
    case "$3" in
    *"$2"*) report_pass "$1" ;;
    *) report_fail "$1" "output containing '$2'" "$3" ;;
    esac
}

assert_not_contains() {
    case "$3" in
    *"$2"*) report_fail "$1" "no '$2' in output" "$3" ;;
    *) report_pass "$1" ;;
    esac
}

# ── 1. epoch_from_stamp ──────────────────────────────────────────────────────
# Sourced with the test seam so nothing starts watching.
ANHUR_WATCH_DEFINE_ONLY=1
export ANHUR_WATCH_DEFINE_ONLY
# shellcheck source=./anhur-memory-watch.sh
. "$watch_script"
unset ANHUR_WATCH_DEFINE_ONLY

printf 'epoch_from_stamp\n'
assert_equals "unix epoch" "0" "$(epoch_from_stamp '1970-01-01T00:00:00Z')"
assert_equals "known instant" "1600000000" "$(epoch_from_stamp '2020-09-13T12:26:40Z')"
# Leading zeros in month/day/hour/minute/second are the classic POSIX $(( )) octal trap.
# Constant checked against: date -u -d '2028-08-09T08:09:09Z' +%s
assert_equals "leading zeros (08/09)" "1849421349" "$(epoch_from_stamp '2028-08-09T08:09:09Z')"
assert_equals "leap day" "1709164800" "$(epoch_from_stamp '2024-02-29T00:00:00Z')"
assert_equals "line from the real log" "1785434207" "$(epoch_from_stamp '2026-07-30T17:56:47Z')"
assert_equals "garbage returns empty" "" "$(epoch_from_stamp 'not-a-timestamp' || true)"
assert_equals "empty returns empty" "" "$(epoch_from_stamp '' || true)"

# Cross-check against date(1) when the GNU form is available: proves the inline calendar
# math, not just its own constants.
if date -u -d '2026-07-30T17:56:47Z' +%s >/dev/null 2>&1; then
    assert_equals "agrees with GNU date" \
        "$(date -u -d '2026-07-30T17:56:47Z' +%s)" \
        "$(epoch_from_stamp '2026-07-30T17:56:47Z')"
fi

# ── fixtures ─────────────────────────────────────────────────────────────────
fixture_root=$(mktemp -d "${TMPDIR:-/tmp}/anhur-watch-test.XXXXXX")
trap 'rm -rf "$fixture_root"' EXIT INT TERM

# run_assessment <fixture-log> [stale-minutes] → whatever assess_log() prints
run_assessment() {
    fixture_log=$1
    ANHUR_STATE_DIR=$(dirname "$fixture_log")
    export ANHUR_STATE_DIR
    ANHUR_WATCH_STALE_MINUTES=${2:-2880}
    export ANHUR_WATCH_STALE_MINUTES
    ANHUR_WATCH_DEFINE_ONLY=1 sh -c '
        . "$1"
        assess_log
    ' _ "$watch_script"
    unset ANHUR_STATE_DIR ANHUR_WATCH_STALE_MINUTES
}

printf 'assess_log\n'

# (a) The 2026-07 blackout: hooks firing, key missing, nothing saved for days.
blackout_dir="$fixture_root/blackout"
mkdir -p "$blackout_dir"
{
    printf '2026-07-18T18:34:03Z persist: lines 1-40 (session=abc, chunks=1 sent=1 queued=0)\n'
    fixture_day=18
    while [ "$fixture_day" -le 29 ]; do
        printf '2026-07-%02dT10:00:00Z ANHUR_API_KEY not set — skipping\n' "$fixture_day"
        printf '2026-07-%02dT18:00:00Z ANHUR_API_KEY not set — skipping\n' "$fixture_day"
        fixture_day=$((fixture_day + 1))
    done
} >"$blackout_dir/plugin.log"
blackout_output=$(run_assessment "$blackout_dir/plugin.log")
assert_contains "blackout: reports the failure count" "24 failure line(s)" "$blackout_output"
assert_contains "blackout: scopes it to the last success" "since the last successful AnhurDB call" "$blackout_output"
assert_contains "blackout: quotes the real log line" "ANHUR_API_KEY not set" "$blackout_output"
assert_contains "blackout: routes to a skill" "/anhurdb-memory:memory-health" "$blackout_output"

# (a2) The SAME blackout, already fixed: the failures are still in the log, but a successful
# call came after them. Re-announcing history at every session start is how a monitor teaches
# people to ignore it — the alarm must clear when the problem does.
recovered_dir="$fixture_root/recovered"
mkdir -p "$recovered_dir"
{
    cat "$blackout_dir/plugin.log"
    printf '2026-07-30T18:21:38Z persist: lines 11241-11269 (session=abc, chunks=1 sent=1 queued=0)\n'
} >"$recovered_dir/plugin.log"
assert_equals "recovered: old failures are history, not an alarm" "" \
    "$(run_assessment "$recovered_dir/plugin.log")"

# (a3) …and a failure AFTER that recovery is news again.
relapsed_dir="$fixture_root/relapsed"
mkdir -p "$relapsed_dir"
{
    cat "$recovered_dir/plugin.log"
    printf '2026-07-30T19:00:00Z persist: CreateSession failed — queueing all chunks: failed to connect to AnhurDB server\n'
} >"$relapsed_dir/plugin.log"
assert_contains "relapse after recovery is reported" "1 failure line(s)" \
    "$(run_assessment "$relapsed_dir/plugin.log")"

# (b) Healthy log, used minutes ago → the monitor must stay completely silent.
healthy_dir="$fixture_root/healthy"
mkdir -p "$healthy_dir"
{
    printf '2026-07-30T20:56:23Z persist: lines 11270-11315 (session=abc, chunks=1 sent=1 queued=0)\n'
    printf '2026-07-30T20:58:21Z recall: wrote memory block to stdout (bytes=2048)\n'
    printf '2026-07-30T21:00:41Z persist: lines 11325-11341 (session=abc, chunks=1 sent=1 queued=0)\n'
} >"$healthy_dir/plugin.log"
assert_equals "healthy: silent" "" "$(run_assessment "$healthy_dir/plugin.log")"

# (c) Healthy but IDLE for a month: the last success is ancient in wall-clock terms, yet
# nothing has run since. Measuring staleness against wall clock would scream here — the
# monitor must not, or the first session back from a vacation opens with a false alarm.
idle_dir="$fixture_root/idle"
mkdir -p "$idle_dir"
printf '2026-06-01T09:00:00Z persist: lines 1-40 (session=abc, chunks=1 sent=1 queued=0)\n' >"$idle_dir/plugin.log"
assert_equals "idle machine: no false stale alarm" "" "$(run_assessment "$idle_dir/plugin.log")"

# (d) Engine running for days, never once succeeding, and NOT matching the failure
# vocabulary (a future failure mode nobody has seen yet). The staleness rule is the
# backstop that still catches it.
stale_dir="$fixture_root/stale"
mkdir -p "$stale_dir"
{
    printf '2026-07-01T09:00:00Z config: key source=file env file=/x/env vars=3\n'
    printf '2026-07-05T09:00:00Z config: key source=file env file=/x/env vars=3\n'
    printf '2026-07-09T09:00:00Z config: key source=file env file=/x/env vars=3\n'
} >"$stale_dir/plugin.log"
stale_output=$(run_assessment "$stale_dir/plugin.log")
assert_contains "no success ever: raises a blackout alarm" "without a single successful AnhurDB call" "$stale_output"
assert_contains "no success ever: routes to the runbook" "/anhurdb-memory:memory-blackout" "$stale_output"

# (e) Success followed by days of activity with no further success.
regressed_dir="$fixture_root/regressed"
mkdir -p "$regressed_dir"
{
    printf '2026-07-01T09:00:00Z persist: lines 1-40 (session=abc, chunks=1 sent=1 queued=0)\n'
    printf '2026-07-06T09:00:00Z config: key source=file env file=/x/env vars=3\n'
} >"$regressed_dir/plugin.log"
assert_contains "success then 5 days of nothing: alarm" "without a single successful AnhurDB call" \
    "$(run_assessment "$regressed_dir/plugin.log")"
assert_equals "same log, 10-day threshold: silent" "" \
    "$(run_assessment "$regressed_dir/plugin.log" 14400)"

# ── 2. the follow filter (stream_notifications) ──────────────────────────────
#
# The SHIPPED function is exercised — never a copy of its rules, which would keep passing
# while the real one rotted. failure_pattern also comes from sourcing the watch script, so
# the log vocabulary under test is the log vocabulary that ships.
printf 'follow filter\n'

# run_filter <repeat_every> <max_notifications> — stdin: log lines, stdout: notifications.
run_filter() {
    repeat_every_occurrences=${1:-25}
    max_notifications=${2:-40}
    signature_counts_directory=$(mktemp -d "$fixture_root/counts.XXXXXX")
    stream_notifications
    rm -rf "$signature_counts_directory"
}

success_only=$(
    {
        printf '2026-07-30T21:00:41Z persist: lines 1-40 (session=abc, chunks=1 sent=1 queued=0)\n'
        printf '2026-07-30T21:01:41Z flushed queued chunk /home/u/.anhur-claude-memory/queue/x.txt\n'
        printf '2026-07-30T21:02:41Z recall: wrote memory block to stdout (bytes=2048)\n'
        printf '2026-07-30T21:03:41Z config: key source=file env file=/x/env vars=3\n'
    } | run_filter
)
assert_equals "healthy lines produce no notification" "" "$success_only"

# "queued chunk ->" is the no-silent-loss path WORKING; the failure line that caused it is
# what notifies. Alarming on both would double every outage.
queued_only=$(printf '2026-07-30T17:56:47Z queued chunk -> /home/u/.anhur-claude-memory/queue/x.txt\n' | run_filter)
assert_equals "a queued chunk alone is not an alarm" "" "$queued_only"

# The line that ran 743 times in silence.
key_missing=$(printf '2026-07-30T10:00:00Z ANHUR_API_KEY not set (env file: /home/u/.anhur-claude-memory/env, 0 vars loaded) — MEMORY IS NOT BEING SAVED\n' | run_filter)
assert_contains "missing key notifies" "MEMORY FAILURE" "$key_missing"
assert_contains "first notification carries the next step" "/anhurdb-memory:memory-health" "$key_missing"
assert_equals "one line in, one notification out" "1" "$(printf '%s\n' "$key_missing" | grep -c .)"

# Repetition: 30 occurrences that differ only by path/number are ONE problem.
repeated=$(
    repeat_index=1
    while [ "$repeat_index" -le 30 ]; do
        printf '2026-07-30T17:56:%02dZ flush still failing for /home/u/.anhur-claude-memory/queue/2026073%d.txt (retried on every persist): failed to connect\n' \
            "$((repeat_index % 60))" "$repeat_index"
        repeat_index=$((repeat_index + 1))
    done | run_filter
)
assert_equals "30 repeats of one failure = 2 notifications" "2" "$(printf '%s\n' "$repeated" | grep -c .)"
assert_contains "the repeat notification says how many" "still repeating (25 occurrences)" "$repeated"

# Distinct failures are distinct problems and must each be announced.
distinct=$(
    {
        printf '2026-07-30T17:00:00Z recall: profile failed (AnhurDB unreachable?): dial tcp\n'
        printf '2026-07-30T17:00:01Z queue write FAILED for chunk (data at risk): no space left on device\n'
        printf '2026-07-30T17:00:02Z ERROR: chunk has no session marker — quarantined /a -> /b (will NOT be persisted; needs manual review)\n'
        printf '2026-07-30T17:00:03Z persist: CreateSession failed — queueing all chunks: failed to connect to AnhurDB server\n'
        printf '2026-07-30T17:00:04Z archive: rename failed: read-only file system\n'
        printf '2026-07-30T17:00:05Z panic recovered: runtime error\n'
    } | run_filter
)
assert_equals "six distinct failures = six notifications" "6" "$(printf '%s\n' "$distinct" | grep -c .)"

# The cap bounds the worst case: 40 DIFFERENT failures (each a new kind, so dedupe cannot
# help) must not become 40 notifications in the model's context.
flood=$(
    for flood_suffix in alpha bravo charlie delta echo foxtrot golf hotel india juliet \
        kilo lima mike november oscar papa quebec romeo sierra tango; do
        printf '2026-07-30T17:00:00Z recall: profile failed (AnhurDB unreachable?): host-%s\n' "$flood_suffix"
    done | run_filter 25 5
)
assert_equals "notification cap holds" "6" "$(printf '%s\n' "$flood" | grep -c .)"
assert_contains "cap announces itself" "notification cap reached" "$flood"

# A counter file left empty by an interrupted write must not break the alarm path.
corrupt_counts_dir=$(mktemp -d "$fixture_root/corrupt.XXXXXX")
: >"$corrupt_counts_dir/notifications-sent"
corrupt_output=$(
    repeat_every_occurrences=25
    max_notifications=40
    signature_counts_directory=$corrupt_counts_dir
    printf '2026-07-30T10:00:00Z ANHUR_API_KEY not set — MEMORY IS NOT BEING SAVED\n' |
        stream_notifications 2>/dev/null
)
assert_contains "an empty counter file does not break the alarm" "MEMORY FAILURE" "$corrupt_output"

# Junior Tip [o oposto do que este teste afirmava — 2026-07-30]: existia aqui um caso
# provando que linhas "anhur-memory: ..." passavam direto pelo filtro. Esse ramo foi
# REMOVIDO do script: ele era inalcancavel (o produtor mudou) e era a unica rota que
# burlava o filtro de falha e o teto de notificacoes. O teste agora fixa a regra nova —
# nenhuma linha tem passagem livre, nem as que parecem vir do proprio script.
no_passthrough=$(printf 'anhur-memory: uma linha informativa qualquer.\n' | run_filter)
assert_equals "nenhuma linha tem passagem livre pelo filtro" "" "$no_passthrough"

# ── 3. missing log (fresh install vs. dead hooks) ────────────────────────────
#
# These last two groups run the real script end to end, which needs a way to stop it: it is
# a persistent watcher by design. Where timeout(1) is absent (stock macOS), they are skipped
# LOUDLY rather than silently passing.
if ! command -v timeout >/dev/null 2>&1; then
    printf 'missing log / live watch\n  SKIP (timeout(1) not available — run these on a host that has coreutils)\n'
    printf '\n%s test(s), %s failure(s)\n' "$tests_run" "$tests_failed"
    [ "$tests_failed" -eq 0 ]
    exit $?
fi

printf 'missing log\n'
missing_dir="$fixture_root/missing"
mkdir -p "$missing_dir"
missing_output=$(
    ANHUR_STATE_DIR="$missing_dir" \
        ANHUR_WATCH_STARTUP_DELAY_SECONDS=0 \
        ANHUR_WATCH_MISSING_LOG_SECONDS=0 \
        ANHUR_WATCH_MAX_NOTIFICATIONS=40 \
        timeout 6 sh "$watch_script" 2>/dev/null || true
)
assert_contains "absent log eventually warns" "no hook has written" "$missing_output"
assert_equals "and warns exactly once" "1" "$(printf '%s\n' "$missing_output" | grep -c 'no hook has written')"

fresh_output=$(
    ANHUR_STATE_DIR="$missing_dir" \
        ANHUR_WATCH_STARTUP_DELAY_SECONDS=0 \
        ANHUR_WATCH_MISSING_LOG_SECONDS=600 \
        timeout 4 sh "$watch_script" 2>/dev/null || true
)
assert_equals "fresh install stays quiet during the grace period" "" "$fresh_output"

# ── 4. end to end: the script watching a live log ────────────────────────────
printf 'live watch\n'
live_dir="$fixture_root/live"
mkdir -p "$live_dir"
printf '2026-07-30T21:00:41Z persist: lines 1-40 (session=abc, chunks=1 sent=1 queued=0)\n' >"$live_dir/plugin.log"
live_capture="$fixture_root/live-output.txt"
(
    ANHUR_STATE_DIR="$live_dir" \
        ANHUR_WATCH_STARTUP_DELAY_SECONDS=0 \
        ANHUR_WATCH_MISSING_LOG_SECONDS=600 \
        timeout 8 sh "$watch_script" >"$live_capture" 2>/dev/null
) &
live_pid=$!
sleep 3
printf '2026-07-30T21:05:00Z ANHUR_API_KEY not set (env file: /x/env, 0 vars loaded) — MEMORY IS NOT BEING SAVED\n' >>"$live_dir/plugin.log"
printf '2026-07-30T21:05:01Z persist: lines 41-80 (session=abc, chunks=1 sent=1 queued=0)\n' >>"$live_dir/plugin.log"
sleep 3
wait "$live_pid" 2>/dev/null || true
live_output=$(cat "$live_capture" 2>/dev/null || printf '')
assert_contains "a new failure surfaces while the session runs" "MEMORY IS NOT BEING SAVED" "$live_output"
assert_not_contains "the healthy line after it stays quiet" "sent=1" "$live_output"

# ── 5. shutdown ──────────────────────────────────────────────────────────────
#
# Regression test for a defect found on 2026-07-30: the cleanup trap handled TERM without
# exiting, so the shell RESUMED the interrupted sleep and kept polling forever. Every
# session would have leaked one watcher process. A monitor is a persistent process by
# design — that makes "it stops when told to" a correctness property, not a detail.
printf 'shutdown\n'
shutdown_dir="$fixture_root/shutdown"
mkdir -p "$shutdown_dir"
printf '2026-07-30T21:00:41Z persist: lines 1-40 (session=abc, chunks=1 sent=1 queued=0)\n' >"$shutdown_dir/plugin.log"
scratch_root="$fixture_root/scratch"
mkdir -p "$scratch_root"
ANHUR_STATE_DIR="$shutdown_dir" \
    ANHUR_WATCH_STARTUP_DELAY_SECONDS=0 \
    ANHUR_WATCH_MISSING_LOG_SECONDS=600 \
    ANHUR_WATCH_POLL_SECONDS=1 \
    TMPDIR="$scratch_root" \
    sh "$watch_script" >/dev/null 2>&1 &
watcher_pid=$!
sleep 2
assert_equals "the watcher owns a scratch dir while running" "1" \
    "$(ls -1 "$scratch_root" | wc -l | tr -d ' ')"
kill -TERM "$watcher_pid" 2>/dev/null
# A POSIX shell runs a trap only BETWEEN commands, so the handler fires after the current
# `sleep` returns: shutdown takes up to one poll interval. Wait for that instead of racing it.
shutdown_wait=0
while [ "$shutdown_wait" -lt 8 ] && kill -0 "$watcher_pid" 2>/dev/null; do
    sleep 1
    shutdown_wait=$((shutdown_wait + 1))
done
if kill -0 "$watcher_pid" 2>/dev/null; then
    report_fail "SIGTERM stops the watcher" "process gone" "still running"
    kill -KILL "$watcher_pid" 2>/dev/null
else
    report_pass "SIGTERM stops the watcher"
fi
wait "$watcher_pid" 2>/dev/null || true
assert_equals "the scratch dir is removed on exit" "0" \
    "$(ls -1 "$scratch_root" | wc -l | tr -d ' ')"

printf '\n%s test(s), %s failure(s)\n' "$tests_run" "$tests_failed"
[ "$tests_failed" -eq 0 ]
