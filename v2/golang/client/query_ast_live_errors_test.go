//go:build astlive

package client

// query_ast_live_errors_test.go — every query the grammar forbids, and the
// question that actually matters: does THIS SDK refuse it before a byte leaves
// the process, or does it send it and hand back the server's 400?
//
// Domain: the rejection contract. Two different contracts hide behind the same
// word "error", and the three SDKs are free to disagree about which one a given
// mistake gets.
//
// Junior Tip [why "who rejected it" is recorded, not just "it errored",
// 2026-09-14]: a client-side refusal costs no round trip and can carry a
// message the server never wrote; a server-side refusal costs a request and
// carries the server's exact bytes. A caller that retries on Retryable(), or
// that matches on the message text, behaves differently in the two worlds. The
// `wantClientSide` column below is therefore an assertion about the CONTRACT,
// and `wireWasEmpty` is its proof: nothing reached the tap.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// astErrorCase is one forbidden query and the contract it is expected to meet.
type astErrorCase struct {
	name             string
	builderCall      string
	build            func(fixture *astFixture) *QueryRequest
	wantClientSide   bool
	wantErrSubstring string
	wantHTTPStatus   int       // meaningful only when the request reached the server
	wantKind         ErrorKind // meaningful only when the request reached the server
	note             string
}

// astErrorCases is the full forbidden-query matrix for the Go SDK.
var astErrorCases = []astErrorCase{
	{
		name:             "unknown filter column",
		builderCall:      `scoped.Where("bogus_col", QueryOp{Eq: 1})`,
		build:            func(fixture *astFixture) *QueryRequest { return scoped(fixture).Where("bogus_col", QueryOp{Eq: 1}) },
		wantClientSide:   true,
		wantErrSubstring: `query: field "bogus_col" is not allowed in filters`,
	},
	{
		name:             "filter column in the wrong case",
		builderCall:      `scoped.Where("TYPE", QueryOp{Eq: "fact"})`,
		build:            func(fixture *astFixture) *QueryRequest { return scoped(fixture).Where("TYPE", QueryOp{Eq: "fact"}) },
		wantClientSide:   true,
		wantErrSubstring: `query: field "TYPE" is not allowed in filters`,
	},
	{
		name:        "consolidate_id — a real column the grammar does not expose",
		builderCall: `scoped.Where("consolidate_id", QueryOp{Eq: 1})`,
		build: func(fixture *astFixture) *QueryRequest {
			return scoped(fixture).Where("consolidate_id", QueryOp{Eq: 1})
		},
		wantClientSide:   true,
		wantErrSubstring: `query: field "consolidate_id" is not allowed in filters`,
	},
	{
		name:        "semantic_search pseudo-key — the SERVER accepts and skips it, Go refuses it",
		builderCall: `scoped.Where("semantic_search", QueryOp{Eq: "anything"})`,
		build: func(fixture *astFixture) *QueryRequest {
			return scoped(fixture).Where("semantic_search", QueryOp{Eq: "x"})
		},
		wantClientSide:   true,
		wantErrSubstring: `query: field "semantic_search" is not allowed in filters`,
		note:             "DIVERGENCE: server returns 200 for this key (record_ast_query.go:169-172); Go cannot send it at all",
	},
	{
		name:             "operator object with no operator set",
		builderCall:      `scoped.Where("type", QueryOp{})`,
		build:            func(fixture *astFixture) *QueryRequest { return scoped(fixture).Where("type", QueryOp{}) },
		wantClientSide:   true,
		wantErrSubstring: `query: filter "type": no operator survived encoding`,
	},
	{
		name:        "$eq with an explicit nil — indistinguishable from unset in Go",
		builderCall: `scoped.Where("superseded_by", QueryOp{Eq: nil})`,
		build: func(fixture *astFixture) *QueryRequest {
			return scoped(fixture).Where("superseded_by", QueryOp{Eq: nil})
		},
		wantClientSide:   true,
		wantErrSubstring: `query: filter "superseded_by": no operator survived encoding`,
		note: "NOT A GAP (amended 2026-09-14): QueryOp.Eq is interface{} with omitempty so `$eq: null` is unreachable " +
			"from Go, and that is CORRECT. Live 2026-09-14: {\"superseded_by\":{\"$eq\":null}} answers 200 count=0 on a " +
			"tenant whose unfiltered page returns 1000 rows, every one of which satisfies superseded_by IS NULL — the " +
			"server compiles it to `col = NULL`, never true. Python/TS can send a dead predicate; Go cannot.",
	},
	{
		name:        "$in with an empty list",
		builderCall: `scoped.Where("type", QueryOp{In: []interface{}{}})`,
		build: func(fixture *astFixture) *QueryRequest {
			return scoped(fixture).Where("type", QueryOp{In: []interface{}{}})
		},
		wantClientSide:   true,
		wantErrSubstring: `query: filter "type": $in requires a non-empty list of values`,
	},
	{
		name:             "sort on a column outside the whitelist",
		builderCall:      `scoped.OrderBy("bogus", "asc")`,
		build:            func(fixture *astFixture) *QueryRequest { return scoped(fixture).OrderBy("bogus", "asc") },
		wantClientSide:   true,
		wantErrSubstring: `query: sort field "bogus" is not allowed`,
	},
	{
		name:             "sort with an unrecognised direction — the SERVER silently uses DESC",
		builderCall:      `scoped.OrderBy("id", "sideways")`,
		build:            func(fixture *astFixture) *QueryRequest { return scoped(fixture).OrderBy("id", "sideways") },
		wantClientSide:   true,
		wantErrSubstring: `query: sort order "sideways" is not allowed`,
		note:             "DIVERGENCE: server answers 200 and orders DESC (record_ast_query.go:240-242); Go refuses",
	},
	{
		name:             "limit above the server cap — the SERVER silently clamps to 1000",
		builderCall:      `scoped.Limit(1001)`,
		build:            func(fixture *astFixture) *QueryRequest { return scoped(fixture).Limit(1001) },
		wantClientSide:   true,
		wantErrSubstring: "query: limit must be between 1 and 1000, got 1001",
		note:             "DIVERGENCE: server clamps silently (astQueryMaxLimit); Go refuses, so the clamp is unreachable from Go",
	},
	{
		name:             "limit zero — the SERVER falls back to 50",
		builderCall:      `scoped.Limit(0)`,
		build:            func(fixture *astFixture) *QueryRequest { return scoped(fixture).Limit(0) },
		wantClientSide:   true,
		wantErrSubstring: "query: limit must be between 1 and 1000, got 0",
		note:             "DIVERGENCE: server applies the default 50; Go refuses",
	},
	{
		name:             "negative offset — the SERVER coerces to 0",
		builderCall:      `scoped.Offset(-1)`,
		build:            func(fixture *astFixture) *QueryRequest { return scoped(fixture).Offset(-1) },
		wantClientSide:   true,
		wantErrSubstring: "query: offset cannot be negative, got -1",
		note:             "DIVERGENCE: server coerces to 0 and answers 200; Go refuses",
	},
	{
		name:        "$eq with an OBJECT value — Go does NOT check scalarity, the server does",
		builderCall: `scoped.Where("type", QueryOp{Eq: map[string]interface{}{"a": 1}})`,
		build: func(fixture *astFixture) *QueryRequest {
			return scoped(fixture).Where("type", QueryOp{Eq: map[string]interface{}{"a": 1}})
		},
		wantClientSide:   false,
		wantErrSubstring: "operator $eq expects a single value",
		wantHTTPStatus:   400,
		wantKind:         KindInvalidRequest,
	},
	{
		name:        "$eq with an ARRAY value — also only caught server-side",
		builderCall: `scoped.Where("score", QueryOp{Eq: []interface{}{1, 2}})`,
		build: func(fixture *astFixture) *QueryRequest {
			return scoped(fixture).Where("score", QueryOp{Eq: []interface{}{1, 2}})
		},
		wantClientSide:   false,
		wantErrSubstring: "operator $eq expects a single value",
		wantHTTPStatus:   400,
		wantKind:         KindInvalidRequest,
	},
	{
		name:        "$in containing a nested array element",
		builderCall: `scoped.Where("type", QueryOp{In: []interface{}{[]string{"a"}}})`,
		build: func(fixture *astFixture) *QueryRequest {
			return scoped(fixture).Where("type", QueryOp{In: []interface{}{[]string{"a"}}})
		},
		wantClientSide:   true,
		wantErrSubstring: `query: filter "type": $in[0] is a []string — $in elements must be scalars`,
		note: "AMENDED 2026-09-14: this row expected the SERVER to reject it, and was already stale when written — the same commit added the client-side scalar guard that prevents the round trip. " +
			"The SERVER fact it recorded is still true: a nested array inside $in answers HTTP 400 \"expects a single value\". Go cannot observe that any more (Validate() runs on every path, " +
			"no raw-AST escape hatch); Python and TypeScript still prove it with a hand-written AST (their query_ast_live_null_contract suites).",
	},
	{
		name:        "request body above the 1 MiB REST cap",
		builderCall: `scoped.Where("summary", QueryOp{In: 20000 long strings})`,
		build: func(fixture *astFixture) *QueryRequest {
			padding := strings.Repeat("x", 64)
			values := make([]interface{}, 0, 20000)
			for element := 0; element < 20000; element++ {
				values = append(values, fmt.Sprintf("%s-%d", padding, element))
			}
			return scoped(fixture).Where("summary", QueryOp{In: values})
		},
		wantClientSide:   false,
		wantErrSubstring: "invalid json ast",
		wantHTTPStatus:   400,
		wantKind:         KindInvalidRequest,
		note:             "the oversize message is SHORTER than the parse-failure message — a client matching on the long text will not recognise it",
	},
}

// runASTErrorMatrix exercises the rejection contract and records every case.
func runASTErrorMatrix(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture) {
	testHandle.Helper()
	for _, errorCase := range astErrorCases {
		currentCase := errorCase
		testHandle.Run(currentCase.name, func(subTest *testing.T) {
			outcome := runASTCase(subTest, harness, "error/"+currentCase.name,
				currentCase.builderCall, currentCase.build(fixture))

			if outcome.queryErr == nil {
				subTest.Fatalf("expected a rejection, got %d rows (status %d, wire=%s)",
					len(outcome.raw), outcome.status, outcome.wireBody)
			}
			errorText := outcome.queryErr.Error()
			if !strings.Contains(errorText, currentCase.wantErrSubstring) {
				subTest.Errorf("error text %q does not contain %q", errorText, currentCase.wantErrSubstring)
			}

			wireWasEmpty := outcome.wireBody == ""
			if currentCase.wantClientSide && !wireWasEmpty {
				subTest.Errorf("expected a CLIENT-SIDE refusal but %d bytes reached the server (status %d)",
					len(outcome.wireBody), outcome.status)
			}
			if !currentCase.wantClientSide {
				if wireWasEmpty {
					subTest.Fatalf("expected the request to REACH the server, but nothing left the process")
				}
				if outcome.status != currentCase.wantHTTPStatus {
					subTest.Errorf("server answered HTTP %d, expected %d", outcome.status, currentCase.wantHTTPStatus)
				}
				var apiError *APIError
				if !errors.As(outcome.queryErr, &apiError) {
					subTest.Fatalf("server rejection is not a *APIError (%T) — the caller cannot read Kind()/Retryable()",
						outcome.queryErr)
				}
				if apiError.Kind() != currentCase.wantKind {
					subTest.Errorf("Kind()=%q expected %q", apiError.Kind(), currentCase.wantKind)
				}
				if apiError.Retryable() {
					subTest.Errorf("Retryable()=true for a %d — a caller would loop on a permanent rejection", apiError.StatusCode)
				}
			}
			fmt.Printf("AST_ERROR name=%q client_side=%v wire_bytes=%d status=%d note=%q\n",
				currentCase.name, wireWasEmpty, len(outcome.wireBody), outcome.status, currentCase.note)
		})
	}
}

// assertClientSideErrorsAreTypedLikePythonAndTypeScript pins the parity Go
// reached with newValidationError: a refusal the BUILDER makes is the same
// *APIError the transport path returns, so one errors.As covers both origins.
//
// AMENDED 2026-09-14: this used to assert the OPPOSITE (bare fmt.Errorf, no
// status), with its own escape clause — "if someone later makes Validate()
// return a typed error, this test goes red and the parity note gets updated
// instead of rotting". That happened; the note is updated rather than the
// assertion loosened, because the divergence is GONE.
//
// Junior Tip [the type alone is not the contract]: callers branch on errors.As,
// then on Kind() and Retryable(). A typed error with the WRONG Kind is worse
// than an untyped one — it looks classifiable and classifies wrong. So all
// three are asserted, plus clientSide: a refusal that reached the server is not
// a refusal, it is a complaint filed after the fact.
func assertClientSideErrorsAreTypedLikePythonAndTypeScript(testHandle *testing.T, fixture *astFixture) {
	testHandle.Helper()
	request := scoped(fixture).Where("bogus_col", QueryOp{Eq: 1})
	validationErr := request.Validate()
	if validationErr == nil {
		testHandle.Fatal("Validate accepted a column outside the whitelist")
	}
	var apiError *APIError
	if !errors.As(validationErr, &apiError) {
		testHandle.Fatalf("client-side validation returned %T, not *APIError — a caller branching "+
			"on errors.As cannot classify it, the divergence this test closes", validationErr)
	}
	if apiError.Kind() != KindInvalidRequest {
		testHandle.Errorf("client-side rejection Kind() = %v, want %v", apiError.Kind(), KindInvalidRequest)
	}
	if apiError.Retryable() {
		testHandle.Error("a malformed query must never report Retryable() true")
	}
	if !apiError.clientSide {
		testHandle.Error("clientSide must be true — the 400 is a prediction of what the server " +
			"WOULD have answered, not a status it did answer")
	}
	fmt.Printf("AST_CONTRACT client_side_error_is_typed=true err_type=%T kind=%v retryable=%v\n",
		validationErr, apiError.Kind(), apiError.Retryable())
}

// assertNilRequestIsRefused covers the degenerate call.
func assertNilRequestIsRefused(testHandle *testing.T, harness *astLiveHarness) {
	testHandle.Helper()
	harness.resetSnapshot()
	records, queryErr := harness.memory.Query(testContext(), nil)
	if queryErr == nil {
		testHandle.Fatalf("Query(nil) returned %d records instead of an error", len(records))
	}
	if !strings.Contains(queryErr.Error(), "request is required") {
		testHandle.Errorf("Query(nil) error %q does not name the missing request", queryErr.Error())
	}
	wireBody, _, _ := harness.snapshot()
	if wireBody != "" {
		testHandle.Errorf("Query(nil) still put %d bytes on the wire", len(wireBody))
	}
}
