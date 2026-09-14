//go:build astlive

package client

// query_ast_live_oracle_test.go — running one AST case against production and
// judging the answer against an INDEPENDENT oracle.
//
// Domain: case execution + the SQL-semantics oracle the expectations are
// written against.
//
// Junior Tip [why an oracle instead of a hard-coded id list, 2026-09-14]: the
// fixture lives in a production tenant where background agents (enrichment,
// decay, consolidation) may rewrite weight, status or summary at any moment. A
// hard-coded expectation would go red for a reason that has nothing to do with
// the query grammar. So every expectation is COMPUTED in Go from a snapshot of
// the same rows, using SQL's comparison rules — and then compared with what the
// server's SQL returned. The two paths share no code: one is Go predicates over
// decoded JSON, the other is SQLite executing generated SQL. Agreement is
// therefore evidence, not a tautology.
//
// Junior Tip [SQL NULL is not Go nil]: in SQLite any comparison with NULL is
// NULL, which the WHERE clause treats as false. `superseded_by = NULL` matches
// NOTHING — not even rows whose superseded_by IS NULL. nullSafe* below encode
// that, and it is the rule that makes the `$eq: null` case in the error suite
// interesting rather than academic.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/models"
)

// astRawRecord is one record exactly as the server serialised it. Timestamps
// stay as TEXT so an ordering assertion compares what SQLite compared.
type astRawRecord struct {
	ID           int     `json:"id"`
	UUID         string  `json:"uuid"`
	Type         string  `json:"type"`
	Weight       float64 `json:"weight"`
	Score        int     `json:"score"`
	Status       string  `json:"status"`
	Consolidated bool    `json:"consolidated"`
	Archived     bool    `json:"archived"`
	Metadata     string  `json:"metadata"`
	Summary      string  `json:"summary"`
	SupersededBy *int    `json:"superseded_by"`
	ValidFrom    *string `json:"valid_from"`
	ValidUntil   *string `json:"valid_until"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// astQueryEnvelope is the success wire shape: {"records": [...]|null, "count": N}.
type astQueryEnvelope struct {
	Records []astRawRecord `json:"records"`
	Count   *int           `json:"count"`
}

// astQueryOutcome is everything one case produced.
type astQueryOutcome struct {
	raw       []astRawRecord
	sdk       []models.Record
	rawIsNull bool // the wire carried "records": null
	count     int
	wireBody  string
	status    int
	queryErr  error
}

// rawQuery sends one request through the SDK and returns both the SDK-decoded
// records and the untouched wire rows.
func (harness *astLiveHarness) rawQuery(ctx context.Context, request *QueryRequest) ([]astRawRecord, *astQueryOutcome, error) {
	harness.resetSnapshot()
	sdkRecords, queryErr := harness.memory.Query(ctx, request)
	wireBody, status, responseBytes := harness.snapshot()

	outcome := &astQueryOutcome{sdk: sdkRecords, wireBody: wireBody, status: status, queryErr: queryErr}
	if len(responseBytes) > 0 && status == 200 {
		var envelope astQueryEnvelope
		// Junior Tip [a tap that cannot parse must SHOUT]: this decode used to
		// swallow its error, so an unreadable capture was indistinguishable from a
		// genuinely empty result — exactly the silent zero this repository hunts.
		if decodeErr := json.Unmarshal(responseBytes, &envelope); decodeErr != nil {
			return nil, outcome, fmt.Errorf("capture tap could not parse a 200 response (%d bytes): %w",
				len(responseBytes), decodeErr)
		}
		outcome.raw = envelope.Records
		outcome.rawIsNull = envelope.Records == nil
		if envelope.Count != nil {
			outcome.count = *envelope.Count
		}
	}
	return outcome.raw, outcome, queryErr
}

// runASTCase executes one case, records the wire line, and returns the outcome.
func runASTCase(testHandle *testing.T, harness *astLiveHarness, caseName, builderCall string,
	request *QueryRequest) *astQueryOutcome {
	testHandle.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	_, outcome, _ := harness.rawQuery(ctx, request)

	capture := astWireCapture{
		Case:        caseName,
		BuilderCall: builderCall,
		WireBody:    outcome.wireBody,
		HTTPStatus:  outcome.status,
		ResultIDs:   rawIDs(outcome.raw),
	}
	if outcome.queryErr != nil {
		capture.ErrorText = outcome.queryErr.Error()
	}
	if outcome.wireBody == "" {
		capture.Note = "NO BYTES LEFT THE PROCESS — rejected client-side by the Go SDK"
	}
	// Junior Tip [the oversize case would otherwise poison the log]: the body-cap
	// case is deliberately over 1 MiB. Writing it whole would make go_wire.jsonl
	// unreadable for the byte-for-byte cross-SDK diff, so the log keeps a prefix
	// and states the true length — the assertion still ran against the full body.
	if len(capture.WireBody) > astWireBodyCaptureLimit {
		capture.Note = fmt.Sprintf("wire body truncated in this log: %d bytes were actually sent",
			len(capture.WireBody))
		capture.WireBody = capture.WireBody[:astWireBodyCaptureLimit]
	}
	harness.record(capture)
	return outcome
}

// astWireBodyCaptureLimit caps what one JSONL line may carry.
const astWireBodyCaptureLimit = 8192

// rawIDs projects ids in wire order.
func rawIDs(records []astRawRecord) []int {
	identifiers := make([]int, 0, len(records))
	for _, record := range records {
		identifiers = append(identifiers, record.ID)
	}
	return identifiers
}

// sdkIDs projects ids from the SDK-decoded records.
func sdkIDs(records []models.Record) []int {
	identifiers := make([]int, 0, len(records))
	for _, record := range records {
		identifiers = append(identifiers, record.ID)
	}
	return identifiers
}

// sortedCopy returns a sorted copy so a SET comparison ignores order.
func sortedCopy(identifiers []int) []int {
	duplicate := append([]int(nil), identifiers...)
	sort.Ints(duplicate)
	return duplicate
}

// sameIDSet reports set equality.
func sameIDSet(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	leftSorted, rightSorted := sortedCopy(left), sortedCopy(right)
	for position := range leftSorted {
		if leftSorted[position] != rightSorted[position] {
			return false
		}
	}
	return true
}

// sameIDSequence reports order-sensitive equality.
func sameIDSequence(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for position := range left {
		if left[position] != right[position] {
			return false
		}
	}
	return true
}

// filterRaw applies a Go predicate to a snapshot and returns the matching ids.
func filterRaw(snapshot []astRawRecord, predicate func(astRawRecord) bool) []int {
	matched := make([]int, 0, len(snapshot))
	for _, record := range snapshot {
		if predicate(record) {
			matched = append(matched, record.ID)
		}
	}
	return matched
}

// nullSafeStringCompare mirrors SQLite: a NULL operand never satisfies a
// comparison. `present` is false when the column was NULL (absent on the wire).
func nullSafeStringCompare(value *string, present bool, compare func(string) bool) bool {
	if !present || value == nil {
		return false
	}
	return compare(*value)
}

// nullSafeIntCompare is the same rule for a nullable integer column.
func nullSafeIntCompare(value *int, compare func(int) bool) bool {
	if value == nil {
		return false
	}
	return compare(*value)
}

// assertIDSet fails the case when the server's rows are not the oracle's rows.
func assertIDSet(testHandle *testing.T, caseName string, outcome *astQueryOutcome, expected []int) {
	testHandle.Helper()
	if outcome.queryErr != nil {
		testHandle.Errorf("case %q: expected rows, got error: %v (wire=%s)", caseName, outcome.queryErr, outcome.wireBody)
		return
	}
	if outcome.status != 200 {
		testHandle.Errorf("case %q: HTTP %d (wire=%s)", caseName, outcome.status, outcome.wireBody)
		return
	}
	actual := rawIDs(outcome.raw)
	if !sameIDSet(actual, expected) {
		testHandle.Errorf("case %q: server returned ids %v, oracle expected %v (wire=%s)",
			caseName, sortedCopy(actual), sortedCopy(expected), outcome.wireBody)
		return
	}
	// The SDK's own decode must not lose a row the wire carried.
	if !sameIDSet(sdkIDs(outcome.sdk), actual) {
		testHandle.Errorf("case %q: SDK decoded ids %v but the wire carried %v — the SDK is dropping rows",
			caseName, sortedCopy(sdkIDs(outcome.sdk)), sortedCopy(actual))
	}
	if outcome.count != len(actual) {
		testHandle.Errorf("case %q: envelope count=%d but %d records were returned",
			caseName, outcome.count, len(actual))
	}
}

// assertIDSequence is assertIDSet plus order, for sort cases.
func assertIDSequence(testHandle *testing.T, caseName string, outcome *astQueryOutcome, expected []int) {
	testHandle.Helper()
	if outcome.queryErr != nil || outcome.status != 200 {
		testHandle.Errorf("case %q: expected ordered rows, got status=%d err=%v", caseName, outcome.status, outcome.queryErr)
		return
	}
	actual := rawIDs(outcome.raw)
	if !sameIDSequence(actual, expected) {
		testHandle.Errorf("case %q: server order %v, oracle order %v (wire=%s)",
			caseName, actual, expected, outcome.wireBody)
	}
}

// describeIDs renders an id slice for a failure message.
func describeIDs(identifiers []int) string { return fmt.Sprint(sortedCopy(identifiers)) }
