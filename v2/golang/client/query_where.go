package client

// query_where.go — the Where clause of the fluent AST builder: how one column
// accumulates its operator object across multiple Where calls.
//
// Domain: per-column predicate accumulation for POST /api/v1/query. Moved out
// of types.go on 2026-09-14: types.go is already past the ~300-line house cut,
// growing such a file is forbidden, and Where had to grow to close B3 below —
// so the part being touched moved out first.
//
// Junior Tip [B3 — why Where MERGES instead of replacing, 2026-09-14]: until
// this date, `Where("score", QueryOp{Gte: 3}).Where("score", QueryOp{Lte: 5})`
// put ONLY `{"score":{"$lte":5}}` on the wire — the second call REPLACED the
// column's operator object, the $gte predicate vanished, the server answered
// HTTP 200, and the caller got 5 rows where 3 were correct. Measured live
// against production on 2026-09-14 by the three-SDK differential harness (one
// fixture, three SDKs, one pass): TypeScript and Python returned 3 rows, Go
// returned 5, with no error anywhere. The server grammar explicitly supports
// two operators on one column ANDed together ({"score":{"$gte":3,"$lte":5}} is
// a legal range), and both other SDKs accumulate per column (typescript
// query.ts where(); python builder.py where()). Go now merges too.

// Where adds the operator object for a column, MERGING with whatever a
// previous Where already set on the same column:
//
//	client.NewQuery().
//	    Where("score", client.QueryOp{Gte: 3}).
//	    Where("score", client.QueryOp{Lte: 5})
//
// produces the range `{"score":{"$gte":3,"$lte":5}}` — the same AST that
// chaining `.where("score","$gte",3).where("score","$lte",5)` builds in
// TypeScript and `where(score__gte=3, score__lte=5)` builds in Python.
//
// Setting the SAME operator twice on one column (two $eq, two $gte, …) is
// refused loudly: the wire format has exactly one slot per operator (filters
// are a JSON object), so honouring both values is impossible and keeping
// either one silently is the defect class the merge exists to kill. The
// refusal is a typed validation error (Kind "invalid_request", Retryable
// false) that surfaces at Validate/Build/Query time, like every other builder
// error. TypeScript and Python currently overwrite last-wins in that case —
// see the dated 2026-09-14 entry in PARITY_SPEC.md, which records the refusal
// as the intended cross-SDK behaviour.
//
// Returns the receiver so calls chain.
func (request *QueryRequest) Where(field string, operator QueryOp) *QueryRequest {
	if request.Filters == nil {
		request.Filters = map[string]QueryOp{}
	}
	// Junior Tip [checagem aqui E em Validate(), de proposito]: aqui ela aponta
	// a LINHA da cadeia que errou, o que e o que o desenvolvedor precisa; em
	// Validate() ela cobre quem monta o QueryRequest como struct literal, sem
	// passar por Where(). Python e TypeScript tem as duas pelo mesmo motivo.
	if !astAllowedFilterColumns[field] {
		request.buildErrors = append(request.buildErrors,
			newValidationError("query: field %q is not allowed in filters — allowed: %s", field, sortedAllowedColumns()))
	}
	existingOperator, columnAlreadyFiltered := request.Filters[field]
	if !columnAlreadyFiltered {
		request.Filters[field] = operator
		return request
	}
	mergedOperator, mergeErr := mergeQueryOps(field, existingOperator, operator)
	if mergeErr != nil {
		request.buildErrors = append(request.buildErrors, mergeErr)
		return request
	}
	request.Filters[field] = mergedOperator
	return request
}

// mergeQueryOps combines two operator objects aimed at the same column into
// the single object the wire format allows. Every operator slot may be filled
// by at most ONE of the two sides; a slot filled by both is refused.
//
// Junior Tip [why refusing beats picking a winner]: with one JSON key per
// operator, a duplicate means one of the caller's two values cannot reach the
// server. Whichever one we kept, the discarded predicate would change the
// result set with HTTP 200 and no error — a silent wrong answer, exactly what
// B3 was. An error at build time costs the caller one obvious fix; a silently
// dropped predicate costs whoever reads the wrong rows.
func mergeQueryOps(field string, existingOperator QueryOp, incomingOperator QueryOp) (QueryOp, error) {
	mergedOperator := existingOperator
	if incomingOperator.Eq != nil {
		if mergedOperator.Eq != nil {
			return QueryOp{}, duplicateOperatorError(field, "$eq")
		}
		mergedOperator.Eq = incomingOperator.Eq
	}
	if incomingOperator.Gt != nil {
		if mergedOperator.Gt != nil {
			return QueryOp{}, duplicateOperatorError(field, "$gt")
		}
		mergedOperator.Gt = incomingOperator.Gt
	}
	if incomingOperator.Gte != nil {
		if mergedOperator.Gte != nil {
			return QueryOp{}, duplicateOperatorError(field, "$gte")
		}
		mergedOperator.Gte = incomingOperator.Gte
	}
	if incomingOperator.Lt != nil {
		if mergedOperator.Lt != nil {
			return QueryOp{}, duplicateOperatorError(field, "$lt")
		}
		mergedOperator.Lt = incomingOperator.Lt
	}
	if incomingOperator.Lte != nil {
		if mergedOperator.Lte != nil {
			return QueryOp{}, duplicateOperatorError(field, "$lte")
		}
		mergedOperator.Lte = incomingOperator.Lte
	}
	if incomingOperator.In != nil {
		if mergedOperator.In != nil {
			return QueryOp{}, duplicateOperatorError(field, "$in")
		}
		mergedOperator.In = incomingOperator.In
	}
	return mergedOperator, nil
}

// duplicateOperatorError is the one wording for a refused duplicate operator,
// so every slot in mergeQueryOps reports it identically.
func duplicateOperatorError(field string, wireOperator string) *APIError {
	return newValidationError(
		"query: filter %q: %s is already set for this column — the wire format has one slot per operator, "+
			"so a second %s would have to silently overwrite the first, and a silently dropped predicate "+
			"returns wrong rows with HTTP 200; combine both constraints into one Where call, or build a new query",
		field, wireOperator, wireOperator)
}
