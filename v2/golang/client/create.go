package client

// create.go — the full-fidelity single-record write, POST /api/v1/records with
// caller-controlled type, score, status, related ids, metadata and validity
// window (options in create_options.go), plus its CreateInSession sugar.

import (
	"context"
	"encoding/json"
	"fmt"
)

// Create writes exactly one typed record via POST /api/v1/records (no extraction).
//
// Agent UX — write path: use when you already know type + content. For raw text
// use plain Add(ctx, text) / MCP ingest_memory instead. No satellite LLM job;
// billing is embed-only for this one record.
//
// Caller controls type, score, related_ids, metadata, status, valid_from and
// valid_until through functional options. Bare Create(ctx, sessionUUID, content) defaults
// to episodic / score 5 / saved.
//
//	mem.Create(ctx, "chat-42", "user asked about pricing") // defaults
//	mem.Create(ctx, "chat-42", "Paulo works at Yoven",
//	    client.WithCreateType("fact"),
//	    client.WithCreateScore(9),
//	    client.WithCreateRelatedIDs([]int64{101, 102}),
//	    client.WithCreateValidFrom("2026-01-01T00:00:00Z"),
//	    client.WithCreateValidUntil("2027-01-01T00:00:00Z"))
func (m *Memory) Create(ctx context.Context, sessionUUID, content string, opts ...CreateOption) (*AddResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if sessionUUID == "" {
		return nil, fmt.Errorf("Create: sessionUUID is required")
	}
	if content == "" {
		return nil, ErrEmptyInput
	}

	cfg := &createConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// to the gzip file store. Mirror createRecord's codepoint-safe truncation so
	// the summary column stays index-friendly while content keeps the full text.
	summary := truncateSummary(content)

	// Apply defaults, then let caller overrides win. score 0 and "" type are
	// LEGAL explicit values, so we only override when the option was actually
	// supplied (pointer non-nil) — same three-state rationale as addConfig.
	recordType := "episodic"
	score := 5
	status := "saved"
	relatedIDs := []int64{}
	var extraMetadata map[string]interface{}
	if cfg.memType != nil {
		recordType = *cfg.memType
	}
	if cfg.score != nil {
		score = *cfg.score
	}
	if cfg.status != nil {
		status = *cfg.status
	}
	if cfg.relatedIDs != nil {
		relatedIDs = cfg.relatedIDs
	}
	if cfg.metadata != nil {
		extraMetadata = cfg.metadata
	}

	// valid_from is honoured by the server ONLY as a metadata key on this route,
	// so fold it into the metadata envelope before building the JSON string.
	if cfg.validFrom != "" {
		if extraMetadata == nil {
			extraMetadata = map[string]interface{}{}
		}
		extraMetadata["valid_from"] = cfg.validFrom
	}
	// Same story for the closing bound: service.createRecord falls back to the
	// metadata keys for BOTH halves of the bi-temporal window when the dedicated
	// input fields are empty, and the REST create route never fills them.
	if cfg.validUntil != "" {
		if extraMetadata == nil {
			extraMetadata = map[string]interface{}{}
		}
		extraMetadata["valid_until"] = cfg.validUntil
	}

	payload := map[string]interface{}{
		"uuid":           sessionUUID,
		"type":           recordType,
		"dimension":      0,
		"prefix":         "",
		"weight":         float64(score) / 10,
		"score":          score,
		"vector":         "",
		"related_ids":    relatedIDs,
		"main_ids":       []int64{},
		"consolidate_id": 0,
		"metadata":       buildMetadataJSONWith(m.containerTag, extraMetadata),
		"summary":        summary,
		"content":        content,
		"consolidated":   false,
		"status":         status,
	}

	respBytes, postErr := m.conn.Post(ctx, "/api/v1/records", payload)
	if postErr != nil {
		return nil, postErr
	}

	var resp recordCreateResponse
	if decodeErr := json.Unmarshal(respBytes, &resp); decodeErr != nil {
		return nil, fmt.Errorf("parsing record response: %w", decodeErr)
	}

	return &AddResult{
		ID:        resp.ID,
		SessionID: sessionUUID,
		Records:   []RecordSummary{{ID: resp.ID, Type: recordType, Summary: summary}},
		Status:    "ok",
		Mode:      addModeRecords,
	}, nil
}

// CreateInSession stores `text` directly via POST /api/v1/records as an
// episodic record under the supplied sessionUUID. The session must exist
// (POST /api/v1/sessions first on session-first servers).
func (m *Memory) CreateInSession(ctx context.Context, text string, sessionUUID string) (*AddResult, error) {
	return m.Create(ctx, sessionUUID, text)
}
