package client

// response.go — turning ONE *http.Response into a body or a typed error.
//
// Split out of connection.go (house 300-line cut, 2026-11-20): connection.go
// owns how a request is BUILT and SENT (headers, multipart, transport errors);
// this file owns what an ANSWER MEANS. They change for different reasons — a
// new verb touches the first, a new server error contract (like the session
// record limit, ADR-0033) touches only this one.

import (
	"fmt"
	"io"
	"net/http"
)

// handleResponse reads the response body and maps HTTP errors to typed errors.
//
// Security:
//   - Response body is capped at maxResponseSize (100 MB).
//   - Error messages never include the API key.
//   - Redirect responses (3xx) are treated as errors.
func (c *HTTPConnection) handleResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()

	// SECURITY: Cap response body to prevent memory exhaustion.
	limitedReader := io.LimitReader(resp.Body, maxResponseSize+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if int64(len(body)) > maxResponseSize {
		return nil, fmt.Errorf("response exceeds maximum size (%d MB)",
			maxResponseSize/(1024*1024))
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return body, nil
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, ErrUnauthorized
	case resp.StatusCode == 404:
		return nil, ErrNotFound
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		// Redirect responses — blocked for security.
		return nil, fmt.Errorf(
			"server returned redirect (HTTP %d); redirects are disabled "+
				"to prevent credential leakage", resp.StatusCode)
	case resp.StatusCode == 400 || resp.StatusCode == 422:
		// An upload refused by the tenant-library collision guard arrives as 400.
		if scopeErr := scopeErrorFromResponse(resp.StatusCode, body); scopeErr != nil {
			return nil, scopeErr
		}
		// Truncate body in error to prevent oversized log entries.
		truncated := string(body)
		if len(truncated) > 500 {
			truncated = truncated[:500]
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Body: truncated}
	case resp.StatusCode == 409:
		// Junior Tip [one 409 is not like the others, 2026-11-20]: a full
		// session (ADR-0033) is a refusal that NEVER heals by retrying the same
		// session, while other conflicts are ordinary contract errors. Callers
		// that only see "HTTP 409" cannot tell the two apart, which is how a
		// full session once looked like a transient outage for ten days. The
		// typed error still unwraps to the same *APIError, so code that already
		// branches on StatusCode == 409 keeps working unchanged.
		if limitErr := sessionRecordLimitFromResponse(resp.StatusCode, body); limitErr != nil {
			return nil, limitErr
		}
		// L2 library answers (collision, ambiguous upload id) — scope_errors.go.
		if scopeErr := scopeErrorFromResponse(resp.StatusCode, body); scopeErr != nil {
			return nil, scopeErr
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	case resp.StatusCode >= 500:
		// A partial tenant-library delete carries per-leg outcomes that can exceed
		// the excerpt below, so it is decoded from the FULL body first.
		if scopeErr := scopeErrorFromResponse(resp.StatusCode, body); scopeErr != nil {
			return nil, scopeErr
		}
		truncated := string(body)
		if len(truncated) > 500 {
			truncated = truncated[:500]
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Body: truncated}
	default:
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}
}
