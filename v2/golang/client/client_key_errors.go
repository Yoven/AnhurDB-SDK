package client

// client_key_errors.go — the two typed errors of the client-key surface:
//
//   - *ClientAPIError          the server REFUSED (non-2xx). Code is body.error.
//   - *ClientAPIResponseError  the server said yes (2xx) but the answer cannot
//     be trusted: malformed, or an impersonation echo weaker than requested.
//
// Messages are byte-identical in Python (client_key_errors.py) and TypeScript
// (clientKeyErrors.ts).

import (
	"errors"
	"fmt"
)

var (
	// ErrL1OnlyNotHonoured: l1_only was requested and the echo is not true.
	ErrL1OnlyNotHonoured = errors.New("anhurdb: impersonation l1_only not honoured")
	// ErrScopeNotHonoured: a scope was requested and the echo is unknown or higher.
	ErrScopeNotHonoured = errors.New("anhurdb: impersonation scope not honoured")
)

// Codes carried by ClientAPIResponseError.Code.
const (
	ResponseCodeUnexpected          = "unexpected_response"
	ResponseCodeL1OnlyNotHonoured   = "l1_only_not_honoured"
	ResponseCodeScopeNotHonoured    = "scope_not_honoured"
	clientAPIErrorExcerptCharacters = 500
)

// ClientAPIError is a non-2xx answer from /api/v1/client/*.
//
// Code is body.error verbatim. On the ADR-0034 envelope it is a machine code
// (child_suspended, child_not_found, child_limit, tenant_limit,
// invalid_scope, invalid_tenant_id, invalid_profile, invalid_child_id,
// invalid_external_ref, invalid_json, key_not_found, key_not_owned, ...). A
// few refusals are frozen SENTENCES in the same field ("client API key
// required", "tenant not owned by this client", the 409 collision text), and
// a not_leader 503 carries prose in "error" with ErrorType "not_leader".
//
// Junior Tip [why the transport's generic mapping is bypassed here]: the
// data-route mapping (handleResponse) only types 401/403 (*AuthRefusedError)
// and 404; the client-key surface needs the body's code on EVERY status
// (child_limit 429, not_leader 503, ...), so it decodes the same envelope
// itself. Unwrap still returns the shared sentinels, so
// errors.Is(err, ErrUnauthorized) and errors.Is(err, ErrNotFound) keep their
// meaning.
type ClientAPIError struct {
	// Status is the HTTP status.
	Status int
	// Code is body.error ("" when the body is not a JSON object).
	Code string
	// Detail is body.detail ("" when absent).
	Detail string
	// ErrorType is body.error_type ("not_leader" on a provably uncommitted write).
	ErrorType string
	// Method and Path identify the request (API path, no base URL).
	Method string
	Path   string
	// Body is the raw body excerpt (≤500 characters), for non-JSON answers.
	Body string
}

// Error is the cross-SDK message.
func (apiError *ClientAPIError) Error() string {
	prefix := fmt.Sprintf("Client API error (HTTP %d) on %s %s: ", apiError.Status, apiError.Method, apiError.Path)
	switch {
	case apiError.Code != "" && apiError.Detail != "":
		return prefix + apiError.Code + ": " + apiError.Detail
	case apiError.Code != "":
		return prefix + apiError.Code
	case apiError.Body != "":
		return prefix + apiError.Body
	default:
		return prefix + "(empty body)"
	}
}

// StatusCode returns the HTTP status.
func (apiError *ClientAPIError) StatusCode() int { return apiError.Status }

// Kind classifies by status (KindFor).
func (apiError *ClientAPIError) Kind() ErrorKind { return KindFor(apiError.Status) }

// Retryable follows Kind, except the two hard ceilings.
//
// Junior Tip [429 is not always "slow down"]: child_limit and tenant_limit
// are caps, not rate limits — the same request is refused until an operator
// raises the cap. Classifying them retryable would make a retry loop hammer a
// wall forever.
func (apiError *ClientAPIError) Retryable() bool {
	if apiError.Code == "child_limit" || apiError.Code == "tenant_limit" {
		return false
	}
	return apiError.Kind().Retryable()
}

// IsNotLeader reports the provably-uncommitted 503 (safe to retry at once).
func (apiError *ClientAPIError) IsNotLeader() bool { return apiError.ErrorType == "not_leader" }

// Unwrap maps the status to the SDK's existing sentinel, so errors.Is keeps
// working exactly as it does for the data-plane methods.
func (apiError *ClientAPIError) Unwrap() error {
	switch {
	case apiError.Status == 401 || apiError.Status == 403:
		return ErrUnauthorized
	case apiError.Status == 404:
		return ErrNotFound
	case apiError.Status == 400 || apiError.Status == 422:
		return ErrInvalidQuery
	case apiError.Status >= 500:
		return ErrServerError
	default:
		return nil
	}
}

// ClientAPIResponseError is a 2xx answer the SDK refuses to hand over.
//
// KeyID is set when an impersonation token WAS minted but its echo was
// weaker than requested; the token stays valid server-side until it expires,
// so a careful caller revokes it with RevokeImpersonation(KeyID).
type ClientAPIResponseError struct {
	Method string
	Path   string
	// Code is unexpected_response, l1_only_not_honoured or scope_not_honoured.
	Code string
	// Reason is the human sentence.
	Reason string
	// KeyID is the minted token id, when there is one.
	KeyID string
}

// Error is the cross-SDK message.
func (responseError *ClientAPIResponseError) Error() string {
	return fmt.Sprintf("Untrusted client API response from %s %s: %s: %s",
		responseError.Method, responseError.Path, responseError.Code, responseError.Reason)
}

// Kind is KindServer: the server (or something posing as it) broke the contract.
func (responseError *ClientAPIResponseError) Kind() ErrorKind { return KindServer }

// Retryable is false: the same server answers the same way.
func (responseError *ClientAPIResponseError) Retryable() bool { return false }

// Unwrap lets errors.Is match the code's sentinel.
func (responseError *ClientAPIResponseError) Unwrap() error {
	switch responseError.Code {
	case ResponseCodeL1OnlyNotHonoured:
		return ErrL1OnlyNotHonoured
	case ResponseCodeScopeNotHonoured:
		return ErrScopeNotHonoured
	default:
		return ErrUnexpectedResponse
	}
}

// excerpt bounds a body quoted inside an error message.
func excerpt(bodyText string) string {
	if len(bodyText) > clientAPIErrorExcerptCharacters {
		return bodyText[:clientAPIErrorExcerptCharacters]
	}
	return bodyText
}
