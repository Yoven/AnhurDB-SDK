// l2_scope.go — the "L2/ADR-0033" section of the Go live parity probe.
//
// One domain in one sentence: "upload one throwaway file into the tenant
// library and walk it through every by-id door with an explicit scope, then
// prove the chat plane cannot see it and delete it by file".
//
// The Python (probe_python_l2.py) and TypeScript (probe_typescript_l2.ts)
// probes run the SAME steps with the SAME op names; run_all.sh diffs the
// RESULT lines of the three into one PASS/FAIL matrix. Renaming an op here
// without renaming it there is a parity break the matrix will report.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/client"
)

// l2Reporter carries the probe's three verdict printers into the section.
type l2Reporter struct {
	pass   func(operation, detail string)
	failOp func(operation string, opErr error, detail string)
	skip   func(operation, detail string)
}

// l2SearchBudget / l2ReadBackBudget bound the two eventually-consistent waits.
// Junior Tip [why poll at all]: the library hit appears only after the
// file_ingestor wrote its root/chapters AND the search index caught up; a
// PATCH is applied through Raft and a load-balanced read may land on a node
// one index behind. One read would turn replication lag into a false FAIL.
const (
	l2UploadWaitBudget = 180 * time.Second
	l2SearchBudget     = 120 * time.Second
	l2SearchInterval   = 3 * time.Second
	l2ReadBackBudget   = 20 * time.Second
	l2ReadBackInterval = 2 * time.Second
)

// runL2Section runs the L2/ADR-0033 steps and reports each one exactly once.
// A step whose prerequisite failed is reported FAIL "blocked" (never omitted),
// so every SDK prints the same op set and the matrix stays rectangular.
func runL2Section(mem *client.Memory, token int64, reporter l2Reporter) {
	sectionContext, cancelSection := context.WithTimeout(context.Background(), 420*time.Second)
	defer cancelSection()
	blocked := func(operation string) {
		reporter.failOp(operation, errors.New("blocked"), "prior L2 step failed")
	}

	// Junior Tip [the file identity is computed, not looked up]: the server
	// hashes the uploaded bytes with SHA-256 (service/upload_core.go) and the
	// file_ingestor keys every root/chapter as "<first 16 hex>:...". Hashing the
	// same bytes here gives the delete-by-file prefix without trusting any
	// response field to carry it.
	fileText := fmt.Sprintf("parity-l2-go: AnhurDB L2 tenant library probe token=%d\n"+
		"This throwaway document proves by-id scope on the tenant library plane.\n", token)
	fileBytes := []byte(fileText)
	contentDigest := sha256.Sum256(fileBytes)
	ingestKeyPrefix := hex.EncodeToString(contentDigest[:])[:16]

	uploadResult, uploadErr := mem.UploadFile(sectionContext, fmt.Sprintf("parity-l2-go-%d.txt", token),
		fileBytes, "", 0, client.WithUploadMode("tenant_shared"))
	if uploadErr != nil || uploadResult == nil || uploadResult.RecordID <= 0 {
		reporter.failOp("L2.UploadTenantShared", uploadErr, fmt.Sprintf("result=%+v", uploadResult))
		for _, operation := range l2DependentOps {
			blocked(operation)
		}
		reportSessionCapSkip(reporter)
		return
	}
	reporter.pass("L2.UploadTenantShared", fmt.Sprintf("record_id=%d prefix=%s", uploadResult.RecordID, ingestKeyPrefix))

	if uploadResult.Scope == client.RecordScopeTenantShared {
		reporter.pass("L2.UploadScope", string(uploadResult.Scope))
	} else {
		reporter.failOp("L2.UploadScope", fmt.Errorf("scope=%q", uploadResult.Scope), "want tenant_shared")
	}

	finalStatus, waitErr := mem.WaitForUpload(sectionContext, uploadResult.RecordID, client.WaitUploadOptions{
		Timeout: l2UploadWaitBudget, Scope: uploadResult.Scope,
	})
	if waitErr != nil || finalStatus == nil || strings.EqualFold(finalStatus.Status, "failed") {
		reporter.failOp("L2.UploadStatusScoped", waitErr, fmt.Sprintf("status=%+v", finalStatus))
	} else {
		reporter.pass("L2.UploadStatusScoped", "status="+finalStatus.Status)
	}

	libraryHit, searchErr := findLibraryHit(sectionContext, mem, ingestKeyPrefix)
	if searchErr != nil {
		reporter.failOp("L2.SearchTenantShared", searchErr, "no hit whose metadata carries the file prefix")
		for _, operation := range l2DependentOps[3:8] {
			blocked(operation)
		}
		runDeleteByFile(sectionContext, mem, ingestKeyPrefix, reporter)
		reportSessionCapSkip(reporter)
		return
	}
	hitID := int64(libraryHit.ID)
	reporter.pass("L2.SearchTenantShared", fmt.Sprintf("id=%d uuid=%s", hitID, libraryHit.UUID))

	hitScope := client.TenantSharedScopeForHit(libraryHit.UUID)
	if hitScope == client.RecordScopeTenantShared {
		reporter.pass("L2.HitScope", string(hitScope))
	} else {
		reporter.failOp("L2.HitScope", fmt.Errorf("scope=%q", hitScope), "fresh upload must land in the _lib store")
	}

	scopedRecord, getErr := mem.Get(sectionContext, hitID, client.WithRecordScope(hitScope))
	if getErr != nil || recordIDOf(scopedRecord) != hitID || fmt.Sprint(scopedRecord["uuid"]) != libraryHit.UUID {
		reporter.failOp("L2.GetScoped", getErr, fmt.Sprintf("id=%v uuid=%v", scopedRecord["id"], scopedRecord["uuid"]))
	} else {
		reporter.pass("L2.GetScoped", fmt.Sprintf("id=%d", hitID))
	}

	newSummary := fmt.Sprintf("parity-l2 summary updated token=%d", token)
	if updateErr := mem.Update(sectionContext, hitID, map[string]interface{}{"summary": newSummary},
		client.WithRecordScope(hitScope)); updateErr != nil {
		reporter.failOp("L2.UpdateSummaryScoped", updateErr, "")
		blocked("L2.ReadBackSummary")
	} else {
		reporter.pass("L2.UpdateSummaryScoped", "ok")
		readBackSummary, readBackErr := waitForSummary(sectionContext, mem, hitID, hitScope, newSummary)
		if readBackErr != nil {
			reporter.failOp("L2.ReadBackSummary", readBackErr, "last="+readBackSummary)
		} else {
			reporter.pass("L2.ReadBackSummary", "match")
		}
	}

	// Junior Tip [leak check BEFORE the delete]: after delete-by-file the
	// library row is tombstoned, so a sessions read could "pass" only because
	// there is nothing left to leak. Checking while the file is alive is the
	// check that can actually fail.
	reportNoLeak(sectionContext, mem, hitID, libraryHit.UUID, newSummary, reporter)

	runDeleteByFile(sectionContext, mem, ingestKeyPrefix, reporter)
	reportSessionCapSkip(reporter)
}

// l2DependentOps lists, in print order, the ops after L2.UploadTenantShared
// that need the upload; a failed upload reports each of them as blocked.
var l2DependentOps = []string{
	"L2.UploadScope", "L2.UploadStatusScoped",
	"L2.SearchTenantShared", "L2.HitScope", "L2.GetScoped", "L2.UpdateSummaryScoped",
	"L2.ReadBackSummary", "L2.NoLeakSessions", "L2.DeleteByFile",
}

// findLibraryHit polls SearchTenantShared until a hit's metadata carries the
// file's ingest-key prefix (root/chapter ingest_key, or the file row checksum).
func findLibraryHit(ctx context.Context, mem *client.Memory, ingestKeyPrefix string) (*libraryHitRef, error) {
	deadline := time.Now().Add(l2SearchBudget)
	var lastErr error
	for {
		searchHits, searchErr := mem.SearchTenantShared(ctx, "AnhurDB L2 tenant library probe", client.SessionsAll())
		lastErr = searchErr
		for _, searchHit := range searchHits {
			if searchHit.Record.ID > 0 && strings.Contains(searchHit.Record.Metadata, ingestKeyPrefix) {
				return &libraryHitRef{ID: searchHit.Record.ID, UUID: searchHit.Record.UUID}, nil
			}
		}
		if time.Now().Add(l2SearchInterval).After(deadline) {
			if lastErr == nil {
				lastErr = fmt.Errorf("no matching hit within %s (last n=%d)", l2SearchBudget, len(searchHits))
			}
			return nil, lastErr
		}
		time.Sleep(l2SearchInterval)
	}
}

// libraryHitRef is the (id, session uuid) pair every later by-id call needs.
type libraryHitRef struct {
	ID   int
	UUID string
}

// waitForSummary re-reads the record (scoped) until its summary equals want.
func waitForSummary(ctx context.Context, mem *client.Memory, recordID int64, scope client.RecordScope, want string) (string, error) {
	deadline := time.Now().Add(l2ReadBackBudget)
	lastSummary := ""
	for {
		readBack, readErr := mem.Get(ctx, recordID, client.WithRecordScope(scope))
		if readErr == nil {
			lastSummary = fmt.Sprint(readBack["summary"])
			if lastSummary == want {
				return lastSummary, nil
			}
		}
		if time.Now().Add(l2ReadBackInterval).After(deadline) {
			if readErr != nil {
				return lastSummary, readErr
			}
			return lastSummary, fmt.Errorf("summary not updated within %s", l2ReadBackBudget)
		}
		time.Sleep(l2ReadBackInterval)
	}
}

// reportNoLeak reads the library hit's id on the CHAT plane. PASS = 404, or a
// different record (ids are per store, so the chat store may own the same
// number). FAIL = the answer is the library file (same uuid or our summary).
func reportNoLeak(ctx context.Context, mem *client.Memory, recordID int64, libraryUUID, newSummary string, reporter l2Reporter) {
	sessionsRecord, sessionsErr := mem.Get(ctx, recordID, client.WithRecordScope(client.RecordScopeSessions))
	switch {
	case sessionsErr != nil && errors.Is(sessionsErr, client.ErrNotFound):
		reporter.pass("L2.NoLeakSessions", "not_found")
	case sessionsErr != nil:
		reporter.failOp("L2.NoLeakSessions", sessionsErr, "want 404 or another record")
	case fmt.Sprint(sessionsRecord["uuid"]) == libraryUUID || fmt.Sprint(sessionsRecord["summary"]) == newSummary:
		reporter.failOp("L2.NoLeakSessions", errors.New("cross-plane leak"),
			fmt.Sprintf("uuid=%v", sessionsRecord["uuid"]))
	default:
		reporter.pass("L2.NoLeakSessions", fmt.Sprintf("other_record uuid=%v", sessionsRecord["uuid"]))
	}
}

// runDeleteByFile deletes the file from the tenant library (both stores) and
// asserts the answer names its legs and removed at least one row.
func runDeleteByFile(ctx context.Context, mem *client.Memory, ingestKeyPrefix string, reporter l2Reporter) {
	deleteResult, deleteErr := mem.DeleteFile(ctx, "", ingestKeyPrefix,
		client.WithDeleteFileScope(client.RecordScopeTenantShared))
	if deleteErr != nil || deleteResult == nil {
		reporter.failOp("L2.DeleteByFile", deleteErr, "")
		return
	}
	legErrors := 0
	for _, deleteLeg := range deleteResult.Legs {
		if deleteLeg.Error != "" {
			legErrors++
		}
	}
	detail := fmt.Sprintf("legs=%d deleted=%d leg_errors=%d", len(deleteResult.Legs), deleteResult.DeletedCount, legErrors)
	if len(deleteResult.Legs) == 0 || deleteResult.DeletedCount < 1 || legErrors > 0 {
		reporter.failOp("L2.DeleteByFile", errors.New("want legs>=1, deleted>=1, no leg error"), detail)
		return
	}
	reporter.pass("L2.DeleteByFile", detail)
}

// reportSessionCapSkip records why the session_record_limit 409 is not driven.
// Junior Tip [do not invent the door]: the per-tenant cap is set only through
// POST /internal/admin/config (MasterOnly, server router.go) and no SDK exposes
// it, so a probe running with a tenant key cannot lower it.
func reportSessionCapSkip(reporter l2Reporter) {
	reporter.skip("L2.SessionRecordLimit", "SKIP session_record_limit: needs admin")
}

// recordIDOf reads the numeric "id" of a decoded record map (JSON numbers
// decode to float64).
func recordIDOf(record map[string]interface{}) int64 {
	if numericID, isNumber := record["id"].(float64); isNumber {
		return int64(numericID)
	}
	return 0
}
