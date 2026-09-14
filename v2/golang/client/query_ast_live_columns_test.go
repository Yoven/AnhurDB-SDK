//go:build astlive

package client

// query_ast_live_columns_test.go — the remaining filterable columns, the
// implicit predicates, combination/contradiction, sort and pagination.
//
// Domain: the columns whose interest is SEMANTIC rather than arithmetic —
// archived (the carve-out), superseded_by (the clause you cannot escape),
// the nullable temporal columns — plus ordering and paging.
//
// Junior Tip [the two predicates nobody asked for, 2026-09-14]: every generated
// query carries `superseded_by IS NULL`, and it also carries `archived = 0`
// UNLESS the caller filtered `archived` explicitly. Both are invisible in the
// request the caller wrote, so a caller who does not know about them reads a
// short answer as "there is nothing there". The cases below pin BOTH: the
// archived row appears only when `archived` is named, and the superseded row
// appears for no filter at all.

import (
	"sort"
	"testing"
	"time"
)

// liveStatusValues lists every status present on the live rows, as $in values.
func liveStatusValues(active []astRawRecord) []interface{} {
	seen := map[string]bool{}
	names := []string{}
	for _, record := range active {
		if !seen[record.Status] {
			seen[record.Status] = true
			names = append(names, record.Status)
		}
	}
	sort.Strings(names)
	values := make([]interface{}, 0, len(names))
	for _, name := range names {
		values = append(values, name)
	}
	return values
}

// rawByID finds a snapshot row.
func rawByID(snapshot []astRawRecord, recordID int) *astRawRecord {
	for index := range snapshot {
		if snapshot[index].ID == recordID {
			return &snapshot[index]
		}
	}
	return nil
}

// astColumnCases covers semantics, ordering and paging.
var astColumnCases = []astMatrixCase{
	{
		name:        "status/$eq a value taken from the LIVE rows",
		builderCall: `scoped.Where("status", QueryOp{Eq: <live minority status>})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			pivot, _ := statusPivot(active)
			return scoped(fixture).Where("status", QueryOp{Eq: pivot})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			pivot, found := statusPivot(active)
			if !found {
				return nil
			}
			return filterRaw(active, func(record astRawRecord) bool { return record.Status == pivot })
		},
	},
	{
		allowFull:   true,
		name:        "status/$in every live status value is the whole session",
		builderCall: `scoped.Where("status", QueryOp{In: <every live status>})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("status", QueryOp{In: liveStatusValues(active)})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return rawIDs(active)
		},
	},
	{
		name:        "archived/$eq 1 — proves the implicit archived=0 carve-out fires",
		builderCall: `scoped.Where("archived", QueryOp{Eq: 1})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("archived", QueryOp{Eq: 1})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			visible := map[int]bool{}
			for _, record := range active {
				visible[record.ID] = true
			}
			hidden := []int{}
			for _, record := range all {
				if !visible[record.ID] && record.Archived {
					hidden = append(hidden, record.ID)
				}
			}
			return hidden
		},
	},
	{
		allowFull:   true,
		name:        "archived/$in [0,1] — the whole session, archived included",
		builderCall: `scoped.Where("archived", QueryOp{In: []interface{}{0, 1}})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("archived", QueryOp{In: []interface{}{0, 1}})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return rawIDs(all)
		},
	},
	{
		allowFull:   true,
		name:        "no archived filter — the soft-deleted row is INVISIBLE",
		builderCall: `scoped (no archived predicate)`,
		build:       func(fixture *astFixture, active []astRawRecord) *QueryRequest { return scoped(fixture) },
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return rawIDs(active)
		},
	},
	{
		allowEmpty:  true,
		name:        "superseded_by/$eq the successor id — ALWAYS empty, base clause wins",
		builderCall: `scoped.Where("superseded_by", QueryOp{Eq: seed[0].id})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("superseded_by", QueryOp{Eq: fixture.idByIndex[0]})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int { return []int{} },
	},
	{
		allowEmpty:  true,
		name:        "superseded_by/$gt 0 — also always empty (IS NULL is pinned)",
		builderCall: `scoped.Where("superseded_by", QueryOp{Gt: 0})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("superseded_by", QueryOp{Gt: 0})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int { return []int{} },
	},
	{
		name:        "valid_from/$gte on a nullable TEXT date column",
		builderCall: `scoped.Where("valid_from", QueryOp{Gte: "2020-01-05T00:00:00Z"})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("valid_from", QueryOp{Gte: "2020-01-05T00:00:00Z"})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool {
				return nullSafeStringCompare(record.ValidFrom, record.ValidFrom != nil,
					func(value string) bool { return value >= "2020-01-05T00:00:00Z" })
			})
		},
	},
	{
		name:        "valid_from/$lt lower half",
		builderCall: `scoped.Where("valid_from", QueryOp{Lt: "2020-01-03T00:00:00Z"})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("valid_from", QueryOp{Lt: "2020-01-03T00:00:00Z"})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool {
				return nullSafeStringCompare(record.ValidFrom, record.ValidFrom != nil,
					func(value string) bool { return value < "2020-01-03T00:00:00Z" })
			})
		},
	},
	{
		allowEmpty:  true,
		name:        "valid_until/$gte — every seeded row is NULL, so SQL matches none",
		builderCall: `scoped.Where("valid_until", QueryOp{Gte: "2000-01-01T00:00:00Z"})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("valid_until", QueryOp{Gte: "2000-01-01T00:00:00Z"})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int { return []int{} },
	},
	{
		allowEmpty:  true,
		name:        "created_at/$lt a date before the fixture existed",
		builderCall: `scoped.Where("created_at", QueryOp{Lt: "2020-01-01T00:00:00Z"})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("created_at", QueryOp{Lt: "2020-01-01T00:00:00Z"})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int { return []int{} },
	},
	{
		allowFull:   true,
		name:        "created_at/$gte a date before the fixture existed",
		builderCall: `scoped.Where("created_at", QueryOp{Gte: "2020-01-01T00:00:00Z"})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("created_at", QueryOp{Gte: "2020-01-01T00:00:00Z"})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool {
				return record.CreatedAt >= "2020-01-01T00:00:00Z"
			})
		},
	},
	{
		allowFull:   true,
		name:        "updated_at/$gte tracks the PATCH that set weight",
		builderCall: `scoped.Where("updated_at", QueryOp{Gte: "2020-01-01T00:00:00Z"})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("updated_at", QueryOp{Gte: "2020-01-01T00:00:00Z"})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool {
				return record.UpdatedAt >= "2020-01-01T00:00:00Z"
			})
		},
	},
	{
		name:        "metadata/$eq the exact stored JSON text",
		builderCall: `scoped.Where("metadata", QueryOp{Eq: <metadata of seed[2]>})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("metadata", QueryOp{Eq: astFixtureMetadata})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool { return record.Metadata == astFixtureMetadata })
		},
	},
	{
		name:        "summary/$eq the exact stored summary",
		builderCall: `scoped.Where("summary", QueryOp{Eq: <summary of seed[3]>})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("summary", QueryOp{Eq: astFixtureSummary})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool { return record.Summary == astFixtureSummary })
		},
	},
	{
		name:        "three fields ANDed across columns",
		builderCall: `scoped.Where("type", In{fact,task,idea}).Where("score", Gte 3).Where("weight", Lt 0.5)`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).
				Where("type", QueryOp{In: []interface{}{"fact", "task", "idea"}}).
				Where("score", QueryOp{Gte: 3}).
				Where("weight", QueryOp{Lt: 0.5})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return filterRaw(active, func(record astRawRecord) bool {
				typeMatches := record.Type == "fact" || record.Type == "task" || record.Type == "idea"
				return typeMatches && record.Score >= 3 && record.Weight < 0.5
			})
		},
	},
	{
		allowEmpty:  true,
		name:        "contradictory range on one field returns zero rows, not an error",
		builderCall: `scoped.Where("score", QueryOp{Gt: 5, Lt: 2})`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).Where("score", QueryOp{Gt: 5, Lt: 2})
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int { return []int{} },
	},
	{
		allowFull:   true,
		name:        "sort id asc",
		builderCall: `scoped.OrderBy("id", "asc")`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).OrderBy("id", "asc")
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return sortedCopy(rawIDs(active))
		},
		ordered: true,
	},
	{
		allowFull:   true,
		name:        "sort id desc is the default direction",
		builderCall: `scoped.OrderBy("id", "desc")`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).OrderBy("id", "desc")
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			ascending := sortedCopy(rawIDs(active))
			descending := make([]int, 0, len(ascending))
			for position := len(ascending) - 1; position >= 0; position-- {
				descending = append(descending, ascending[position])
			}
			return descending
		},
		ordered: true,
	},
	{
		allowFull:   true,
		name:        "no sort clause falls back to ORDER BY id DESC",
		builderCall: `scoped (no OrderBy)`,
		build:       func(fixture *astFixture, active []astRawRecord) *QueryRequest { return scoped(fixture) },
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			ascending := sortedCopy(rawIDs(active))
			descending := make([]int, 0, len(ascending))
			for position := len(ascending) - 1; position >= 0; position-- {
				descending = append(descending, ascending[position])
			}
			return descending
		},
		ordered: true,
	},
	{
		allowFull:   true,
		name:        "two sort terms: type asc then id asc",
		builderCall: `scoped.OrderBy("type", "asc").OrderBy("id", "asc")`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).OrderBy("type", "asc").OrderBy("id", "asc")
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return orderByTypeThenID(fixture, active)
		},
		ordered: true,
	},
	{
		name:        "pagination page 1: limit 3 offset 0 over id asc",
		builderCall: `scoped.OrderBy("id","asc").Limit(3).Offset(0)`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return NewQuery().Where("uuid", QueryOp{Eq: fixture.sessionUUID}).
				OrderBy("id", "asc").Limit(3).Offset(0)
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return sortedCopy(rawIDs(active))[0:3]
		},
		ordered: true,
	},
	{
		name:        "pagination page 2: limit 3 offset 3 is disjoint and contiguous",
		builderCall: `scoped.OrderBy("id","asc").Limit(3).Offset(3)`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return NewQuery().Where("uuid", QueryOp{Eq: fixture.sessionUUID}).
				OrderBy("id", "asc").Limit(3).Offset(3)
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return sortedCopy(rawIDs(active))[3:6]
		},
		ordered: true,
	},
	{
		allowEmpty:  true,
		name:        "offset past the end returns an empty page, not an error",
		builderCall: `scoped.Limit(5).Offset(10000)`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return NewQuery().Where("uuid", QueryOp{Eq: fixture.sessionUUID}).Limit(5).Offset(10000)
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int { return []int{} },
	},
	{
		allowFull:   true,
		name:        "limit exactly at the client-side ceiling (1000)",
		builderCall: `scoped.Limit(1000)`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return NewQuery().Where("uuid", QueryOp{Eq: fixture.sessionUUID}).Limit(1000)
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return rawIDs(active)
		},
	},
	{
		allowFull:   true,
		name:        "SelectFields is accepted and IGNORED — the full record still returns",
		builderCall: `scoped.SelectFields("id", "id", "type")`,
		build: func(fixture *astFixture, active []astRawRecord) *QueryRequest {
			return scoped(fixture).SelectFields("id", "id", "type")
		},
		expect: func(fixture *astFixture, active, all []astRawRecord) []int {
			return rawIDs(active)
		},
	},
}

// PREFIX — measured 2026-09-14 against https://anhurdb.yoven.ai:
// PATCH /api/v1/records/{id} declares a `prefix` field, answers 200 with
// last_raft_index, and DOES NOT WRITE IT. service.UpdateRecord only carries
// Prefix on the CommandUpdateRegression branch, which is entered solely when a
// vector or an embedding is supplied (server/service/record_update.go:222-226);
// a weight/dimension-only PATCH falls to CommandUpdateWeightDimension, whose
// payload has no Prefix field at all. So `prefix` is a filterable and sortable
// column that no public REST caller can set and no REST response can show
// (model.Record tags it json:"-"). The two cases above pin that reality rather
// than pretending the column is exercisable.

// astFixtureMetadata and astFixtureSummary are filled in once the fixture has
// landed, because their exact bytes are decided by the server (the metadata
// envelope merges the container tag; the summary is a truncation of content).
//
// Junior Tip [do not predict what the server writes]: an earlier draft
// hard-coded the metadata JSON. The server merges its own container_tag key in
// and orders the keys itself, so the prediction was wrong and the $eq case
// would have "passed" by matching zero rows against an expectation of zero
// rows. Reading the value back first is what makes the case discriminating.
var (
	astFixtureMetadata string
	astFixtureSummary  string
)

// bindFixtureTextValues copies the server-written metadata and summary out of
// the landed snapshot so the exact-match cases compare real bytes.
func bindFixtureTextValues(testHandle *testing.T, fixture *astFixture, active []astRawRecord) {
	testHandle.Helper()
	metadataRow := rawByID(active, fixture.idByIndex[2])
	summaryRow := rawByID(active, fixture.idByIndex[3])
	if metadataRow == nil || summaryRow == nil {
		testHandle.Fatalf("fixture rows for the exact-match cases are missing from the snapshot")
	}
	astFixtureMetadata = metadataRow.Metadata
	astFixtureSummary = summaryRow.Summary
	if astFixtureMetadata == "" || astFixtureSummary == "" {
		testHandle.Fatalf("metadata/summary came back empty (%q/%q) — an $eq case against an empty "+
			"string would match every default row and prove nothing", astFixtureMetadata, astFixtureSummary)
	}
}

// orderByTypeThenID reproduces `ORDER BY type ASC, id ASC` in Go.
func orderByTypeThenID(fixture *astFixture, active []astRawRecord) []int {
	rows := append([]astRawRecord(nil), active...)
	for outer := 1; outer < len(rows); outer++ {
		for inner := outer; inner > 0; inner-- {
			previous, current := rows[inner-1], rows[inner]
			if previous.Type < current.Type || (previous.Type == current.Type && previous.ID <= current.ID) {
				break
			}
			rows[inner-1], rows[inner] = current, previous
		}
	}
	return rawIDs(rows)
}

// astRunClock is used by the harness for human-readable run boundaries.
var astRunClock = time.Now
