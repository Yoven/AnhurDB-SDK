package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capturedDeleteRequest guarda o que o SDK realmente pôs no fio. O contrato
// deste endpoint é a URL (verbo + caminho + query), então é a URL que os testes
// asseguram — não a intenção do código.
type capturedDeleteRequest struct {
	method          string
	path            string
	sessionUUID     string
	ingestKeyPrefix string
	dryRun          string
}

// newDeleteFileServer devolve um servidor que responde o envelope indicado e
// registra o pedido recebido.
func newDeleteFileServer(t *testing.T, responseBody string, captured *capturedDeleteRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		captured.method = request.Method
		captured.path = request.URL.Path
		captured.sessionUUID = request.URL.Query().Get("session")
		captured.ingestKeyPrefix = request.URL.Query().Get("ingest_key_prefix")
		captured.dryRun = request.URL.Query().Get("dry_run")
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, responseBody)
	}))
}

// TestDeleteFileWireContract prova o pedido: DELETE /api/v1/records/by-file com
// session, ingest_key_prefix e dry_run=false na query.
func TestDeleteFileWireContract(t *testing.T) {
	captured := &capturedDeleteRequest{}
	server := newDeleteFileServer(t, `{"session_uuid":"chat-42","ingest_key_prefix":"ef9976f1ef5d5176","matched_count":511,"deleted_count":511,"deleted_ids":[1,2,3],"dry_run":false,"raft_index":123}`, captured)
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	deleteResult, deleteErr := mem.DeleteFile(context.Background(), "chat-42", "ef9976f1ef5d5176")
	if deleteErr != nil {
		t.Fatalf("DeleteFile must succeed, got: %v", deleteErr)
	}

	if captured.method != http.MethodDelete {
		t.Errorf("expected DELETE, got %s", captured.method)
	}
	if captured.path != "/api/v1/records/by-file" {
		t.Errorf("expected /api/v1/records/by-file, got %s", captured.path)
	}
	if captured.sessionUUID != "chat-42" {
		t.Errorf("expected session=chat-42, got %q", captured.sessionUUID)
	}
	if captured.ingestKeyPrefix != "ef9976f1ef5d5176" {
		t.Errorf("expected ingest_key_prefix=ef9976f1ef5d5176, got %q", captured.ingestKeyPrefix)
	}
	if captured.dryRun != "false" {
		t.Errorf("expected dry_run=false, got %q", captured.dryRun)
	}

	// A contagem é a resposta ao usuário — se ela se perder na decodificação, o
	// SDK devolve "sucesso" sem dizer o que aconteceu.
	if deleteResult.MatchedCount != 511 || deleteResult.DeletedCount != 511 {
		t.Errorf("expected 511/511, got %d/%d", deleteResult.MatchedCount, deleteResult.DeletedCount)
	}
	if len(deleteResult.DeletedIDs) != 3 {
		t.Errorf("expected 3 deleted ids, got %d", len(deleteResult.DeletedIDs))
	}
	if deleteResult.RaftIndex != 123 {
		t.Errorf("expected raft_index 123, got %d", deleteResult.RaftIndex)
	}
	if deleteResult.DryRun {
		t.Error("dry_run must decode as false")
	}
}

// TestDeleteFileDryRunSendsFlag prova que a rede de segurança chega ao servidor
// e que o envelope sem deleted_ids/raft_index (omitempty) decodifica limpo.
func TestDeleteFileDryRunSendsFlag(t *testing.T) {
	captured := &capturedDeleteRequest{}
	server := newDeleteFileServer(t, `{"session_uuid":"chat-42","ingest_key_prefix":"ef9976f1ef5d5176","matched_count":511,"deleted_count":0,"dry_run":true}`, captured)
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	deleteResult, deleteErr := mem.DeleteFile(
		context.Background(), "chat-42", "ef9976f1ef5d5176", WithDeleteFileDryRun(true))
	if deleteErr != nil {
		t.Fatalf("dry-run DeleteFile must succeed, got: %v", deleteErr)
	}

	if captured.dryRun != "true" {
		t.Errorf("expected dry_run=true on the wire, got %q", captured.dryRun)
	}
	if !deleteResult.DryRun {
		t.Error("dry_run must decode as true")
	}
	if deleteResult.MatchedCount != 511 {
		t.Errorf("expected matched_count 511, got %d", deleteResult.MatchedCount)
	}
	if deleteResult.DeletedCount != 0 {
		t.Errorf("dry-run must not report deletions, got %d", deleteResult.DeletedCount)
	}
	if deleteResult.DeletedIDs != nil {
		t.Errorf("absent deleted_ids must stay nil, got %v", deleteResult.DeletedIDs)
	}
}

// TestDeleteFileTrimsArguments garante que espaço em volta não vira parte da
// identidade do arquivo (o servidor também apara — os dois lados concordam).
func TestDeleteFileTrimsArguments(t *testing.T) {
	captured := &capturedDeleteRequest{}
	server := newDeleteFileServer(t, `{"matched_count":0,"deleted_count":0,"dry_run":false}`, captured)
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	if _, deleteErr := mem.DeleteFile(context.Background(), "  chat-42 ", " ef9976f1ef5d5176\n"); deleteErr != nil {
		t.Fatalf("DeleteFile must succeed, got: %v", deleteErr)
	}
	if captured.sessionUUID != "chat-42" {
		t.Errorf("session must be trimmed, got %q", captured.sessionUUID)
	}
	if captured.ingestKeyPrefix != "ef9976f1ef5d5176" {
		t.Errorf("prefix must be trimmed, got %q", captured.ingestKeyPrefix)
	}
}

// TestDeleteFileLocalValidation prova que argumento vazio falha SEM tocar a
// rede: o servidor do teste marca qualquer chamada que chegue.
func TestDeleteFileLocalValidation(t *testing.T) {
	serverWasCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		serverWasCalled = true
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))

	testCases := []struct {
		name            string
		sessionUUID     string
		ingestKeyPrefix string
	}{
		{name: "empty session", sessionUUID: "", ingestKeyPrefix: "ef9976f1ef5d5176"},
		{name: "blank session", sessionUUID: "   ", ingestKeyPrefix: "ef9976f1ef5d5176"},
		{name: "empty prefix", sessionUUID: "chat-42", ingestKeyPrefix: ""},
		{name: "blank prefix", sessionUUID: "chat-42", ingestKeyPrefix: "\t"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			deleteResult, deleteErr := mem.DeleteFile(
				context.Background(), testCase.sessionUUID, testCase.ingestKeyPrefix)
			if deleteErr == nil {
				subTest.Fatalf("expected a local error, got result %+v", deleteResult)
			}
			if deleteResult != nil {
				subTest.Errorf("failed call must not return a result, got %+v", deleteResult)
			}
		})
	}

	if serverWasCalled {
		t.Error("local validation must not reach the server")
	}
}

// TestDeleteFileEmptyAPIKey mantém o contrato dos demais métodos: sem chave, o
// erro é ErrEmptyAPIKey e não um panic.
func TestDeleteFileEmptyAPIKey(t *testing.T) {
	mem := NewMemory("")
	if _, deleteErr := mem.DeleteFile(context.Background(), "chat-42", "ef9976f1ef5d5176"); deleteErr != ErrEmptyAPIKey {
		t.Errorf("expected ErrEmptyAPIKey, got %v", deleteErr)
	}
}

// TestDeleteFileServerErrorSurfaces prova que um 400 do servidor (prefixo curto
// ou inválido — regra que vive no servidor) chega ao chamador como erro, nunca
// como resultado vazio.
func TestDeleteFileServerErrorSurfaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusBadRequest)
		io.WriteString(responseWriter, `{"error":"ingest key prefix \"abc\" is too short (minimum 8 characters)"}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	deleteResult, deleteErr := mem.DeleteFile(context.Background(), "chat-42", "abc")
	if deleteErr == nil {
		t.Fatalf("HTTP 400 must surface as an error, got %+v", deleteResult)
	}
	if deleteResult != nil {
		t.Errorf("failed call must not return a result, got %+v", deleteResult)
	}
}
