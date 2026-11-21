package core

// session_limit.go — a FULL SESSION is not an outage, and the plugin must say so.
//
// Domain, in one sentence: recognise the AnhurDB per-session record cap refusal
// (ADR-0033) on every delivery path, log it once in words a human can act on,
// and stamp it on the queued chunk so `status` and memory-health can name the
// session and the numbers while it lasts.
//
// Junior Tip [the queue behaviour does NOT change — only its visibility]: a
// refused chunk stays queued and is retried with the normal backoff (up to
// 30 min). Nothing is dropped, and nothing is ever written into another session
// to get around the cap: that would merge two conversations and break the
// inviolable "1 Claude session = 1 AnhurDB session". What used to be wrong was
// the log: a full session read exactly like a server that was down, and in
// 2026-07 that cost ten days of memory before anyone noticed the HTTP 409.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/client"
)

// sessionLimitMarker starts the log line AND the persisted queue cause. The
// watcher (scripts/anhur-memory-watch.sh) and `status` match on it — renaming it
// is a contract change for both.
const sessionLimitMarker = "SESSION RECORD LIMIT"

// legacySessionLimitText is how the server words the refusal. Queue rows written
// before this file existed carry only the SDK's raw error, so `status` must
// recognise this text too or it would miss the very chunks already stuck.
//
// Junior Tip [why this survives the SDK 4.0.0 legacy removal, verified
// 2026-11-21]: this is NOT a server-compatibility branch — it reads USER DATA
// already on disk (queue.db last_error). The drain re-stamps a row in the new
// format only when that row is ATTEMPTED and refused again (queue_bridge.go:
// noteSessionLimit → markQueueFailure → MarkFailed). But the drain attempts
// only the OLDEST item of a session and Releases the rest untouched
// (blockedSessions; Release does not write last_error), so while the cap holds
// every later row of a full session keeps its old text indefinitely. Dropping
// this match would make `status` report those chunks as ordinary backlog
// instead of naming the full session.
const legacySessionLimitText = "session has reached the maximum of"

// sessionLimitCauseFields reads back the fields sessionLimitCause wrote.
var sessionLimitCauseFields = regexp.MustCompile(sessionLimitMarker + `: session=(\S+) limit=(\S+) count=(\S+)`)

// legacySessionLimitNumber reads N out of "maximum of N records".
var legacySessionLimitNumber = regexp.MustCompile(`maximum of (\d+) records`)

// sessionLimitRefusal returns the typed refusal inside deliveryErr, or nil.
// The SDK recognises the structured 409 by its code (session_record_limit).
func sessionLimitRefusal(deliveryErr error) *client.SessionRecordLimitError {
	var refusal *client.SessionRecordLimitError
	if errors.As(deliveryErr, &refusal) {
		return refusal
	}
	return nil
}

// sessionLimitCause is the short, parseable statement persisted as the queue
// row's last_error (the column holds 300 runes). The session is the server's
// when it sent one, otherwise the session this plugin was writing to — which is
// the same session by construction (every write pins it explicitly).
func sessionLimitCause(sessionID string, refusal *client.SessionRecordLimitError) string {
	if refusal.SessionUUID != "" {
		sessionID = refusal.SessionUUID
	}
	return fmt.Sprintf("%s: session=%s limit=%d count=%d — AnhurDB refuses new records for this session",
		sessionLimitMarker, sessionID, refusal.Limit, refusal.Count)
}

// noteSessionLimit logs the ONE clear line for a session-limit refusal and
// returns the cause to persist with the queued chunk. For any other failure it
// logs nothing and returns the error text unchanged, so callers can use it on
// every failure path without branching.
func noteSessionLimit(cfg config, sessionID string, deliveryErr error) (cause string, isSessionLimit bool) {
	refusal := sessionLimitRefusal(deliveryErr)
	if refusal == nil {
		if deliveryErr == nil {
			return "", false
		}
		return deliveryErr.Error(), false
	}
	cause = sessionLimitCause(sessionID, refusal)
	logLine(cfg, cause+"; the chunk stays in the local queue and is retried with backoff (never dropped, never written to another session)")
	return cause, true
}

// sessionLimitFromCause recognises a persisted cause — this file's format or the
// legacy raw SDK text (see legacySessionLimitText) — and returns the limit and
// count as text ("unknown" when an old row did not carry them; rows written by
// a 3.x plugin may also say "unknown"). ok is false for any other cause.
func sessionLimitFromCause(cause string) (limitText string, countText string, ok bool) {
	if fields := sessionLimitCauseFields.FindStringSubmatch(cause); fields != nil {
		return fields[2], fields[3], true
	}
	if strings.Contains(cause, legacySessionLimitText) {
		limitText = "unknown"
		if number := legacySessionLimitNumber.FindStringSubmatch(cause); number != nil {
			limitText = number[1]
		}
		return limitText, "unknown", true
	}
	return "", "", false
}
