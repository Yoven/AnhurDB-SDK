package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUpdateRefusesScoreInsteadOfDroppingIt fixa a falha medida em 2026-08-15.
//
// PATCH /api/v1/records/{id} não tem campo score: o servidor responde 200 e
// descarta a chave. Update com {"score": 8} devolvia sucesso e não gravava
// nada — a mesma forma do defeito do campo `archived` em 2026-06-16.
//
// O servidor de teste registra se foi chamado: a guarda tem que barrar ANTES
// do transporte, senão o cliente ainda gastaria uma requisição para nada.
func TestUpdateRefusesScoreInsteadOfDroppingIt(t *testing.T) {
	serverWasCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		serverWasCalled = true
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	memory := NewMemory("test-key", WithURL(server.URL))

	err := memory.Update(context.Background(), 42, map[string]interface{}{"score": 8})

	if err == nil {
		t.Fatal("Update aceitou score — antes desta guarda isso devolvia sucesso e não gravava nada")
	}
	if !strings.Contains(err.Error(), "SetScore") {
		t.Errorf("a mensagem tem que nomear o método que funciona, got: %v", err)
	}
	if serverWasCalled {
		t.Error("a guarda deixou a requisição sair — tem que barrar antes do transporte")
	}
}

// TestUpdateRefusesScoreAlongsideOtherFields cobre o caso perigoso: o summary
// seria gravado e o score não, produzindo sucesso PARCIAL — perda silenciosa
// por outra porta.
func TestUpdateRefusesScoreAlongsideOtherFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	memory := NewMemory("test-key", WithURL(server.URL))

	err := memory.Update(context.Background(), 42, map[string]interface{}{
		"summary": "novo resumo",
		"score":   8,
	})

	if err == nil {
		t.Fatal("Update aceitou score junto de outro campo — sucesso parcial é perda silenciosa")
	}
}

// TestUpdateStillAcceptsOrdinaryFields: a guarda não pode desligar o Update
// para todo mundo.
func TestUpdateStillAcceptsOrdinaryFields(t *testing.T) {
	var capturedPath, capturedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedPath, capturedMethod = request.URL.Path, request.Method
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	memory := NewMemory("test-key", WithURL(server.URL))

	if err := memory.Update(context.Background(), 42, map[string]interface{}{"summary": "novo"}); err != nil {
		t.Fatalf("Update de campo comum falhou: %v", err)
	}
	if capturedMethod != http.MethodPatch || capturedPath != "/api/v1/records/42" {
		t.Errorf("rota errada: %s %s", capturedMethod, capturedPath)
	}
}

// TestSetScoreHitsTheDurableRoute prova ROTA e CORPO — é o que separa esta
// correção de repetir o defeito num endereço novo.
func TestSetScoreHitsTheDurableRoute(t *testing.T) {
	var capturedPath, capturedMethod string
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedPath, capturedMethod = request.URL.Path, request.Method
		rawBody, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(rawBody, &capturedBody)
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	memory := NewMemory("test-key", WithURL(server.URL))

	if err := memory.SetScore(context.Background(), 42, 8); err != nil {
		t.Fatalf("SetScore falhou: %v", err)
	}

	if capturedMethod != http.MethodPost {
		t.Errorf("método = %s, esperado POST", capturedMethod)
	}
	if capturedPath != "/api/v1/records/set-score" {
		t.Errorf("rota = %s, esperado /api/v1/records/set-score (o comando replicado)", capturedPath)
	}
	if capturedBody["score"] != float64(8) {
		t.Errorf("corpo sem score correto: %v", capturedBody)
	}
	ids, hasIDs := capturedBody["ids"].([]interface{})
	if !hasIDs || len(ids) != 1 || ids[0] != float64(42) {
		t.Errorf("corpo sem ids=[42]: %v", capturedBody)
	}
}

// TestSetScoreValidatesRange: a faixa do schema é 1..10, pontas inclusive.
func TestSetScoreValidatesRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	memory := NewMemory("test-key", WithURL(server.URL))

	for _, invalidScore := range []int{0, -1, 11, 99} {
		if err := memory.SetScore(context.Background(), 42, invalidScore); err == nil {
			t.Errorf("SetScore aceitou %d, fora de [1,10]", invalidScore)
		}
	}
	// As pontas são válidas: a guarda não pode encolher a faixa do schema.
	for _, validScore := range []int{1, 10} {
		if err := memory.SetScore(context.Background(), 42, validScore); err != nil {
			t.Errorf("SetScore rejeitou %d, que está dentro da faixa: %v", validScore, err)
		}
	}
}

// TestSetScoreRequiresConnection espelha o contrato dos outros métodos.
func TestSetScoreRequiresConnection(t *testing.T) {
	memory := &Memory{}

	if err := memory.SetScore(context.Background(), 42, 8); err != ErrEmptyAPIKey {
		t.Errorf("SetScore sem conexão = %v, esperado ErrEmptyAPIKey", err)
	}
}
