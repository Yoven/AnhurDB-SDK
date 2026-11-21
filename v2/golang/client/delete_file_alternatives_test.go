package client

// delete_file_alternatives_test.go — offline decode of the zero-match answer of
// DELETE /api/v1/records/by-file, which names the sessions that DO hold the file.

import (
	"encoding/json"
	"testing"
)

// zeroMatchServerJSON is byte-for-byte what service.DeleteFileResult marshals to
// when the prefix matched nothing in the requested session but exists in two
// others (server/service/record_delete_file.go:26-62, element type
// database.FileSessionCandidate at server/database/file_record_sessions.go:38-48;
// the second candidate's root is gone, so its omitempty keys are absent).
const zeroMatchServerJSON = `{"session_uuid":"chat-wrong","ingest_key_prefix":"ef9976f1ef5d5176",` +
	`"matched_count":0,"deleted_count":0,"dry_run":false,"purge":false,` +
	`"alternative_sessions":[` +
	`{"session_uuid":"chat-42","matched_count":511,` +
	`"root_checksum":"ef9976f1ef5d51760a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",` +
	`"root_filename":"lei-8666.pdf"},` +
	`{"session_uuid":"chat-7","matched_count":3}]}`

// TestDeleteFileResultDecodesAlternativeSessions proves the diagnostic survives
// decoding: losing it turns "wrong session, here is the right one" back into an
// indistinguishable "deleted 0".
func TestDeleteFileResultDecodesAlternativeSessions(t *testing.T) {
	var deleteResult DeleteFileResult
	if decodeError := json.Unmarshal([]byte(zeroMatchServerJSON), &deleteResult); decodeError != nil {
		t.Fatalf("decode: %v", decodeError)
	}
	if deleteResult.MatchedCount != 0 || deleteResult.RaftIndex != 0 {
		t.Fatalf("expected matched 0 / raft_index 0, got %d / %d", deleteResult.MatchedCount, deleteResult.RaftIndex)
	}
	if len(deleteResult.AlternativeSessions) != 2 {
		t.Fatalf("expected 2 alternative sessions, got %d", len(deleteResult.AlternativeSessions))
	}
	firstCandidate := deleteResult.AlternativeSessions[0]
	expectedFirst := FileSessionCandidate{
		SessionUUID:  "chat-42",
		MatchedCount: 511,
		RootChecksum: "ef9976f1ef5d51760a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
		RootFilename: "lei-8666.pdf",
	}
	if firstCandidate != expectedFirst {
		t.Errorf("first candidate mismatch: got %+v want %+v", firstCandidate, expectedFirst)
	}
	secondCandidate := deleteResult.AlternativeSessions[1]
	if secondCandidate != (FileSessionCandidate{SessionUUID: "chat-7", MatchedCount: 3}) {
		t.Errorf("second candidate mismatch: got %+v", secondCandidate)
	}
}

// TestDeleteFileResultWithoutAlternativesStaysNil proves absence (any match > 0,
// or a store that never had the file) decodes to nil, not to an empty sentinel.
func TestDeleteFileResultWithoutAlternativesStaysNil(t *testing.T) {
	var deleteResult DeleteFileResult
	successJSON := `{"session_uuid":"chat-42","ingest_key_prefix":"ef9976f1ef5d5176","matched_count":511,` +
		`"deleted_count":511,"deleted_ids":[1,2],"dry_run":false,"purge":false}`
	if decodeError := json.Unmarshal([]byte(successJSON), &deleteResult); decodeError != nil {
		t.Fatalf("decode: %v", decodeError)
	}
	if deleteResult.AlternativeSessions != nil {
		t.Errorf("absent alternative_sessions must stay nil, got %v", deleteResult.AlternativeSessions)
	}
}
