//go:build astlive

package client

// query_ast_live_operators_test.go — the operator half of the matrix: every one
// of the six operators the grammar admits, on the columns whose values this
// fixture controls numerically or lexically.
//
// Domain: $eq $gt $gte $lt $lte $in over id, uuid, type, score, weight,
// plus the $in boundary cases. The columns the REST envelope hides (dimension,
// prefix) are covered by the partition oracle in query_ast_live_blind_test.go.
//
// Junior Tip [every case asserts ROWS, never just a status code, 2026-09-14]:
// an operator that returns HTTP 200 and the wrong rows is worse than one that
// errors, because nothing alerts. Each case below names the exact id set the
// oracle derived, and a mismatch prints both sets.

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// astMatrixCase is one query, its wire-level label, and the oracle that judges it.
type astMatrixCase struct {
	name        string
	builderCall string
	// build receives the CONTEMPORANEOUS snapshot so a case can pick a pivot
	// from real column values instead of guessing one.
	build  func(fixture *astFixture, active []astRawRecord) *QueryRequest
	expect func(fixture *astFixture, active, all []astRawRecord) []int
	// ordered compares the id SEQUENCE instead of the set.
	ordered bool
	// allowEmpty / allowFull switch off the discrimination guard for the cases
	// whose whole point is to return nothing, or everything.
	//
	// Junior Tip [the guard is the anti-vacuity rule, 2026-09-14]: a filter case
	// whose oracle expects ZERO rows passes whether the operator works or is a
	// no-op that returns zero rows for everything. Same for a case that expects
	// EVERY row: a filter the server ignored entirely would also return every
	// row. Unless a case declares one of those shapes on purpose, the oracle must
	// land on a PROPER SUBSET — the only shape that can tell a working operator
	// from a broken one.
	allowEmpty bool
	allowFull  bool
}

// scoped starts every query pinned to this run's session, which is how the
// suite guarantees it can never observe another session's rows.
func scoped(fixture *astFixture) *QueryRequest {
	return NewQuery().Where("uuid", QueryOp{Eq: fixture.sessionUUID}).Limit(1000)
}

// astOperatorCases covers the six operators against controllable columns.
var astOperatorCases = []astMatrixCase{
	{
		allowFull:   true,
		name:        "uuid/$eq scopes to the session",
		builderCall: `NewQuery().Where("uuid", QueryOp{Eq: session}).Limit(1000)`,
		build:       func(fixture *astFixture, active []astRawRecord) *QueryRequest { return scoped(fixture) },
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return rawIDs(active)
		},
	},
	{
		allowFull:   true,
		name:        "uuid/$in with a real and a bogus session",
		builderCall: `Where("uuid", QueryOp{In: []interface{}{session, "no-such-session"}})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return NewQuery().Where("uuid", QueryOp{In: []interface{}{fixture.sessionUUID, "ast-teste-go-no-such-session"}}).Limit(1000)
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return rawIDs(active)
		},
	},
	{
		name:        "id/$eq one exact row",
		builderCall: `scoped.Where("id", QueryOp{Eq: seed[2].id})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{Eq: fixture.idByIndex[2]})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return []int{fixture.idByIndex[2]}
		},
	},
	{
		name:        "id/$gt strictly greater",
		builderCall: `scoped.Where("id", QueryOp{Gt: seed[3].id})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{Gt: fixture.idByIndex[3]})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			boundary := fixture.idByIndex[3]
			return filterRaw(active, func(record astRawRecord) bool { return record.ID > boundary })
		},
	},
	{
		name:        "id/$gte includes the boundary",
		builderCall: `scoped.Where("id", QueryOp{Gte: seed[3].id})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{Gte: fixture.idByIndex[3]})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			boundary := fixture.idByIndex[3]
			return filterRaw(active, func(record astRawRecord) bool { return record.ID >= boundary })
		},
	},
	{
		name:        "id/$lt strictly less",
		builderCall: `scoped.Where("id", QueryOp{Lt: seed[3].id})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{Lt: fixture.idByIndex[3]})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			boundary := fixture.idByIndex[3]
			return filterRaw(active, func(record astRawRecord) bool { return record.ID < boundary })
		},
	},
	{
		name:        "id/$lte includes the boundary",
		builderCall: `scoped.Where("id", QueryOp{Lte: seed[3].id})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{Lte: fixture.idByIndex[3]})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			boundary := fixture.idByIndex[3]
			return filterRaw(active, func(record astRawRecord) bool { return record.ID <= boundary })
		},
	},
	{
		name:        "id/$gte+$lte on ONE field AND together (closed range)",
		builderCall: `scoped.Where("id", QueryOp{Gte: seed[1].id, Lte: seed[4].id})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{Gte: fixture.idByIndex[1], Lte: fixture.idByIndex[4]})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			low, high := fixture.idByIndex[1], fixture.idByIndex[4]
			return filterRaw(active, func(record astRawRecord) bool { return record.ID >= low && record.ID <= high })
		},
	},
	{
		name:        "id/$in three explicit ids",
		builderCall: `scoped.Where("id", QueryOp{In: []interface{}{id0, id2, id4}})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{In: []interface{}{
				fixture.idByIndex[0], fixture.idByIndex[2], fixture.idByIndex[4]}})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return []int{fixture.idByIndex[0], fixture.idByIndex[2], fixture.idByIndex[4]}
		},
	},
	{
		name:        "id/$in single element (boundary: the smallest legal list)",
		builderCall: `scoped.Where("id", QueryOp{In: []interface{}{id5}})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{In: []interface{}{fixture.idByIndex[5]}})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return []int{fixture.idByIndex[5]}
		},
	},
	{
		name:        "id/$in 500 elements (large list, one real id buried in it)",
		builderCall: `scoped.Where("id", QueryOp{In: 500 ids, only seed[6] real})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			values := make([]interface{}, 0, 500)
			for filler := 1; filler <= 499; filler++ {
				values = append(values, -filler)
			}
			values = append(values, fixture.idByIndex[6])
			return scoped(fixture).Where("id", QueryOp{In: values})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return []int{fixture.idByIndex[6]}
		},
	},
	{
		allowEmpty:  true,
		name:        "id/$in matching nothing (valid list, empty result)",
		builderCall: `scoped.Where("id", QueryOp{In: []interface{}{-1, -2}})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("id", QueryOp{In: []interface{}{-1, -2}})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int { return []int{} },
	},
	{
		name:        "type/$eq exact string",
		builderCall: `scoped.Where("type", QueryOp{Eq: "task"})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("type", QueryOp{Eq: "task"})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool { return record.Type == "task" })
		},
	},
	{
		name:        "type/$in two values",
		builderCall: `scoped.Where("type", QueryOp{In: []interface{}{"fact", "risk"}})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("type", QueryOp{In: []interface{}{"fact", "risk"}})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool {
				return record.Type == "fact" || record.Type == "risk"
			})
		},
	},
	{
		name:        "type/$gt LEXICOGRAPHIC on a text column (not a type error)",
		builderCall: `scoped.Where("type", QueryOp{Gt: "m"})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("type", QueryOp{Gt: "m"})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool { return record.Type > "m" })
		},
	},
	{
		name:        "score/$eq integer",
		builderCall: `scoped.Where("score", QueryOp{Eq: 4})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("score", QueryOp{Eq: 4})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool { return record.Score == 4 })
		},
	},
	{
		name:        "score/$gt vs $gte differ by exactly the boundary row",
		builderCall: `scoped.Where("score", QueryOp{Gt: 4})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("score", QueryOp{Gt: 4})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool { return record.Score > 4 })
		},
	},
	{
		name:        "score/$gte boundary included",
		builderCall: `scoped.Where("score", QueryOp{Gte: 4})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("score", QueryOp{Gte: 4})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool { return record.Score >= 4 })
		},
	},
	{
		name:        "score/$lte and $lt",
		builderCall: `scoped.Where("score", QueryOp{Lt: 3})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("score", QueryOp{Lt: 3})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool { return record.Score < 3 })
		},
	},
	{
		name:        "score/$in list of integers",
		builderCall: `scoped.Where("score", QueryOp{In: []interface{}{1, 3, 7}})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("score", QueryOp{In: []interface{}{1, 3, 7}})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool {
				return record.Score == 1 || record.Score == 3 || record.Score == 7
			})
		},
	},
	{
		allowEmpty:  true,
		name:        "score/$eq a FRACTIONAL number against an INTEGER column",
		builderCall: `scoped.Where("score", QueryOp{Eq: 4.5})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("score", QueryOp{Eq: 4.5})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int { return []int{} },
	},
	{
		name:        "weight/$gte on a REAL column (pivot taken from LIVE values)",
		builderCall: "scoped.Where(\"weight\", QueryOp{Gte: <live pivot>})",
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("weight", QueryOp{Gte: weightPivot(active)})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			pivot := weightPivot(active)
			return filterRaw(active, func(record astRawRecord) bool { return record.Weight >= pivot })
		},
	},
	{
		name:        "weight/$lt is the exact complement of $gte",
		builderCall: "scoped.Where(\"weight\", QueryOp{Lt: <live pivot>})",
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("weight", QueryOp{Lt: weightPivot(active)})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			pivot := weightPivot(active)
			return filterRaw(active, func(record astRawRecord) bool { return record.Weight < pivot })
		},
	},
}

// runASTMatrix executes a slice of cases as subtests.
//
// Junior Tip [the snapshot is re-taken for EVERY case, 2026-09-14]: this fixture
// lives in a production tenant whose background workers rewrite `weight` (the
// regression/decay worker recomputes it) and can touch `status`. An oracle built
// from a snapshot taken minutes earlier would disagree with the server for a
// reason that has nothing to do with the query grammar — and the failure would
// look exactly like a grammar bug. Re-reading immediately before each case keeps
// the oracle contemporaneous; the residual race is the second between the two
// reads, and a case that fails twice running is real.
func runASTMatrix(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture, cases []astMatrixCase) {
	testHandle.Helper()
	for _, matrixCase := range cases {
		currentCase := matrixCase
		testHandle.Run(currentCase.name, func(subTest *testing.T) {
			active, all := takeSnapshotPair(subTest, harness, fixture)

			request := currentCase.build(fixture, active)
			expected := currentCase.expect(fixture, active, all)

			if len(expected) == 0 && !currentCase.allowEmpty {
				subTest.Fatalf("CASE DID NOT DISCRIMINATE: the oracle expects ZERO of %d live rows, so this case "+
					"would also pass against a server that returns nothing for every query. "+
					"Fix the fixture or declare allowEmpty.", len(active))
			}
			if !currentCase.allowFull && len(active) > 0 && len(expected) >= len(active) {
				subTest.Fatalf("CASE DID NOT DISCRIMINATE: the oracle expects %d of %d live rows, so this case "+
					"would also pass against a server that ignored the filter entirely. "+
					"Fix the fixture or declare allowFull.", len(expected), len(active))
			}

			outcome := runMatrixCaseWithSkewRetry(subTest, harness, fixture, currentCase, request, expected)
			if !subTest.Failed() {
				fmt.Printf("AST_CASE ok name=%q rows=%d expected=%s\n",
					currentCase.name, len(outcome.raw), describeIDs(expected))
			}
		})
	}
}

// runMatrixCaseWithSkewRetry runs a case and, on disagreement, re-reads the
// snapshot and runs it again before calling it a failure.
//
// Junior Tip [why a retry here is NOT hiding a bug, 2026-09-14]: this cluster
// serves reads from several replicas behind one router, and POST /api/v1/query
// accepts no read-index barrier — two consecutive reads can legitimately land on
// nodes with different applied indexes, so a row archived a second ago may be
// present in one answer and absent in the next. That skew makes the ORACLE
// stale, not the operator wrong. A grammar defect is deterministic and survives
// every attempt; skew does not. The retry therefore separates two failures that
// look identical, and the attempt count is printed so a "passed on attempt 3"
// is never mistaken for a clean pass.
func runMatrixCaseWithSkewRetry(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture,
	currentCase astMatrixCase, request *QueryRequest, expected []int) *astQueryOutcome {
	testHandle.Helper()
	const maxAttempts = 3

	var outcome *astQueryOutcome
	currentRequest, currentExpected := request, expected
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		outcome = runASTCase(testHandle, harness, currentCase.name, currentCase.builderCall, currentRequest)
		agreed := outcome.queryErr == nil && outcome.status == 200
		if agreed {
			actual := rawIDs(outcome.raw)
			if currentCase.ordered {
				agreed = sameIDSequence(actual, currentExpected)
			} else {
				agreed = sameIDSet(actual, currentExpected)
			}
		}
		if agreed {
			if attempt > 1 {
				fmt.Printf("AST_CASE skew name=%q agreed only on attempt %d\n", currentCase.name, attempt)
			}
			break
		}
		if attempt == maxAttempts {
			break
		}
		time.Sleep(2 * time.Second)
		freshActive, freshAll := takeSnapshotPair(testHandle, harness, fixture)
		currentRequest = currentCase.build(fixture, freshActive)
		currentExpected = currentCase.expect(fixture, freshActive, freshAll)
	}

	if currentCase.ordered {
		assertIDSequence(testHandle, currentCase.name, outcome, currentExpected)
	} else {
		assertIDSet(testHandle, currentCase.name, outcome, currentExpected)
	}
	return outcome
}

// statusPivot returns a status value present on SOME but not all live rows, so
// an $eq on it is a proper subset. Background workers own this column, so the
// value cannot be decided in advance.
func statusPivot(active []astRawRecord) (string, bool) {
	counts := map[string]int{}
	for _, record := range active {
		counts[record.Status]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if counts[name] > 0 && counts[name] < len(active) {
			return name, true
		}
	}
	return "", false
}

// weightPivot returns a weight bound that splits the snapshot whenever the
// column carries more than one distinct value.
func weightPivot(active []astRawRecord) float64 {
	distinct := map[float64]bool{}
	for _, record := range active {
		distinct[record.Weight] = true
	}
	values := make([]float64, 0, len(distinct))
	for value := range distinct {
		values = append(values, value)
	}
	sort.Float64s(values)
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		// No pivot can split a single distinct value. Returning it makes $gte
		// match everything, which the discrimination guard reports — far better
		// than a case that quietly passes on a column nobody could vary.
		return values[0]
	}
	return values[len(values)/2]
}
