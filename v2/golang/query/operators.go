package query

type QueryOperator string

const (
	OpEq   QueryOperator = "$eq"
	OpNeq  QueryOperator = "$neq"
	OpGt   QueryOperator = "$gt"
	OpGte  QueryOperator = "$gte"
	OpLt   QueryOperator = "$lt"
	OpLte  QueryOperator = "$lte"
	OpIn   QueryOperator = "$in"
	OpNin  QueryOperator = "$nin"
	OpLike QueryOperator = "$like"
)

type SemanticMode string

const (
	ModeText   SemanticMode = "$text"
	ModeHybrid SemanticMode = "$hybrid"
)

type SortDirection string

const (
	Asc  SortDirection = "asc"
	Desc SortDirection = "desc"
)
