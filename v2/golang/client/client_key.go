package client

// client_key.go — ClientAPI, the handle for a CLIENT key (kind=client) on the
// /api/v1/client/* surface (ADR-0034): its construction, the one round trip
// every method shares, and the local refusals applied before any request.
// Methods live in client_key_children.go, client_key_tenants.go and
// client_key_impersonate.go; shapes in client_key_types.go; errors in
// client_key_errors.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ClientAPI talks to the client-key surface. It is safe for concurrent use
// (the underlying http.Client is goroutine-safe and ClientAPI holds no
// mutable state).
type ClientAPI struct {
	conn *HTTPConnection
}

// NewClientAPI builds a ClientAPI for clientKey. It accepts the same options
// as NewMemory; WithURL, WithTimeout and WithHTTPClient apply, while
// WithUserID and WithTenantID are ignored.
//
// Junior Tip [never X-Tenant-ID with a client key]: a client key is not bound
// to a tenant — the tenant travels in the BODY of each call. Sending
// X-Tenant-ID alongside it would invite a future server change to read the
// header and act on a tenant the body did not name. TenantID is therefore
// forced empty here, whatever the options say.
func NewClientAPI(clientKey string, opts ...Option) *ClientAPI {
	cfg := &memoryConfig{url: DefaultCloudURL}
	for _, opt := range opts {
		opt(cfg)
	}
	conn := newConnectionFromConfig(clientKey, cfg)
	conn.TenantID = ""
	return &ClientAPI{conn: conn}
}

// send performs ONE request and returns the 2xx body, or *ClientAPIError for
// any other status, or *TransportError when no answer arrived. There is no
// retry: a 503 not_leader is reported (IsNotLeader) and the caller decides.
func (api *ClientAPI) send(ctx context.Context, method, path string, requestBody interface{}) ([]byte, error) {
	if api == nil || api.conn == nil || api.conn.APIKey == "" {
		return nil, ErrEmptyAPIKey
	}
	var bodyReader io.Reader
	if requestBody != nil {
		encodedBody, marshalError := json.Marshal(requestBody)
		if marshalError != nil {
			return nil, fmt.Errorf("marshalling request body: %w", marshalError)
		}
		bodyReader = bytes.NewReader(encodedBody)
	}
	httpRequest, requestError := api.conn.newRequest(ctx, method, api.conn.BaseURL+path, bodyReader)
	if requestError != nil {
		return nil, wrapRequestError("creating "+method+" request", requestError)
	}
	api.conn.setHeaders(httpRequest)

	httpResponse, doError := api.conn.HTTPClient.Do(httpRequest)
	if doError != nil {
		return nil, newTransportError(method+" "+path, doError)
	}
	defer httpResponse.Body.Close()

	responseBody, readError := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseSize+1))
	if readError != nil {
		return nil, fmt.Errorf("reading response body: %w", readError)
	}
	if int64(len(responseBody)) > maxResponseSize {
		return nil, fmt.Errorf("response exceeds maximum size (%d MB)", maxResponseSize/(1024*1024))
	}
	if httpResponse.StatusCode >= 200 && httpResponse.StatusCode < 300 {
		return responseBody, nil
	}
	return nil, clientAPIErrorFromBody(httpResponse.StatusCode, method, path, responseBody)
}

// clientAPIErrorFromBody decodes {"error","detail","error_type"} when the body
// is a JSON object; otherwise it keeps a bounded excerpt of the raw body.
// ClientKeyOnly answers with Content-Type text/plain but a JSON body, so the
// body is parsed regardless of the declared type.
func clientAPIErrorFromBody(status int, method, path string, responseBody []byte) *ClientAPIError {
	apiError := &ClientAPIError{Status: status, Method: method, Path: path}
	// Same decoder as the data-route 401/403 (auth_refused.go), so the two
	// surfaces read one envelope one way.
	if envelope, decoded := decodeErrorEnvelope(responseBody); decoded {
		apiError.Code = envelope.code
		apiError.Detail = envelope.detail
		apiError.ErrorType = envelope.errorType
		return apiError
	}
	apiError.Body = excerpt(strings.TrimSpace(string(responseBody)))
	return apiError
}

// decodeObject unmarshals a 2xx body into target, or reports an untrusted
// response when it is not a JSON object.
func decodeObject(method, path string, responseBody []byte, target interface{}) error {
	trimmedBody := bytes.TrimSpace(responseBody)
	if len(trimmedBody) == 0 || trimmedBody[0] != '{' || json.Unmarshal(trimmedBody, target) != nil {
		return unexpectedClientResponse(method, path, "response is not a JSON object")
	}
	return nil
}

// unexpectedClientResponse builds the unexpected_response refusal.
func unexpectedClientResponse(method, path, reason string) *ClientAPIResponseError {
	return &ClientAPIResponseError{Method: method, Path: path, Code: ResponseCodeUnexpected, Reason: reason}
}

// missingField is the reason used when a required response field is absent.
func missingField(fieldName string) string {
	return fmt.Sprintf("\"%s\" is missing or invalid", fieldName)
}

// ── Local refusals (400, nothing is sent) ────────────────────────────────────
//
// Junior Tip [why refuse locally what the server also refuses]: these are the
// inputs that can never succeed. Refusing them before the request keeps them
// out of the server's audit log and, for the child id, out of the URL path —
// a non-canonical id spliced into a path could address another route.

// requireCanonicalChildID mirrors isCanonicalChildID in
// server/handler/client_children.go: 36 characters, 8-4-4-4-12 hex groups,
// either case (the server then looks it up as given).
func requireCanonicalChildID(childID string) error {
	if !isCanonicalUUID(childID) {
		return newValidationError("INVALID_PARAM: client API: child_id must be a canonical UUID (got \"%s\")", childID)
	}
	return nil
}

// isCanonicalUUID reports the 36-character hyphenated hex form.
func isCanonicalUUID(candidate string) bool {
	if len(candidate) != 36 {
		return false
	}
	for position := 0; position < len(candidate); position++ {
		character := candidate[position]
		if position == 8 || position == 13 || position == 18 || position == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		isHexDigit := (character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')
		if !isHexDigit {
			return false
		}
	}
	return true
}

// requireTenantID refuses an empty (after trim) or wildcard tenant id.
func requireTenantID(tenantID string) error {
	trimmedTenantID := strings.TrimSpace(tenantID)
	if trimmedTenantID == "" {
		return newValidationError("INVALID_PARAM: client API: tenant_id is required")
	}
	if trimmedTenantID == "*" {
		return newValidationError("INVALID_PARAM: client API: tenant_id must not be \"*\"")
	}
	return nil
}

// requireScope accepts "" (only when allowEmpty) and the three known scopes.
func requireScope(scope KeyScope, allowEmpty bool) error {
	if scope == "" {
		if allowEmpty {
			return nil
		}
		return newValidationError("INVALID_PARAM: client API: scope is required on the child impersonate route")
	}
	if scopeRank(scope) == 0 {
		return newValidationError("INVALID_PARAM: client API: scope must be one of admin, write, read_only (got \"%s\")", string(scope))
	}
	return nil
}
