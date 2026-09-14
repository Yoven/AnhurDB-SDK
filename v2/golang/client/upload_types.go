package client

// upload_types.go — one domain: the two response shapes of the file-upload
// route (POST /api/v1/upload and GET /api/v1/upload/{id}/status).
//
// Split out of types.go on 2026-09-14 (types.go was past the ~300-line house
// cut) while both structs were being reconciled with the handler.

// UploadResult is the HTTP 202 acceptance envelope of POST /api/v1/upload.
//
// Field set is the handler's fixed map, server/handler/upload.go:109-119. There
// is NO `id` key on this route and there never was: the struct declared one
// until 2026-09-14 and it decoded to zero on every single upload, which is why
// UploadID() needed a fallback that could never fire.
//
// Junior Tip [202 means accepted, not ingested]: the record exists but its
// content is still being extracted. Status here is the status AT ACCEPTANCE.
// Poll UploadStatus (or WaitForUpload) for the terminal one.
type UploadResult struct {
	Message      string `json:"message"`
	RecordID     int64  `json:"record_id"`
	UUID         string `json:"uuid,omitempty"`
	Filename     string `json:"filename,omitempty"`
	MIME         string `json:"mime,omitempty"`
	MIMEDetected string `json:"mime_detected,omitempty"`
	Extension    string `json:"extension,omitempty"`
	SizeBytes    int64  `json:"size_bytes,omitempty"`
	Status       string `json:"status,omitempty"`
}

// UploadID returns the server record id used for UploadStatus polling.
//
// Junior Tip [why this is not just a field read]: it used to fall back to a
// phantom `ID` field when RecordID was zero. That fallback made a zero
// RecordID — which only happens when the response failed to decode — look like
// a legitimate id of 0, and the caller then polled /upload/0/status forever.
// One source, no fallback: a zero return now means "the accept response did not
// carry a record_id", which is a real failure the caller must see.
func (uploadResult UploadResult) UploadID() int64 {
	return uploadResult.RecordID
}

// UploadStatusResult describes the processing status of a file upload.
//
// Field set is the handler's fixed 7-key map, server/handler/upload.go:220-236:
// record_id, uuid, status, type, summary, metadata, completed. Nothing else,
// ever.
//
// Junior Tip [failure arrives as status, never as an error field, 2026-09-14]:
// this struct used to declare ID, Filename and Error. The server sends none of
// the three. Error in particular was load-bearing in the wrong direction — the
// wait loop treated `Error != ""` as a terminal condition, a branch that could
// not fire, so a failed ingest was only ever caught by status == "failed"
// sitting next to it. A failed ingest is reported through `status`, and only
// through `status`.
type UploadStatusResult struct {
	RecordID  int64  `json:"record_id"`
	UUID      string `json:"uuid,omitempty"`
	Status    string `json:"status"` // "processing", "completed", "failed", "saved"
	Type      string `json:"type,omitempty"`
	Completed bool   `json:"completed"`
	Summary   string `json:"summary,omitempty"`
	Metadata  string `json:"metadata,omitempty"`
}
