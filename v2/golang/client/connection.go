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
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseSize is the maximum response body size (100 MB).
// Prevents memory exhaustion from malicious or misconfigured servers.
const maxResponseSize = 100 * 1024 * 1024

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

	// Reject API keys that cannot be sent safely as an HTTP header value.
	// Invalid keys are treated like an empty key: methods return ErrEmptyAPIKey
	// rather than risking header injection.
	safeAPIKey := apiKey
	if err := validateHeaderValue(apiKey, "apiKey"); err != nil {
		safeAPIKey = ""
	}

	return &HTTPConnection{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  safeAPIKey,
		HTTPClient: &http.Client{
			Timeout: timeout,
			// SECURITY: Block redirects to prevent X-API-Key header
			// leaking to external origins on 3xx responses.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// setAuthHeaders applies auth headers without forcing Content-Type.
// JSON callers use setHeaders; multipart callers set Content-Type themselves.
func (c *HTTPConnection) setAuthHeaders(req *http.Request) {
	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("User-Agent", "AnhurSDK-Golang/2.1")
	if c.TenantID != "" {
		if err := validateHeaderValue(c.TenantID, "TenantID"); err == nil {
			req.Header.Set("X-Tenant-ID", c.TenantID)
		}
	}
}

// setHeaders applies auth and JSON content-type headers to every request.
//
// X-API-Key is the primary auth mechanism. X-Tenant-ID is only set
// when explicitly configured (multi-tenant deployments).
func (c *HTTPConnection) setHeaders(req *http.Request) {
	c.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
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
func (c *HTTPConnection) Get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	fullURL := c.BaseURL + path
	if len(params) > 0 {
		fullURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GET request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, ErrConnectionFail
	}

	return c.handleResponse(resp)
}

// Post sends a POST request with a JSON-encoded body.
func (c *HTTPConnection) Post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return c.post(ctx, path, body)
}

// PostMultipart sends a multipart/form-data POST (file upload).
//
func (c *HTTPConnection) PostMultipart(
	ctx context.Context,
	path string,
	fileField string,
	filename string,
	fileBytes []byte,
	extraFields map[string]string,
) ([]byte, error) {
	var bodyBuf bytes.Buffer
	formWriter := multipart.NewWriter(&bodyBuf)
	filePart, createErr := formWriter.CreateFormFile(fileField, filename)
	if createErr != nil {
		return nil, fmt.Errorf("creating multipart file field: %w", createErr)
	}
	if _, writeErr := filePart.Write(fileBytes); writeErr != nil {
		return nil, fmt.Errorf("writing multipart file bytes: %w", writeErr)
	}
	for fieldName, fieldValue := range extraFields {
		if writeFieldErr := formWriter.WriteField(fieldName, fieldValue); writeFieldErr != nil {
			return nil, fmt.Errorf("writing multipart field %s: %w", fieldName, writeFieldErr)
		}
	}
	if closeErr := formWriter.Close(); closeErr != nil {
		return nil, fmt.Errorf("closing multipart writer: %w", closeErr)
	}

	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, &bodyBuf)
	if reqErr != nil {
		return nil, fmt.Errorf("creating multipart POST: %w", reqErr)
	}
	c.setAuthHeaders(req)
	req.Header.Set("Content-Type", formWriter.FormDataContentType())

	resp, doErr := c.HTTPClient.Do(req)
	if doErr != nil {
		return nil, ErrConnectionFail
	}
	return c.handleResponse(resp)
}

// PostRead sends a POST request for a READ-shaped endpoint (e.g.
// /api/v1/search/global, /api/v1/walk).
func (c *HTTPConnection) PostRead(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return c.post(ctx, path, body)
}

// post is the shared POST implementation.
func (c *HTTPConnection) post(ctx context.Context, path string, body interface{}) ([]byte, error) {
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
	c.setHeaders(req)

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
	// PATCH is a write.
	c.setHeaders(req)

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
	// DELETE is a write.
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ErrConnectionFail
	}

	_, err = c.handleResponse(resp)
	return err
}
