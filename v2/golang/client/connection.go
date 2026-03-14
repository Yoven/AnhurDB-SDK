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

// HTTPConnection manages network requests to AnhurDB.
//
// [V2 ARCHITECTURE NOTE]:
// This connection struct acts as an MCP Tunnel Gateway.
// It intercepts REST-like endpoints and automatically translates them into 
// explicit MCP Tool payload invocations (`create_memory`, `execute_ast`)
// hitting the `/api/v1/mcp/direct` endpoint. It does not communicate securely
// with the internal Raft nodes.
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

// Post sends a JSON body conceptually to the given AnhurDB endpoint, but translates it into an MCP Tool Payload.
func (c *HTTPConnection) Post(ctx context.Context, endpoint string, body interface{}) ([]byte, error) {
	// Roteamento Translúcido: Converter endpoints REST em Chamadas de Tools MCP
	var toolName string
	args := make(map[string]interface{})
	
	// Merge the original payload dynamically into the arguments dict
	bodyBytes, _ := json.Marshal(body)
	json.Unmarshal(bodyBytes, &args)
	
	args["api_key"] = c.APIKey

	if endpoint == "/v2/records" {
		toolName = "create_memory"
	} else if endpoint == "/v2/search/ast" {
		toolName = "execute_ast"
	} else {
		return nil, ErrInvalidQuery
	}

	payload := map[string]interface{}{
		"tool": toolName,
		"args": args,
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/v1/mcp/direct", bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
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

	// Unwrap the MCP Tool Result payload (Standard format: {"content": [{"text": "{...JSON...}"}]})
	var mcpRes map[string]interface{}
	if err := json.Unmarshal(respBody, &mcpRes); err != nil {
		return respBody, nil // if it fails parsing, return raw text
	}

	if isErr, ok := mcpRes["isError"].(bool); ok && isErr {
		return nil, ErrInvalidQuery
	}

	if content, ok := mcpRes["content"].([]interface{}); ok && len(content) > 0 {
		if firstElement, ok := content[0].(map[string]interface{}); ok {
			if textVal, ok := firstElement["text"].(string); ok {
				return []byte(textVal), nil
			}
		}
	}

	return []byte("{}"), nil
}
