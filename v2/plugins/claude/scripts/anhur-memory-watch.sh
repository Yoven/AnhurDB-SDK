#!/bin/sh
# anhur-memory-watch.sh — the background monitor declared in monitors/monitors.json.
#
# WHY THIS EXISTS (2026-07-30): between 2026-07-18 and 2026-07-30 this plugin ran 743 hook
# invocations and wrote NOTHING to AnhurDB. Every one of those runs appended
# "ANHUR_API_KEY not set — skipping" to plugin.log and exited 0 with empty stdout/stderr.
# The evidence was on disk from day one and nobody read it, because no human tails a log
# file and the model was never shown one. This script is the reader that was missing: it
# turns the plugin's own log into notifications INSIDE the session, where the model can see
# them and tell the user.
#
# Junior Tip [the watchdog must not depend on what it watches]: this is a plain POSIX shell
# script — no Go binary, no API key, no network. It reports on the engine WITHOUT going
# through the engine, so a broken key, an unreachable AnhurDB, or a crashing binary cannot
# take the alarm down with it. The one class it cannot cover is the plugin failing to load
# at all (then Claude Code never starts this monitor either); that one is only provable by
# asking the model whether it received the <anhur-memory> block. See README → Diagnosing.
#
# Junior Tip [exceptions only, never a heartbeat]: every stdout line here becomes a
# notification injected into the model's context, so noise is a cost paid on every turn of
# the session. The healthy path is already visible elsewhere — the <anhur-memory> block IS
# the proof that recall worked — so this script stays SILENT when nothing is wrong and
# speaks only for failure, staleness, or a log that never appears.
#
# Tunables (environment, all optional): ANHUR_STATE_DIR, ANHUR_WATCH_STARTUP_DELAY_SECONDS,
# ANHUR_WATCH_MISSING_LOG_SECONDS, ANHUR_WATCH_SCAN_LINES, ANHUR_WATCH_STALE_MINUTES,
# ANHUR_WATCH_POLL_SECONDS, ANHUR_WATCH_REPEAT_EVERY, ANHUR_WATCH_MAX_NOTIFICATIONS.

set -u

# ── configuration ────────────────────────────────────────────────────────────
#
# Junior Tip [resolve the state dir EXACTLY like the binary does]: core.go reads
# ANHUR_STATE_DIR from the process environment and falls back to $HOME/.anhur-claude-memory;
# it does NOT learn that path from the env file (the env file lives inside the state dir, so
# it could not). Mirroring that rule keeps this monitor pointed at the same plugin.log the
# hooks write to. If someone relocates the state dir only inside the env file, the binary and
# this script are wrong in the SAME way — far safer than being wrong in different ways.
state_directory="${ANHUR_STATE_DIR:-$HOME/.anhur-claude-memory}"
log_file_path="$state_directory/plugin.log"

# Wait before the first assessment so THIS session's own SessionStart recall line has landed:
# judging the log before the hook has written to it invents alarms.
startup_delay_seconds="${ANHUR_WATCH_STARTUP_DELAY_SECONDS:-20}"

# How long a missing plugin.log counts as "fresh install, nothing has run yet" before it
# counts as "no hook is firing in this session".
missing_log_grace_seconds="${ANHUR_WATCH_MISSING_LOG_SECONDS:-240}"

# How much history the one-shot startup assessment reads. The log is append-only and is never
# rotated by the engine, so it must not be scanned whole on every session start.
startup_scan_line_count="${ANHUR_WATCH_SCAN_LINES:-400}"

# How long the engine may keep running without a single successful AnhurDB call before that
# counts as a blackout. Measured in the LOG's own time base, not wall clock — see assess_log.
stale_success_minutes="${ANHUR_WATCH_STALE_MINUTES:-2880}"

# How often new log lines are picked up. A memory failure is not a millisecond-latency event;
# polling buys portability and a process that dies the instant the session ends.
poll_interval_seconds="${ANHUR_WATCH_POLL_SECONDS:-5}"

# A repeating failure is re-announced every Nth occurrence, so escalation stays visible
# without one notification per turn.
repeat_every_occurrences="${ANHUR_WATCH_REPEAT_EVERY:-25}"

# Hard ceiling on notifications per session: a pathological loop must not flood the context.
max_notifications="${ANHUR_WATCH_MAX_NOTIFICATIONS:-40}"

# ── the log vocabulary ───────────────────────────────────────────────────────
#
# Junior Tip [match the REAL log lines, not invented ones]: every alternative below is copied
# from a logLine() call site in plugins/core/core.go. Guessing here would reproduce the very
# bug this component exists to catch — a safety net that silently matches nothing. When
# core.go adds or renames a failure line, this pattern must change in the same commit.
#
#   ANHUR_API_KEY not set / MEMORY IS NOT BEING SAVED  → Run(), missing key (the 2026-07 blackout)
#   panic recovered                                    → Run(), recovered panic
#   profile failed                                     → cmdRecall(), AnhurDB unreachable
#   transcript not found / cannot read transcript      → cmdPersist(), nothing was persisted
#   CreateSession failed                               → cmdPersist()/flushQueue(), write blocked
#   flush still failing                                → flushQueue(), a queued chunk keeps failing
#   queue write FAILED                                 → queueChunk(), data at risk on disk
#   ERROR:                                             → quarantine paths (never self-healing)
#   " archive: " / archive without key                 → archiveTranscript(), lossless copy failed
#   unknown subcommand / usage: anhur                  → the hook is invoking the binary wrong
#
# Deliberately NOT matched: "queued chunk -> …" and "persist: … queued=N". A queued chunk is
# the no-silent-loss path WORKING, and it always accompanies a CreateSession/flush failure
# line that IS matched, so matching it too would double every outage.
failure_pattern='ANHUR_API_KEY not set|MEMORY IS NOT BEING SAVED|panic recovered|profile failed|transcript not found|cannot read transcript|CreateSession failed|flush still failing|queue write FAILED|ERROR:| archive: |archive without key|unknown subcommand|usage: anhur'

# Proof that the engine actually reached AnhurDB: a persist that sent at least one chunk, a
# drained queue chunk, or a recall that rendered a block (which requires a successful Profile).
success_pattern='sent=[1-9]|flushed queued chunk|wrote memory block to stdout'

# notify writes ONE notification. Claude Code delivers each stdout line as its own
# notification, so a notification must never span lines.
notify() {
    printf 'anhur-memory: %s\n' "$1"
}

# epoch_from_stamp converts an RFC3339 UTC stamp ("2026-07-30T17:56:47Z" — the format
# logLine() writes) into seconds since the Unix epoch. Prints nothing and fails on garbage.
#
# Junior Tip [why the calendar math is inline]: `date -d '2 days ago'` is GNU-only and
# `date -v-2d` is BSD-only, and this file ships to macOS and Linux alike; relying on either
# would silently disable the staleness check on half the installs. The conversion below is
# the standard days-from-civil algorithm and needs nothing but POSIX integer arithmetic.
# Two-digit fields drop a leading zero first because "08" is an INVALID octal literal inside
# POSIX $(( )) and aborts the shell.
epoch_from_stamp() {
    stamp_digits=$(printf '%s' "${1:-}" | tr -cd '0-9')
    if [ "${#stamp_digits}" -lt 14 ]; then
        return 1
    fi
    stamp_year=$(printf '%s' "$stamp_digits" | cut -c1-4)
    stamp_month=$(printf '%s' "$stamp_digits" | cut -c5-6)
    stamp_day=$(printf '%s' "$stamp_digits" | cut -c7-8)
    stamp_hour=$(printf '%s' "$stamp_digits" | cut -c9-10)
    stamp_minute=$(printf '%s' "$stamp_digits" | cut -c11-12)
    stamp_second=$(printf '%s' "$stamp_digits" | cut -c13-14)
    stamp_month=${stamp_month#0}
    stamp_day=${stamp_day#0}
    stamp_hour=${stamp_hour#0}
    stamp_minute=${stamp_minute#0}
    stamp_second=${stamp_second#0}

    shifted_year=$stamp_year
    if [ "$stamp_month" -le 2 ]; then
        shifted_year=$((stamp_year - 1))
    fi
    if [ "$shifted_year" -ge 0 ]; then
        era=$((shifted_year / 400))
    else
        era=$(((shifted_year - 399) / 400))
    fi
    year_of_era=$((shifted_year - era * 400))
    if [ "$stamp_month" -gt 2 ]; then
        month_index=$((stamp_month - 3))
    else
        month_index=$((stamp_month + 9))
    fi
    day_of_year=$(((153 * month_index + 2) / 5 + stamp_day - 1))
    day_of_era=$((year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year))
    days_since_epoch=$((era * 146097 + day_of_era - 719468))
    printf '%s' "$((days_since_epoch * 86400 + stamp_hour * 3600 + stamp_minute * 60 + stamp_second))"
}

# assess_log runs ONCE, at session start, and prints at most two notifications: one for
# failures already recorded, one for a blackout (engine running, nothing succeeding).
#
# Junior Tip [staleness is measured in the LOG's own time base]: comparing the last success
# against wall-clock "now" would scream at anyone who simply did not use Claude Code for a
# week — the last success is old because nothing ran, not because anything broke. What
# actually indicates a blackout is the engine RUNNING and never succeeding, so the gap
# measured here is (newest log line) − (last successful call). An idle machine has no newer
# lines and stays silent; the 12,8-day blackout had 743 newer lines and would have fired on
# day one.
assess_log() {
    # Junior Tip [only failures SINCE the last success are news, 2026-07-30]: scanning a fixed
    # window would re-announce an outage that already healed — the log still holds the 743
    # lines of the July blackout, and shouting about them at every session start would train
    # the user to dismiss this monitor, which is precisely how the incident survived. The log
    # is append-only, so anything after the last successful AnhurDB call is by definition
    # unresolved, and a problem that is still real keeps writing new lines (flushQueue retries
    # on every persist). Failures older than the last success are history, not an alarm.
    last_success_line_number=$(grep -n -E "$success_pattern" "$log_file_path" 2>/dev/null |
        tail -n 1 | cut -d: -f1)
    if [ -n "$last_success_line_number" ]; then
        unresolved_failures=$(tail -n "+$((last_success_line_number + 1))" "$log_file_path" 2>/dev/null |
            grep -E "$failure_pattern")
        unresolved_scope="since the last successful AnhurDB call"
    else
        # Nothing in this log has ever reached AnhurDB: judge the recent window instead.
        unresolved_failures=$(tail -n "$startup_scan_line_count" "$log_file_path" 2>/dev/null |
            grep -E "$failure_pattern")
        unresolved_scope="in the last $startup_scan_line_count lines, with NO successful AnhurDB call at all"
    fi
    failure_count=$(printf '%s\n' "$unresolved_failures" | grep -c .)
    if [ "$failure_count" -gt 0 ]; then
        newest_failure=$(printf '%s\n' "$unresolved_failures" | tail -n 1)
        notify "$(printf '%s failure line(s) %s in %s. Newest: %.200s' \
            "$failure_count" "$unresolved_scope" "$log_file_path" "$newest_failure")"
        notify "memory may not be saving. Tell the user now, then run /anhurdb-memory:memory-health."
        return 0
    fi

    newest_log_line=$(tail -n 1 "$log_file_path" 2>/dev/null)
    newest_epoch=$(epoch_from_stamp "${newest_log_line%% *}") || return 0
    last_success_line=$(grep -E "$success_pattern" "$log_file_path" 2>/dev/null | tail -n 1)
    if [ -n "$last_success_line" ]; then
        reference_epoch=$(epoch_from_stamp "${last_success_line%% *}") || return 0
        reference_label="last successful AnhurDB call was ${last_success_line%% *}"
    else
        oldest_log_line=$(head -n 1 "$log_file_path" 2>/dev/null)
        reference_epoch=$(epoch_from_stamp "${oldest_log_line%% *}") || return 0
        reference_label="the log holds NO successful AnhurDB call at all (it starts ${oldest_log_line%% *})"
    fi
    if [ -z "$newest_epoch" ] || [ -z "$reference_epoch" ]; then
        return 0
    fi

    gap_minutes=$(((newest_epoch - reference_epoch) / 60))
    if [ "$gap_minutes" -ge "$stale_success_minutes" ]; then
        notify "the memory engine kept running for $((gap_minutes / 60))h without a single successful AnhurDB call — $reference_label."
        notify "this is what a silent memory blackout looks like. Tell the user now, then run /anhurdb-memory:memory-blackout."
    fi
}

# stream_notifications reads plugin.log lines on stdin and writes notifications on stdout.
# It needs $signature_counts_directory: a writable directory it owns, holding one small file
# per failure KIND plus the running notification total.
#
# Junior Tip [state lives on disk, not in a variable, 2026-07-30]: this function is invoked
# once per poll batch, each time inside a pipeline — that is a SUBSHELL, so any counter kept
# in a shell variable dies with the batch and every batch would re-announce the same failure
# as if it were new. The counters therefore live in files. Same reason the cap survives:
# without it, a failure repeating every turn for an eight-hour session would push out the
# rest of the model's context.
#
# Junior Tip [dedupe by KIND, not by line]: one stuck queue writes one failure line PER CHUNK
# PER PERSIST. Announcing each would flood the session with the same news, and a flooded
# channel gets ignored — which is how the incident stayed invisible in the first place.
# Timestamps, paths and numbers are stripped to reduce a line to its kind: the first
# occurrence speaks, then every Nth, so escalation is still visible.
stream_notifications() {
    notifications_sent_file="$signature_counts_directory/notifications-sent"
    notifications_sent=$(cat "$notifications_sent_file" 2>/dev/null || printf '0')
    # `+ 0` normalises an empty file (an interrupted write) to 0. Without it the `-ge` test
    # below would fail with "Illegal number" — the alarm path must not break on its own state.
    notifications_sent=$((notifications_sent + 0))

    while IFS= read -r log_line; do
        # Junior Tip [nao existe passagem livre — removido em 2026-07-30]: havia aqui um
        # ramo que deixava passar direto qualquer linha comecando por "anhur-memory: ",
        # sob a premissa de que o proprio script a tinha formatado. Duas coisas estavam
        # erradas: o produtor que gerava essas linhas foi substituido (o ramo virou
        # inalcancavel), e enquanto existiu ele era o UNICO caminho que burlava as duas
        # protecoes deste laco — o filtro de falha e o teto de notificacoes. Um monitor
        # que pode inundar o modelo e um monitor que sera desligado, e um monitor
        # desligado nao viu o blackout de 12,8 dias. Toda linha passa pelo filtro e pelo
        # teto, sem excecao.
        printf '%s\n' "$log_line" | grep -qE "$failure_pattern" || continue
        if [ "$notifications_sent" -ge "$max_notifications" ]; then
            continue
        fi

        # cksum is POSIX and yields a stable numeric name for the kind — no md5/sha needed.
        signature_key=$(printf '%s' "$log_line" |
            sed 's/^[^ ]* //; s#/[^ ]*#<path>#g; s/[0-9][0-9]*/<n>/g' |
            cksum | cut -d' ' -f1)
        occurrence_file="$signature_counts_directory/kind-$signature_key"
        occurrence_count=$(cat "$occurrence_file" 2>/dev/null || printf '0')
        occurrence_count=$((occurrence_count + 0))
        occurrence_count=$((occurrence_count + 1))
        printf '%s' "$occurrence_count" >"$occurrence_file"

        if [ "$occurrence_count" -eq 1 ]; then
            # Only the FIRST notification of the session carries the next step: repeating
            # that instruction on every line costs context and adds no information.
            if [ "$notifications_sent" -eq 0 ]; then
                notify "$(printf 'MEMORY FAILURE — %.200s — tell the user, then run /anhurdb-memory:memory-health' "$log_line")"
            else
                notify "$(printf 'MEMORY FAILURE — %.200s' "$log_line")"
            fi
            notifications_sent=$((notifications_sent + 1))
        elif [ "$((occurrence_count % repeat_every_occurrences))" -eq 0 ]; then
            notify "$(printf 'MEMORY FAILURE still repeating (%s occurrences) — %.200s' "$occurrence_count" "$log_line")"
            notifications_sent=$((notifications_sent + 1))
        fi

        if [ "$notifications_sent" -eq "$max_notifications" ]; then
            notify "notification cap reached for this session — further failures are only in $log_file_path"
            notifications_sent=$((notifications_sent + 1))
        fi
    done

    printf '%s' "$notifications_sent" >"$notifications_sent_file"
}

# ── test seam ────────────────────────────────────────────────────────────────
#
# Junior Tip [a safety net nobody tests is not a safety net]: the 2026-07 blackout happened
# in code that had never been exercised by a single test. With ANHUR_WATCH_DEFINE_ONLY=1
# this file can be sourced for its functions without starting the watch, so the log
# vocabulary, the calendar math and the alarm rules are all verifiable offline — see
# scripts/anhur-memory-watch_test.sh.
if [ "${ANHUR_WATCH_DEFINE_ONLY:-}" = "1" ]; then
    return 0 2>/dev/null || exit 0
fi

# ── phase 0: the watcher prepares its own scratch space ──────────────────────
#
# Junior Tip [a broken watchdog must not be a quiet watchdog]: without this directory the
# dedupe counters have nowhere to live, and a monitor that looks alive while reporting
# nothing is the exact shape of the incident it exists to prevent. Say it once and stop.
signature_counts_directory=$(mktemp -d "${TMPDIR:-/tmp}/anhur-memory-watch.XXXXXX" 2>/dev/null) || signature_counts_directory=""
if [ -z "$signature_counts_directory" ]; then
    notify "memory monitor cannot start: no writable temp directory, so $log_file_path is NOT being watched this session. Tell the user."
    exit 1
fi
# Junior Tip [the signal traps must EXIT, not just clean up]: a trap handler that returns
# resumes the interrupted command, so a TERM at session end would clean the temp dir and
# then keep polling forever — one orphaned watcher per session. Exiting from the handler
# runs the EXIT trap, which is where the removal lives.
trap 'rm -rf "$signature_counts_directory"' EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

# ── phase 1: wait for the log to exist ───────────────────────────────────────
#
# A fresh install has no plugin.log until the first hook runs, so its absence is normal for a
# few minutes and must not produce noise. Absence that PERSISTS is the opposite: it means no
# hook has executed the binary in this session — the invisible failure of 2026-07-16, when
# the plugin stopped loading and every hook silently stopped with it.
sleep "$startup_delay_seconds"
waited_seconds=$startup_delay_seconds
warned_about_missing_log=0
while [ ! -f "$log_file_path" ]; do
    if [ "$warned_about_missing_log" -eq 0 ] && [ "$waited_seconds" -ge "$missing_log_grace_seconds" ]; then
        notify "no hook has written $log_file_path in the first $((waited_seconds / 60)) minute(s) of this session — the memory hooks may not be firing at all."
        notify "if this is not a brand-new install, tell the user now, then run /anhurdb-memory:memory-blackout."
        warned_about_missing_log=1
    fi
    sleep 10
    waited_seconds=$((waited_seconds + 10))
done

# ── phase 2: one-shot assessment of what is already in the log ───────────────
assess_log

# ── phase 3: follow new lines, failures only, deduplicated ───────────────────
#
# Junior Tip [poll instead of `tail -F`, 2026-07-30]: the first version piped `tail -F` into
# awk. It passed every offline test and delivered NOTHING live, because mawk (Debian's
# default awk) reads a pipe in 4 KiB blocks and sat waiting for a full block while the memory
# was already failing — a monitor with a false sense of coverage, the same defect class as a
# hook that exits 0 with an empty stderr. Polling keeps everything in ONE process: no awk
# buffering, no `tail -F` differences between GNU and BSD, no orphaned children when the
# session ends. The cost is up to $poll_interval_seconds of delay on an alarm about a memory
# that is already failing — irrelevant at this timescale.
#
# Junior Tip [start where the assessment stopped]: reading from the current end of file would
# lose any line written between the assessment and the first poll. Starting at
# (lines counted during the assessment + 1) closes that window with no risk of re-announcing
# what was just summarised.
last_processed_line_count=$(wc -l <"$log_file_path" 2>/dev/null || printf '0')
last_processed_line_count=$((last_processed_line_count))

while :; do
    sleep "$poll_interval_seconds"

    current_line_count=$(wc -l <"$log_file_path" 2>/dev/null || printf '0')
    current_line_count=$((current_line_count))

    # A shorter log means it was truncated or rotated underneath us (a human, a disk tool).
    # Restart from the top rather than going blind for the rest of the session; the on-disk
    # dedupe counters keep that from re-announcing what was already reported.
    if [ "$current_line_count" -lt "$last_processed_line_count" ]; then
        last_processed_line_count=0
    fi

    if [ "$current_line_count" -gt "$last_processed_line_count" ]; then
        tail -n "+$((last_processed_line_count + 1))" "$log_file_path" 2>/dev/null |
            stream_notifications
        last_processed_line_count=$current_line_count
    fi
done
