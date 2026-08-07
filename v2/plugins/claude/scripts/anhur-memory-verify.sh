#!/bin/sh
# anhur-memory-verify.sh — one command, one question: IS THE MEMORY WORKING RIGHT NOW?
#
# WHY THIS EXISTS (2026-07-30): between 2026-07-18 and 2026-07-30 this plugin ran 743 hook
# invocations and wrote NOTHING to AnhurDB. Every single run exited 0, with empty stdout and
# empty stderr. Everything that "checks a process" would have reported success for 12,8 days.
#
# So this script never asks "did it exit 0". It asks "what CHANGED": a record written and
# read back, a config line the binary itself logged, a checksum, a file count. The exit code
# of the engine is deliberately ignored everywhere below — see step 2, which proves the
# engine still screams on a missing key precisely because a silent success is indistinguishable
# from a silent failure.
#
# Junior Tip [what this script can and cannot prove]: it exercises the ENGINE end to end
# (install → config → network → write → read back). It CANNOT prove that the <anhur-memory>
# block reaches the model — that depends on Claude Code having loaded the plugin and fired the
# hook, which no external process can observe. The verdict says so out loud instead of
# implying a coverage it does not have. A verifier that says OK when it is wrong is worse than
# no verifier at all.
#
# Usage: sh anhur-memory-verify.sh [--offline] [--help]
# Exit:  0 = alive   1 = dead (something must be fixed)   2 = alive with warnings / not proved
#
# Tunables (environment, all optional):
#   ANHUR_STATE_DIR                 the plugin state dir under test (default ~/.anhur-claude-memory)
#   ANHUR_ENV_FILE                  the config file under test (default <state dir>/env)
#   CLAUDE_CONFIG_DIR               Claude Code's config tree (default ~/.claude)
#   ANHUR_PLUGIN_SOURCE_DIR         the source worktree to compare the installed snapshot against
#   ANHUR_VERIFY_CONTAINER          container the probe record is written to (default claude-ltm-verify)
#   ANHUR_VERIFY_READBACK_ATTEMPTS  read-back retries before giving up (default 5, 3s apart)
#   ANHUR_VERIFY_HOOK_WINDOW_MINUTES  "are the hooks firing" window in step 6 (default 60)

set -u

# Junior Tip [no `set -e`, on purpose]: this script must reach its verdict even when a step
# fails — that is its entire job. Aborting halfway would leave the owner with a truncated
# report to interpret by hand, which is the failure mode this tool exists to remove. Every
# step records its own outcome and execution continues.

# Junior Tip [refuse to run traced]: with `sh -x` the shell echoes every expansion, and the
# read-back step holds the API key in a variable. Whatever is printed in a Claude Code session
# is persisted into AnhurDB and archived verbatim on disk — a key must never get there. Better
# to refuse than to leak.
case "$-" in
*x*)
    echo "anhur-memory-verify: refusing to run with shell tracing (-x): it would print the API key." >&2
    exit 1
    ;;
esac

# ── usage ────────────────────────────────────────────────────────────────────

usage() {
    cat <<'USAGE'
anhur-memory-verify.sh — is the AnhurDB long-term memory working RIGHT NOW?

    sh anhur-memory-verify.sh [--offline]

Runs six checks against the INSTALLED plugin (not the source tree) and prints one verdict:

  1 INSTALLATION   plugin installed, enabled, loaded; installed snapshot vs source
  2 CONFIG         can the engine READ the key? (proved by running it, twice)
  3 CONNECTIVITY   is the endpoint up, and is the key accepted?
  4 WRITE          persist a real record through the real engine
  5 READ-BACK      find that record again — the only check that closes the loop
  6 STATE          queue, quarantine, whether the hooks are firing, and the failure log

Options:
  --offline, --no-network   skip steps 3-5: this script then makes no HTTP call of its own
                            (step 1 still shells out to `claude plugin list`). The write/read
                            cycle is NOT proved, and the verdict says so.
  -h, --help                this text.

Exit codes:
  0  memory is alive (write + read-back both succeeded, nothing else wrong)
  1  memory is dead — at least one check failed; each failure prints the command that fixes it
  2  alive with warnings, or --offline (the cycle was not proved)

Step 4 WRITES one small record, into container "claude-ltm-verify" and a fresh session named
anhur-verify-<utc>-<rand> — never into your real memory container and never into an existing
session. What it created is printed at the end.

The API key is never printed, never passed on a command line, and never written to disk by
this script.
USAGE
}

# ── options ──────────────────────────────────────────────────────────────────

network_enabled=1
while [ $# -gt 0 ]; do
    case "$1" in
    -h | --help)
        usage
        exit 0
        ;;
    --offline | --no-network | --skip-network)
        network_enabled=0
        ;;
    *)
        echo "anhur-memory-verify: unknown option '$1' (try --help)" >&2
        exit 1
        ;;
    esac
    shift
done

# ── outcome recording ────────────────────────────────────────────────────────
#
# Junior Tip [record_* must never run inside a pipeline]: in POSIX sh each stage of a pipeline
# is a SUBSHELL, so a counter incremented there dies with the stage and the verdict would
# silently under-count failures. Every call below is made in the main shell.

failure_count=0
warning_count=0
first_failure_reason=""

report() { printf '  %-6s %s\n' "$1" "$2"; }

report_fix() { printf '         fix: %s\n' "$1"; }

record_ok() { report "[ OK ]" "$1"; }

record_info() { report "[info]" "$1"; }

record_warn() {
    report "[WARN]" "$1"
    [ -n "${2:-}" ] && report_fix "$2"
    warning_count=$((warning_count + 1))
}

record_fail() {
    report "[FAIL]" "$1"
    [ -n "${2:-}" ] && report_fix "$2"
    failure_count=$((failure_count + 1))
    [ -z "$first_failure_reason" ] && first_failure_reason="$1"
}

step_header() { printf '\n[%s] %s\n' "$1" "$2"; }

# ── small helpers ────────────────────────────────────────────────────────────

# run_with_timeout SECONDS COMMAND... — `timeout` is coreutils and is absent from a stock
# macOS, so fall back to running the command bare rather than skipping the check entirely.
run_with_timeout() {
    timeout_seconds=$1
    shift
    if command -v timeout >/dev/null 2>&1; then
        timeout "$timeout_seconds" "$@"
    else
        "$@"
    fi
}

# strip_ansi removes colour escapes so `claude plugin list` can be parsed the same way whether
# or not it thinks it is writing to a terminal.
strip_ansi() {
    LC_ALL=C sed "s/$(printf '\033')\[[0-9;]*[a-zA-Z]//g"
}

# json_string_after FILE ANCHOR_KEY WANTED_KEY — prints the first "WANTED_KEY": "value" that
# appears AFTER the last occurrence of "ANCHOR_KEY" in the file. Pass an empty anchor to search
# from the start.
#
# Junior Tip [why newlines become spaces and nothing else is stripped]: a raw newline is
# ILLEGAL inside a JSON string (it must be escaped), so joining lines can never corrupt a
# value — but stripping spaces would, and installPath can legitimately contain them. This is
# the one normalisation that makes the same expression work on pretty-printed and compact JSON
# alike, which matters because Claude Code owns the formatting of these files, not us.
json_string_after() {
    tr '\n' ' ' <"$1" 2>/dev/null |
        sed "s/.*\"$2\"//" |
        grep -o "\"$3\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
        head -n 1 |
        sed 's/^[^:]*:[[:space:]]*"//; s/"$//'
}

# env_file_value FILE NAME — reads one variable out of the plugin's env file.
#
# Junior Tip [this MIRRORS plugins/core/envfile.go — and is not the source of truth]: the
# accepted format (optional `export ` prefix, one symmetric quote pair, `#` comments, trimmed
# spaces, environment wins) is reproduced here ONLY so the read-back step can authenticate.
# The CONFIG verdict in step 2 is decided exclusively by the binary's own log line, never by
# this parser — otherwise a divergence between the two would let the script certify a config
# the engine cannot actually read, which is exactly how the July blackout stayed invisible.
env_file_value() {
    [ -r "$1" ] || return 1
    while IFS= read -r raw_line || [ -n "$raw_line" ]; do
        trimmed_line=$(printf '%s' "$raw_line" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
        case "$trimmed_line" in
        '' | '#'*) continue ;;
        'export '*) trimmed_line=${trimmed_line#export } ;;
        esac
        variable_name=${trimmed_line%%=*}
        [ "$variable_name" = "$trimmed_line" ] && continue
        variable_value=${trimmed_line#*=}
        variable_name=$(printf '%s' "$variable_name" | sed 's/[[:space:]]*$//')
        [ -z "$variable_name" ] && continue
        [ "$variable_name" != "$2" ] && continue
        variable_value=$(printf '%s' "$variable_value" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
        case "$variable_value" in
        '"'*'"') variable_value=$(printf '%s' "$variable_value" | sed 's/^"//; s/"$//') ;;
        "'"*"'") variable_value=$(printf '%s' "$variable_value" | sed "s/^'//; s/'$//") ;;
        esac
        printf '%s' "$variable_value"
        return 0
    done <"$1"
    return 1
}

# checksum_of FILE — sha256 where available (Linux: sha256sum, macOS: shasum), else the POSIX
# cksum. Weaker, but any of the three answers the only question asked: same bytes or not?
checksum_of() {
    [ -f "$1" ] || return 1
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" 2>/dev/null | cut -d' ' -f1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" 2>/dev/null | cut -d' ' -f1
    else
        cksum "$1" 2>/dev/null | cut -d' ' -f1
    fi
}

# count_files DIR PATTERN — how many matching files, 0 when the directory is absent.
#
# Junior Tip [no `|| printf 0` here — it produced "0\n0"]: `grep -c .` already PRINTS 0 for an
# empty stream, it just EXITS 1 while doing it. Adding a fallback on that exit status appended a
# second zero, and the caller's `[ "$count" -eq 0 ]` then died with "Illegal number", falling
# through to a warning about a queue that was empty. Caught on the very first real run — a
# verifier that invents a failure is only marginally better than one that hides a real one.
count_files() {
    find "$1" -maxdepth 1 -name "$2" -type f 2>/dev/null | grep -c .
}

# random_hex — a per-run token. /dev/urandom is present on macOS and Linux; the fallback keeps
# the run possible (uniqueness only has to hold against this tenant's other verify records).
random_hex() {
    if [ -r /dev/urandom ]; then
        od -An -N6 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n'
    else
        printf '%s%s' "$$" "$(date -u +%s 2>/dev/null)"
    fi
}

# api_get PATH_AND_QUERY OUTPUT_FILE — authenticated GET, prints the HTTP status.
#
# Junior Tip [the key goes in on STDIN, never in argv and never to disk]: `-H "X-API-Key: …"`
# would expose it in `ps` to every user on the machine, and a temp header file would leave it
# on disk. `curl -K -` reads its config from a pipe, so the key exists only in this process
# and curl's memory.
api_get() {
    printf 'header = "X-API-Key: %s"\n' "$readback_api_key" |
        curl -sS -K - -o "$2" -w '%{http_code}' --max-time 25 "$anhur_url$1" 2>/dev/null
}

# api_post PATH BODY_FILE OUTPUT_FILE — authenticated POST, prints the HTTP status.
api_post() {
    printf 'header = "X-API-Key: %s"\nheader = "Content-Type: application/json"\n' "$readback_api_key" |
        curl -sS -K - -X POST --data-binary @"$2" -o "$3" -w '%{http_code}' --max-time 25 "$anhur_url$1" 2>/dev/null
}

# ── scratch space ────────────────────────────────────────────────────────────

work_dir=""
cleanup() { [ -n "$work_dir" ] && rm -rf "$work_dir"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/anhur-memory-verify.XXXXXX" 2>/dev/null) || work_dir=""
if [ -z "$work_dir" ]; then
    echo "anhur-memory-verify: no writable temp directory — cannot run." >&2
    exit 1
fi

# ── what is under test ───────────────────────────────────────────────────────
#
# Junior Tip [probe the INSTALLED copy, compare against the source]: the hooks run the snapshot
# in ~/.claude/plugins/cache, never the worktree. Testing the source would answer a question
# nobody asked and would have passed happily during the July blackout. The source is read for
# exactly one purpose: telling the owner that the snapshot is behind it.
real_state_dir="${ANHUR_STATE_DIR:-$HOME/.anhur-claude-memory}"
real_log_path="$real_state_dir/plugin.log"
# Junior Tip [resolve the env file EXACTLY like the engine does]: core's resolveEnvFilePath
# takes ANHUR_ENV_FILE when set and <stateDir>/env otherwise. Hardcoding the second half here
# would report "cannot read the key" to anyone using the override — a verifier that is wrong in
# a DIFFERENT way from the engine is worse than one that is wrong in the same way.
real_env_file="${ANHUR_ENV_FILE:-$real_state_dir/env}"
verify_container="${ANHUR_VERIFY_CONTAINER:-claude-ltm-verify}"
readback_attempts="${ANHUR_VERIFY_READBACK_ATTEMPTS:-5}"
plugin_id="anhurdb-memory@anhur"

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
plugin_source_dir="${ANHUR_PLUGIN_SOURCE_DIR:-$(dirname -- "$script_dir")}"

printf 'AnhurDB memory verifier — %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
printf 'state dir:   %s\n' "$real_state_dir"
printf 'config file: %s\n' "$real_env_file"

# ── step 1: installation ─────────────────────────────────────────────────────

step_header "1/6" "INSTALLATION — is the plugin installed, enabled, and current?"

# Junior Tip [CLAUDE_CONFIG_DIR is Claude Code's own override, not a test hook]: it relocates
# the whole ~/.claude tree, and anyone using it would get a false "plugin not installed" from a
# hardcoded $HOME/.claude. Read it the way Claude Code does, from the same variable.
claude_config_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
installed_plugins_file="$claude_config_dir/plugins/installed_plugins.json"
settings_file="$claude_config_dir/settings.json"
marketplaces_file="$claude_config_dir/plugins/known_marketplaces.json"

installed_dir=$(json_string_after "$installed_plugins_file" "$plugin_id" "installPath")
installed_version=$(json_string_after "$installed_plugins_file" "$plugin_id" "version")

# The marketplace is registered at the REPO ROOT (that is where .claude-plugin/marketplace.json
# lives), not at the plugin directory — so walk up from v2/plugins/claude to the repo.
marketplace_root_guess=$(CDPATH= cd -- "$plugin_source_dir/../../.." 2>/dev/null && pwd)
[ -n "$marketplace_root_guess" ] || marketplace_root_guess="<path-to-AnhurDB-SDK>"

if [ -z "$installed_dir" ]; then
    record_fail "$plugin_id is NOT installed (no entry in $installed_plugins_file)" \
        "claude plugin marketplace add $marketplace_root_guess && claude plugin install $plugin_id --scope user"
elif [ ! -d "$installed_dir" ]; then
    record_fail "installed snapshot is missing from disk: $installed_dir" \
        "claude plugin install $plugin_id --scope user"
else
    record_ok "installed: $plugin_id version ${installed_version:-unknown} -> $installed_dir"
fi

# `claude plugin list` is the only source that reports a plugin that FAILED TO LOAD — the JSON
# on disk still looks perfectly installed in that case, which is what made the 2026-07-16
# dangling-marketplace outage invisible for hours.
plugin_list_output="$work_dir/plugin-list.txt"
if command -v claude >/dev/null 2>&1; then
    run_with_timeout 90 claude plugin list 2>/dev/null | strip_ansi >"$plugin_list_output"
    plugin_block=$(sed -n "/$plugin_id/,/^\$/p" "$plugin_list_output")
    if [ ! -s "$plugin_list_output" ]; then
        # Junior Tip [an empty listing is a broken COMMAND, not an absent plugin]: if the CLI
        # timed out or errored, every plugin is "missing" — declaring the memory dead on that
        # evidence would be an alarm about the wrong thing, and a verifier that cries wolf gets
        # ignored exactly when it is right.
        record_warn "'claude plugin list' produced no output (timed out or failed) — load status unknown" \
            "claude plugin list   # run it by hand"
    elif [ -z "$plugin_block" ]; then
        record_fail "'claude plugin list' does not list $plugin_id" \
            "claude plugin install $plugin_id --scope user"
    else
        # Order matters: "disabled" CONTAINS "enabled", so the negative case is tested first.
        # Getting this backwards would make the verifier certify a disabled plugin as healthy.
        case "$plugin_block" in
        *"failed to load"*)
            record_fail "$plugin_id FAILED TO LOAD — no hook is registered, nothing is being saved" \
                "claude plugin marketplace update anhur   # and check the marketplace path still exists"
            ;;
        *disabled*)
            record_fail "$plugin_id is installed but DISABLED — no hook is registered" \
                "claude plugin enable $plugin_id"
            ;;
        *enabled*)
            record_ok "loaded and enabled according to 'claude plugin list'"
            ;;
        *)
            record_warn "could not read a status for $plugin_id from 'claude plugin list'" \
                "claude plugin list   # and read the Status line by hand"
            ;;
        esac
    fi
else
    record_warn "the 'claude' CLI is not on PATH — cannot detect a plugin that failed to LOAD" \
        "run this script from a shell where 'claude' resolves"
    if [ -f "$settings_file" ] &&
        tr '\n' ' ' <"$settings_file" | grep -q "\"$plugin_id\"[[:space:]]*:[[:space:]]*true"; then
        record_ok "enabled in $settings_file (fallback check)"
    else
        record_fail "$plugin_id is not enabled in $settings_file" \
            "claude plugin enable $plugin_id"
    fi
fi

# The marketplace is a `directory` source: it reads the live worktree on every load, so a moved
# or renamed manifest breaks the memory silently. Prove the registered path still holds one.
marketplace_path=$(json_string_after "$marketplaces_file" "anhur" "installLocation")
if [ -n "$marketplace_path" ]; then
    if [ -f "$marketplace_path/.claude-plugin/marketplace.json" ]; then
        record_ok "marketplace 'anhur' resolves: $marketplace_path/.claude-plugin/marketplace.json"
    else
        record_fail "marketplace 'anhur' is DANGLING: no .claude-plugin/marketplace.json under $marketplace_path" \
            "restore that file, then: claude plugin marketplace update anhur"
    fi
else
    record_warn "no 'anhur' marketplace registered in $marketplaces_file" \
        "claude plugin marketplace add <path-or-repo-of-AnhurDB-SDK>"
fi

# ── step 1b: is the snapshot behind the source? ──────────────────────────────
#
# Junior Tip [the number one cause of "I fixed it and nothing changed"]: installing COPIES the
# plugin into the cache. Editing the worktree afterwards changes nothing about what runs, and
# `claude plugin update` only re-copies when the version in plugin.json moved — so the
# dangerous state is not "different versions", it is SAME version, DIFFERENT bytes. Both are
# reported, and the second one names the bump as part of the fix.
source_version=""
if [ -f "$plugin_source_dir/.claude-plugin/plugin.json" ]; then
    source_version=$(json_string_after "$plugin_source_dir/.claude-plugin/plugin.json" "" "version")
fi

# Mirror bin/anhur-claude-memory's os/arch mapping so we compare the binary that actually runs.
case "$(uname -s)" in
Darwin) platform_os=darwin ;;
Linux) platform_os=linux ;;
*) platform_os="" ;;
esac
case "$(uname -m)" in
arm64 | aarch64) platform_arch=arm64 ;;
x86_64 | amd64) platform_arch=amd64 ;;
*) platform_arch="" ;;
esac

if [ -z "$installed_dir" ]; then
    : # nothing is installed — step 1 already said so; a staleness warning here is only noise
elif [ -z "$source_version" ]; then
    record_info "no source worktree found at $plugin_source_dir — skipping the snapshot comparison"
elif [ "$source_version" != "${installed_version:-}" ]; then
    record_warn "installed snapshot is version ${installed_version:-unknown}, source is $source_version — you are NOT running your source" \
        "claude plugin update $plugin_id   # then restart Claude Code"
elif [ -n "$platform_os" ] && [ -n "$platform_arch" ] && [ -n "$installed_dir" ]; then
    platform_binary="bin/anhur-claude-memory-$platform_os-$platform_arch"
    installed_checksum=$(checksum_of "$installed_dir/$platform_binary")
    source_checksum=$(checksum_of "$plugin_source_dir/$platform_binary")
    if [ -z "$installed_checksum" ] || [ -z "$source_checksum" ]; then
        record_warn "cannot compare engine binaries (missing $platform_binary in the snapshot or the source)" \
            "(cd $plugin_source_dir && make release-binaries)"
    elif [ "$installed_checksum" != "$source_checksum" ]; then
        record_warn "SAME version ($source_version) but a DIFFERENT engine binary is installed — the snapshot is stale" \
            "(cd $plugin_source_dir && make release-binaries) && bump 'version' in .claude-plugin/plugin.json && claude plugin update $plugin_id"
    else
        record_ok "snapshot matches source (version $source_version, identical $platform_os/$platform_arch engine)"
    fi
fi

# The hooks file is what turns the engine into a memory. An installed plugin with no hook entry
# saves nothing and says nothing.
engine_path=""
if [ -n "$installed_dir" ] && [ -d "$installed_dir" ]; then
    hooks_file="$installed_dir/hooks/hooks.json"
    if [ -f "$hooks_file" ] && grep -q 'recall' "$hooks_file" && grep -q 'persist' "$hooks_file"; then
        record_ok "hooks declared: SessionStart->recall, Stop/SessionEnd->persist ($hooks_file)"
    else
        record_fail "installed snapshot has no usable hooks/hooks.json — nothing will recall or persist" \
            "claude plugin update $plugin_id"
    fi

    # Run the WRAPPER, exactly like the hook does, so its own failure modes (unsupported arch,
    # missing prebuilt binary, lost exec bit) are covered instead of bypassed.
    engine_path="$installed_dir/bin/anhur-claude-memory"
    if [ -x "$engine_path" ]; then
        record_ok "engine wrapper is executable: $engine_path"
    else
        record_fail "engine wrapper missing or not executable: $engine_path" \
            "chmod +x '$engine_path'   # or: claude plugin update $plugin_id"
        engine_path=""
    fi
fi

# ── step 2: config ───────────────────────────────────────────────────────────

step_header "2/6" "CONFIG — can the engine READ the key? (proved by running it)"

key_source=""
if [ -z "$engine_path" ]; then
    record_fail "no runnable engine — cannot test the configuration" \
        "fix step 1 first"
else
    # 2a. No key anywhere. The engine MUST shout on stderr. A silent exit here means the
    # installed binary predates the 2026-07-30 fail-loud fix, and a future blackout would be
    # just as invisible as the last one — that is a defect even while memory happens to work.
    nokey_state_dir="$work_dir/nokey"
    mkdir -p "$nokey_state_dir"
    env -u ANHUR_API_KEY \
        ANHUR_STATE_DIR="$nokey_state_dir" \
        ANHUR_ENV_FILE="$nokey_state_dir/absent-env" \
        "$engine_path" recall </dev/null >"$work_dir/nokey.out" 2>"$work_dir/nokey.err"
    if grep -q 'MEMORY IS NOT BEING SAVED' "$work_dir/nokey.err" 2>/dev/null; then
        record_ok "engine fails LOUD on a missing key (stderr carries 'MEMORY IS NOT BEING SAVED')"
    else
        record_fail "engine stayed SILENT with no key — this build predates the fail-loud fix (2026-07-30)" \
            "(cd $plugin_source_dir && make release-binaries) && bump 'version' in .claude-plugin/plugin.json && claude plugin update $plugin_id"
    fi

    # 2b. The real env file. `env -u ANHUR_API_KEY` is the point of the test: hooks are spawned
    # by Claude Code, not by your terminal, so a key that only exists as a shell export is a key
    # the hook does not have. The engine must find it in the FILE.
    #
    # `persist` with empty stdin touches no network at all: the queue in a fresh temp state dir
    # is empty and the transcript resolves to "" — so this stays a pure local config probe.
    config_state_dir="$work_dir/config"
    mkdir -p "$config_state_dir"
    env -u ANHUR_API_KEY \
        ANHUR_STATE_DIR="$config_state_dir" \
        ANHUR_ENV_FILE="$real_env_file" \
        "$engine_path" persist </dev/null >/dev/null 2>"$work_dir/config.err"
    config_log="$config_state_dir/plugin.log"
    key_source=$(grep -o 'key source=[a-z]*' "$config_log" 2>/dev/null | head -n 1 | cut -d= -f2)
    loaded_variable_count=$(grep -o 'vars=[0-9]*' "$config_log" 2>/dev/null | head -n 1 | cut -d= -f2)
    if [ "$key_source" = "file" ]; then
        record_ok "engine read the key from $real_env_file (key source=file, ${loaded_variable_count:-?} vars loaded)"
    elif grep -q 'ANHUR_API_KEY not set' "$config_log" 2>/dev/null; then
        # Junior Tip [never hand the owner a command that overwrites this file blindly]: it holds
        # the only copy of a key. When the file already exists the fix must ADD a line, not
        # truncate the file with `>`; the create-from-scratch form is offered only when there is
        # nothing there to destroy.
        if [ ! -e "$real_env_file" ]; then
            record_fail "engine CANNOT read a key: $real_env_file does not exist — memory is not being saved" \
                "install -m 700 -d '$real_state_dir' && umask 177 && printf 'export ANHUR_API_KEY=\"anhur_…\"\\nexport ANHUR_URL=\"https://anhurdb.yoven.ai\"\\nexport ANHUR_CONTAINER=\"claude-ltm\"\\n' > '$real_env_file'"
        elif [ ! -r "$real_env_file" ]; then
            record_fail "engine CANNOT read a key: $real_env_file exists but is not readable — memory is not being saved" \
                "chmod 600 '$real_env_file'"
        else
            record_fail "engine CANNOT read a key: no ANHUR_API_KEY line in $real_env_file — memory is not being saved" \
                "umask 177 && printf 'export ANHUR_API_KEY=\"anhur_…\"\\n' >> '$real_env_file'   # APPEND, do not overwrite"
        fi
    else
        record_fail "engine produced no config line — it may not have run at all" \
            "$engine_path persist </dev/null   # and read the error it prints"
        printf '         engine stderr: %s\n' "$(tail -n 1 "$work_dir/config.err" 2>/dev/null | cut -c1-200)"
    fi

    # Permission report. The mode string is read positionally (2-4 owner, 5-10 group+other)
    # instead of matched whole, so mode 000 is reported as what it is — unreadable by the owner —
    # rather than as "readable beyond the owner", which would be a false statement.
    if [ -e "$real_env_file" ]; then
        env_file_mode=$(ls -ld "$real_env_file" 2>/dev/null | cut -c1-10)
        env_file_owner_bits=$(printf '%s' "$env_file_mode" | cut -c2-4)
        env_file_other_bits=$(printf '%s' "$env_file_mode" | cut -c5-10)
        if [ "$env_file_other_bits" != "------" ]; then
            record_warn "env file is readable beyond the owner ($env_file_mode) — it holds your API key" \
                "chmod 600 '$real_env_file'"
        elif [ "$env_file_owner_bits" = "rw-" ]; then
            record_ok "env file permissions are owner-only ($env_file_mode)"
        else
            record_warn "env file has unusual owner permissions ($env_file_mode)" \
                "chmod 600 '$real_env_file'"
        fi
    fi

    # A key exported in the caller's shell explains the cruellest symptom of the July blackout:
    # 187 successes interleaved with 743 skips, because a manual run inherited a key the hook
    # never saw. Say it out loud so nobody debugs the wrong machine state again.
    if [ -n "${ANHUR_API_KEY:-}" ]; then
        record_info "ANHUR_API_KEY is also exported in this shell — irrelevant to hooks, which never see it (this test unset it)"
    fi
fi

# ── endpoint + read-back credentials ─────────────────────────────────────────
#
# Junior Tip [mirror loadConfig's precedence, and its default]: the engine takes ANHUR_URL from
# the environment, then the env file, and falls back to the SDK constant client.DefaultCloudURL
# (v2/golang/client/client.go). That constant is duplicated here — if the SDK ever moves it,
# this line must move with it, or the verifier will probe a different server than the engine.
anhur_url="${ANHUR_URL:-}"
[ -n "$anhur_url" ] || anhur_url=$(env_file_value "$real_env_file" ANHUR_URL || printf '')
[ -n "$anhur_url" ] || anhur_url="https://anhurdb.yoven.ai"
anhur_url=${anhur_url%/}

real_container="${ANHUR_CONTAINER:-}"
[ -n "$real_container" ] || real_container=$(env_file_value "$real_env_file" ANHUR_CONTAINER || printf '')
[ -n "$real_container" ] || real_container="claude-ltm"

readback_api_key="${ANHUR_API_KEY:-}"
[ -n "$readback_api_key" ] || readback_api_key=$(env_file_value "$real_env_file" ANHUR_API_KEY || printf '')

# Refuse to write the probe into the live memory profile. Recall filters HARD by container, so a
# separate container is what keeps a test record out of the owner's real memory block.
if [ "$verify_container" = "$real_container" ]; then
    echo "anhur-memory-verify: ANHUR_VERIFY_CONTAINER equals your real container ('$real_container')." >&2
    echo "  Refusing: the probe record would land in your live memory profile." >&2
    exit 1
fi

verify_token=""
verify_session=""

# ── step 3: connectivity ─────────────────────────────────────────────────────

step_header "3/6" "CONNECTIVITY — is $anhur_url up, and is the key accepted?"

endpoint_reachable=0
if [ "$network_enabled" -eq 0 ]; then
    record_info "skipped (--offline)"
elif ! command -v curl >/dev/null 2>&1; then
    record_warn "curl is not installed — cannot probe the endpoint independently of the engine" \
        "install curl, or rely on steps 4-5"
else
    health_status=$(curl -sS -o "$work_dir/health.json" -w '%{http_code}' --max-time 20 \
        "$anhur_url/api/v1/health" 2>"$work_dir/health.err")
    curl_exit=$?
    if [ "$curl_exit" -ne 0 ]; then
        record_fail "cannot reach $anhur_url (curl exit $curl_exit: $(tr -d '\n' <"$work_dir/health.err" | cut -c1-120))" \
            "curl -v $anhur_url/api/v1/health   # DNS, TLS, proxy or VPN"
    elif [ "$health_status" = "200" ]; then
        endpoint_reachable=1
        record_ok "endpoint healthy: $(cut -c1-100 <"$work_dir/health.json")"
    else
        record_fail "endpoint answered HTTP $health_status on /api/v1/health" \
            "curl -i $anhur_url/api/v1/health"
    fi

    if [ "$endpoint_reachable" -eq 1 ]; then
        if [ -z "$readback_api_key" ]; then
            record_warn "this script could not extract a key from $real_env_file — skipping the auth probe" \
                "check the file has a line 'export ANHUR_API_KEY=…' (step 4 still tests it via the engine)"
        else
            auth_status=$(api_get "/api/v1/profile?tag=$verify_container" "$work_dir/profile.json")
            case "$auth_status" in
            200) record_ok "key accepted (HTTP 200 on /api/v1/profile)" ;;
            401 | 403)
                record_fail "key REJECTED by $anhur_url (HTTP $auth_status) — the key is wrong, revoked, or for another deployment" \
                    "issue a fresh per-tenant key and rewrite $real_env_file"
                ;;
            *) record_warn "unexpected HTTP $auth_status from /api/v1/profile" \
                "curl -i -H 'X-API-Key: …' '$anhur_url/api/v1/profile?tag=$verify_container'" ;;
            esac
        fi
    fi
fi

# ── step 4: write ────────────────────────────────────────────────────────────

step_header "4/6" "WRITE — persist one real record through the real engine"

write_succeeded=0
if [ "$network_enabled" -eq 0 ]; then
    record_info "skipped (--offline) — the write path is NOT proved"
elif [ -z "$engine_path" ] || [ "$key_source" != "file" ]; then
    record_info "skipped — the engine could not be run with a readable config (see steps 1-2)"
else
    verify_token="anhurverify$(random_hex)"
    verify_session="anhur-verify-$(date -u '+%Y%m%dT%H%M%SZ')-$(random_hex)"
    write_state_dir="$work_dir/write"
    mkdir -p "$write_state_dir"

    # A minimal but REAL Claude Code transcript: the engine's own extractor has to accept it,
    # so this exercises parsing, chunking, CreateSession, ingest, the cursor and the archive —
    # the same code the Stop hook runs. The token comes first so it survives summary truncation.
    printf '{"type":"user","message":{"content":[{"type":"text","text":"%s — AnhurDB end-to-end memory verification probe. If this text can be read back, the write path works."}]}}\n' \
        "$verify_token" >"$work_dir/transcript.jsonl"
    printf '{"session_id":"%s","transcript_path":"%s"}' \
        "$verify_session" "$work_dir/transcript.jsonl" >"$work_dir/hookinput.json"

    # Junior Tip [pin ANHUR_ARCHIVE_DIR to the temp dir]: the owner's config may point the
    # archive at a shared path (NFS is a documented option). Letting the probe inherit it would
    # drop a fake transcript into the real archive AND make the assertion below look for a file
    # somewhere else — a false warning. The real archive location is checked separately, in
    # step 6, without writing to it.
    env -u ANHUR_API_KEY \
        ANHUR_STATE_DIR="$write_state_dir" \
        ANHUR_ENV_FILE="$real_env_file" \
        ANHUR_CONTAINER="$verify_container" \
        ANHUR_ARCHIVE_DIR="$write_state_dir/archive" \
        "$engine_path" persist <"$work_dir/hookinput.json" >/dev/null 2>"$work_dir/write.err"

    # Junior Tip [read the EFFECT, never the exit code]: the engine exits 0 on every path by
    # design, so its status says nothing. Its own log line does: `sent=N` counts chunks AnhurDB
    # accepted, `queued=N` counts chunks it refused and that were parked on disk instead. Those
    # are the same tokens the background monitor matches on (scripts/anhur-memory-watch.sh).
    # Junior Tip [never point the owner at a path that will not exist]: the probe's state dir is
    # a temp dir this script deletes on exit, so "read $write_log" would be an instruction that
    # cannot be followed by the time it is read. Whatever the engine said is quoted INLINE here
    # instead, and the fix names a durable action.
    write_log="$write_state_dir/plugin.log"
    write_summary=$(grep -o 'chunks=[0-9]* sent=[0-9]* queued=[0-9]*' "$write_log" 2>/dev/null | tail -n 1)
    if printf '%s' "$write_summary" | grep -q 'sent=[1-9]'; then
        write_succeeded=1
        record_ok "AnhurDB accepted the write ($write_summary, session=$verify_session)"
    elif printf '%s' "$write_summary" | grep -q 'queued=[1-9]'; then
        record_fail "write REFUSED by AnhurDB — the turn was queued to disk instead ($write_summary)" \
            "fix the endpoint/key failures above, then re-run this script"
        printf '         engine said: %s\n' \
            "$(grep -E 'CreateSession failed|flush still failing|profile failed' "$write_log" 2>/dev/null | tail -n 1 | cut -c1-200)"
    else
        record_fail "the engine persisted nothing at all (no sent/queued line in its log)" \
            "$engine_path persist </dev/null   # and read the error it prints"
        printf '         engine log: %s\n' "$(tail -n 1 "$write_log" 2>/dev/null | cut -c1-200)"
        printf '         engine stderr: %s\n' "$(tail -n 1 "$work_dir/write.err" 2>/dev/null | cut -c1-200)"
    fi

    # The lossless archive is the net under the net: it is written on every persist, even when
    # the key is missing. Its absence does not stop today's memory, but it removes the recovery
    # path for the day something else breaks.
    if [ "$(count_files "$write_state_dir/archive" '*.jsonl')" -gt 0 ]; then
        record_ok "verbatim transcript archive written (the lossless copy is working)"
    else
        record_warn "no archive file was produced for the probe session" \
            "check ANHUR_ARCHIVE / ANHUR_ARCHIVE_DIR in $real_env_file"
    fi
fi

# ── step 5: read-back ────────────────────────────────────────────────────────

step_header "5/6" "READ-BACK — find that record again (this is the check that matters)"

readback_succeeded=0
if [ "$network_enabled" -eq 0 ]; then
    record_info "skipped (--offline) — the memory cycle is NOT proved"
elif [ "$write_succeeded" -eq 0 ]; then
    record_info "skipped — nothing was written to read back"
elif [ -z "$readback_api_key" ]; then
    record_warn "no key available to this script — cannot read the record back (the write itself did succeed)" \
        "check that $real_env_file has a line 'export ANHUR_API_KEY=…'"
else
    # Scope the query to the probe session: an unscoped hit could come from an older run, which
    # would let a broken write pass. The token is random per run, so a hit proves THIS record.
    printf '{"text":"%s","limit":5,"scope":"sessions","sessions":["%s"]}' \
        "$verify_token" "$verify_session" >"$work_dir/search.json"
    # Normalise the retry count: a non-numeric or zero value would either abort the comparison
    # below with "Illegal number" or skip the loop entirely and leave $search_status unset —
    # either way the script would fail to answer its own question.
    case "$readback_attempts" in
    '' | *[!0-9]* | 0) readback_attempts=5 ;;
    esac
    search_status=""
    readback_attempt=0
    while [ "$readback_attempt" -lt "$readback_attempts" ]; do
        readback_attempt=$((readback_attempt + 1))
        search_status=$(api_post "/api/v1/search" "$work_dir/search.json" "$work_dir/search.json.out")
        # The response echoes no part of the query, so finding the token in the body means the
        # STORED text came back — a strictly stronger claim than a non-zero result count.
        if [ "$search_status" = "200" ] && grep -q "$verify_token" "$work_dir/search.json.out" 2>/dev/null; then
            readback_succeeded=1
            break
        fi
        [ "$readback_attempt" -lt "$readback_attempts" ] && sleep 3
    done
    if [ "$readback_succeeded" -eq 1 ]; then
        if [ "$readback_attempt" -eq 1 ]; then
            record_ok "read the record back verbatim — the write/read cycle is CLOSED"
        else
            record_ok "read the record back verbatim after $readback_attempt attempts — cycle closed, but the index lagged"
        fi
    else
        record_fail "wrote the record but CANNOT read it back (last HTTP $search_status, $readback_attempt attempts)" \
            "curl -i -H 'X-API-Key: …' -X POST '$anhur_url/api/v1/search' -d '{\"text\":\"$verify_token\",\"limit\":5,\"scope\":\"sessions\",\"sessions\":[\"$verify_session\"]}'"
    fi
fi

# ── step 6: state on disk ────────────────────────────────────────────────────

step_header "6/6" "STATE — anything stuck, and what does the engine's own log say?"

queued_chunk_count=$(count_files "$real_state_dir/queue" '*.txt')
quarantined_chunk_count=$(count_files "$real_state_dir/queue/quarantine" '*.txt')

if [ "$queued_chunk_count" -eq 0 ]; then
    record_ok "queue is empty — no turn is waiting to reach AnhurDB"
else
    record_warn "$queued_chunk_count turn(s) queued on disk (not lost: retried on every persist and session start)" \
        "ls -l '$real_state_dir/queue' && grep -E 'flush still failing|CreateSession failed' '$real_log_path' | tail -n 3"
fi

if [ "$quarantined_chunk_count" -gt 0 ]; then
    record_warn "$quarantined_chunk_count chunk(s) QUARANTINED — never retried, a human must resolve them" \
        "ls -l '$real_state_dir/queue/quarantine'   # their session could not be proven; writing them elsewhere would merge two conversations"
fi

# The verbatim archive is the last net: it is written even when the key is missing, so a
# non-writable archive directory removes the recovery path for every other failure at once —
# silently, because archiveTranscript only logs. Checked by inspection, never by writing to it.
# Mirrors loadConfig: ANHUR_ARCHIVE is on unless it is literally "false".
configured_archive_enabled="${ANHUR_ARCHIVE:-$(env_file_value "$real_env_file" ANHUR_ARCHIVE || printf '')}"
configured_archive_dir="${ANHUR_ARCHIVE_DIR:-$(env_file_value "$real_env_file" ANHUR_ARCHIVE_DIR || printf '')}"
[ -n "$configured_archive_dir" ] || configured_archive_dir="$real_state_dir/archive"
if [ "$configured_archive_enabled" = "false" ]; then
    record_warn "the verbatim transcript archive is DISABLED (ANHUR_ARCHIVE=false) — no lossless copy on disk" \
        "remove the ANHUR_ARCHIVE=false line from $real_env_file"
elif [ -d "$configured_archive_dir" ]; then
    if [ -w "$configured_archive_dir" ]; then
        record_ok "archive directory is writable: $configured_archive_dir"
    else
        record_warn "archive directory is NOT writable: $configured_archive_dir — the lossless copy is being lost" \
            "chmod u+w '$configured_archive_dir'   # or fix the mount it lives on"
    fi
else
    record_info "archive directory does not exist yet: $configured_archive_dir (created on the first persist)"
fi

# Junior Tip [the failure vocabulary is SOURCED, not copied]: the monitor already owns the list
# of log lines that mean "broken" (scripts/anhur-memory-watch.sh, guarded by
# ANHUR_WATCH_DEFINE_ONLY). Re-typing it here would let the two drift, and a verifier matching
# strings the engine no longer writes reports "all clear" forever. The fallback below exists
# only for a snapshot too old to have the monitor, and is deliberately narrower.
if [ -n "$installed_dir" ] && [ -f "$installed_dir/scripts/anhur-memory-watch.sh" ]; then
    ANHUR_WATCH_DEFINE_ONLY=1
    export ANHUR_WATCH_DEFINE_ONLY
    # shellcheck source=/dev/null
    . "$installed_dir/scripts/anhur-memory-watch.sh"
    unset ANHUR_WATCH_DEFINE_ONLY
else
    failure_pattern='ANHUR_API_KEY not set|MEMORY IS NOT BEING SAVED|panic recovered|profile failed|transcript not found|cannot read transcript|CreateSession failed|flush still failing|queue write FAILED|ERROR:'
    success_pattern='sent=[1-9]|flushed queued chunk|wrote memory block to stdout'
    record_info "installed snapshot has no monitor script — using a reduced failure vocabulary"
fi

if [ ! -f "$real_log_path" ]; then
    record_warn "no $real_log_path — no hook has ever run the engine on this machine" \
        "start a NEW Claude Code session, then re-run this script"
else
    # Only failures AFTER the last success are unresolved; older ones are history. The July
    # blackout's 743 lines are still in this log, and re-announcing them forever would train
    # the owner to ignore the one report that matters.
    last_success_line_number=$(grep -n -E "$success_pattern" "$real_log_path" 2>/dev/null | tail -n 1 | cut -d: -f1)
    if [ -n "$last_success_line_number" ]; then
        unresolved_failures=$(tail -n "+$((last_success_line_number + 1))" "$real_log_path" 2>/dev/null | grep -E "$failure_pattern")
        last_success_stamp=$(sed -n "${last_success_line_number}p" "$real_log_path" 2>/dev/null | cut -d' ' -f1)
        unresolved_scope="since the last successful AnhurDB call ($last_success_stamp)"
    else
        unresolved_failures=$(tail -n 400 "$real_log_path" 2>/dev/null | grep -E "$failure_pattern")
        unresolved_scope="in the last 400 lines, with NO successful AnhurDB call at all"
    fi
    unresolved_failure_count=$(printf '%s\n' "$unresolved_failures" | grep -c .)
    if [ "$unresolved_failure_count" -eq 0 ]; then
        record_ok "no unresolved failures in $real_log_path ($unresolved_scope)"
    else
        record_warn "$unresolved_failure_count unresolved failure line(s) $unresolved_scope" \
            "tail -n 20 '$real_log_path'"
        printf '         newest: %.160s\n' "$(printf '%s\n' "$unresolved_failures" | tail -n 1)"
    fi
    record_info "last engine activity: $(tail -n 1 "$real_log_path" 2>/dev/null | cut -c1-20) (UTC; 'date -u' to compare)"

    # Junior Tip [the one class every other check misses: a hook that never fires]: install,
    # config, network, write and read-back can all be green while Claude Code simply never calls
    # the engine — that is the 2026-07-16 shape, and it leaves no trace anywhere except in what
    # DID NOT happen. It is detectable indirectly: Claude Code rewrites a session transcript on
    # every turn, and the engine appends at least its `config:` line on every invocation, so
    # recent transcript activity with a frozen plugin.log means the hooks are not running.
    #
    # This comparison is only valid because every probe above ran against a TEMP state dir: the
    # verifier must never write to the log it is about to use as evidence.
    #
    # `find -mmin` avoids date arithmetic, which has no portable form between GNU and BSD.
    hook_window_minutes="${ANHUR_VERIFY_HOOK_WINDOW_MINUTES:-60}"
    transcripts_dir="$claude_config_dir/projects"
    if [ -d "$transcripts_dir" ]; then
        recent_transcript=$(find "$transcripts_dir" -name '*.jsonl' -type f -mmin "-$hook_window_minutes" 2>/dev/null | head -n 1)
        recent_engine_run=$(find "$real_log_path" -mmin "-$hook_window_minutes" 2>/dev/null | head -n 1)
        if [ -n "$recent_engine_run" ]; then
            record_ok "the engine ran in the last $hook_window_minutes min (something invoked it — a hook, or you)"
        elif [ -n "$recent_transcript" ]; then
            record_warn "Claude Code wrote a transcript in the last $hook_window_minutes min but $real_log_path did NOT move — the hooks may not be firing (or a turn is still running)" \
                "start a NEW Claude Code session, then: tail -n 3 '$real_log_path'   # a fresh line must appear"
        fi
    fi
fi

# ── verdict ──────────────────────────────────────────────────────────────────

printf '\n%s\n' "----------------------------------------------------------------------"

if [ "$failure_count" -gt 0 ]; then
    printf 'VERDICT: MEMORY IS DEAD — %s\n' "$first_failure_reason"
    printf '         %s failure(s), %s warning(s). Fix the [FAIL] lines above, top to bottom,\n' \
        "$failure_count" "$warning_count"
    printf '         then re-run: sh %s\n' "$0"
    verdict_exit=1
elif [ "$network_enabled" -eq 0 ]; then
    printf 'VERDICT: LOCAL CHECKS PASSED — but the write/read cycle was NOT tested (--offline).\n'
    printf '         This does NOT prove the memory is alive. Re-run without --offline.\n'
    verdict_exit=2
elif [ "$readback_succeeded" -eq 1 ] && [ "$warning_count" -eq 0 ]; then
    printf 'VERDICT: MEMORY IS ALIVE — wrote a record and read it back, just now.\n'
    verdict_exit=0
elif [ "$readback_succeeded" -eq 1 ]; then
    printf 'VERDICT: MEMORY IS ALIVE, WITH %s WARNING(S) — wrote a record and read it back,\n' "$warning_count"
    printf '         but see the [WARN] lines above.\n'
    verdict_exit=2
else
    printf 'VERDICT: NOT PROVED — no check failed outright, but the write/read cycle did not complete.\n'
    printf '         Treat the memory as unverified until a run closes the cycle.\n'
    verdict_exit=2
fi

if [ -n "$verify_token" ] && [ "$write_succeeded" -eq 1 ]; then
    printf '\nCreated by this run (safe to leave; it can never surface in your real memory,\n'
    printf 'because recall filters strictly by container):\n'
    printf '  container: %s   session: %s\n' "$verify_container" "$verify_session"
fi

printf '\nNOT proved by this script: that the <anhur-memory> block reaches the MODEL.\n'
printf 'No external process can observe that. Start a NEW session and ask:\n'
printf '  claude -p "Without using tools: did you receive an <anhur-memory> block?" </dev/null\n'

exit "$verdict_exit"
