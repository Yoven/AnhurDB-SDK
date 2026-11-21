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
	return decodeRecordsEnvelope(respBytes, "recent")
}

// firstJSONToken returns the first non-whitespace byte of a JSON payload, or 0
// if the payload is empty/whitespace-only. The envelope decoders use it to treat
// an EMPTY body as "no rows" instead of a decode error.
func firstJSONToken(raw []byte) byte {
	for _, currentByte := range raw {
		switch currentByte {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return currentByte
		}
	}
	return 0
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
	return decodeManifestPage(respBytes, "global manifest")
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
	return decodeManifestPage(respBytes, "session manifest")
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
	return decodeRecordsEnvelope(respBytes, "list chat")
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
// "has_more":bool}. label names the call site for clearer parse errors.
//
// Junior Tip [envelope only, 4.0.0]: the server always sends this object
// (ManifestGlobal server/handler/search_aux.go:224, ManifestSession
// server/handler/record_session.go:305-311). The 3.x branch that wrapped a
// bare JSON array for pre-envelope servers is gone — an array is now a decode error.
// "records": null (the server's zero-hit answer) still becomes an empty slice,
// and an empty body is still an empty page, not an error.
func decodeManifestPage(raw []byte, label string) (*ManifestPage, error) {
	if firstJSONToken(raw) == 0 {
		return &ManifestPage{Records: []models.Record{}}, nil
	}
	var page ManifestPage
	if parseErr := json.Unmarshal(raw, &page); parseErr != nil {
		return nil, fmt.Errorf("parsing %s response: %w", label, parseErr)
	}
	if page.Records == nil {
		page.Records = []models.Record{}
	}
	return &page, nil
}

// decodeRecordsEnvelope decodes a {"records":[Record],"count":int} envelope (no
// pagination) — used by Recent (server/handler/record_session.go:455) and
// ListChat (server/handler/record_session.go:184).
// Same discipline as decodeManifestPage: envelope only, null records and an
// empty body are an empty slice.
func decodeRecordsEnvelope(raw []byte, label string) ([]models.Record, error) {
	if firstJSONToken(raw) == 0 {
		return []models.Record{}, nil
	}
	var wrapped queryResponse // identical shape: {records, count}
	if parseErr := json.Unmarshal(raw, &wrapped); parseErr != nil {
		return nil, fmt.Errorf("parsing %s response: %w", label, parseErr)
	}
	if wrapped.Records == nil {
		return []models.Record{}, nil
	}
	return wrapped.Records, nil
}
