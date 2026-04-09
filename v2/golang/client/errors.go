package client

import (
	"errors"
	"fmt"
)

// Junior Tip: Typed errors let callers use errors.Is / errors.As to
// handle specific failure modes (auth vs. network vs. bad request)
// without parsing error strings.

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
// Junior Tip: This lets callers inspect the raw server response when
// a high-level error like ErrServerError is too coarse.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("AnhurDB API error (HTTP %d): %s", e.StatusCode, e.Body)
}
