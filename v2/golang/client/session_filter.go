package client

import (
	"net/url"
	"strings"
)

// --------------------------------------------------------------------------
// Session filter (ADR-0014) — the `sessions` argument every search carries
// --------------------------------------------------------------------------

// SessionWildcard selects every session inside the current scope boundary.
//
// Junior Tip [why a wildcard instead of an optional field]: the defect this
// model replaces was a session filter that was silently NOT applied — the
// caller asked for one chat and got five. Making the argument optional with an
// implicit "all" default reintroduces exactly that class of bug: a client that
// forgets the field, or computes an empty one, widens the query to the whole
// tenant with no signal at all. Requiring the caller to spell out "*" turns
// widening into a deliberate, auditable choice. Same constant, same rule, in
// the Python and TypeScript SDKs and in the server.
const SessionWildcard = "*"

// MaxSessionFilterUUIDs caps how many explicit sessions one request may name.
//
// Junior Tip: this is a product limit, not a technical one — the server's
// vendored SQLite accepts ~32k bound parameters. The cap exists so that "I need
// thousands of sessions" surfaces as a design conversation (a grouping label)
// instead of an ever-growing IN clause. The SDK enforces it locally so the
// caller learns before paying for the round trip; the server enforces the same
// number independently, because a client-side check is a convenience and never
// a guarantee.
const MaxSessionFilterUUIDs = 1000

// SessionsAll returns the wildcard filter — every session inside the scope
// boundary. Prefer it over a hand-written []string{"*"} so the intent reads at
// the call site:
//
//	hits, err := mem.Search(ctx, "what does this user do?", client.SessionsAll())
//
// Mirrors Python sessions_all() and TypeScript sessionsAll().
func SessionsAll() []string {
	return []string{SessionWildcard}
}

// normalizeSessionFilter validates the caller's `sessions` argument and returns
// the exact list to put on the wire.
//
// The contract, in full (ADR-0014 D1/D2):
//
//	["*"]                  → every session in the scope boundary
//	["uuid-a"]             → exactly that session
//	["uuid-a","uuid-b"]    → exactly those, up to MaxSessionFilterUUIDs
//	[]                     → error: ambiguous
//	nil                    → error: the argument is mandatory
//	["*","uuid-a"]         → error: contradictory
//
// Junior Tip [why empty and contradictory are errors, not lenient defaults]:
// every rejected case here is a caller that does not know what it wants.
// Guessing on their behalf is how a scoping bug becomes a data-exposure bug.
// Failing gives them a message they can act on; guessing gives them results
// they cannot audit. The error strings are byte-identical across the three
// SDKs and the server so one support answer covers every client.
// Junior Tip [why these return *APIError and not fmt.Errorf, 2026-09-05]: every
// rejection below is a rule the SERVER also enforces, answering HTTP 400 with the
// same INVALID_PARAM text. Returning a bare fmt.Errorf forced callers to match on
// the message to tell "you sent something invalid" from "the network died", and
// message-matching is exactly what the SDK error contract exists to stop. The
// values are now *APIError (StatusCode 400, Kind() == KindInvalidRequest,
// Retryable() == false), while Error() still renders the identical string — see
// APIError.Error for why that byte-identity is load-bearing across the 3 SDKs.
func normalizeSessionFilter(rawSessions []string) ([]string, error) {
	if rawSessions == nil {
		return nil, newValidationError(
			"INVALID_PARAM: 'sessions' is required; use [%q] for every session in scope", SessionWildcard)
	}
	if len(rawSessions) == 0 {
		return nil, newValidationError(
			"INVALID_PARAM: 'sessions' cannot be empty; use [%q] for every session in scope", SessionWildcard)
	}

	cleaned := make([]string, 0, len(rawSessions))
	wildcardSeen := false
	for _, rawSession := range rawSessions {
		session := strings.TrimSpace(rawSession)
		if session == "" {
			return nil, newValidationError("INVALID_PARAM: 'sessions' contains an empty entry")
		}
		if session == SessionWildcard {
			wildcardSeen = true
			continue
		}
		cleaned = append(cleaned, session)
	}

	if wildcardSeen {
		if len(cleaned) > 0 {
			return nil, newValidationError(
				"INVALID_PARAM: 'sessions' mixes %q with %d explicit session(s); "+
					"the wildcard must stand alone", SessionWildcard, len(cleaned))
		}
		return []string{SessionWildcard}, nil
	}

	// Junior Tip: dedup before the cap so a caller repeating the same session is
	// not punished for it, and so the server never binds the same value twice.
	deduplicated := make([]string, 0, len(cleaned))
	seen := make(map[string]struct{}, len(cleaned))
	for _, session := range cleaned {
		if _, already := seen[session]; already {
			continue
		}
		seen[session] = struct{}{}
		deduplicated = append(deduplicated, session)
	}

	if len(deduplicated) > MaxSessionFilterUUIDs {
		return nil, newValidationError(
			"INVALID_PARAM: at most %d sessions per request (got %d); use [%q] for all",
			MaxSessionFilterUUIDs, len(deduplicated), SessionWildcard)
	}

	return deduplicated, nil
}

// appendSessionsQueryParam writes the resolved filter onto a GET query string.
//
// Junior Tip [repeated key, not a comma-joined value]: session uuids are opaque
// caller-supplied strings, so any in-band separator is a value that some caller
// will eventually put inside an id. `?sessions=a&sessions=b` is the standard
// multi-valued encoding, it needs no escaping convention, and it is what
// url.Values / r.URL.Query()["sessions"] produce and read on both ends.
func appendSessionsQueryParam(params url.Values, sessions []string) {
	for _, session := range sessions {
		params.Add("sessions", session)
	}
}
