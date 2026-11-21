package client

// add.go — the Add write path: plain text goes to POST /api/v1/ingest; a caller
// that pins mode "regular", score, type or metadata goes to POST /api/v1/records.
// Also owns the metadata envelope and summary truncation every record-create
// path (Add and Create) shares.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// buildMetadataJSON wraps containerTag into the canonical metadata JSON
// envelope `{"container_tag":"<tag>"}`. Used by every record-create code path
// so the `metadata` column always holds a valid JSON object — not the raw
// container_tag string.
func buildMetadataJSON(containerTag string) string {
	if containerTag == "" {
		return "{}"
	}
	encoded, marshalErr := json.Marshal(map[string]string{"container_tag": containerTag})
	if marshalErr != nil {
		// json.Marshal of map[string]string is guaranteed to succeed for any
		// valid Go string; treat this as a hard programming error.
		return "{}"
	}
	return string(encoded)
}

// buildMetadataJSONWith merges caller-supplied metadata keys on top of the
// canonical {"container_tag": tag} envelope and returns the JSON string for the
// `metadata` column.
func buildMetadataJSONWith(containerTag string, extra map[string]interface{}) string {
	if len(extra) == 0 {
		return buildMetadataJSON(containerTag)
	}
	merged := make(map[string]interface{}, len(extra)+1)
	for key, value := range extra {
		merged[key] = value
	}
	if containerTag != "" {
		merged["container_tag"] = containerTag
	}
	encoded, marshalErr := json.Marshal(merged)
	if marshalErr != nil {
		// Caller metadata may contain an unmarshalable value (e.g. a channel);
		// fall back to the safe canonical envelope rather than writing garbage.
		return buildMetadataJSON(containerTag)
	}
	return string(encoded)
}

// Add stores raw text via the ingest write path (default).
//
// Session-first servers require CreateSession before Add succeeds.
//
// Agent UX — pick the write path once:
//   - Raw chat/notes → Add(ctx, text) or WithMode("ingest") → POST /ingest
//     (1 episodic + async satellites; extraction LLM billed). MCP: ingest_memory.
//   - Direct episodic record → Add(ctx, text, WithMode("regular")) or
//     Create(...) → POST /records (no extraction). MCP: create_memory.
//
// Trap: WithScore / WithType / WithMetadata also forces /records on ingest mode
// (create path — no extraction). Plain Add(ctx, text) always uses ingest; a
// 404 from /ingest is returned as ErrNotFound, never retried on /records.
//
// AddResult.Mode names the door that ran: "ingest" or "records".
//
//	mem.Add(ctx, "plain text") // ingest
//	mem.Add(ctx, "fact", client.WithMode("regular")) // records path
//	mem.Add(ctx, "fact", client.WithScore(9), client.WithType("fact")) // records path
func (m *Memory) Add(ctx context.Context, text string, opts ...AddOption) (*AddResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if text == "" {
		return nil, ErrEmptyInput
	}

	cfg := &addConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	writeMode := "ingest"
	if cfg.writeMode != "" {
		writeMode = cfg.writeMode
	}

	// When the caller pins score, type, OR metadata, the cloud ingest endpoint
	// would silently drop them — route to the synchronous records path that
	// persists all three.
	//
	forceRecordsPath := writeMode == "regular" ||
		cfg.score != nil || cfg.memType != nil || len(cfg.metadata) > 0

	if forceRecordsPath {
		return m.createRecord(ctx, text, cfg)
	}

	// Junior Tip [no /records fallback, 4.0.0]: until 3.x a 404 from /ingest
	// was remembered for the life of the client and every later Add silently
	// went to /records — skipping extraction — for servers that predated the
	// route. POST /api/v1/ingest is now mounted unconditionally
	// (server/router.go:135), so a 404 here is a real error (wrong URL, proxy)
	// and must reach the caller instead of quietly changing the write path.
	return m.ingest(ctx, text, cfg)
}

// resolveWriteSessionID returns the session uuid for a write.
// Explicit WithSessionID wins (server validates registration). Otherwise the
// client session must already be registered via CreateSession/OpenSession.
func (m *Memory) resolveWriteSessionID(cfg *addConfig) (string, error) {
	if cfg != nil {
		explicitSessionID := strings.TrimSpace(cfg.sessionID)
		if explicitSessionID != "" {
			return explicitSessionID, nil
		}
	}
	if !m.sessionRegistered || m.sessionUUID == "" {
		return "", fmt.Errorf(
			"session_id is required — create a session first (POST /api/v1/sessions)",
		)
	}
	return m.sessionUUID, nil
}

// ingest writes raw text via POST /api/v1/ingest (one episodic + async
// extraction satellites). Every transport or HTTP error is returned as-is.
func (m *Memory) ingest(ctx context.Context, text string, cfg *addConfig) (*AddResult, error) {
	resolvedSessionID, resolveErr := m.resolveWriteSessionID(cfg)
	if resolveErr != nil {
		return nil, resolveErr
	}
	payload := map[string]interface{}{
		"content":       text,
		"container_tag": m.containerTag,
		"session_id":    resolvedSessionID,
	}

	respBytes, postErr := m.conn.Post(ctx, "/api/v1/ingest", payload)
	if postErr != nil {
		return nil, postErr
	}

	var resp ingestResponse
	if unmarshalErr := json.Unmarshal(respBytes, &resp); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing ingest response: %w", unmarshalErr)
	}

	records := resp.Records
	firstID := resp.ID
	if len(records) > 0 {
		firstID = records[0].ID
	}

	return &AddResult{
		ID:        firstID,
		SessionID: resolvedSessionID,
		Records:   records,
		Status:    "ok",
		Mode:      addModeIngest,
	}, nil
}

// createRecord stores text directly via POST /api/v1/records.
//
// Without server-side embedding, we store the text in both summary
// empty — the server handles records without vectors via text search.
//
// Caller-supplied score/type/metadata (via AddOption) override the historical
// defaults of score=5, type="episodic", and the bare container_tag envelope.
func (m *Memory) createRecord(ctx context.Context, text string, cfg *addConfig) (*AddResult, error) {
	summary := truncateSummary(text)
	resolvedSessionID, resolveErr := m.resolveWriteSessionID(cfg)
	if resolveErr != nil {
		return nil, resolveErr
	}

	// Apply defaults, then let caller overrides win. score 0 and type "" are
	// legal explicit values, so we only override when the option was actually
	// supplied (cfg field non-nil) — see addConfig's pointer rationale.
	score := 5
	recordType := "episodic"
	var extraMetadata map[string]interface{}
	if cfg != nil {
		if cfg.score != nil {
			score = *cfg.score
		}
		if cfg.memType != nil {
			recordType = *cfg.memType
		}
		extraMetadata = cfg.metadata
	}

	payload := map[string]interface{}{
		"uuid":           resolvedSessionID,
		"type":           recordType,
		"dimension":      0,
		"prefix":         "",
		"weight":         float64(score) / 10,
		"score":          score,
		"vector":         "",
		"related_ids":    []int{},
		"main_ids":       []int{},
		"consolidate_id": 0,
		"metadata":       buildMetadataJSONWith(m.containerTag, extraMetadata),
		"summary":        summary,
		"content":        text,
		"consolidated":   false,
		"status":         "saved",
	}

	respBytes, postErr := m.conn.Post(ctx, "/api/v1/records", payload)
	if postErr != nil {
		return nil, postErr
	}

	var resp recordCreateResponse
	if unmarshalErr := json.Unmarshal(respBytes, &resp); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing record response: %w", unmarshalErr)
	}

	return &AddResult{
		ID:        resp.ID,
		SessionID: resolvedSessionID,
		Records:   []RecordSummary{{ID: resp.ID, Type: recordType, Summary: summary}},
		Status:    "ok",
		Mode:      addModeRecords,
	}, nil
}

// truncateSummary returns text capped at 200 Unicode code points (runes) with an
// ellipsis when truncated.
func truncateSummary(text string) string {
	const maxSummaryRunes = 200
	runes := []rune(text)
	if len(runes) <= maxSummaryRunes {
		return text
	}
	return string(runes[:maxSummaryRunes]) + "..."
}
