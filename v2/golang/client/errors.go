package client

import (
	"context"
	"errors"
	"fmt"
	"net"
)

var (
	// ErrUnauthorized is returned when the API key is invalid or expired (401/403).
	ErrUnauthorized = errors.New("authentication failed: invalid API key")

	// ErrInvalidQuery is returned when the server rejects the request as malformed (400/422).
	ErrInvalidQuery = errors.New("invalid query request")

	// ErrConnectionFail is returned when the SDK cannot reach the AnhurDB server.
	ErrConnectionFail = errors.New("failed to connect to AnhurDB server")

	// ErrServerError is returned for 5xx responses from the server.
	ErrServerError = errors.New("internal server error")

	// ErrNotFound is returned when the requested resource does not exist (404).
	ErrNotFound = errors.New("resource not found")

	// ErrEmptyInput is returned when a required input parameter is empty.
	ErrEmptyInput = errors.New("input cannot be empty")

	// ErrEmptyAPIKey is returned when the API key is not provided.
	ErrEmptyAPIKey = errors.New("api_key is required")
)

// APIError wraps an HTTP status code with the response body for debugging.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("AnhurDB API error (HTTP %d): %s", e.StatusCode, e.Body)
}

// ── Error classification (parity with the Python/TypeScript SDKs) ───────────
//
// Junior Tip [why kinds and not just sentinels, 2026-08-13]: a benchmark run
// died on an error carrying no message and no status — a timeout that escaped
// the client's error handling. Callers cannot decide anything from that. Every
// error the SDK returns must answer one question: is retrying this worth it?
//
// Retryable does NOT mean the SDK retries writes for you. A timeout on a write
// means the server may or may not have committed it, so the idempotency
// decision stays with the caller. See SDK_ERROR_CONTRACT.md.

// ErrorKind classifies a failure so callers branch on meaning, not on strings.
type ErrorKind string

const (
	KindAuth           ErrorKind = "auth"
	KindInvalidRequest ErrorKind = "invalid_request"
	KindNotFound       ErrorKind = "not_found"
	KindConflict       ErrorKind = "conflict"
	KindRateLimited    ErrorKind = "rate_limited"
	KindUnavailable    ErrorKind = "unavailable"
	KindTimeout        ErrorKind = "timeout"
	KindTransport      ErrorKind = "transport"
	KindServer         ErrorKind = "server"
)

// Retryable reports whether repeating the same call could produce a different
// result. Note this is about the transport, not about safety: retrying a write
// after a timeout may duplicate it.
func (k ErrorKind) Retryable() bool {
	switch k {
	case KindRateLimited, KindUnavailable, KindTimeout, KindTransport, KindServer:
		return true
	default:
		return false
	}
}

// KindFor classifies an HTTP status. StatusCode 0 means the request never
// reached the server.
func KindFor(statusCode int) ErrorKind {
	switch {
	case statusCode == 0:
		return KindTransport
	case statusCode == 401 || statusCode == 403:
		return KindAuth
	case statusCode == 404:
		return KindNotFound
	case statusCode == 409:
		return KindConflict
	case statusCode == 429:
		return KindRateLimited
	case statusCode == 503:
		return KindUnavailable
	case statusCode >= 400 && statusCode < 500:
		return KindInvalidRequest
	default:
		return KindServer
	}
}

// Kind returns the classification of this API error.
func (e *APIError) Kind() ErrorKind { return KindFor(e.StatusCode) }

// Retryable reports whether this error is worth retrying.
func (e *APIError) Retryable() bool { return e.Kind().Retryable() }

// TransportError is returned when the request never produced an HTTP response:
// a timeout, a cancelled context, or a connection failure.
//
// Junior Tip [the trap this closes]: Go's analogue of the Python defect is
// letting context.DeadlineExceeded bubble up bare. It carries no status and its
// message says nothing about AnhurDB, so a caller cannot tell "the server
// rejected me" from "I never reached the server". Always wrap.
type TransportError struct {
	Op   string // e.g. "POST /api/v1/search"
	kind ErrorKind
	err  error
}

func (e *TransportError) Error() string {
	if e.kind == KindTimeout {
		return fmt.Sprintf("%s: request timed out (the server may still have processed it)", e.Op)
	}
	return fmt.Sprintf("%s: could not reach AnhurDB", e.Op)
}

func (e *TransportError) Unwrap() error   { return e.err }
func (e *TransportError) Kind() ErrorKind { return e.kind }
func (e *TransportError) Retryable() bool { return e.kind.Retryable() }

// StatusCode is always 0: the request never reached the server.
func (e *TransportError) StatusCode() int { return 0 }

// newTransportError classifies a non-HTTP failure. Deadline and cancellation
// become KindTimeout; everything else is KindTransport.
//
// SECURITY: never embed the URL — it carries the server address into logs.
func newTransportError(op string, err error) *TransportError {
	kind := KindTransport
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		kind = KindTimeout
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		kind = KindTimeout
	}
	return &TransportError{Op: op, kind: kind, err: err}
}
