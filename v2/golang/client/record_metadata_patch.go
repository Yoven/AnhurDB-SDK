package client

// record_metadata_patch.go — how Update puts "metadata" on the wire.
//
// One domain in one sentence: turn the caller's "metadata" value into the JSON
// STRING the server's PATCH /api/v1/records/{id} decodes, so a map can carry
// null values (= delete that key) under the server's merge-patch contract.
//
// Server contract (AnhurDB feat/metadata-merge-patch, ADR-0035): record update
// metadata is an RFC 7396 JSON merge patch — present keys overwrite, a null
// value deletes the key, absent keys are untouched, nested objects merge,
// arrays are replaced whole. A patch that is not a JSON object is refused (400 /
// InvalidArgument), and so is an update over stored metadata that is not a JSON
// object. The REST handler decodes the field as a Go string
// (server/handler/record_crud.go, `Metadata string json:"metadata"`), so the
// body must hold a JSON-encoded string, never a nested object.
//
// Parity: Python anhurdb/client/record_metadata_patch.py, TypeScript
// src/recordMetadataPatch.ts — same accepted shapes, same refusal.

import (
	"encoding/json"
)

// encodeUpdateMetadata returns the body Update should send.
//
// A "metadata" value of type map[string]interface{} is JSON-encoded to a string
// with nil values kept as null; every other value (a string, as in 3.x/4.0.0,
// or anything else) is sent unchanged so the server stays the judge of it.
// The caller's map is never mutated: a shallow copy is returned when the
// metadata value had to be encoded.
//
// Junior Tip [why the map is encoded HERE and not left to the transport]: the
// transport marshals the whole updates map, which would put metadata on the
// wire as a nested JSON object. The server field is a string, so that body is
// a 400 "invalid request body" — the map form never worked before this change. The
// nil values are the point of accepting a map: under merge-patch a key the
// caller wants REMOVED must travel as null; dropping it would leave the stored
// key untouched and the caller would believe it was deleted.
func encodeUpdateMetadata(method string, updates map[string]interface{}) (map[string]interface{}, error) {
	metadataValue, hasMetadata := updates["metadata"]
	if !hasMetadata {
		return updates, nil
	}
	metadataPatch, isObjectPatch := metadataValue.(map[string]interface{})
	if !isObjectPatch {
		return updates, nil
	}
	encodedPatch, marshalError := json.Marshal(metadataPatch)
	if marshalError != nil {
		return nil, newValidationError(
			"INVALID_PARAM: %s: metadata cannot be encoded as JSON: %v", method, marshalError)
	}
	encodedUpdates := make(map[string]interface{}, len(updates))
	for fieldName, fieldValue := range updates {
		encodedUpdates[fieldName] = fieldValue
	}
	encodedUpdates["metadata"] = string(encodedPatch)
	return encodedUpdates, nil
}
