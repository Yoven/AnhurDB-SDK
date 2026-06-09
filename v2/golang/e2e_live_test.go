//go:build e2e

// Package anhurdb e2e: exercises the Go SDK against a LIVE AnhurDB at
// localhost:8000. Run with: go test -tags e2e -run TestLiveE2E -v
//
// Junior Tip: gated behind the `e2e` build tag so the normal unit-test
// suite (which must not require a running server) is unaffected.
package anhurdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/anhurdb/sdk-go/v2/client"
	"github.com/anhurdb/sdk-go/v2/models"
)

const (
	liveURL    = "http://localhost:8000"
	liveAPIKey = "019c9f5b-d3cb-74af-9f01-5b761aaf7245"
)

// step logs a PASS/FAIL line in a grep-friendly format.
func step(testHandle *testing.T, operation string, opErr error, detail string) {
	if opErr != nil {
		testHandle.Errorf("RESULT op=%q status=FAIL err=%v detail=%s", operation, opErr, detail)
		return
	}
	fmt.Printf("RESULT op=%q status=PASS detail=%s\n", operation, detail)
}

func TestLiveE2E(testHandle *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Fresh, isolated tenant per run so we never collide with existing data.
	tenant := fmt.Sprintf("sdk-go-e2e-%d", time.Now().Unix())
	fmt.Fprintf(os.Stdout, "TENANT=%s\n", tenant)

	mem := NewMemory(liveAPIKey,
		WithURL(liveURL),
		WithTenantID(tenant),
		WithTimeout(60*time.Second),
	)
	fmt.Printf("CLIENT=%s\n", mem.String())

	// --- OP 1: Add (create a record) ---
	uniqueText := fmt.Sprintf("Paulo is a backend engineer who works on AnhurDB in Go. token=%d", time.Now().UnixNano())
	// Retry to absorb transient Raft not_leader (HTTP 500); the SDK itself does
	// NOT retry on the not_leader hint, so the caller must.
	var addResult *client.AddResult
	var addErr error
	for attempt := 1; attempt <= 8; attempt++ {
		addResult, addErr = mem.Add(ctx, uniqueText)
		if addErr == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	detail := ""
	if addResult != nil {
		detail = fmt.Sprintf("id=%d mode=%s status=%s records=%d", addResult.ID, addResult.Mode, addResult.Status, len(addResult.Records))
	}
	step(testHandle, "Add", addErr, detail)
	if addErr != nil || addResult == nil || addResult.ID == 0 {
		testHandle.Fatalf("Add did not yield a usable record id; aborting dependent ops. result=%+v", addResult)
	}
	recordID := addResult.ID

	// --- OP 2: ReadContent (read it back) ---
	// Junior Tip: reads are load-balanced across cluster nodes, so a content
	// read fired immediately after a write may hit a follower that hasn't yet
	// applied the Raft log entry (read-after-write lag). Retry to absorb that.
	var content string
	var readErr error
	for attempt := 1; attempt <= 6; attempt++ {
		content, readErr = mem.ReadContent(ctx, recordID)
		if readErr == nil && content != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	step(testHandle, "ReadContent", readErr, fmt.Sprintf("id=%d len=%d body=%q", recordID, len(content), truncate(content, 80)))

	// --- OP 2b: RecentMemories (alternate read-back path) ---
	// Retry: RecentMemories has a parse bug on EMPTY manifests (object vs
	// bare-array), and a follower read may transiently return empty. Retrying
	// lets a populated read succeed; if it never populates the parse bug surfaces.
	var recent []models.Record
	var recentErr error
	foundRecent := false
	for attempt := 1; attempt <= 6; attempt++ {
		recent, recentErr = mem.RecentMemories(ctx, 5)
		for _, rec := range recent {
			if int64(rec.ID) == recordID {
				foundRecent = true
			}
		}
		if recentErr == nil && foundRecent {
			break
		}
		time.Sleep(2 * time.Second)
	}
	step(testHandle, "RecentMemories", recentErr, fmt.Sprintf("count=%d contains_new=%v", len(recent), foundRecent))

	// --- OP 3: Search (give async indexing a chance, retry a few times) ---
	var hits []client.SearchResult
	var searchErr error
	var found bool
	for attempt := 1; attempt <= 6; attempt++ {
		hits, searchErr = mem.Search(ctx, "backend engineer AnhurDB Go", WithLimit(10))
		if searchErr != nil {
			break
		}
		for _, hit := range hits {
			if hit.ID == recordID {
				found = true
			}
		}
		if len(hits) > 0 {
			break
		}
		time.Sleep(3 * time.Second)
	}
	step(testHandle, "Search", searchErr, fmt.Sprintf("hits=%d found_new=%v", len(hits), found))

	// --- OP 4: UpsertEntity (create entity) ---
	entName := fmt.Sprintf("AnhurDB-%d", time.Now().Unix())
	// Retry to absorb transient Raft not_leader (HTTP 500) during leadership flap.
	var entResult *client.EntityResult
	var entErr error
	for attempt := 1; attempt <= 6; attempt++ {
		entResult, entErr = mem.UpsertEntity(ctx, entName, "org", "Cognitive memory DB", map[string]interface{}{"lang": "go"})
		if entErr == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	entDetail := ""
	if entResult != nil {
		entDetail = fmt.Sprintf("id=%d name=%s status=%s", entResult.ID, entResult.Name, entResult.Status)
	}
	step(testHandle, "UpsertEntity", entErr, entDetail)

	// --- OP 5: SearchEntities (query entity back) ---
	if entErr == nil && entResult != nil {
		var entities []client.Entity
		var entSearchErr error
		var entFound bool
		for attempt := 1; attempt <= 4; attempt++ {
			entities, entSearchErr = mem.SearchEntities(ctx, entName, "", 20)
			if entSearchErr != nil {
				break
			}
			for _, ent := range entities {
				if ent.ID == entResult.ID || ent.Name == entName {
					entFound = true
				}
			}
			if entFound {
				break
			}
			time.Sleep(2 * time.Second)
		}
		step(testHandle, "SearchEntities", entSearchErr, fmt.Sprintf("results=%d found_new=%v", len(entities), entFound))

		// --- OP 6: LinkRecordEntity (cross-layer link) ---
		linkErr := mem.LinkRecordEntity(ctx, recordID, entResult.ID, "mentions")
		step(testHandle, "LinkRecordEntity", linkErr, fmt.Sprintf("record=%d entity=%d", recordID, entResult.ID))

		// --- OP 7: GetRecordEntities (verify the link) ---
		// Retry to absorb read-after-write lag on the cross-layer link.
		var linkedEnts []client.Entity
		var getEntErr error
		linkFound := false
		for attempt := 1; attempt <= 6; attempt++ {
			linkedEnts, getEntErr = mem.GetRecordEntities(ctx, recordID)
			for _, ent := range linkedEnts {
				if ent.ID == entResult.ID {
					linkFound = true
				}
			}
			if getEntErr == nil && linkFound {
				break
			}
			time.Sleep(2 * time.Second)
		}
		step(testHandle, "GetRecordEntities", getEntErr, fmt.Sprintf("count=%d link_present=%v", len(linkedEnts), linkFound))
	}

	// --- OP 8: ListSessions ---
	sessions, sessErr := mem.ListSessions(ctx)
	step(testHandle, "ListSessions", sessErr, fmt.Sprintf("count=%d", len(sessions)))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// liveRecordView is the subset of GET /api/v1/records/{id} we assert on:
// proving the caller-supplied score and type actually landed in the DB.
type liveRecordView struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Score int    `json:"score"`
}

// getRecordRaw reads a single record directly over HTTP (the SDK does not
// expose a typed score field, so we parse the wire JSON here). Retries to
// absorb read-after-write lag from load-balanced follower reads.
//
// Junior Tip [tenant-scoped IDs — verified 2026-06-09]: record IDs are scoped
// per X-Tenant-ID. A GET for id=1 WITHOUT the tenant header returns a totally
// different record (a different tenant's id=1). The readback MUST carry the
// same tenant the write used, or the score assertion checks the wrong row.
func getRecordRaw(ctx context.Context, recordID int64, tenant string) (*liveRecordView, error) {
	url := fmt.Sprintf("%s/api/v1/records/%d", liveURL, recordID)
	var lastErr error
	for attempt := 1; attempt <= 8; attempt++ {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		request.Header.Set("X-API-Key", liveAPIKey)
		request.Header.Set("X-Tenant-ID", tenant)
		response, doErr := http.DefaultClient.Do(request)
		if doErr != nil {
			lastErr = doErr
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		if response.StatusCode == http.StatusOK {
			var view liveRecordView
			decodeErr := json.NewDecoder(response.Body).Decode(&view)
			response.Body.Close()
			if decodeErr == nil && view.ID == recordID {
				return &view, nil
			}
			lastErr = decodeErr
		} else {
			response.Body.Close()
			lastErr = fmt.Errorf("status %d", response.StatusCode)
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return nil, lastErr
}

// TestLiveAddScoreType proves Bug 1 (parity): Add with WithScore + WithType
// persists those exact values, verified by reading the record back from the
// live DB. Run with: go test -tags e2e -run TestLiveAddScoreType -v
func TestLiveAddScoreType(testHandle *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	tenant := fmt.Sprintf("sdk-go-scoretype-%d", time.Now().Unix())
	fmt.Fprintf(os.Stdout, "TENANT=%s\n", tenant)

	mem := NewMemory(liveAPIKey,
		WithURL(liveURL),
		WithTenantID(tenant),
		WithTimeout(60*time.Second),
	)

	// Use type "episodic": Add routes OSS-fallback to /api/v1/records, and an
	// episodic record needs no pre-existing anchor. The score 8 is the load-
	// bearing assertion (default is 5, so a wrong value is unmistakable).
	const wantScore = 8
	const wantType = "episodic"
	uniqueText := fmt.Sprintf("score/type parity probe token=%d", time.Now().UnixNano())

	addResult, addErr := mem.Add(ctx, uniqueText,
		WithScore(wantScore),
		WithType(wantType),
		WithMetadata(map[string]interface{}{"probe": "scoretype"}),
	)
	detail := ""
	if addResult != nil {
		detail = fmt.Sprintf("id=%d mode=%s", addResult.ID, addResult.Mode)
	}
	step(testHandle, "Add(score+type)", addErr, detail)
	if addErr != nil || addResult == nil || addResult.ID == 0 {
		testHandle.Fatalf("Add did not yield a record id; result=%+v err=%v", addResult, addErr)
	}

	view, readErr := getRecordRaw(ctx, addResult.ID, tenant)
	step(testHandle, "GET record raw", readErr, fmt.Sprintf("id=%d", addResult.ID))
	if readErr != nil {
		testHandle.Fatalf("could not read record %d back: %v", addResult.ID, readErr)
	}

	if view.Score != wantScore {
		testHandle.Errorf("RESULT op=%q status=FAIL got_score=%d want_score=%d", "verify score", view.Score, wantScore)
	} else {
		fmt.Printf("RESULT op=%q status=PASS score=%d\n", "verify score", view.Score)
	}
	if view.Type != wantType {
		testHandle.Errorf("RESULT op=%q status=FAIL got_type=%q want_type=%q", "verify type", view.Type, wantType)
	} else {
		fmt.Printf("RESULT op=%q status=PASS type=%q\n", "verify type", view.Type)
	}
}
