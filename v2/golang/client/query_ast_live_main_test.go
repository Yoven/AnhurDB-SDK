//go:build astlive

package client

// query_ast_live_main_test.go — the one entry point that orders the whole AST
// exercise: map the surface, seed, PROVE the seed landed, run the matrix, run
// the error matrix, dump the wire log, clean up.
//
// Domain: run orchestration. Sequencing lives here and nowhere else, because
// the guard must sit between the seed and the first assertion.
//
// Run it with:
//
//	source ~/.anhur-claude-memory/env
//	go test -tags astlive -run TestASTQueryLiveAgainstProduction -v ./client/
//
// Junior Tip [why one Test and not ten, 2026-09-14]: Go runs top-level Test
// functions in an order it does not promise. Ten independent live tests would
// each need their own fixture (ten times the writes into a production tenant)
// or would share one through a package variable whose initialisation order is
// exactly the thing that goes wrong at 2am. One function, explicit order, one
// fixture, one cleanup.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// testContext is the default budget for a single live call.
func testContext() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	// The cancel is intentionally deferred to GC of the timer: these are
	// short-lived one-shot calls inside a test binary. Do not copy this into
	// library code.
	_ = cancel
	return ctx
}

// TestASTQueryLiveAgainstProduction is the complete AST-surface exercise.
func TestASTQueryLiveAgainstProduction(testHandle *testing.T) {
	harness := newASTLiveHarness(testHandle)
	testHandle.Cleanup(func() { harness.flushWire(testHandle) })

	fmt.Printf("AST_RUN start=%s upstream=%s proxy=%s sdk_version=%s\n",
		astRunClock().UTC().Format(time.RFC3339), harness.upstreamURL, harness.proxy.URL, Version)

	fixture := seedASTFixture(testHandle, harness)
	testHandle.Cleanup(func() { cleanupASTFixture(testHandle, harness, fixture) })

	// The door. Nothing below may run until the server shows the seed back.
	requireFixtureLanded(testHandle, harness, fixture)

	activeSnapshot := takeActiveSnapshot(testHandle, harness, fixture)
	if len(activeSnapshot) != len(astSeedPlan)-2 {
		testHandle.Fatalf("active snapshot has %d rows, expected %d (seeds minus the archived and superseded ones) — "+
			"the fixture roles did not take effect, so every archived/superseded assertion below would be vacuous",
			len(activeSnapshot), len(astSeedPlan)-2)
	}
	bindFixtureTextValues(testHandle, fixture, activeSnapshot)
	requireFixtureDiscriminates(testHandle, activeSnapshot)

	testHandle.Run("surface", func(subTest *testing.T) { assertASTSurface(subTest, fixture) })
	testHandle.Run("operators", func(subTest *testing.T) {
		runASTMatrix(subTest, harness, fixture, astOperatorCases)
	})
	testHandle.Run("columns_sort_pagination", func(subTest *testing.T) {
		runASTMatrix(subTest, harness, fixture, astColumnCases)
	})
	testHandle.Run("errors", func(subTest *testing.T) {
		runASTErrorMatrix(subTest, harness, fixture)
		assertClientSideErrorsAreUntyped(subTest, fixture)
		assertNilRequestIsRefused(subTest, harness)
	})
	testHandle.Run("blind_columns", func(subTest *testing.T) {
		runBlindColumnPartitions(subTest, harness, fixture)
		runPrefixWriteReality(subTest, harness, fixture)
	})
	testHandle.Run("empty_result_envelope", func(subTest *testing.T) {
		assertEmptyResultEnvelope(subTest, harness, fixture)
	})
}

// takeSnapshotPair re-reads the fixture twice: the ordinary view (no archived
// predicate) and the archived-inclusive view. Both are needed because the
// implicit `archived = 0` clause makes them genuinely different result sets.
func takeSnapshotPair(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture) ([]astRawRecord, []astRawRecord) {
	testHandle.Helper()
	active := takeActiveSnapshot(testHandle, harness, fixture)
	all, outcome, queryErr := harness.rawQuery(testContext(),
		NewQuery().Where("uuid", QueryOp{Eq: fixture.sessionUUID}).
			Where("archived", QueryOp{In: []interface{}{0, 1}}).Limit(1000))
	if queryErr != nil {
		testHandle.Fatalf("archived-inclusive snapshot failed: %v (status %d)", queryErr, outcome.status)
	}
	return active, all
}

// takeActiveSnapshot reads the session WITHOUT an archived predicate, which is
// the view every ordinary query sees.
func takeActiveSnapshot(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture) []astRawRecord {
	testHandle.Helper()
	records, outcome, queryErr := harness.rawQuery(testContext(),
		NewQuery().Where("uuid", QueryOp{Eq: fixture.sessionUUID}).Limit(1000))
	if queryErr != nil {
		testHandle.Fatalf("active snapshot query failed: %v (status %d)", queryErr, outcome.status)
	}
	return records
}

// requireFixtureDiscriminates refuses to run the matrix unless the columns the
// operators are tested on actually VARY across the fixture.
//
// Junior Tip [a uniform column is an untested operator, 2026-09-14]: if every
// seeded row carried score 5, `score $gt 4` and `score $gte 4` would return the
// same rows and a server that confused the two would pass. The per-case
// discrimination guard catches this one case at a time; this function catches
// the whole class up front and names the column, which is the message a person
// can act on.
func requireFixtureDiscriminates(testHandle *testing.T, active []astRawRecord) {
	testHandle.Helper()
	distinctTypes := map[string]bool{}
	distinctScores := map[int]bool{}
	distinctWeights := map[float64]bool{}
	distinctStatuses := map[string]bool{}
	for _, record := range active {
		distinctTypes[record.Type] = true
		distinctScores[record.Score] = true
		distinctWeights[record.Weight] = true
		distinctStatuses[record.Status] = true
	}
	fmt.Printf("AST_FIXTURE_VARIANCE rows=%d types=%d scores=%d weights=%d statuses=%d\n",
		len(active), len(distinctTypes), len(distinctScores), len(distinctWeights), len(distinctStatuses))
	if len(distinctTypes) < 2 || len(distinctScores) < 2 {
		testHandle.Fatalf("fixture does not discriminate: %d distinct types, %d distinct scores — "+
			"the operator matrix would pass against a broken server",
			len(distinctTypes), len(distinctScores))
	}
	if len(distinctWeights) < 2 {
		testHandle.Errorf("weight has ONE distinct value across %d rows — the weight cases below cannot "+
			"distinguish a working $gte from a no-op. Background workers rewrite weight; re-run if this "+
			"is transient, and report it if it is not.", len(active))
	}
}

// assertASTSurface pins the public ways this SDK can build or send an AST query.
//
// Junior Tip [a surface test is a contract test]: if a future change adds a
// second send path (say a raw ExecuteAST), this test still passes — but the
// matrix above will then be exercising only one of two paths, and the reviewer
// reading this function is the person who notices. Keep the list here honest.
func assertASTSurface(testHandle *testing.T, fixture *astFixture) {
	testHandle.Helper()

	built, buildErr := NewQuery().
		SelectFields("id").
		WhereEquals("type", "fact").
		Where("score", QueryOp{Gte: 1}).
		OrderBy("id", "asc").
		Limit(10).
		Offset(0).
		Build()
	if buildErr != nil {
		testHandle.Fatalf("the documented fluent chain does not build: %v", buildErr)
	}
	if built.Filters["type"].Eq != "fact" {
		testHandle.Errorf("WhereEquals did not route through Where: %+v", built.Filters["type"])
	}
	if len(built.Select) != 1 || built.Select[0] != "id" {
		testHandle.Errorf("SelectFields/dedupe produced %v", built.Select)
	}
	if built.Pagination["limit"] != 10 || built.Pagination["offset"] != 0 {
		testHandle.Errorf("pagination is %v", built.Pagination)
	}

	// Execute(ctx, memory) is the other send path and must refuse a nil Memory
	// rather than panic.
	if _, executeErr := NewQuery().Limit(1).Execute(testContext(), nil); executeErr == nil {
		testHandle.Error("Execute(ctx, nil) did not return an error")
	}
	fmt.Printf("AST_SURFACE builder=NewQuery/Where/WhereEquals/SelectFields/OrderBy/Limit/Offset/Build/Execute send=Memory.Query\n")
}

// assertEmptyResultEnvelope pins the zero-hit wire shape and what the SDK makes
// of it.
//
// Junior Tip [null is not []]: the server returns a nil Go slice, so the wire
// carries `"records": null`. The Go SDK normalises that to an EMPTY slice so a
// caller can range without a nil check — which means a Go caller cannot tell
// "no rows" from "field absent". That normalisation is deliberate; this test
// exists so nobody removes it by accident and hands callers a nil.
func assertEmptyResultEnvelope(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture) {
	testHandle.Helper()
	request := NewQuery().
		Where("uuid", QueryOp{Eq: fixture.sessionUUID}).
		Where("type", QueryOp{Eq: "no-such-type-ast-teste-go"}).
		Limit(5)
	outcome := runASTCase(testHandle, harness, "empty result envelope",
		`scoped.Where("type", QueryOp{Eq: "no-such-type-ast-teste-go"})`, request)

	if outcome.queryErr != nil {
		testHandle.Fatalf("empty-result query errored: %v", outcome.queryErr)
	}
	if !outcome.rawIsNull {
		testHandle.Errorf("expected the wire to carry \"records\": null, got %d rows", len(outcome.raw))
	}
	if outcome.sdk == nil {
		testHandle.Error("the SDK returned a nil slice for a zero-hit query — callers must be able to range safely")
	}
	if len(outcome.sdk) != 0 {
		testHandle.Errorf("zero-hit query decoded %d records", len(outcome.sdk))
	}
}
