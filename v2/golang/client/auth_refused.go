package client

// auth_refused.go — what a 401/403 on a DATA route means, and the one decoder
// of the server's {"error","detail","error_type"} refusal envelope.
//
// Domain, in one sentence: turn an authentication/authorization refusal into a
// typed error that keeps the server's reason instead of collapsing every
// refusal into "invalid API key".
//
// Messages are byte-identical in Python (anhurdb/client/auth_errors.py) and
// TypeScript (src/authErrors.ts) whenever the body carries a code.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Codes the server puts in body.error on a 401/403 (AnhurDB
// server/middleware/auth*.go, handler/service_bridge.go). Compare through the
// AuthRefusedError predicates rather than against these strings directly.
const (
	// AuthCodeL1Only: a token restricted to L1 sessions touched another plane (403).
	AuthCodeL1Only = "l1_only"
	// AuthCodeChildSuspended: the key's client (a child) is suspended (403).
	AuthCodeChildSuspended = "child_suspended"
	// AuthCodeImpersonationRequired: a client key was used on a data route;
	// mint an impersonation token first (403).
	AuthCodeImpersonationRequired = "client_key_requires_impersonation"
	// AuthCodeInsufficientScopePrefix starts the frozen sentence
	// "insufficient scope — key does not have permission for this operation" (403).
	AuthCodeInsufficientScopePrefix = "insufficient scope"
	// AuthCodeInvalidKey: the key is unknown, revoked or inactive (401).
	AuthCodeInvalidKey = "invalid or inactive API key"
)

// AuthRefusedError is every HTTP 401/403 answer on a data route.
//
// Code is body.error verbatim (parsed whatever the Content-Type: the auth
// middleware answers text/plain with a JSON body). It is "" when the body is
// not a JSON object carrying a string "error".
//
// Junior Tip [why a type and not the bare sentinel, 2026-11-21]: handleResponse
// used to return ErrUnauthorized for EVERY 401/403 and drop the body, so a
// scope refusal, an l1_only token touching the wrong plane and a suspended
// child all read "authentication failed: invalid API key" — a caller rotated a
// perfectly valid key because the SDK lied about the reason. The server always
// said why; this type keeps it. Unwrap still returns ErrUnauthorized, so every
// errors.Is(err, ErrUnauthorized) branch (upload wait, the plugin's delivery
// retry) keeps working unchanged. Never compare with == ErrUnauthorized: that
// was never the contract and is now false.
type AuthRefusedError struct {
	// Status is the HTTP status (401 or 403).
	Status int
	// Code is body.error verbatim ("" for a non-JSON body).
	Code string
	// Detail is body.detail ("" when absent).
	Detail string
	// ErrorType is body.error_type ("" when absent).
	ErrorType string
}

// Error is the cross-SDK message when a code is present:
// "Authentication failed (HTTP <s>): <code>[: <detail>]". Without a code it
// keeps the historical ErrUnauthorized text so existing log matchers survive.
func (authError *AuthRefusedError) Error() string {
	switch {
	case authError.Code != "" && authError.Detail != "":
		return fmt.Sprintf("Authentication failed (HTTP %d): %s: %s", authError.Status, authError.Code, authError.Detail)
	case authError.Code != "":
		return fmt.Sprintf("Authentication failed (HTTP %d): %s", authError.Status, authError.Code)
	default:
		return ErrUnauthorized.Error()
	}
}

// Unwrap lets errors.Is(err, ErrUnauthorized) match every *AuthRefusedError.
func (authError *AuthRefusedError) Unwrap() error { return ErrUnauthorized }

// StatusCode returns the HTTP status (401 or 403).
func (authError *AuthRefusedError) StatusCode() int { return authError.Status }

// Kind is KindAuth.
func (authError *AuthRefusedError) Kind() ErrorKind { return KindFor(authError.Status) }

// Retryable is false: the same credential is refused the same way next time.
func (authError *AuthRefusedError) Retryable() bool { return false }

// IsL1Only reports the 403 for an l1_only token outside the L1 plane.
func (authError *AuthRefusedError) IsL1Only() bool { return authError.Code == AuthCodeL1Only }

// IsChildSuspended reports the 403 for a key whose child client is suspended.
func (authError *AuthRefusedError) IsChildSuspended() bool {
	return authError.Code == AuthCodeChildSuspended
}

// IsInsufficientScope reports the 403 for a key whose scope does not allow the
// operation (read_only writing, write calling an admin route).
//
// Junior Tip [why a prefix and not equality]: this refusal is a frozen
// SENTENCE in body.error, not a machine code. Matching its stable head keeps
// the predicate true if the server ever rewords the explanation after the dash.
func (authError *AuthRefusedError) IsInsufficientScope() bool {
	return strings.HasPrefix(authError.Code, AuthCodeInsufficientScopePrefix)
}

// IsImpersonationRequired reports the 403 for a client key on a data route.
func (authError *AuthRefusedError) IsImpersonationRequired() bool {
	return authError.Code == AuthCodeImpersonationRequired
}

// IsInvalidKey reports the 401 for an unknown, revoked or inactive key.
func (authError *AuthRefusedError) IsInvalidKey() bool {
	return authError.Code == AuthCodeInvalidKey
}

// authRefusedFromBody builds the typed refusal for a 401/403 body.
func authRefusedFromBody(status int, responseBody []byte) *AuthRefusedError {
	authError := &AuthRefusedError{Status: status}
	if envelope, decoded := decodeErrorEnvelope(responseBody); decoded {
		authError.Code = envelope.code
		authError.Detail = envelope.detail
		authError.ErrorType = envelope.errorType
	}
	return authError
}

// errorEnvelope is the server's refusal body {"error","detail","error_type"}.
type errorEnvelope struct {
	code      string
	detail    string
	errorType string
}

// decodeErrorEnvelope reads the refusal envelope when the body is a JSON
// object whose "error" is a non-empty string; decoded is false otherwise.
//
// Junior Tip [field by field, on purpose]: unmarshalling straight into a
// struct of strings fails the WHOLE body when one optional field has another
// type (say "detail": 5), and the code would be lost with it. Python and
// TypeScript keep the code and drop only the mistyped field, so Go decodes
// each field on its own to behave the same. The Content-Type is ignored
// because http.Error answers text/plain even when the body is JSON.
func decodeErrorEnvelope(responseBody []byte) (envelope errorEnvelope, decoded bool) {
	var rawFields map[string]json.RawMessage
	if json.Unmarshal(responseBody, &rawFields) != nil || rawFields == nil {
		return errorEnvelope{}, false
	}
	envelope.code = stringField(rawFields, "error")
	if envelope.code == "" {
		return errorEnvelope{}, false
	}
	envelope.detail = stringField(rawFields, "detail")
	envelope.errorType = stringField(rawFields, "error_type")
	return envelope, true
}

// stringField returns rawFields[fieldName] when it is a JSON string, else "".
func stringField(rawFields map[string]json.RawMessage, fieldName string) string {
	var fieldValue string
	if json.Unmarshal(rawFields[fieldName], &fieldValue) != nil {
		return ""
	}
	return fieldValue
}
