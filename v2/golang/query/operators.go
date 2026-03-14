package query

type QueryOperator string

const (
	Eq   QueryOperator = "$eq"
	Neq  QueryOperator = "$neq"
	Gt   QueryOperator = "$gt"
	Gte  QueryOperator = "$gte"
	Lt   QueryOperator = "$lt"
	Lte  QueryOperator = "$lte"
	In   QueryOperator = "$in"
	Nin  QueryOperator = "$nin"
	Like QueryOperator = "$like"

	// Old aliases for backwards compatibility
	OpEq   = Eq
	OpNeq  = Neq
	OpGt   = Gt
	OpGte  = Gte
	OpLt   = Lt
	OpLte  = Lte
	OpIn   = In
	OpNin  = Nin
	OpLike = Like
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
