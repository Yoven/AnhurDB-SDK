package anhurdb

import (
	"context"

	"github.com/yoven/anhurdb-sdk/v2/golang/v2/client"
	"github.com/yoven/anhurdb-sdk/v2/golang/v2/query"
)

// AnhurClient represents the entry point for AnhurSDK V2.
type AnhurClient struct {
	conn *client.HTTPConnection
}

// NewClient initializes the root client referencing a specific database endpoint and tenant key.
func NewClient(url, apiKey string) *AnhurClient {
	return &AnhurClient{
		conn: client.NewConnection(url, apiKey),
	}
}

// Memories provides access to the Fluent Query Builder for cognitive operations.
func (c *AnhurClient) Memories(ctx context.Context) *query.Builder {
	executor := query.NewExecutor(c.conn, ctx)
	return query.NewBuilder(executor)
}
