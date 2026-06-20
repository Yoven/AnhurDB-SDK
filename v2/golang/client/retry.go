package client

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Retry tuning for transient WRITE failures.
//
// Junior Tip [why these exact numbers]: AnhurDB is a Raft cluster. The two
// failures we retry are SHORT-LIVED by construction:
//   - not_leader (HTTP 500): a follower briefly thought it could serve a write
//     during a leadership flap; a new leader is elected within a few hundred ms.
//   - episodic-anchor rejection (HTTP 422): a non-episodic record was created
//     in a session whose episodic anchor is still being applied on this node;
//     read-after-write/apply lag clears in well under a second.
//
// Three attempts across 50ms -> 100ms -> 200ms (total ~350ms of sleeping) is
// enough to ride out both without turning a permanent error into a long stall.
const (
	writeRetryMaxAttempts  = 3
	writeRetryBaseDelay    = 50 * time.Millisecond
	writeRetryBackoffScale = 2
)

// transientWriteErrorMarkers are case-insensitive substrings that identify the
// ONLY two server messages we treat as transient on a write. We match on the
// message text (not just the HTTP status) because the episodic-anchor case
// shares HTTP 422 with genuine, permanent validation errors (bad enum, missing
// field) that must NOT be retried — retrying a permanent 422 just amplifies
// load and hides the real bug from the caller.
var transientWriteErrorMarkers = []string{
	"not_leader",
	"not leader",
	"without an episodic anchor",
}

// isTransientWriteError reports whether err is one of the known short-lived
// cluster conditions that a write should retry. It deliberately inspects the
// APIError body so that a 422 carrying a permanent validation message (e.g.
// "invalid type") is NOT retried.
//
// Junior Tip [never retry a permanent error]: this function is the single
// gate between "safe to retry" and "stop and surface to the caller". Adding a
// marker here changes retry behaviour for EVERY write — be conservative.
func isTransientWriteError(err error) bool {
	if err == nil {
		return false
	}

	// A 5xx without a parseable body still maps to APIError; the generic
	// ErrServerError sentinel could also appear. Inspect the structured error
	// body when present.
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		// Only 5xx and 422 are eligible; a 400/401/403/404 is permanent.
		if apiErr.StatusCode != 422 && apiErr.StatusCode < 500 {
			return false
		}
		return messageMatchesTransientMarker(apiErr.Body)
	}

	// Fall back to matching the rendered error string for non-APIError paths
	// (e.g. a wrapped server error). Connection failures are NOT retried here:
	// they are not idempotency-safe to blind-retry from this layer.
	if errors.Is(err, ErrConnectionFail) {
		return false
	}
	return messageMatchesTransientMarker(err.Error())
}

// messageMatchesTransientMarker checks a message against the transient marker
// list, case-insensitively.
func messageMatchesTransientMarker(message string) bool {
	lowered := strings.ToLower(message)
	for _, marker := range transientWriteErrorMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// isEpisodicAnchorError reports whether err is specifically the server's "no
// episodic anchor in this session yet" 422 — the ONE write rejection the SDK
// self-heals by seeding an episodic anchor and retrying (see
// postRecordSeedingAnchor). It is narrower than isTransientWriteError, which also
// matches not_leader; here we must NOT seed for a leadership flap.
//
// Junior Tip [anchor-seed parity, 2026-06-18]: matched on message text because
// this 422 shares its status with permanent validation errors (bad enum/missing
// field) that must surface to the caller, not be papered over with a seed+retry.
func isEpisodicAnchorError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return strings.Contains(strings.ToLower(apiErr.Body), "without an episodic anchor")
	}
	return strings.Contains(strings.ToLower(err.Error()), "without an episodic anchor")
}

// withWriteRetry runs the supplied idempotent write operation, retrying ONLY on
// transient cluster errors with exponential backoff. It is generic over the
// result type so every write path (Add, and any future create) can share one
// retry policy.
//
// Junior Tip [idempotency requirement]: callers MUST only wrap operations that
// are safe to repeat. Add satisfies this: a duplicate record from a retried
// not_leader is far cheaper than the alternative (losing the write or stalling
// the agent pipeline), and the server dedups episodic anchors per session. Do
// NOT wrap PATCH/DELETE-with-side-effects flows in this helper without
// auditing their idempotency first.
func withWriteRetry[ResultType any](
	ctx context.Context,
	operation func() (ResultType, error),
) (ResultType, error) {
	var lastResult ResultType
	var lastErr error

	delay := writeRetryBaseDelay
	for attempt := 1; attempt <= writeRetryMaxAttempts; attempt++ {
		lastResult, lastErr = operation()
		if lastErr == nil {
			return lastResult, nil
		}
		if !isTransientWriteError(lastErr) {
			return lastResult, lastErr
		}
		// Transient and we have attempts left: back off, honouring ctx
		// cancellation so a caller timeout still wins.
		if attempt < writeRetryMaxAttempts {
			select {
			case <-ctx.Done():
				return lastResult, ctx.Err()
			case <-time.After(delay):
			}
			delay *= writeRetryBackoffScale
		}
	}
	return lastResult, lastErr
}
