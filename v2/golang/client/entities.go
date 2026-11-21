package client

// entities.go — the Layer-2 entity knowledge graph: listing, searching and
// upserting named entities, their typed temporal edges, BFS graph and timeline
// reads, and the record ↔ entity links.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// ListEntities returns a paginated snapshot of all entities for the tenant,
// ordered by id ASC (stable cursor — pages never shift under concurrent inserts).
//
// Use this instead of SearchEntities with a placeholder query whenever you
// need to walk EVERY entity (cluster discovery, exports, dashboards). The
// returned EntitiesPage carries HasMore + NextOffset so the caller can keep
// paging until exhausted:
//
//	offset := 0
//	for {
//	    page, err := mem.ListEntities(ctx, 200, offset)
//	    if err != nil { return err }
//	    process(page.Entities)
//	    if !page.HasMore { break }
//	    offset = page.NextOffset
//	}
//
// limit is server-clamped to [1, 500]; offset is clamped to >= 0.
func (m *Memory) ListEntities(ctx context.Context, limit, offset int) (*EntitiesPage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	params.Set("offset", strconv.Itoa(offset))

	respBytes, getErr := m.conn.Get(ctx, "/api/v1/entities/list", params)
	if getErr != nil {
		return nil, getErr
	}

	if shapeErr := requireEnvelope(respBytes, "/api/v1/entities/list", "entities"); shapeErr != nil {
		return nil, shapeErr
	}
	var page EntitiesPage
	if decodeErr := json.Unmarshal(respBytes, &page); decodeErr != nil {
		return nil, fmt.Errorf("parsing entities list response: %w", decodeErr)
	}
	return &page, nil
}

// SearchEntities searches named entities (people, organisations, concepts).
func (m *Memory) SearchEntities(ctx context.Context, query, entityType string, limit int) ([]Entity, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	if entityType != "" {
		params.Set("type", entityType)
	}
	params.Set("limit", strconv.Itoa(limit))

	respBytes, err := m.conn.Get(ctx, "/api/v1/entities", params)
	if err != nil {
		return nil, err
	}

	// Junior Tip [envelope only, 4.0.0]: the server always answers
	// {"entities": [...], "count": N} (server/handler/entity.go:254-257); the
	// 3.x bare-array fallback for pre-envelope servers is gone.
	if shapeErr := requireEnvelope(respBytes, "/api/v1/entities", "entities"); shapeErr != nil {
		return nil, shapeErr
	}
	var resp entitiesResponse
	if decodeErr := json.Unmarshal(respBytes, &resp); decodeErr != nil {
		return nil, fmt.Errorf("parsing entities response: %w", decodeErr)
	}
	return resp.Entities, nil
}

// UpsertEntity creates or updates a named entity (idempotent by name).
func (m *Memory) UpsertEntity(ctx context.Context, name, entityType, summary string, attributes map[string]interface{}) (*EntityResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	payload := map[string]interface{}{"name": name}
	if entityType != "" {
		payload["entity_type"] = entityType
	}
	if summary != "" {
		payload["summary"] = summary
	}
	if attributes != nil {
		payload["attributes"] = attributes
	}

	respBytes, err := m.conn.Post(ctx, "/api/v1/entities", payload)
	if err != nil {
		return nil, err
	}

	var result EntityResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing upsert entity response: %w", err)
	}

	return &result, nil
}

// EntityGraph performs BFS traversal of entity relationships.
//
// Starting from an entity, discovers connected entities through typed
// edges (works_at, knows, part_of, etc.).
func (m *Memory) EntityGraph(ctx context.Context, entityID int64, depth int) (*EntityGraphResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	params.Set("depth", strconv.Itoa(depth))

	path := fmt.Sprintf("/api/v1/entities/%d/graph", entityID)
	respBytes, err := m.conn.Get(ctx, path, params)
	if err != nil {
		return nil, err
	}

	var result EntityGraphResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing entity graph response: %w", err)
	}

	return &result, nil
}

// EntityTimeline returns the full temporal history of an entity's
// relationships, including invalidated edges ordered by event time.
func (m *Memory) EntityTimeline(ctx context.Context, entityID int64) (*EntityTimelineResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/entities/%d/timeline", entityID)
	respBytes, err := m.conn.Get(ctx, path, nil)
	if err != nil {
		return nil, err
	}

	var result EntityTimelineResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing entity timeline response: %w", err)
	}

	return &result, nil
}

// UpsertEntityEdge creates or updates a typed relationship between
// two entities with optional temporal metadata.
func (m *Memory) UpsertEntityEdge(ctx context.Context, sourceID, targetID int64, relation string, opts ...EntityEdgeOption) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	payload := map[string]interface{}{
		"source_id": sourceID,
		"target_id": targetID,
		"relation":  relation,
	}

	cfg := &entityEdgeConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.eventTime != "" {
		payload["event_time"] = cfg.eventTime
	}
	if cfg.confidence > 0 {
		payload["confidence"] = cfg.confidence
	}
	if cfg.sourceRecordID > 0 {
		payload["source_record_id"] = cfg.sourceRecordID
	}

	_, err := m.conn.Post(ctx, "/api/v1/entities/edges", payload)
	return err
}

// LinkRecordEntity links a memory record to an entity (cross-layer).
func (m *Memory) LinkRecordEntity(ctx context.Context, recordID, entityID int64, role string) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	payload := map[string]interface{}{
		"record_id": recordID,
		"entity_id": entityID,
	}
	if role != "" {
		payload["role"] = role
	}

	_, err := m.conn.Post(ctx, "/api/v1/entities/link", payload)
	return err
}

// GetRecordEntities returns entities linked to a specific memory record.
func (m *Memory) GetRecordEntities(ctx context.Context, recordID int64) ([]Entity, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	path := fmt.Sprintf("/api/v1/records/%d/entities", recordID)
	respBytes, err := m.conn.Get(ctx, path, nil)
	if err != nil {
		return nil, err
	}

	// The server always answers {"record_id", "entities", "count"}
	// (server/handler/entity.go:563-567); the 3.x bare-array fallback is gone.
	if shapeErr := requireEnvelope(respBytes, "/api/v1/records/{id}/entities", "entities"); shapeErr != nil {
		return nil, shapeErr
	}
	var resp entitiesResponse
	if decodeErr := json.Unmarshal(respBytes, &resp); decodeErr != nil {
		return nil, fmt.Errorf("parsing record entities response: %w", decodeErr)
	}
	return resp.Entities, nil
}
