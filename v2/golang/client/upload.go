package client

// upload.go — POST /api/v1/upload and GET /api/v1/upload/{id}/status.
//
// One domain in one sentence: "send a file to the plane the caller chose, remember
// that plane, and ask for the status IN that plane".
//
// Moved out of client.go (past the house 300-line cut) on 2026-11-20 when the
// status poll gained a scope.
//
// Junior Tip [why the status poll must name the plane — server fix/l2-lib-scope]: a
// default (or tenant_shared) upload now lands in the library store {c}_{t}_lib and a
// client_shared one in {c}_shared, but the status route is keyed by the record id
// only, and a record id is a rowid PER STORE. Without ?scope= the server searches
// every store this caller can upload to and answers 409 when the id is a file in more
// than one of them (server/handler/upload_status_scope.go:143-198). The SDK knows the
// plane it uploaded to, so it records it in UploadResult.Scope and passes it on —
// no guessing on either side.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// UploadFile uploads a document for async ingestion (multipart/form-data).
//
// Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown, HTML, DOCX.
// Chat plane: sessionID from CreateSession + linkedEpisodicID (required) — file
// hangs as a sub-tree of that episodic; server sets has_file=true.
// Shared planes: WithUploadMode("tenant_shared"|"client_shared") with empty session.
//
// The returned UploadResult.Scope is the plane the file landed in; pass it to
// UploadStatus / WaitForUpload (WithRecordScope / WaitUploadOptions.Scope).
func (m *Memory) UploadFile(ctx context.Context, filename string, content []byte, sessionID string, linkedEpisodicID int64, opts ...UploadOption) (*UploadResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	uploadCfg := &uploadConfig{}
	for _, opt := range opts {
		opt(uploadCfg)
	}

	extraFields := map[string]string{}
	mode := strings.TrimSpace(strings.ToLower(uploadCfg.mode))
	if sessionID != "" && mode == "" {
		mode = "chat"
	}
	switch mode {
	case "chat":
		if sessionID == "" {
			return nil, fmt.Errorf("session_id is required — create a session first (POST /api/v1/sessions)")
		}
		if linkedEpisodicID <= 0 {
			return nil, fmt.Errorf("linked_episodic_id is required for chat uploads — attach the file to the episodic turn")
		}
		extraFields["mode"] = "chat"
		extraFields["session_id"] = sessionID
		extraFields["linked_episodic_id"] = strconv.FormatInt(linkedEpisodicID, 10)
	case "tenant_shared", "client_shared":
		extraFields["mode"] = mode
	case "":
		// shared default on server when no session — leave fields empty
	default:
		// Typed invalid_request (no request sent), message byte-identical to
		// Python upload_file and TypeScript uploadFile.
		return nil, newValidationError(
			"INVALID_PARAM: upload: mode '%s' is not an upload mode (want chat|tenant_shared|client_shared)",
			uploadCfg.mode)
	}

	respBytes, postErr := m.conn.PostMultipart(ctx, "/api/v1/upload", "file", filename, content, extraFields)
	if postErr != nil {
		return nil, postErr
	}

	var result UploadResult
	if parseErr := json.Unmarshal(respBytes, &result); parseErr != nil {
		return nil, fmt.Errorf("parsing upload response: %w", parseErr)
	}
	result.Scope = UploadStatusScope(mode, sessionID)

	return &result, nil
}

// UploadStatus checks the processing status of a file upload.
//
// Pass WithRecordScope(uploadResult.Scope). Without it the server searches every
// plane this caller can upload to and answers *UploadStatusAmbiguousError (HTTP 409)
// when the id is a file in more than one.
func (m *Memory) UploadStatus(ctx context.Context, uploadID int64, opts ...ByIDOption) (*UploadStatusResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}
	scope, scopeErr := resolveByIDScope("UploadStatus", opts)
	if scopeErr != nil {
		return nil, scopeErr
	}

	path := fmt.Sprintf("/api/v1/upload/%d/status", uploadID)
	respBytes, err := m.conn.Get(ctx, path, scopeQuery(scope))
	if err != nil {
		return nil, err
	}

	var result UploadStatusResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing upload status response: %w", err)
	}

	return &result, nil
}
