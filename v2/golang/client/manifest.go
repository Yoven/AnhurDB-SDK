package client

// manifest.go — the record LISTINGS (not searches): newest-N (Recent), the
// paginated global and per-session manifests, and the whole-chat listing. All of
// them answer with the {"records": [...], "count": N, ...} envelope; this file
// owns decoding it (including the server's "records": null for zero hits).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/models"
)

// ManifestPage is the paginated envelope returned by ManifestGlobal and
// ManifestSession (GET /api/v1/manifest and /api/v1/chats/{uuid}/manifest):
// a page of records plus the server's pagination cursor.
type ManifestPage struct {
	Records []models.Record `json:"records"`
	Count   int             `json:"count"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
	HasMore bool            `json:"has_more"`
}

// Recent retrieves the most recent records.
//
// GET /api/v1/recent?limit=N hits the server's dedicated ListRecent endpoint,
// which returns records ordered by creation time (newest first).
func (m *Memory) Recent(ctx context.Context, limit int) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))

	respBytes, getErr := m.conn.Get(ctx, "/api/v1/recent", params)
	if getErr != nil {
		return nil, getErr
	}
	return decodeRecordsEnvelope(respBytes, "/api/v1/recent")
}

// ManifestGlobal returns a paginated page of the tenant's full record manifest
// via GET /api/v1/manifest, with the complete envelope (records + pagination
// cursor). Use this when you need has_more / effective limit / offset; use
// Recent when you only want the newest-N records as a flat slice.
//
// (WithAsOf/WithSince/WithUntil) scope created_at; as_of is mutually exclusive
// with since/until (the server returns HTTP 400 on violation — we surface it).
func (m *Memory) ManifestGlobal(ctx context.Context, keyword string, limit, offset int, opts ...ReadOption) (*ManifestPage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	if keyword != "" {
		params.Set("q", keyword)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	applyTemporalParams(params, cfg)

	respBytes, getErr := m.conn.Get(ctx, "/api/v1/manifest", params)
	if getErr != nil {
		return nil, getErr
	}
	return decodeManifestPage(respBytes, "/api/v1/manifest")
}

// ManifestSession returns a paginated page of one session's record manifest via
// GET /api/v1/chats/{uuid}/manifest. Same envelope as ManifestGlobal, scoped to
// the session.
func (m *Memory) ManifestSession(ctx context.Context, sessionUUID, keyword string, limit, offset int, opts ...ReadOption) (*ManifestPage, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if sessionUUID == "" {
		return nil, fmt.Errorf("ManifestSession: sessionUUID is required")
	}

	cfg := applyReadOptions(opts)

	params := url.Values{}
	if keyword != "" {
		params.Set("q", keyword)
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	applyTemporalParams(params, cfg)

	path := fmt.Sprintf("/api/v1/chats/%s/manifest", url.PathEscape(sessionUUID))
	respBytes, getErr := m.conn.Get(ctx, path, params)
	if getErr != nil {
		return nil, getErr
	}
	return decodeManifestPage(respBytes, "/api/v1/chats/{uuid}/manifest")
}

// ListChat returns every record for a single chat/session via GET
// /api/v1/chats/{uuid} — metadata only (no .gz body), no pagination. It is the
// MCP list_chat contract: the whole matching set for the session in one call.
//
// consolidated is a tri-state filter:
//   - nil      → no filter (all records for the session)
//   - &true    → only consolidated records
//   - &false   → only non-consolidated records
//
// status (optional, "" = no filter) is an exact status match
// (e.g. "saved","processing","failed").
func (m *Memory) ListChat(ctx context.Context, sessionUUID string, consolidated *bool, status string) ([]models.Record, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	if sessionUUID == "" {
		return nil, fmt.Errorf("ListChat: sessionUUID is required")
	}

	params := url.Values{}
	if consolidated != nil {
		params.Set("consolidated", strconv.FormatBool(*consolidated))
	}
	if status != "" {
		params.Set("status", status)
	}

	path := fmt.Sprintf("/api/v1/chats/%s", url.PathEscape(sessionUUID))
	respBytes, getErr := m.conn.Get(ctx, path, params)
	if getErr != nil {
		return nil, getErr
	}
	return decodeRecordsEnvelope(respBytes, "/api/v1/chats/{uuid}")
}

// applyTemporalParams folds the optional bi-temporal read filters
// (as_of / since / until) from a resolved searchConfig into a url.Values. Kept
// in one place so every temporal-aware read (the manifests) emits identical
// query keys; the server enforces the as_of-vs-since/until exclusivity and
// returns HTTP 400 on a violation, which the caller surfaces.
func applyTemporalParams(params url.Values, cfg searchConfig) {
	if cfg.asOf != "" {
		params.Set("as_of", cfg.asOf)
	}
	if cfg.since != "" {
		params.Set("since", cfg.since)
	}
	if cfg.until != "" {
		params.Set("until", cfg.until)
	}
}

// decodeManifestPage decodes the manifest envelope shared by ManifestGlobal and
// ManifestSession: {"records":[Record],"count":int,"limit":int,"offset":int,
// "has_more":bool}. route is the route template, for the error message.
//
// Junior Tip [envelope only, 4.0.0]: requireEnvelope (envelope.go) refuses a
// bare array, an empty body or a missing "records" key with a typed
// *UnexpectedResponseError; "records": null (the server's zero-hit answer)
// still becomes an empty slice.
func decodeManifestPage(raw []byte, route string) (*ManifestPage, error) {
	if shapeErr := requireEnvelope(raw, route, "records"); shapeErr != nil {
		return nil, shapeErr
	}
	var page ManifestPage
	if parseErr := json.Unmarshal(raw, &page); parseErr != nil {
		return nil, fmt.Errorf("parsing %s response: %w", route, parseErr)
	}
	if page.Records == nil {
		page.Records = []models.Record{}
	}
	return &page, nil
}

// decodeRecordsEnvelope decodes a {"records":[Record],"count":int} envelope (no
// pagination) — used by Recent and ListChat. Same discipline as
// decodeManifestPage.
func decodeRecordsEnvelope(raw []byte, route string) ([]models.Record, error) {
	if shapeErr := requireEnvelope(raw, route, "records"); shapeErr != nil {
		return nil, shapeErr
	}
	var wrapped queryResponse // identical shape: {records, count}
	if parseErr := json.Unmarshal(raw, &wrapped); parseErr != nil {
		return nil, fmt.Errorf("parsing %s response: %w", route, parseErr)
	}
	if wrapped.Records == nil {
		return []models.Record{}, nil
	}
	return wrapped.Records, nil
}
