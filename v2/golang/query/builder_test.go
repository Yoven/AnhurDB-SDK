package query_test

import (
	"testing"
	"github.com/yoven/anhurdb-sdk/v2/golang/v2/query"
)

// MockExecutor for testing purely the AST builder
type MockExecutor struct {
	LastAST query.QueryAST
}

func (m *MockExecutor) ExecuteQuery(ast query.QueryAST) (interface{}, error) {
	m.LastAST = ast
	return nil, nil
}

func TestQueryBuilder(t *testing.T) {
	mockExec := &MockExecutor{}
	builder := query.NewBuilder(mockExec)

	builder.
		Select("id", "summary").
		WhereEq("type", "risk").
		WhereGt("weight", 0.8).
		OrderBy("weight", query.Desc).
		Limit(10)

	_, err := builder.Execute()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	ast := mockExec.LastAST

	if ast.Pagination.Limit != 10 {
		t.Errorf("Expected limit 10, got %d", ast.Pagination.Limit)
	}

	if len(ast.Select) != 2 || ast.Select[0] != "id" {
		t.Errorf("Expected select [id, summary], got %v", ast.Select)
	}

	if val, ok := ast.Filters["type"].(map[string]interface{}); ok {
		if val["$eq"] != "risk" {
			t.Errorf("Expected type $eq risk, got %v", val["$eq"])
		}
	} else {
		t.Error("Type filter not set correctly")
	}
}
