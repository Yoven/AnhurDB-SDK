package client

// query_request.go — the POST /api/v1/query AST: QueryRequest and QueryOp, the
// fluent NewQuery/OrderBy/Limit/Offset builders, and the response envelope.
// Where lives in query_where.go, validation in query_validation.go and the
// remaining builder helpers in query_builder.go.

import (
	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/models"
)

// QueryRequest is the structured AST sent to POST /api/v1/query. It mirrors the
// filter/sort/pagination grammar over the record columns.
//
// Build it directly, or fluently via NewQuery().Where(...).OrderBy(...).Limit(...).
type QueryRequest struct {
	// Select lists columns to return. The server parses this field but does not
	// project columns; the full record is always returned.
	// the server — the SELECT list is fixed and the FULL record always returns.
	// Kept for forward-compat / parity with Python's QueryBuilder.
	Select []string `json:"select,omitempty"`
	// Filters maps a whitelisted column name to an operator object. An invalid
	// column name yields HTTP 400 "invalid filter field" from the server.
	Filters map[string]QueryOp `json:"filters,omitempty"`
	// Sort is an ordered list of {field, order} maps. order is "asc"/"desc"
	// (anything else falls back to DESC server-side); an invalid field yields
	// HTTP 400 "invalid sort field". Default when omitted: ORDER BY id DESC.
	Sort []map[string]string `json:"sort,omitempty"`
	// Pagination carries {"limit":int,"offset":int}. limit defaults to 50 and is
	// hard-capped at 1000 server-side; offset defaults to 0.
	//
	// Junior Tip [o doc dizia "must be >= 0" e o servidor nao recusava]: um
	// offset negativo era SILENCIOSAMENTE trocado por 0 (record_ast_query.go),
	// entao "must be" descrevia uma regra que ninguem aplicava. Agora Validate()
	// a aplica no cliente, igual a Python e TypeScript.
	Pagination map[string]int `json:"pagination,omitempty"`

	// buildErrors coleta problemas detectados pelos metodos fluentes, que nao
	// podem devolver erro sem quebrar o encadeamento. Validate() os entrega.
	buildErrors []error
}

// QueryOp is a per-column operator object for QueryRequest.Filters. Each field
// maps to one of the server-supported operators; set only the ones you need —
// the omitempty tags ensure unset operators never reach the wire.
type QueryOp struct {
	Eq  interface{}   `json:"$eq,omitempty"`
	Gt  interface{}   `json:"$gt,omitempty"`
	Gte interface{}   `json:"$gte,omitempty"`
	Lt  interface{}   `json:"$lt,omitempty"`
	Lte interface{}   `json:"$lte,omitempty"`
	In  []interface{} `json:"$in,omitempty"`
}

// NewQuery starts a fluent QueryRequest builder. The zero builder is a valid
// "match everything (server defaults)" query.
//
//	req := client.NewQuery().
//	    Where("type", client.QueryOp{Eq: "fact"}).
//	    OrderBy("created_at", "desc").
//	    Limit(20)
func NewQuery() *QueryRequest {
	return &QueryRequest{}
}

// Where lives in query_where.go — it MERGES operator objects per column and
// the merge logic needed room this file (past the ~300-line cut) cannot give.

// OrderBy appends a sort clause ({field, order}). order should be "asc" or
// "desc"; an unrecognised value falls back to DESC server-side. Returns the
// receiver so calls chain.
func (request *QueryRequest) OrderBy(field, order string) *QueryRequest {
	request.Sort = append(request.Sort, map[string]string{"field": field, "order": order})
	return request
}

// Limit sets the pagination limit (server default 50, hard cap 1000). Returns
// the receiver so calls chain.
func (request *QueryRequest) Limit(limit int) *QueryRequest {
	if request.Pagination == nil {
		request.Pagination = map[string]int{}
	}
	if limit < 1 || limit > astQueryLimitMax {
		request.buildErrors = append(request.buildErrors,
			newValidationError("query: limit must be between 1 and %d, got %d", astQueryLimitMax, limit))
	}
	request.Pagination["limit"] = limit
	return request
}

// Offset sets the pagination offset (server default 0, must be >= 0). Returns
// the receiver so calls chain.
func (request *QueryRequest) Offset(offset int) *QueryRequest {
	if request.Pagination == nil {
		request.Pagination = map[string]int{}
	}
	if offset < 0 {
		request.buildErrors = append(request.buildErrors,
			newValidationError("query: offset cannot be negative, got %d", offset))
	}
	request.Pagination["offset"] = offset
	return request
}

// queryResponse is the wire format for POST /api/v1/query and the
// {"records":[Record],"count":int} envelope shared by ListChat.
type queryResponse struct {
	Records []models.Record `json:"records"`
	Count   int             `json:"count"`
}
