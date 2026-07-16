// parity_probe exercises the Go SDK Memory surface against a live AnhurDB.
//
//	ANHUR_API_KEY=... ANHUR_URL=https://anhurdb.yoven.ai go run ./scripts/parity_probe/
//
// Optional: ANHUR_TENANT_ID for an isolated tenant (master key).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	anhurdb "github.com/Yoven/AnhurDB-SDK/v2/golang/v2"
	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

func main() {
	apiKey := os.Getenv("ANHUR_API_KEY")
	if apiKey == "" {
		fail("ANHUR_API_KEY required")
	}
	baseURL := envOr("ANHUR_URL", "https://anhurdb.yoven.ai")
	tenantID := os.Getenv("ANHUR_TENANT_ID")
	if tenantID == "" {
		tenantID = fmt.Sprintf("sdk-go-parity-%d", time.Now().Unix())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	opts := []client.Option{
		anhurdb.WithURL(baseURL),
		anhurdb.WithTenantID(tenantID),
		anhurdb.WithTimeout(60 * time.Second),
	}
	mem := anhurdb.NewMemory(apiKey, opts...)
	fmt.Printf("SDK=go URL=%s TENANT=%s\n", baseURL, tenantID)

	failCount := 0
	pass := func(operation, detail string) {
		fmt.Printf("RESULT sdk=go op=%q status=PASS detail=%s\n", operation, detail)
	}
	failOp := func(operation string, opErr error, detail string) {
		failCount++
		fmt.Printf("RESULT sdk=go op=%q status=FAIL err=%v detail=%s\n", operation, opErr, detail)
	}

	// ── Health ────────────────────────────────────────────────────────────
	health, healthErr := mem.Health(ctx)
	if healthErr != nil {
		failOp("Health", healthErr, "")
	} else {
		pass("Health", fmt.Sprintf("%v", health["status"]))
	}

	// ── Write: Add + Create ───────────────────────────────────────────────
	token := time.Now().UnixNano()
	addText := fmt.Sprintf("parity-go: AnhurDB SDK probe token=%d", token)
	addResult, addErr := mem.Add(ctx, addText)
	if addErr != nil || addResult == nil || addResult.ID == 0 {
		failOp("Add", addErr, fmt.Sprintf("result=%+v", addResult))
		os.Exit(1)
	}
	recordID := addResult.ID
	pass("Add", fmt.Sprintf("id=%d", recordID))

	// ── Read ──────────────────────────────────────────────────────────────
	content, readErr := mem.ReadContent(ctx, recordID)
	if readErr != nil || !strings.Contains(content, "parity-go") {
		failOp("ReadContent", readErr, content)
	} else {
		pass("ReadContent", fmt.Sprintf("len=%d", len(content)))
	}

	got, getErr := mem.Get(ctx, recordID)
	if getErr != nil {
		failOp("Get", getErr, "")
	} else {
		pass("Get", fmt.Sprintf("id=%v type=%v", got["id"], got["type"]))
	}

	// Create needs an episodic anchor in the SAME session uuid as the Add record.
	createSession := addResult.SessionID
	if uuidVal, ok := got["uuid"].(string); ok && uuidVal != "" {
		createSession = uuidVal
	}
	if createSession == "" {
		createSession = mem.SessionID()
	}
	createResult, createErr := mem.Create(ctx, createSession,
		fmt.Sprintf("parity-go create fact token=%d", token),
		client.WithCreateType("fact"), client.WithCreateScore(8))
	if createErr != nil {
		failOp("Create", createErr, "")
	} else {
		pass("Create", fmt.Sprintf("id=%d session=%s", createResult.ID, createSession))
	}

	// ── Search family ─────────────────────────────────────────────────────
	searchHits, searchErr := mem.Search(ctx, "AnhurDB SDK probe")
	if searchErr != nil {
		failOp("Search", searchErr, "")
	} else {
		pass("Search", fmt.Sprintf("n=%d", len(searchHits)))
	}

	profile, profileErr := mem.Profile(ctx)
	if profileErr != nil {
		failOp("Profile", profileErr, "")
	} else {
		pass("Profile", fmt.Sprintf("status=%s", profile.Status))
	}

	counts, countErr := mem.CountByType(ctx)
	if countErr != nil {
		failOp("CountByType", countErr, "")
	} else {
		pass("CountByType", fmt.Sprintf("%v", counts))
	}

	sessions, sessErr := mem.ListSessions(ctx)
	if sessErr != nil {
		failOp("ListSessions", sessErr, "")
	} else {
		pass("ListSessions", fmt.Sprintf("n=%d", len(sessions)))
	}

	_, recentErr := mem.Recent(ctx, 5)
	if recentErr != nil {
		failOp("Recent", recentErr, "")
	} else {
		pass("Recent", "ok")
	}

	_, smartErr := mem.SmartSearch(ctx, "AnhurDB", 5)
	if smartErr != nil {
		failOp("SmartSearch", smartErr, "")
	} else {
		pass("SmartSearch", "ok")
	}

	_, recallErr := mem.Recall(ctx, "AnhurDB", 5)
	if recallErr != nil {
		failOp("Recall", recallErr, "")
	} else {
		pass("Recall", "ok")
	}

	// ── Entities: wire + name normalize (unique token — avoid polluting prod names) ─
	entityBase := fmt.Sprintf("paritychrome%d", token)
	firstUpsert, upsertErr := mem.UpsertEntity(ctx, "  "+strings.ToUpper(entityBase[:1])+entityBase[1:]+" ", "product", "parity probe", nil)
	if upsertErr != nil || firstUpsert == nil {
		failOp("UpsertEntity(caseA)", upsertErr, "")
		os.Exit(1)
	}
	// Upsert response entity_type requires AnhurDB handler fix; list/search already emit it.
	pass("UpsertEntity(caseA)", fmt.Sprintf("id=%d type=%s name=%q", firstUpsert.ID, firstUpsert.EntityType, firstUpsert.Name))

	secondUpsert, upsert2Err := mem.UpsertEntity(ctx, strings.ToUpper(entityBase), "product", "parity probe", nil)
	if upsert2Err != nil || secondUpsert == nil {
		failOp("UpsertEntity(caseB)", upsert2Err, "")
	} else if secondUpsert.ID != firstUpsert.ID {
		failOp("UpsertEntity.dedup", fmt.Errorf("id mismatch %d vs %d", firstUpsert.ID, secondUpsert.ID),
			"server must NormalizeEntityName — redeploy AnhurDB if this fails")
	} else {
		pass("UpsertEntity.dedup", fmt.Sprintf("same_id=%d name=%q", secondUpsert.ID, secondUpsert.Name))
	}

	if linkErr := mem.LinkRecordEntity(ctx, recordID, firstUpsert.ID, "mentions"); linkErr != nil {
		failOp("LinkRecordEntity", linkErr, "")
	} else {
		pass("LinkRecordEntity", "ok")
	}

	linked, linkListErr := mem.GetRecordEntities(ctx, recordID)
	if linkListErr != nil {
		failOp("GetRecordEntities", linkListErr, "")
	} else {
		typeOK := false
		for _, entity := range linked {
			if entity.EntityType != "" {
				typeOK = true
				break
			}
		}
		if !typeOK && len(linked) > 0 {
			failOp("GetRecordEntities.entity_type", fmt.Errorf("all EntityType empty"), "json tag bug")
		} else {
			pass("GetRecordEntities", fmt.Sprintf("n=%d type_ok=%v", len(linked), typeOK || len(linked) == 0))
		}
	}

	listed, listErr := mem.ListEntities(ctx, 50, 0)
	if listErr != nil {
		failOp("ListEntities", listErr, "")
	} else {
		nonEmptyType := 0
		for _, entity := range listed.Entities {
			if entity.EntityType != "" {
				nonEmptyType++
			}
		}
		if len(listed.Entities) > 0 && nonEmptyType == 0 {
			failOp("ListEntities.entity_type", fmt.Errorf("all empty"), "SDK json tag")
		} else {
			pass("ListEntities", fmt.Sprintf("n=%d with_type=%d", len(listed.Entities), nonEmptyType))
		}
	}

	found, searchEntErr := mem.SearchEntities(ctx, entityBase, "product", 10)
	if searchEntErr != nil {
		failOp("SearchEntities", searchEntErr, "")
	} else {
		pass("SearchEntities", fmt.Sprintf("n=%d", len(found)))
	}

	other, otherErr := mem.UpsertEntity(ctx, fmt.Sprintf("parity-org-%d", token), "organization", "", nil)
	if otherErr != nil {
		failOp("UpsertEntity(org)", otherErr, "")
	} else {
		if edgeErr := mem.UpsertEntityEdge(ctx, firstUpsert.ID, other.ID, "related_to",
			client.WithConfidence(1.0)); edgeErr != nil {
			failOp("UpsertEntityEdge", edgeErr, "")
		} else {
			pass("UpsertEntityEdge", "ok")
		}
		graph, graphErr := mem.EntityGraph(ctx, firstUpsert.ID, 2)
		if graphErr != nil {
			failOp("EntityGraph", graphErr, "")
		} else {
			pass("EntityGraph", fmt.Sprintf("nodes=%d", len(graph.Nodes)))
		}
		_, timelineErr := mem.EntityTimeline(ctx, firstUpsert.ID)
		if timelineErr != nil {
			failOp("EntityTimeline", timelineErr, "")
		} else {
			pass("EntityTimeline", "ok")
		}
	}

	types := mem.ListTypes()
	pass("ListTypes", fmt.Sprintf("n=%d", len(types)))

	_, groundErr := mem.GetGrounding(ctx, recordID, 2)
	if groundErr != nil {
		failOp("GetGrounding", groundErr, "")
	} else {
		pass("GetGrounding", "ok")
	}

	if failCount > 0 {
		fmt.Printf("SUMMARY sdk=go FAIL count=%d\n", failCount)
		os.Exit(1)
	}
	fmt.Println("SUMMARY sdk=go PASS")
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "FAIL:", message)
	os.Exit(1)
}
