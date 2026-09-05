package client

// query_builder.go — the fluent AST builder for POST /api/v1/query.
//
// Domain: turning a chain of calls into the QueryRequest the server expects,
// and refusing at the client what the server would refuse at 400.
//
// Junior Tip [why this file exists at all, 2026-09-05]: types.go:873 carried the
// note "Kept for forward-compat / parity with Python's QueryBuilder" for months
// while the Go builder was MISSING three of the five methods the other two SDKs
// expose — Select, WhereEquals and Build/Execute. A parity note that names a
// gap is not parity; it is a TODO wearing a doc comment. The methods below close
// it, and they land in their own file because types.go was already 1042 lines,
// past the ~300-line house cut, and house law forbids growing such a file.
//
// The builder deliberately does NOT return an error per call: it accumulates
// into QueryRequest.buildErrors so the chain stays fluent, and Validate() (or
// Memory.Query, which calls it) surfaces the FIRST error. That is the same
// contract the pre-existing Where/OrderBy/Limit/Offset already used — this file
// extends it, it does not invent a second one.

import (
	"context"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/models"
)

// SelectFields restricts which columns the query asks for.
//
// Junior Tip [why not simply "Select", 2026-09-05]: QueryRequest already has an
// exported FIELD named Select (the AST key the server reads), and Go forbids a
// method and a field sharing a name on the same type. Renaming the field to free
// the name would break every caller that builds a QueryRequest literal. So the
// method carries the longer name; the wire key and the struct field are
// untouched, and Python's select()/TypeScript's select() map here.
//
// Junior Tip [it is accepted and then ignored — on purpose]: the server parses
// `select` but never projects columns; the FULL record always comes back. The
// method exists so a query written against the Python or TypeScript builder
// transliterates to Go without a caller wondering which SDK is wrong. Do not
// "optimise" a payload by relying on it — nothing shrinks.
//
// Repeated calls append; duplicates are dropped at Build time.
func (request *QueryRequest) SelectFields(fields ...string) *QueryRequest {
	request.Select = append(request.Select, fields...)
	return request
}

// WhereEquals is the shorthand for an exact-match ($eq) filter, mirroring
// TypeScript's whereEquals and Python's where(field=value).
//
// It routes through Where, so the column whitelist is enforced exactly once —
// a second copy of that check here is a second thing to forget to update.
func (request *QueryRequest) WhereEquals(field string, value interface{}) *QueryRequest {
	return request.Where(field, QueryOp{Eq: value})
}

// Build validates the accumulated state and returns the request ready to send.
//
// Junior Tip [why Build returns an error and the chain does not]: the chain has
// to stay fluent to read like the other two SDKs, so every mutator swallows its
// complaint into buildErrors. Build is the one place that can honestly answer
// "is this query legal?" before a byte leaves the process — call it, or call
// Memory.Query, which calls Validate for you. What you must NOT do is build a
// chain, ignore the return of both, and wonder later why the server said 400.
func (request *QueryRequest) Build() (*QueryRequest, error) {
	if validationErr := request.Validate(); validationErr != nil {
		return nil, validationErr
	}
	request.Select = dedupeSelectFields(request.Select)
	return request, nil
}

// Execute builds the query and runs it against a Memory, mirroring the
// TypeScript builder's execute(memory) and Python's await builder.execute().
//
// The builder stays ignorant of HTTP by delegating to Memory.Query — it never
// learns a URL, a header or a status code.
func (request *QueryRequest) Execute(ctx context.Context, memory *Memory) ([]models.Record, error) {
	built, buildErr := request.Build()
	if buildErr != nil {
		return nil, buildErr
	}
	if memory == nil {
		return nil, newValidationError("query: Execute requires a non-nil *Memory")
	}
	return memory.Query(ctx, built)
}

// dedupeSelectFields removes repeats while preserving the caller's first-seen
// order, matching the TypeScript builder's `[...new Set(selectFields)]`.
func dedupeSelectFields(fields []string) []string {
	if len(fields) == 0 {
		return fields
	}
	deduplicated := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if _, already := seen[field]; already {
			continue
		}
		seen[field] = struct{}{}
		deduplicated = append(deduplicated, field)
	}
	return deduplicated
}
