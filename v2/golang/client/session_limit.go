package client

// session_limit.go — the per-session record cap refusal, as a typed error.
//
// Domain, in one sentence: recognise "this session is full" in a server answer
// and hand the caller the session, the cap and the count, so the refusal is
// impossible to mistake for an outage.
//
// Wire contract (ADR-0033, AnhurDB server/handler/service_bridge.go:81-82 and
// :109-125, produced only by service/record_session_cap.go:178): HTTP 409, body
//
//	{"error": "session has reached the maximum of N records ...",
//	 "code": "session_record_limit", "limit": N, "count": M, "session_uuid": "..."}
//
// All four structured fields are always present (none is omitempty), so the SDK
// recognises the refusal by `code` ONLY and treats limit/count/session as
// required. gRPC (ResourceExhausted + ErrorInfo reason SESSION_RECORD_LIMIT) is
// part of the contract too, but this SDK has no gRPC transport.
//
// Junior Tip [why the sentence is no longer read, 4.0.0]: until 3.x the SDK also
// matched the text "session has reached the maximum of" and parsed N back out of
// it, for servers that predated the structured body. Every server now sends the
// code; matching prose would keep a second, weaker contract alive that nobody
// tests against the server. A 409 without the code is an ordinary *APIError.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrSessionRecordLimit matches every session-record-limit refusal:
// errors.Is(err, ErrSessionRecordLimit). Use errors.As with
// *SessionRecordLimitError to read the session, the cap and the count.
var ErrSessionRecordLimit = errors.New("anhurdb: session record limit reached")

// SessionRecordLimitCode is the machine-readable `code` the server sends.
const SessionRecordLimitCode = "session_record_limit"

// SessionRecordLimitError is returned when AnhurDB refuses a write because the
// target session already holds its maximum number of records.
//
// Junior Tip [non-retryable, and what that does NOT mean]: Retryable() is false
// because repeating the same write into the same session gets the same answer
// until the cap changes server-side. It does NOT mean "drop the data": a caller
// that must not lose the write keeps it and retries later. Writing it into a
// different session to get around the cap would merge two conversations, which
// the memory plugin forbids (1 agent session = 1 AnhurDB session).
type SessionRecordLimitError struct {
	// SessionUUID is the full session the server refused.
	SessionUUID string
	// Limit is the session cap.
	Limit int
	// Count is how many records the session holds.
	Count int
	// ServerMessage is the server's human sentence, verbatim.
	ServerMessage string
	// response is the plain HTTP error this refusal arrived as. Unwrap exposes
	// it so errors.As(err, **APIError) keeps finding StatusCode 409.
	response *APIError
}

// Error renders the refusal with every number the server gave.
//
// Junior Tip [server sentence FIRST]: log readers and notifiers truncate (the
// plugin's watcher keeps ~200 characters). The server's own words are what a
// human recognises, so they come right after the status; the details follow.
// The format is byte-identical in the Python and TypeScript SDKs.
func (limitError *SessionRecordLimitError) Error() string {
	return fmt.Sprintf("Conflict (HTTP 409): %s (session record limit: session=%s, limit=%d, count=%d)",
		serverMessageExcerpt(limitError.ServerMessage), limitError.SessionUUID, limitError.Limit, limitError.Count)
}

// Unwrap lets errors.Is(err, ErrSessionRecordLimit) and
// errors.As(err, **APIError) both match.
func (limitError *SessionRecordLimitError) Unwrap() []error {
	return []error{ErrSessionRecordLimit, limitError.response}
}

// StatusCode is the HTTP status the refusal came with (409).
func (limitError *SessionRecordLimitError) StatusCode() int { return 409 }

// Kind is KindConflict, the same class every other 409 has.
func (limitError *SessionRecordLimitError) Kind() ErrorKind { return KindConflict }

// Retryable is always false — see the type's Junior Tip.
func (limitError *SessionRecordLimitError) Retryable() bool { return false }

// serverMessageExcerptRunes caps how much of the server sentence the message
// quotes — 500 characters, the same cap Python ([:500]) and TypeScript use.
const serverMessageExcerptRunes = 500

// serverMessageExcerpt returns at most serverMessageExcerptRunes characters
// (runes, not bytes, so a multi-byte character is never cut in half).
func serverMessageExcerpt(serverMessage string) string {
	messageRunes := []rune(serverMessage)
	if len(messageRunes) <= serverMessageExcerptRunes {
		return serverMessage
	}
	return string(messageRunes[:serverMessageExcerptRunes])
}

// sessionRecordLimitWireBody is the subset of a 409 body this file reads.
//
// Junior Tip [pointers detect PRESENCE]: decoding into a plain int turns a
// missing "limit" into 0 — a number the server never said. With pointers a
// missing or null field stays nil, and a mistyped one ("limit":"5", 5.5) fails
// the decode; either way the body is not the contract and stays the generic
// 409. Same rule as Python (session_limit.py) and TypeScript (sessionLimit.ts).
type sessionRecordLimitWireBody struct {
	Error       string  `json:"error"`
	Code        string  `json:"code"`
	Limit       *int    `json:"limit"`
	Count       *int    `json:"count"`
	SessionUUID *string `json:"session_uuid"`
}

// sessionRecordLimitFromResponse returns the typed refusal, or nil when the
// response is any other kind of answer (any other status, a non-JSON body, or
// a JSON body without the session_record_limit code).
func sessionRecordLimitFromResponse(statusCode int, body []byte) *SessionRecordLimitError {
	if statusCode != 409 {
		return nil
	}
	var wireBody sessionRecordLimitWireBody
	if decodeErr := json.Unmarshal(body, &wireBody); decodeErr != nil {
		return nil
	}
	if wireBody.Code != SessionRecordLimitCode {
		return nil
	}
	if wireBody.Limit == nil || wireBody.Count == nil || wireBody.SessionUUID == nil {
		return nil
	}
	return &SessionRecordLimitError{
		SessionUUID:   *wireBody.SessionUUID,
		Limit:         *wireBody.Limit,
		Count:         *wireBody.Count,
		ServerMessage: strings.TrimSpace(wireBody.Error),
		response:      &APIError{StatusCode: statusCode, Body: string(body)},
	}
}
