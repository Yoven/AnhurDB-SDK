package client

// session_limit.go — the per-session record cap refusal, as a typed error.
//
// Domain, in one sentence: recognise "this session is full" in a server answer
// and hand the caller the session, the cap and the count, so the refusal is
// impossible to mistake for an outage.
//
// Wire contract (ADR-0033, AnhurDB):
//   - NEW servers: HTTP 409, body
//     {"error": "session has reached the maximum of N records ...",
//      "code": "session_record_limit", "limit": N, "count": M, "session_uuid": "..."}
//   - OLD servers (production until the deploy): HTTP 409, body
//     {"error": "session has reached the maximum of N records"} — no code, no
//     count, no session.
//
// Both are recognised: by `code`, OR by the text prefix. gRPC (ResourceExhausted
// + ErrorInfo reason SESSION_RECORD_LIMIT; legacy AlreadyExists with the same
// text) is part of the contract too, but this SDK has no gRPC transport, so
// there is no gRPC path here to classify.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrSessionRecordLimit matches every session-record-limit refusal:
// errors.Is(err, ErrSessionRecordLimit). Use errors.As with
// *SessionRecordLimitError to read the session, the cap and the count.
var ErrSessionRecordLimit = errors.New("anhurdb: session record limit reached")

// SessionRecordLimitCode is the machine-readable `code` a new server sends.
const SessionRecordLimitCode = "session_record_limit"

// sessionRecordLimitTextPrefix is how BOTH old and new servers start the
// message. It is the only signal an old server gives.
const sessionRecordLimitTextPrefix = "session has reached the maximum of"

// sessionRecordLimitFromText pulls N out of "...maximum of N records".
var sessionRecordLimitFromText = regexp.MustCompile(`maximum of (\d+) records`)

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
	// SessionUUID is the full session. Empty when an old server omitted it.
	SessionUUID string
	// Limit is the session cap. Nil only when neither the `limit` field nor
	// the message text carried it (an old server always puts N in the text).
	Limit *int
	// Count is how many records the session holds. Nil on old servers.
	Count *int
	// ServerMessage is the server's human sentence, verbatim.
	ServerMessage string
	// response is the plain HTTP error this refusal arrived as. Unwrap exposes
	// it so errors.As(err, **APIError) keeps finding StatusCode 409.
	response *APIError
}

// Error renders the refusal with every number the server gave, and says
// "unknown" for the ones an old server did not send — never a fake zero.
func (limitError *SessionRecordLimitError) Error() string {
	sessionText := limitError.SessionUUID
	if sessionText == "" {
		sessionText = "unknown"
	}
	return fmt.Sprintf("AnhurDB API error (HTTP 409): session record limit reached (session=%s, limit=%s, count=%s): %s",
		sessionText, optionalIntText(limitError.Limit), optionalIntText(limitError.Count), limitError.ServerMessage)
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

// sessionRecordLimitWireBody is the subset of a 409 body this file reads.
type sessionRecordLimitWireBody struct {
	Error       string `json:"error"`
	Code        string `json:"code"`
	Limit       *int   `json:"limit"`
	Count       *int   `json:"count"`
	SessionUUID string `json:"session_uuid"`
}

// sessionRecordLimitFromResponse returns the typed refusal, or nil when the
// response is any other kind of answer.
func sessionRecordLimitFromResponse(statusCode int, body []byte) *SessionRecordLimitError {
	if statusCode != 409 {
		return nil
	}
	var wireBody sessionRecordLimitWireBody
	if decodeErr := json.Unmarshal(body, &wireBody); decodeErr != nil {
		// A non-JSON body (a proxy, a plain-text error) can still carry the text.
		wireBody = sessionRecordLimitWireBody{Error: string(body)}
	}
	serverMessage := strings.TrimSpace(wireBody.Error)
	if wireBody.Code != SessionRecordLimitCode && !strings.HasPrefix(serverMessage, sessionRecordLimitTextPrefix) {
		return nil
	}
	limitValue := wireBody.Limit
	if limitValue == nil {
		// Junior Tip [the text is the server's own number, not a guess]: an old
		// server writes N into the sentence; reading it back is not inventing
		// a value. Count has no such source, so it stays nil.
		if textMatch := sessionRecordLimitFromText.FindStringSubmatch(serverMessage); textMatch != nil {
			if parsedLimit, parseErr := strconv.Atoi(textMatch[1]); parseErr == nil {
				limitValue = &parsedLimit
			}
		}
	}
	return &SessionRecordLimitError{
		SessionUUID:   wireBody.SessionUUID,
		Limit:         limitValue,
		Count:         wireBody.Count,
		ServerMessage: serverMessage,
		response:      &APIError{StatusCode: statusCode, Body: string(body)},
	}
}

// optionalIntText prints a nullable number as the number or "unknown".
func optionalIntText(value *int) string {
	if value == nil {
		return "unknown"
	}
	return strconv.Itoa(*value)
}
