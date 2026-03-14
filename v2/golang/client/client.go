package client

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/yoven/anhurdb-sdk/v2/golang/v2/models"
	"github.com/yoven/anhurdb-sdk/v2/golang/v2/query"
)

// Client is the main high-level API entrypoint for AnhurDB.
type Client struct {
	conn *HTTPConnection
}

// NewClient initializes a new AnhurDB Go SDK client using only API Key auth.
func NewClient(url, apiKey string) *Client {
	return &Client{
		conn: NewConnection(url, apiKey),
	}
}

// Connect verifies the connection and authentication.
func (c *Client) Connect(ctx context.Context) error {
	_, err := c.conn.Post(ctx, "/v2/ping", nil) // Verify proper ping endpoint exists on server? Or just a lightweight auth check
	return err
}

// Create stores a new cognitive or episodic record.
func (c *Client) Create(ctx context.Context, req models.CreateRequest) error {
	_, err := c.conn.Post(ctx, "/v2/records", req)
	return err
}

// SearchResponse represents the struct containing an array of wrapped records.
type SearchResponse struct {
	Records []models.Record `json:"records"`
}

// SearchWithAST uses the AST builder to search inside a specific dataset/session.
func (c *Client) SearchWithAST(ctx context.Context, sessionUUID string, filter *query.Builder) (*SearchResponse, error) {
	// Extract AST
	ast := filter.AST()
	
	// Create payload
	payload := map[string]interface{}{
		"session_uuid": sessionUUID,
		"query":        ast,
	}

	respBody, err := c.conn.Post(ctx, "/v2/search/ast", payload)
	if err != nil {
		return nil, err
	}

	var res SearchResponse
	if err := json.Unmarshal(respBody, &res); err != nil {
		return nil, errors.New("failed to parse search response")
	}

	return &res, nil
}
