package query

// QueryAST represents the final JSON payload sent to the AnhurDB /api/v1/query endpoint.
type QueryAST struct {
	Select     []string               `json:"select,omitempty"`
	Filters    map[string]interface{} `json:"filters"`
	Sort       []SortBlock            `json:"sort,omitempty"`
	Pagination PaginationBlock        `json:"pagination"`
}

type SortBlock struct {
	Field string        `json:"field"`
	Order SortDirection `json:"order"`
}

type PaginationBlock struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type SemanticSearchBlock struct {
	Query string       `json:"query"`
	Mode  SemanticMode `json:"mode"`
}
