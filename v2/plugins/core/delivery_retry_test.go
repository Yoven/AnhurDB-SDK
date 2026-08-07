package core

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// delivery_retry_test.go — o disco é último recurso. Cada teste aqui fixa metade
// da regra do dono: "só grava local se o banco não responder depois de 3
// tentativas" e "quando voltar, descarrega tudo e limpa o local".

// shortenRetryBackoffForTest tira a espera real dos testes sem tirar a lógica.
func shortenRetryBackoffForTest(t *testing.T) {
	t.Helper()
	originalBackoff := deliveryRetryBackoff
	deliveryRetryBackoff = []time.Duration{0, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { deliveryRetryBackoff = originalBackoff })
}

// TestBlipDoesNotCreateLocalState is the whole reason the rule exists: a leader
// election or a reconnect must NOT leave a second copy of the user's memory on disk.
func TestBlipDoesNotCreateLocalState(t *testing.T) {
	shortenRetryBackoffForTest(t)
	attemptCount := 0
	outcome := deliverWithRetry(context.Background(), func() error {
		attemptCount++
		if attemptCount < 2 {
			return &net.OpError{Op: "dial", Err: errors.New("connection refused")}
		}
		return nil // o banco voltou na segunda
	})
	if outcome.err != nil {
		t.Fatalf("um blip virou falha de entrega: %v", outcome.err)
	}
	if attemptCount != 2 {
		t.Errorf("tentativas = %d, want 2 (parar assim que entregar)", attemptCount)
	}
}

// TestUnresponsiveBackendUsesAllThreeAttempts pins the owner's number literally.
func TestUnresponsiveBackendUsesAllThreeAttempts(t *testing.T) {
	shortenRetryBackoffForTest(t)
	attemptCount := 0
	outcome := deliverWithRetry(context.Background(), func() error {
		attemptCount++
		return &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	})
	if attemptCount != maxDeliveryAttempts {
		t.Errorf("tentativas = %d, want %d — o disco so entra depois das tres",
			attemptCount, maxDeliveryAttempts)
	}
	if outcome.err == nil {
		t.Fatal("banco fora do ar reportado como entregue — o chunk nao iria para a fila")
	}
	if outcome.terminal {
		t.Error("banco fora do ar marcado como terminal — ele volta sozinho, e o log diria a coisa errada")
	}
}

// TestServerRefusalIsNotRetried: a 401 or a 409 is the server ANSWERING. Repeating it
// spends the hook's time to reach the same place. The chunk is still queued — what
// changes is how long the user waits for the turn to end.
func TestServerRefusalIsNotRetried(t *testing.T) {
	shortenRetryBackoffForTest(t)
	refusals := []struct {
		name          string
		deliveryError error
	}{
		{"chave rejeitada", client.ErrUnauthorized},
		{"sessao cheia (409)", &client.APIError{StatusCode: 409, Body: "session has reached the maximum of 500 records"}},
		{"payload invalido (422)", &client.APIError{StatusCode: 422, Body: "invalid"}},
		{"rota inexistente", client.ErrNotFound},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			attemptCount := 0
			outcome := deliverWithRetry(context.Background(), func() error {
				attemptCount++
				return refusal.deliveryError
			})
			if attemptCount != 1 {
				t.Errorf("tentativas = %d, want 1 — a resposta do servidor nao muda na segunda", attemptCount)
			}
			if !outcome.terminal {
				t.Error("recusa do servidor nao marcada como terminal — o log confundiria com queda temporaria")
			}
			if outcome.err == nil {
				t.Error("recusa reportada como sucesso: o chunk seria descartado")
			}
		})
	}
}

// TestTransientHTTPStatusesAreRetried pins the two 4xx that DO improve on their own —
// classifying them as terminal would give up early and create local state for nothing.
func TestTransientHTTPStatusesAreRetried(t *testing.T) {
	shortenRetryBackoffForTest(t)
	for _, transientStatus := range []int{408, 429, 500, 502, 503, 504} {
		attemptCount := 0
		deliverWithRetry(context.Background(), func() error {
			attemptCount++
			return &client.APIError{StatusCode: transientStatus, Body: "try again"}
		})
		if attemptCount != maxDeliveryAttempts {
			t.Errorf("HTTP %d: tentativas = %d, want %d — este estado melhora sozinho",
				transientStatus, attemptCount, maxDeliveryAttempts)
		}
	}
}

// TestCancelledContextIsNeverReportedAsDelivered: returning nil on cancellation would
// make the caller believe the chunk was written. It must go to the queue.
func TestCancelledContextIsNeverReportedAsDelivered(t *testing.T) {
	cancellableContext, cancelDelivery := context.WithCancel(context.Background())
	cancelDelivery()
	outcome := deliverWithRetry(cancellableContext, func() error {
		return &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	})
	if outcome.err == nil {
		t.Fatal("cancelamento reportado como entrega — o chunk seria perdido em silencio")
	}
}

// TestLocalStateIsClearedAfterFullDrain is the second half of the owner's rule: once
// everything reached AnhurDB, the local file holds nothing but risk.
func TestLocalStateIsClearedAfterFullDrain(t *testing.T) {
	store := newTestQueueStore(t)
	for _, content := range []string{"turno-1", "turno-2", "turno-3"} {
		if enqueueErr := store.EnqueueChunk("sess-A", content); enqueueErr != nil {
			t.Fatalf("enqueue: %v", enqueueErr)
		}
	}
	claimed, _ := store.ClaimNextBatch(10)
	for _, item := range claimed {
		if deliverErr := store.MarkDelivered(item.ID); deliverErr != nil {
			t.Fatalf("MarkDelivered: %v", deliverErr)
		}
	}

	purged, purgeErr := store.PurgeIfDrained()
	if purgeErr != nil {
		t.Fatalf("PurgeIfDrained: %v", purgeErr)
	}
	if !purged {
		t.Fatal("fila vazia nao foi limpa — o local continuaria guardando risco sem guardar memoria")
	}

	// O conteúdo entregue não pode continuar legível no arquivo: o SQLite marca a
	// página como livre no DELETE, mas só o VACUUM a reescreve.
	databaseBytes, readErr := os.ReadFile(store.path)
	if readErr != nil {
		t.Fatalf("lendo o arquivo do banco: %v", readErr)
	}
	for _, deliveredContent := range []string{"turno-1", "turno-2", "turno-3"} {
		if bytesContain(databaseBytes, deliveredContent) {
			t.Errorf("conteudo entregue (%q) ainda legivel no arquivo local apos a limpeza", deliveredContent)
		}
	}
}

// TestQuarantineHoldsTheCleanup: a quarantined chunk is the ONLY data that exists
// nowhere else. Clearing while one is parked would be the silent loss this whole
// design exists to prevent.
func TestQuarantineHoldsTheCleanup(t *testing.T) {
	store := newTestQueueStore(t)
	if quarantineErr := store.EnqueueQuarantined("orfao sem sessao", "no session marker"); quarantineErr != nil {
		t.Fatalf("EnqueueQuarantined: %v", quarantineErr)
	}
	purged, purgeErr := store.PurgeIfDrained()
	if purgeErr != nil {
		t.Fatalf("PurgeIfDrained: %v", purgeErr)
	}
	if purged {
		t.Fatal("limpou com um chunk em quarentena — o unico dado que so existe aqui seria destruido")
	}
	stillThere, _ := store.ListQuarantined()
	if len(stillThere) != 1 || stillThere[0].Content != "orfao sem sessao" {
		t.Errorf("chunk em quarentena nao sobreviveu a tentativa de limpeza: %+v", stillThere)
	}
}

// bytesContain reports whether needle appears in haystack (substring, byte-wise).
func bytesContain(haystack []byte, needle string) bool {
	needleBytes := []byte(needle)
	if len(needleBytes) == 0 || len(needleBytes) > len(haystack) {
		return false
	}
	for startIndex := 0; startIndex+len(needleBytes) <= len(haystack); startIndex++ {
		matched := true
		for offset := range needleBytes {
			if haystack[startIndex+offset] != needleBytes[offset] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
