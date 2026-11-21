/*
Package client provides the HTTP transport layer for the AnhurDB Go SDK.

Security hardening:
  - X-API-Key header for auth (matches server middleware).
  - Response body capped at 100 MB (prevents memory exhaustion DoS).
  - Redirect following disabled (prevents credential leakage — CVE-2026-34518 class).
  - Tenant ID validated with the server's identifier rule (tenant_header.go);
    an invalid one fails every call before any request is sent.
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
	req.Header.Set("User-Agent", UserAgent)
	// TenantID was validated by newRequest before this runs (see tenant_header.go).
	if c.TenantID != "" {
		req.Header.Set("X-Tenant-ID", c.TenantID)
	}
}

// newRequest builds a request AFTER refusing an invalid TenantID.
//
// Junior Tip [why every verb goes through here]: TenantID is an exported field,
// so it can change after NewConnection/NewMemory. Checking at request time is the
// only place that sees the value actually sent. NewMemory cannot return an error
// without breaking its signature, so the refusal surfaces on the first call — still
// before any byte leaves the process.
func (c *HTTPConnection) newRequest(ctx context.Context, method, fullURL string, body io.Reader) (*http.Request, error) {
	if tenantError := validateTenantIDHeader(c.TenantID); tenantError != nil {
		return nil, tenantError
	}
	return http.NewRequestWithContext(ctx, method, fullURL, body)
}

// setHeaders applies auth and JSON content-type headers to every request.
//
// X-API-Key is the primary auth mechanism. X-Tenant-ID is only set
// when explicitly configured (multi-tenant deployments).
func (c *HTTPConnection) setHeaders(req *http.Request) {
	c.setAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
}

// Get sends a GET request to the given path with optional query parameters.
func (c *HTTPConnection) Get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	fullURL := c.BaseURL + path
	if len(params) > 0 {
		fullURL += "?" + params.Encode()
	}

	req, err := c.newRequest(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, wrapRequestError("creating GET request", err)
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, newTransportError(req.Method+" "+path, err)
	}

	return c.handleResponse(resp, path)
}

// Post sends a POST request with a JSON-encoded body.
func (c *HTTPConnection) Post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return c.post(ctx, path, body)
}

// PostMultipart sends a multipart/form-data POST (file upload).
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

	req, reqErr := c.newRequest(ctx, http.MethodPost, c.BaseURL+path, &bodyBuf)
	if reqErr != nil {
		return nil, wrapRequestError("creating multipart POST", reqErr)
	}
	c.setAuthHeaders(req)
	req.Header.Set("Content-Type", formWriter.FormDataContentType())

	resp, doErr := c.HTTPClient.Do(req)
	if doErr != nil {
		return nil, newTransportError(req.Method+" "+path, doErr)
	}
	return c.handleResponse(resp, path)
}

// PostRead sends a POST request for a READ-shaped endpoint (e.g.
// /api/v1/search, /api/v1/walk).
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

	req, err := c.newRequest(ctx, http.MethodPost, c.BaseURL+path, reader)
	if err != nil {
		return nil, wrapRequestError("creating POST request", err)
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, newTransportError(req.Method+" "+path, err)
	}

	return c.handleResponse(resp, path)
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

	req, err := c.newRequest(ctx, http.MethodPatch, c.BaseURL+path, reader)
	if err != nil {
		return nil, wrapRequestError("creating PATCH request", err)
	}
	// PATCH is a write.
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, newTransportError(req.Method+" "+path, err)
	}

	return c.handleResponse(resp, path)
}

// Delete sends a DELETE request to the given path and discards the body.
func (c *HTTPConnection) Delete(ctx context.Context, path string) error {
	_, deleteErr := c.DeleteWithParams(ctx, path, nil)
	return deleteErr
}

// DeleteWithParams sends a DELETE request with optional query parameters and
// returns the response body.
//
// Junior Tip [por que um DELETE que devolve corpo]: DELETE /api/v1/records/{id}
// responde vazio, mas DELETE /api/v1/records/by-file responde a CONTAGEM do que
// foi apagado — e essa contagem é a resposta do usuário ("apaguei 511"), não um
// detalhe. Descartar o corpo aqui transformaria "apaguei 0" num sucesso mudo,
// que é exatamente o modo de falha que este endpoint existe para evitar.
//
// Junior Tip [por que query string e não corpo]: net/http aceita corpo em
// DELETE, mas proxies e o fetch do navegador o descartam. Os parâmetros deste
// verbo viajam na URL para que os três SDKs falem o mesmo dialeto do servidor.
func (c *HTTPConnection) DeleteWithParams(ctx context.Context, path string, params url.Values) ([]byte, error) {
	fullURL := c.BaseURL + path
	if len(params) > 0 {
		fullURL += "?" + params.Encode()
	}

	req, requestErr := c.newRequest(ctx, http.MethodDelete, fullURL, nil)
	if requestErr != nil {
		return nil, wrapRequestError("creating DELETE request", requestErr)
	}
	// DELETE is a write.
	c.setHeaders(req)

	resp, doErr := c.HTTPClient.Do(req)
	if doErr != nil {
		return nil, newTransportError(req.Method+" "+path, doErr)
	}

	return c.handleResponse(resp, path)
}
