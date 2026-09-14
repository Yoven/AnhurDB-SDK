package client

// create_valid_until_test.go — one domain: the closing half of Create's
// bi-temporal window.
//
// This case fails against the pre-3.0.0 SDK, where WithCreateValidUntil did not
// exist: TypeScript CreateOptions.validUntil and Python
// CreateRequest.valid_until both did, so the same three-language call wrote an
// open-ended record in Go and a bounded one everywhere else.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateSendsValidUntilInMetadata proves the key lands where the server
// reads it. service.createRecord falls back to the METADATA keys for both halves
// of the window (server/service/record_create.go:329-344), and the REST create
// route never fills the dedicated input fields — so metadata is the only door.
func TestCreateSendsValidUntilInMetadata(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			requestBody, _ = io.ReadAll(request.Body)
			io.WriteString(responseWriter, `{"id":101}`)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, createErr := memoryClient.Create(context.Background(), "chat-1", "Paulo works at Yoven",
		WithCreateValidFrom("2026-01-01T00:00:00Z"),
		WithCreateValidUntil("2027-01-01T00:00:00Z")); createErr != nil {
		t.Fatalf("Create returned error: %v", createErr)
	}

	var payload map[string]interface{}
	if unmarshalErr := json.Unmarshal(requestBody, &payload); unmarshalErr != nil {
		t.Fatalf("create body is not JSON: %v (%s)", unmarshalErr, requestBody)
	}
	metadataJSON, isString := payload["metadata"].(string)
	if !isString {
		t.Fatalf("metadata must be a JSON STRING on this route, got %T", payload["metadata"])
	}
	var metadata map[string]interface{}
	if unmarshalErr := json.Unmarshal([]byte(metadataJSON), &metadata); unmarshalErr != nil {
		t.Fatalf("metadata is not JSON: %v (%s)", unmarshalErr, metadataJSON)
	}
	if metadata["valid_from"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("valid_from = %v, want 2026-01-01T00:00:00Z", metadata["valid_from"])
	}
	if metadata["valid_until"] != "2027-01-01T00:00:00Z" {
		t.Fatalf("valid_until = %v, want 2027-01-01T00:00:00Z — "+
			"a window with no close is not the window the caller asked for", metadata["valid_until"])
	}
}

// TestCreateOmitsValidUntilWhenUnset keeps a bare Create byte-identical to what
// it always sent. An always-present key is a key the server can never default.
func TestCreateOmitsValidUntilWhenUnset(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(
		func(responseWriter http.ResponseWriter, request *http.Request) {
			requestBody, _ = io.ReadAll(request.Body)
			io.WriteString(responseWriter, `{"id":101}`)
		}))
	defer server.Close()

	memoryClient := NewMemory("k", WithURL(server.URL))
	if _, createErr := memoryClient.Create(context.Background(), "chat-1", "plain"); createErr != nil {
		t.Fatalf("Create returned error: %v", createErr)
	}
	if strings.Contains(string(requestBody), "valid_until") {
		t.Fatalf("valid_until must be absent when unset: %s", requestBody)
	}
}
