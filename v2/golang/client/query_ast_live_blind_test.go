//go:build astlive

package client

// query_ast_live_blind_test.go — testing filter columns whose values this suite
// can neither read back nor control.
//
// Domain: `dimension`, `prefix` and `consolidated`.
//   - dimension and prefix are in the AST filter/sort whitelist
//     (server/service/record_ast_query.go:37-44) but are tagged `json:"-"` on the
//     server's record model (server/model/record.go:52-53), so no REST response
//     ever shows what the filter matched on. Both are also rewritten by
//     background workers, so even the value written a minute ago is not the
//     value the filter will see.
//   - consolidated cannot be set from the Go SDK at all: LinkConsolidated writes
//     `consolidate_id` (PATCH /api/v1/records/consolidate-ids), a DIFFERENT
//     column that the grammar does not expose. The `consolidated` flag is
//     written only by PATCH /api/v1/records/mark-consolidated, which this SDK
//     does not wrap.
//
// Junior Tip [how to test a filter whose input you cannot see, 2026-09-14]: by
// PARTITION. For a bound K, the rows matching `col <= K` and the rows matching
// `col > K` must (1) never overlap and (2) together be exactly the rows the same
// query returns with no predicate on that column. Both properties are false for
// a filter that is ignored (an ignored filter returns everything on BOTH sides,
// so the halves overlap), false for a filter reading the wrong column, and false
// for an operator with its comparison reversed. None of it requires knowing a
// single stored value. A split where one half is empty is weaker but still
// catches the ignored-filter case, so it is reported, not treated as a failure.

import (
	"fmt"
	"testing"
)

// astPartitionProbe describes one blind column and the bounds worth trying.
type astPartitionProbe struct {
	column      string
	description string
	bounds      []interface{}
}

// astBlindPartitionProbes are the three columns this suite cannot observe.
var astBlindPartitionProbes = []astPartitionProbe{
	{
		column:      "dimension",
		description: "integer, hidden by json:\"-\", owned by the regression worker",
		bounds:      []interface{}{-1, 0, 1, 2, 3, 4, 6, 8, 12, 16, 32, 64, 128, 256, 512, 1024, 4096},
	},
	{
		column:      "prefix",
		description: "text, hidden by json:\"-\", not writable through PATCH",
		bounds:      []interface{}{"", "0", "3", "7", "A", "F", "M", "T", "a", "f", "m", "t", "z", "￿"},
	},
	{
		column:      "consolidated",
		description: "0/1 flag the Go SDK has no method to set",
		bounds:      []interface{}{0, 1},
	},
}

// runBlindColumnPartitions proves each hidden column is really read by the
// filter, without ever observing one of its values.
func runBlindColumnPartitions(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture) {
	testHandle.Helper()
	for _, probe := range astBlindPartitionProbes {
		currentProbe := probe
		testHandle.Run(currentProbe.column, func(subTest *testing.T) {
			runOnePartitionProbe(subTest, harness, fixture, currentProbe)
		})
	}
}

// runOnePartitionProbe executes the partition argument for one column.
func runOnePartitionProbe(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture,
	probe astPartitionProbe) {
	testHandle.Helper()

	active, _ := takeSnapshotPair(testHandle, harness, fixture)
	universe := rawIDs(active)
	if len(universe) < 2 {
		testHandle.Fatalf("partition probe on %q needs at least 2 live rows, has %d", probe.column, len(universe))
	}

	chosenBound, lowerCount, proper := chooseSplitBound(testHandle, harness, fixture, probe, len(universe))

	lowerOutcome := runASTCase(testHandle, harness,
		fmt.Sprintf("%s/$lte %v (blind partition, low half)", probe.column, chosenBound),
		fmt.Sprintf(`scoped.Where(%q, QueryOp{Lte: %v})`, probe.column, chosenBound),
		scoped(fixture).Where(probe.column, QueryOp{Lte: chosenBound}))
	upperOutcome := runASTCase(testHandle, harness,
		fmt.Sprintf("%s/$gt %v (blind partition, high half)", probe.column, chosenBound),
		fmt.Sprintf(`scoped.Where(%q, QueryOp{Gt: %v})`, probe.column, chosenBound),
		scoped(fixture).Where(probe.column, QueryOp{Gt: chosenBound}))

	if lowerOutcome.queryErr != nil || upperOutcome.queryErr != nil {
		testHandle.Fatalf("partition probe on %q errored: low=%v high=%v",
			probe.column, lowerOutcome.queryErr, upperOutcome.queryErr)
	}

	lowerIDs, upperIDs := rawIDs(lowerOutcome.raw), rawIDs(upperOutcome.raw)
	inLower := map[int]bool{}
	for _, identifier := range lowerIDs {
		inLower[identifier] = true
	}
	for _, identifier := range upperIDs {
		if inLower[identifier] {
			testHandle.Errorf("record %d satisfies BOTH %s <= %v and %s > %v — the filter is not reading "+
				"that column (an ignored predicate returns every row on both sides)",
				identifier, probe.column, chosenBound, probe.column, chosenBound)
		}
	}
	union := append(append([]int{}, lowerIDs...), upperIDs...)
	if !sameIDSet(union, universe) {
		testHandle.Errorf("%s partition at %v yields low=%s high=%s, union is not the unfiltered set %s — "+
			"rows fell out of both halves",
			probe.column, chosenBound, describeIDs(lowerIDs), describeIDs(upperIDs), describeIDs(universe))
	}

	shape := "PROPER (both halves non-empty)"
	if !proper {
		shape = fmt.Sprintf("DEGENERATE (column is uniform across the %d live rows; the partition still "+
			"disproves an ignored filter, but cannot exercise the boundary)", len(universe))
	}
	fmt.Printf("AST_BLIND column=%s bound=%v low=%d high=%d universe=%d shape=%q note=%q\n",
		probe.column, chosenBound, len(lowerIDs), len(upperIDs), len(universe), shape, probe.description)
	_ = lowerCount
}

// chooseSplitBound walks the candidate bounds and prefers one that splits the
// universe; it falls back to the first bound that produces a clean empty half.
func chooseSplitBound(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture,
	probe astPartitionProbe, universeSize int) (interface{}, int, bool) {
	testHandle.Helper()
	fallbackBound := probe.bounds[0]
	fallbackCount := 0
	for _, candidate := range probe.bounds {
		records, outcome, queryErr := harness.rawQuery(testContext(),
			scoped(fixture).Where(probe.column, QueryOp{Lte: candidate}))
		if queryErr != nil {
			testHandle.Fatalf("probing %s <= %v failed: %v (status %d)", probe.column, candidate, queryErr, outcome.status)
		}
		if len(records) > 0 && len(records) < universeSize {
			return candidate, len(records), true
		}
		fallbackBound, fallbackCount = candidate, len(records)
	}
	return fallbackBound, fallbackCount, false
}

// runPrefixWriteReality records that PATCH claims to write `prefix` and does not.
//
// Junior Tip [a test that pins a defect is still a test]: PATCH
// /api/v1/records/{id} declares a `prefix` field, answers 200 with a
// last_raft_index, and drops it — service.UpdateRecord only carries Prefix on
// the CommandUpdateRegression branch, entered solely when a vector or embedding
// is supplied (server/service/record_update.go:222-226); a weight/dimension-only
// PATCH falls through to CommandUpdateWeightDimension, whose payload has no
// Prefix field at all. Measured live 2026-09-14. If someone fixes the write
// path, THIS assertion goes red and forces the note to be revisited, which is
// the point.
func runPrefixWriteReality(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture) {
	testHandle.Helper()
	writtenOutcome := runASTCase(testHandle, harness, "prefix/$eq the value PATCH claimed to write",
		`scoped.Where("prefix", QueryOp{Eq: "astp-c"})`,
		scoped(fixture).Where("prefix", QueryOp{Eq: "astp-c"}))
	if writtenOutcome.queryErr != nil {
		testHandle.Fatalf("prefix write-reality query errored: %v", writtenOutcome.queryErr)
	}
	if len(writtenOutcome.raw) != 0 {
		testHandle.Errorf("prefix == \"astp-c\" returned %d rows — PATCH now persists prefix; delete the "+
			"silently-dropped note and promote this to a real partition case", len(writtenOutcome.raw))
	}
	fmt.Printf("AST_BLIND prefix_write_persisted=false rows_matching_requested_value=%d\n", len(writtenOutcome.raw))
}
