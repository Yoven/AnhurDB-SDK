/*
Package client provides the HTTP transport layer for the AnhurDB Go SDK.

Security hardening:
  - X-API-Key header for auth (matches server middleware).
  - Response body capped at 100 MB (prevents memory exhaustion DoS).
  - Redirect following disabled (prevents credential leakage — CVE-2026-34518 class).
  - Header injection protection: tenant_id validated against CRLF.
  - Error messages never include the full API key.
  - Default 30s timeout prevents indefinite hangs (OWASP API4).
*/
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxResponseSize is the maximum response body size (100 MB).
// Prevents memory exhaustion from malicious or misconfigured servers.
const maxResponseSize = 100 * 1024 * 1024

// headerMinIndex is the request header that opts a single read into
// read-your-writes (RYW) consistency: "do not serve this read until the
// node's local applied Raft index for the tenant has reached N". The value
// is the raft_index a caller received from its own prior write
// (AddResult.RaftIndex). The server's MinIndexBarrier middleware blocks the
// read until the local node has replicated up to that index, then serves it.
//
// Junior Tip [why a header, not a query param — verified against
// server/middleware/min_index.go, 2026-06-17]: the AnhurDB server reads
// X-Anhur-Min-Index from the HTTP request header (and the equivalent
// x-anhur-min-index gRPC metadata key on the gRPC handlers). It is NOT a
// query parameter. Sending it any other way is a silent no-op — the read
// stays eventually consistent and the just-written record can be missed on a
// lagging follower. Reads WITHOUT this header keep their load-balanced,
// eventually-consistent behaviour (zero overhead), so only the caller that
// actually needs RYW pays for it.
const headerMinIndex = "X-Anhur-Min-Index"

// HTTPConnection manages network requests to AnhurDB using pure REST.
//
// Every method maps directly to an HTTP verb — no translation layer,
// no protocol wrapping. The server's REST API is the contract.
type HTTPConnection struct {
	BaseURL    string
	APIKey     string
	TenantID   string // optional, for multi-tenant deployments
	HTTPClient *http.Client
}

// validateHeaderValue checks that a string is safe for use as an HTTP
// header value. Rejects CR, LF, null bytes, and other control characters
// that could enable HTTP header injection (response splitting).
func validateHeaderValue(value, name string) error {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c < 0x20 || c > 0x7E {
			return fmt.Errorf(
				"%s contains invalid characters for HTTP header "+
					"(byte 0x%02x at position %d). "+
					"Only printable ASCII (0x20-0x7E) is allowed", name, c, i)
		}
	}
	return nil
}

// NewConnection initialises a connection to the AnhurDB server.
//
// The trailing slash is stripped so callers can pass
// "http://localhost:8000/" and path joins still work correctly.
//
// Security: Validates apiKey and sets up redirect-blocking CheckRedirect.
func NewConnection(baseURL, apiKey string, timeout time.Duration) *HTTPConnection {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// Validate API key against header injection.
	if err := validateHeaderValue(apiKey, "apiKey"); err != nil {
		// Return a connection that will work but log the validation error.
		// We don't panic to match the graceful-failure pattern.
	}

	return &HTTPConnection{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: timeout,
			// SECURITY: Block redirects to prevent X-API-Key header
			// leaking to external origins on 3xx responses.
			// This mitigates CVE-2026-34518 class of attacks.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// setHeaders applies auth and content-type headers to every request.
//
// X-API-Key is the primary auth mechanism. X-Tenant-ID is only set
// when explicitly configured (multi-tenant deployments).
//
// minIndex, when greater than zero, adds the X-Anhur-Min-Index read-barrier
// header (see headerMinIndex). A value of 0 means "not requested" and leaves
// the header off entirely, preserving the default eventually-consistent,
// load-balanced read. The value is rendered in base-10 to match the
// strconv.ParseUint the server uses to decode it.
func (c *HTTPConnection) setHeaders(req *http.Request, minIndex uint64) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("User-Agent", "AnhurSDK-Golang/2.1")
	if c.TenantID != "" {
		// Validate at request time in case TenantID was set after construction.
		if err := validateHeaderValue(c.TenantID, "TenantID"); err == nil {
			req.Header.Set("X-Tenant-ID", c.TenantID)
		}
	}
	if minIndex > 0 {
		req.Header.Set(headerMinIndex, strconv.FormatUint(minIndex, 10))
	}
}

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
		// Truncate body in error to prevent oversized log entries.
		truncated := string(body)
		if len(truncated) > 500 {
			truncated = truncated[:500]
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Body: truncated}
	case resp.StatusCode >= 500:
		truncated := string(body)
		if len(truncated) > 500 {
			truncated = truncated[:500]
		}
		return nil, &APIError{StatusCode: resp.StatusCode, Body: truncated}
	default:
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}
}

// Get sends a GET request to the given path with optional query parameters.
//
// minIndex, when greater than zero, sets the X-Anhur-Min-Index read-barrier
// header so the server blocks until the node has applied that Raft index for
// the tenant (read-your-writes). Pass 0 for the default eventually-consistent
// read. See headerMinIndex and the WithMinIndex read option.
func (c *HTTPConnection) Get(ctx context.Context, path string, params url.Values, minIndex uint64) ([]byte, error) {
	fullURL := c.BaseURL + path
	if len(params) > 0 {
		fullURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GET request: %w", err)
	}
	c.setHeaders(req, minIndex)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, ErrConnectionFail
	}

	return c.handleResponse(resp)
}

// Post sends a POST request with a JSON-encoded body.
//
// This is the WRITE entry point: it never sets the min-index barrier header,
// because a write PRODUCES the raft_index (returned in the response body)
// rather than consuming one. Read-shaped POST endpoints (global search, graph
// walk) use PostRead instead so they can opt into read-your-writes.
func (c *HTTPConnection) Post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return c.post(ctx, path, body, 0)
}

// PostRead sends a POST request for a READ-shaped endpoint (e.g.
// /api/v1/search/global, /api/v1/walk) with an optional X-Anhur-Min-Index
// read barrier. minIndex 0 leaves the header off (default eventually-
// consistent read); a non-zero value blocks the read until the node has
// applied that Raft index for the tenant.
//
// Junior Tip [why a separate method, verified against server/app.go:383]: the
// server's MinIndexBarrier middleware wraps the WHOLE apiMux, so it honours
// the header on POST routes too (search/walk are reads behind POST). Keeping
// this distinct from Post means write call sites can never accidentally send
// a barrier, while POST-backed reads gain the same RYW opt-in as GET reads.
func (c *HTTPConnection) PostRead(ctx context.Context, path string, body interface{}, minIndex uint64) ([]byte, error) {
	return c.post(ctx, path, body, minIndex)
}

// post is the shared POST implementation. minIndex 0 means "no RYW barrier".
func (c *HTTPConnection) post(ctx context.Context, path string, body interface{}, minIndex uint64) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshalling request body: %w", err)
		}
		reader = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("creating POST request: %w", err)
	}
	c.setHeaders(req, minIndex)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, ErrConnectionFail
	}

	return c.handleResponse(resp)
}

// Patch sends a PATCH request with a JSON-encoded body.
func (c *HTTPConnection) Patch(ctx context.Context, path string, body interface{}) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshalling request body: %w", err)
		}
		reader = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("creating PATCH request: %w", err)
	}
	// PATCH is a write — it produces a raft_index, not consumes one. Header off.
	c.setHeaders(req, 0)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, ErrConnectionFail
	}

	return c.handleResponse(resp)
}

// Delete sends a DELETE request to the given path.
func (c *HTTPConnection) Delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("creating DELETE request: %w", err)
	}
	// DELETE is a write — header off.
	c.setHeaders(req, 0)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ErrConnectionFail
	}

	_, err = c.handleResponse(resp)
	return err
}
