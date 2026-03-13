package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPConnection manages network requests to AnhurDB, injecting API keys safely.
type HTTPConnection struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewConnection initializes a secure connection to the AnhurDB cluster.
func NewConnection(url, apiKey string) *HTTPConnection {
	return &HTTPConnection{
		BaseURL: strings.TrimRight(url, "/"),
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Post sends a JSON body to the given AnhurDB endpoint.
func (c *HTTPConnection) Post(ctx context.Context, endpoint string, body interface{}) ([]byte, error) {
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+endpoint, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("User-Agent", "AnhurSDK-Golang-V2/1.0")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, ErrConnectionFail
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusBadRequest {
		return nil, ErrInvalidQuery
	}
	if resp.StatusCode >= 500 {
		return nil, ErrServerError
	}

	return respBody, nil
}
