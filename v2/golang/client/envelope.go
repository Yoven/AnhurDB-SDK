package client

// envelope.go — one domain: the strict decode of the eight listing routes whose
// answer is ALWAYS a JSON object envelope, and the typed error raised when it is
// not.
//
// Routes and the key each one must carry (server truth, AnhurDB main):
//
//	GET /api/v1/sessions/stats       "sessions"  handler/record_session.go:135-142
//	GET /api/v1/recent               "records"   handler/record_session.go:455
//	GET /api/v1/manifest             "records"   handler/search_aux.go:224
//	GET /api/v1/chats/{uuid}/manifest "records"  handler/record_session.go:305-311
//	GET /api/v1/chats/{uuid}         "records"   handler/record_session.go:184
//	GET /api/v1/entities             "entities"  handler/entity.go:254-257
//	GET /api/v1/entities/list        "entities"  handler/entity.go:200-208
//	GET /api/v1/records/{id}/entities "entities" handler/entity.go:561-565
//
// The rule, identical in Python (client/envelope.py) and TypeScript
// (src/envelope.ts): the body must be a JSON object that HAS the key; the
// key's value must be a list or null (null = zero rows, the server's own
// zero-hit answer). Anything else — a bare array, an empty body, a missing
// key, a non-list value — is an *UnexpectedResponseError of kind "server".
//
// Junior Tip [why "no rows" can never double as "I did not understand"]: 3.x
// read a bare array or an empty body as an empty page. An empty list is a
// legitimate answer, so using it as the signal for an unrecognised shape made
// a proxy page or a server bug read as "this tenant has no memories".

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUnexpectedResponse matches every *UnexpectedResponseError:
// errors.Is(err, ErrUnexpectedResponse).
var ErrUnexpectedResponse = errors.New("anhurdb: unexpected response shape")

// UnexpectedResponseError is a 2xx answer that is not the route's envelope.
type UnexpectedResponseError struct {
	// Route is the route TEMPLATE ("/api/v1/chats/{uuid}"), not the concrete
	// path, so the message is byte-identical across the three SDKs.
	Route string
	// Key is the envelope key the answer had to carry.
	Key string
}

// Error is the cross-SDK message (Python and TypeScript print the same bytes).
func (shapeError *UnexpectedResponseError) Error() string {
	return fmt.Sprintf("unexpected response from GET %s: expected a JSON object with \"%s\"", shapeError.Route, shapeError.Key)
}

// Unwrap lets errors.Is(err, ErrUnexpectedResponse) match.
func (shapeError *UnexpectedResponseError) Unwrap() error { return ErrUnexpectedResponse }

// Kind is KindServer: the server (or something posing as it) broke the contract.
func (shapeError *UnexpectedResponseError) Kind() ErrorKind { return KindServer }

// Retryable follows KindServer, exactly like Python and TypeScript.
func (shapeError *UnexpectedResponseError) Retryable() bool { return KindServer.Retryable() }

// requireEnvelope checks raw against the rule above and returns nil when it is
// the envelope. The caller then unmarshals the full typed envelope from raw.
func requireEnvelope(raw []byte, route string, key string) error {
	shapeError := &UnexpectedResponseError{Route: route, Key: key}
	var envelopeFields map[string]json.RawMessage
	if decodeErr := json.Unmarshal(raw, &envelopeFields); decodeErr != nil || envelopeFields == nil {
		return shapeError
	}
	listValue, keyPresent := envelopeFields[key]
	if !keyPresent {
		return shapeError
	}
	if firstJSONToken(listValue) != '[' && string(listValue) != "null" {
		return shapeError
	}
	return nil
}

// firstJSONToken returns the first non-whitespace byte of a JSON payload, or 0
// if the payload is empty/whitespace-only.
func firstJSONToken(raw []byte) byte {
	for _, currentByte := range raw {
		switch currentByte {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return currentByte
		}
	}
	return 0
}
