//go:build astlive

package client

// query_ast_live_fixture_test.go — seeds a disposable session in the live
// tenant with records whose column values are KNOWN, and proves the seed landed
// before any assertion is allowed to run.
//
// Domain: fixture lifecycle (seed, mutate, prove, clean up).
//
// Junior Tip [why the fixture must prove it entered, 2026-09-14]: every
// assertion in this suite compares a filtered result against a set. If the seed
// silently failed, every filter would return the empty set, every expectation
// would also be the empty set, and the suite would report PASS having tested
// nothing. `requireFixtureLanded` is the door: it fails the run unless the
// server can show every seeded id back. An absence is not evidence until you
// prove the measurement ran.
//
// Junior Tip [the fixture writes into the KEY's tenant, on purpose and under
// protest]: X-Tenant-ID is honoured only for cluster master keys
// (server/middleware/auth.go:379-404). This suite runs with an ordinary owner
// key, so the tenant is resolved cryptographically and CANNOT be redirected.
// Isolation is therefore by SESSION (`uuid` column) and container tag, both
// prefixed `ast-teste-go-`, and every query in the suite carries the uuid
// filter so it can never observe — let alone assert on — anybody else's rows.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// astTransientRetries is how many times a seeding call is repeated when the
// cluster answers with a transient condition.
//
// Junior Tip [why the seed retries but the ASSERTIONS do not, 2026-09-14]: a
// 503 "no leader available" is the cluster electing, not the SDK misbehaving —
// retrying the SETUP is honest. Retrying an ASSERTION would let a flaky read
// eventually agree with the expectation and hide a real disagreement, so the
// matrix below never retries; it reports what the server said the first time.
const astTransientRetries = 12

// astFixtureVisibilityWindow is how long the suite waits for a committed write
// to become visible to POST /api/v1/query.
//
// Junior Tip [why a window at all, and what it is NOT evidence of, 2026-09-14]:
// the write path is Raft-replicated and the AST read takes no
// X-Anhur-Min-Index barrier, so a freshly created record can legitimately need a
// moment. The window absorbs that. It is deliberately NOT proof of a
// read-your-writes defect: an earlier run of this suite watched "0 rows" for
// three straight minutes and the cause turned out to be a bug in this file's own
// capture tap, not in the server. Do not turn a wait into a finding without
// reproducing it outside the harness.
const astFixtureVisibilityWindow = 5 * time.Minute

// isTransientClusterError recognises the conditions worth retrying during setup.
func isTransientClusterError(candidate error) bool {
	if candidate == nil {
		return false
	}
	text := candidate.Error()
	return strings.Contains(text, "no leader available") ||
		strings.Contains(text, "HTTP 503") ||
		strings.Contains(text, "HTTP 502") ||
		strings.Contains(text, "HTTP 504") ||
		strings.Contains(text, "context deadline exceeded")
}

// retrySetup repeats a setup step while the cluster reports a transient state.
func retrySetup(label string, action func() error) error {
	var lastError error
	for attempt := 1; attempt <= astTransientRetries; attempt++ {
		lastError = action()
		if lastError == nil {
			return nil
		}
		if !isTransientClusterError(lastError) {
			return lastError
		}
		fmt.Printf("AST_FIXTURE retry %s attempt=%d transient=%v\n", label, attempt, lastError)
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
	return lastError
}

// astSeedSpec is one planted record and the column values planted with it.
type astSeedSpec struct {
	index      int
	recordType string
	score      int
	validFrom  string
	weight     float64
	dimension  int
	prefix     string
	status     string
	summaryKey string
}

// astSeedPlan is the fixture. Ten rows, chosen so every operator in the
// grammar has something to discriminate: types that sort lexicographically
// across the whole alphabet, scores covering a contiguous range so $gt/$gte
// differ by exactly one row, and weights spaced far enough apart that a float
// comparison is never a tie.
var astSeedPlan = []astSeedSpec{
	{index: 0, recordType: "episodic", score: 0, validFrom: "2020-01-01T00:00:00Z", weight: 0.05, dimension: 0, prefix: "astp-z", status: "saved", summaryKey: "anchor"},
	{index: 1, recordType: "fact", score: 1, validFrom: "2020-01-02T00:00:00Z", weight: 0.11, dimension: 1, prefix: "astp-a", status: "saved", summaryKey: "alpha"},
	{index: 2, recordType: "decision", score: 2, validFrom: "2020-01-03T00:00:00Z", weight: 0.22, dimension: 1, prefix: "astp-a", status: "saved", summaryKey: "bravo"},
	{index: 3, recordType: "task", score: 3, validFrom: "2020-01-04T00:00:00Z", weight: 0.33, dimension: 2, prefix: "astp-b", status: "saved", summaryKey: "charlie"},
	{index: 4, recordType: "idea", score: 4, validFrom: "2020-01-05T00:00:00Z", weight: 0.44, dimension: 2, prefix: "astp-b", status: "saved", summaryKey: "delta"},
	{index: 5, recordType: "risk", score: 5, validFrom: "2020-01-06T00:00:00Z", weight: 0.55, dimension: 3, prefix: "astp-c", status: "saved", summaryKey: "echo"},
	{index: 6, recordType: "preference", score: 6, validFrom: "2020-01-07T00:00:00Z", weight: 0.66, dimension: 3, prefix: "astp-c", status: "linked", summaryKey: "foxtrot"},
	{index: 7, recordType: "reasoning", score: 7, validFrom: "2020-01-08T00:00:00Z", weight: 0.77, dimension: 4, prefix: "astp-d", status: "saved", summaryKey: "golf"},
	{index: 8, recordType: "emotion", score: 8, validFrom: "2020-01-09T00:00:00Z", weight: 0.88, dimension: 5, prefix: "astp-e", status: "saved", summaryKey: "hotel"},
	{index: 9, recordType: "fact", score: 9, validFrom: "2020-01-10T00:00:00Z", weight: 0.99, dimension: 6, prefix: "astp-f", status: "saved", summaryKey: "india"},
}

// Fixture roles. Index 8 is soft-deleted (archived=1), index 9 is superseded by
// index 0 (superseded_by NOT NULL, so the base clause hides it from every
// possible query), index 7 is marked consolidated.
//
// Junior Tip [index 0 MUST stay episodic, 2026-09-14]: the server refuses to
// create any derived type in a session that has no episodic anchor
// (service/record_topology.go:120). The first draft of this plan started with a
// `fact` and the whole seed died with HTTP 422 — which the fixture guard caught,
// as designed. The anchor is a fixture row like any other, not a special case.
const (
	astArchivedSeedIndex     = 8
	astSupersededSeedIndex   = 9
	astConsolidatedSeedIndex = 7
)

// astFixture is the planted state, resolved to real record ids.
type astFixture struct {
	sessionUUID  string
	idByIndex    map[int]int
	indexByID    map[int]int
	activeIDs    []int // visible without an explicit archived filter
	archivedID   int
	supersededID int
}

// seedASTFixture plants the rows and returns their ids.
func seedASTFixture(testHandle *testing.T, harness *astLiveHarness) *astFixture {
	testHandle.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	var sessionUUID string
	sessionErr := retrySetup("CreateSession", func() error {
		created, createErr := harness.memory.CreateSession(ctx,
			WithCreateSessionID(harness.memory.SessionID()),
			WithCreateSessionMetadata(map[string]interface{}{"purpose": "ast-teste-go disposable query-grammar fixture"}))
		sessionUUID = created
		return createErr
	})
	if sessionErr != nil {
		testHandle.Fatalf("CreateSession failed — cannot seed, refusing to run vacuously: %v", sessionErr)
	}
	fmt.Printf("AST_FIXTURE session_uuid=%s container_tag=%s\n", sessionUUID, harness.containerTag)

	fixture := &astFixture{
		sessionUUID: sessionUUID,
		idByIndex:   map[int]int{},
		indexByID:   map[int]int{},
	}

	for _, seed := range astSeedPlan {
		content := fmt.Sprintf("ast-teste-go row %d keyword-%s marker-%s", seed.index, seed.summaryKey, harness.containerTag)
		var created *AddResult
		createErr := retrySetup(fmt.Sprintf("Create row %d", seed.index), func() error {
			result, callErr := harness.memory.Create(ctx, sessionUUID, content,
				WithCreateType(seed.recordType),
				WithCreateScore(seed.score),
				WithCreateStatus(seed.status),
				WithCreateValidFrom(seed.validFrom),
				WithCreateMetadata(map[string]interface{}{"ast_row": seed.index, "ast_key": seed.summaryKey}))
			created = result
			return callErr
		})
		if createErr != nil {
			testHandle.Fatalf("seeding row %d failed: %v", seed.index, createErr)
		}
		if created == nil || created.ID == 0 {
			testHandle.Fatalf("seeding row %d returned no id: %+v", seed.index, created)
		}
		fixture.idByIndex[seed.index] = int(created.ID)
		fixture.indexByID[int(created.ID)] = seed.index
		harness.createdIDs = append(harness.createdIDs, created.ID)

		// weight / dimension / prefix are not Create options; PATCH carries them.
		updateErr := retrySetup(fmt.Sprintf("Update row %d", seed.index), func() error {
			return harness.memory.Update(ctx, created.ID, map[string]interface{}{
				"weight":    seed.weight,
				"dimension": seed.dimension,
				"prefix":    seed.prefix,
			})
		})
		if updateErr != nil {
			testHandle.Fatalf("patching row %d (weight/dimension/prefix) failed: %v", seed.index, updateErr)
		}
	}

	fixture.archivedID = fixture.idByIndex[astArchivedSeedIndex]
	fixture.supersededID = fixture.idByIndex[astSupersededSeedIndex]

	consolidateErr := retrySetup("LinkConsolidated", func() error {
		return harness.memory.LinkConsolidated(ctx,
			[]int64{int64(fixture.idByIndex[astConsolidatedSeedIndex])}, int64(fixture.idByIndex[0]))
	})
	if consolidateErr != nil {
		testHandle.Fatalf("LinkConsolidated failed: %v", consolidateErr)
	}
	supersedeErr := retrySetup("Supersede", func() error {
		return harness.memory.Supersede(ctx, int64(fixture.supersededID), int64(fixture.idByIndex[0]))
	})
	if supersedeErr != nil {
		testHandle.Fatalf("Supersede failed: %v", supersedeErr)
	}
	// DELETE /api/v1/records/{id} is a SOFT delete (handler answers "record
	// archived"), which is exactly how this fixture obtains archived=1.
	deleteErr := retrySetup("Delete (soft)", func() error {
		return harness.memory.Delete(ctx, int64(fixture.archivedID))
	})
	if deleteErr != nil {
		testHandle.Fatalf("Delete (soft, to obtain archived=1) failed: %v", deleteErr)
	}

	for _, seed := range astSeedPlan {
		if seed.index == astArchivedSeedIndex || seed.index == astSupersededSeedIndex {
			continue
		}
		fixture.activeIDs = append(fixture.activeIDs, fixture.idByIndex[seed.index])
	}
	return fixture
}

// requireFixtureLanded refuses to let the suite continue unless the server can
// show back every row this fixture planted, with the roles it planted them in.
//
// It retries because the write path is Raft-replicated and the read may land on
// a follower that has not applied yet (read-your-writes is not free here).
func requireFixtureLanded(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture) []astRawRecord {
	testHandle.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var lastSeen []astRawRecord
	var lastError error
	supersededStillVisible := false
	deadline := time.Now().Add(astFixtureVisibilityWindow)
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		request := NewQuery().
			Where("uuid", QueryOp{Eq: fixture.sessionUUID}).
			Where("archived", QueryOp{In: []interface{}{0, 1}}).
			Limit(1000)
		raw, _, queryErr := harness.rawQuery(ctx, request)
		lastError = queryErr
		if queryErr == nil {
			lastSeen = raw
			seen := map[int]bool{}
			for _, record := range raw {
				seen[record.ID] = true
			}
			// Junior Tip [a role that has not landed yet is not a defect,
			// 2026-09-14]: the Supersede and Delete commands replicate through
			// Raft like every other write, and the read may reach a node that has
			// not applied them. An earlier version of this guard treated the
			// still-visible superseded row as an immediate contract violation and
			// killed the run inside ten seconds. Waiting for the state the fixture
			// ASKED for is correct; declaring a violation is only honest after the
			// whole window has elapsed, which is what the final Fatalf does.
			complete := true
			for seedIndex, recordID := range fixture.idByIndex {
				if seedIndex == astSupersededSeedIndex {
					if seen[recordID] {
						supersededStillVisible = true
						complete = false
					} else {
						supersededStillVisible = false
					}
					continue
				}
				if !seen[recordID] {
					complete = false
				}
			}
			if complete && len(raw) >= len(astSeedPlan)-1 {
				fmt.Printf("AST_FIXTURE landed after %d attempt(s): %d rows visible\n", attempt, len(raw))
				return raw
			}
			if attempt%10 == 0 {
				fmt.Printf("AST_FIXTURE waiting: attempt=%d visible=%d/%d superseded_still_visible=%v\n",
					attempt, len(raw), len(astSeedPlan)-1, supersededStillVisible)
			}
		}
		time.Sleep(3 * time.Second)
	}
	if supersededStillVisible {
		testHandle.Fatalf("CONTRACT VIOLATION: record %d was superseded %s ago and is STILL returned by "+
			"POST /api/v1/query, which pins superseded_by IS NULL on every query "+
			"(server/service/record_ast_query.go:141). session=%s",
			fixture.supersededID, astFixtureVisibilityWindow, fixture.sessionUUID)
	}
	testHandle.Fatalf("FIXTURE DID NOT ENTER: after "+astFixtureVisibilityWindow.String()+" the session %s shows %d rows, expected %d "+
		"(last query error: %v). Refusing to run a matrix whose every expectation would be the empty set.",
		fixture.sessionUUID, len(lastSeen), len(astSeedPlan)-1, lastError)
	return nil
}

// cleanupASTFixture soft-deletes every planted row and prints the identifiers so
// the run is auditable. Delete is a SOFT delete server-side, so the rows remain
// as archived=1/status=deleted — that is the strongest removal the public API
// offers and it is recorded here deliberately, not hidden.
func cleanupASTFixture(testHandle *testing.T, harness *astLiveHarness, fixture *astFixture) {
	testHandle.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	identifiers, _ := json.Marshal(map[string]interface{}{
		"session_uuid":  fixture.sessionUUID,
		"container_tag": harness.containerTag,
		"record_ids":    harness.createdIDs,
	})
	fmt.Printf("AST_FIXTURE_CLEANUP %s\n", string(identifiers))

	for _, recordID := range harness.createdIDs {
		if deleteErr := harness.memory.Delete(ctx, recordID); deleteErr != nil {
			fmt.Printf("AST_FIXTURE_CLEANUP warn id=%d err=%v\n", recordID, deleteErr)
		}
	}
}
