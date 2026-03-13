package query

import (
	"errors"
	"fmt"
)

// Executor defines the interface required to execute a built QueryAST.
type Executor interface {
	ExecuteQuery(ast QueryAST) (interface{}, error)
}

// Builder provides a fluent interface for constructing AnhurDB queries.
type Builder struct {
	executor Executor
	ast      QueryAST
}

// NewBuilder creates a new fluent query builder.
func NewBuilder(executor Executor) *Builder {
	return &Builder{
		executor: executor,
		ast: QueryAST{
			Filters: make(map[string]interface{}),
			Pagination: PaginationBlock{
				Limit:  50,
				Offset: 0,
			},
		},
	}
}

// Select specifies which fields to return.
func (b *Builder) Select(fields ...string) *Builder {
	b.ast.Select = append(b.ast.Select, fields...)
	return b
}

// Where adds a filter condition.
func (b *Builder) Where(field string, op QueryOperator, value interface{}) *Builder {
	// If it's an exact match ($eq) and there's no operator map yet
	if op == OpEq {
		b.ast.Filters[field] = map[string]interface{}{string(OpEq): value}
		return b
	}

	// For other operators, ensure map exists
	if _, exists := b.ast.Filters[field]; !exists {
		b.ast.Filters[field] = make(map[string]interface{})
	}

	// Type assertion to ensure it's a map before assigning
	if filterMap, ok := b.ast.Filters[field].(map[string]interface{}); ok {
		filterMap[string(op)] = value
	}
	return b
}

// WhereEq is a syntax sugar for Where(field, OpEq, value).
func (b *Builder) WhereEq(field string, value interface{}) *Builder {
	return b.Where(field, OpEq, value)
}

// WhereGt is a syntax sugar for Where(field, OpGt, value).
func (b *Builder) WhereGt(field string, value interface{}) *Builder {
	return b.Where(field, OpGt, value)
}

// WhereIn is a syntax sugar for Where(field, OpIn, value).
func (b *Builder) WhereIn(field string, values []interface{}) *Builder {
	return b.Where(field, OpIn, values)
}

// SemanticSearch adds a semantic analysis block to the filters.
func (b *Builder) SemanticSearch(query string, mode SemanticMode) *Builder {
	b.ast.Filters["semantic_search"] = SemanticSearchBlock{
		Query: query,
		Mode:  mode,
	}
	return b
}

// OrderBy appends a sorting rule.
func (b *Builder) OrderBy(field string, direction SortDirection) *Builder {
	b.ast.Sort = append(b.ast.Sort, SortBlock{
		Field: field,
		Order: direction,
	})
	return b
}

// Limit sets the maximum number of results.
func (b *Builder) Limit(limit int) *Builder {
	b.ast.Pagination.Limit = limit
	return b
}

// Offset sets the pagination offset.
func (b *Builder) Offset(offset int) *Builder {
	b.ast.Pagination.Offset = offset
	return b
}

// Execute validates and runs the query via the Executor.
func (b *Builder) Execute() (interface{}, error) {
	if b.executor == nil {
		return nil, errors.New("cannot execute: no executor provided to Builder")
	}
	if b.ast.Pagination.Limit < 1 || b.ast.Pagination.Limit > 500 {
		return nil, fmt.Errorf("limit must be between 1 and 500, got %d", b.ast.Pagination.Limit)
	}

	return b.executor.ExecuteQuery(b.ast)
}
