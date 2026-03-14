package query

import (
	"context"
	"encoding/json"

	"github.com/yoven/anhurdb-sdk/v2/golang/v2/client"
	"github.com/yoven/anhurdb-sdk/v2/golang/v2/models"
)

// Executor struct binds the HTTP client to the AST execution.
type GoExecutor struct {
	conn *client.HTTPConnection
	ctx  context.Context // Injected per execution
}

// NewExecutor creates a new executing engine using the existing connection.
func NewExecutor(conn *client.HTTPConnection, ctx context.Context) *GoExecutor {
	return &GoExecutor{
		conn: conn,
		ctx:  ctx,
	}
}

// ExecuteQuery marshals the AST and sends it via HTTP.
func (e *GoExecutor) ExecuteQuery(ast QueryAST) (interface{}, error) {
	respBytes, err := e.conn.Post(e.ctx, "/api/v1/query", ast)
	if err != nil {
		return nil, err
	}

	// Assuming the API returns a {"records": [...]} wrapper
	var result struct {
		Records []models.Record `json:"records"`
	}

	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, err
	}

	return result.Records, nil
}
