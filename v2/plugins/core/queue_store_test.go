package core

import (
	"errors"
	"testing"
	"time"
)

// queue_store_test.go — fixa a máquina de estados da fila. Cada teste aqui
// corresponde a um modo de falha que a fila baseada em NOME DE ARQUIVO permitiu
// em produção; o nome de cada um diz qual.

func newTestQueueStore(t *testing.T) *queueStore {
	t.Helper()
	store, openErr := openQueueStore(t.TempDir())
	if openErr != nil {
		t.Fatalf("openQueueStore: %v", openErr)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestEnqueueRefusesMissingSession pins the inviolable invariant at the door:
// a chunk with no identifiable session can never be stored as deliverable,
// because delivering it would require guessing a session and merging two
// conversations.
func TestEnqueueRefusesMissingSession(t *testing.T) {
	store := newTestQueueStore(t)
	if enqueueErr := store.EnqueueChunk("", "conteudo orfao"); enqueueErr == nil {
		t.Fatal("enfileirar sem sessao tem de ser RECUSADO — adivinhar a sessao funde conversas")
	}
}

// TestDrainPreservesChronologicalOrder: the items are pieces of a conversation.
// Delivering turn 7 before turn 5 writes the conversation out of order and no
// later consolidation repairs it.
func TestDrainPreservesChronologicalOrder(t *testing.T) {
	store := newTestQueueStore(t)
	for _, content := range []string{"primeiro", "segundo", "terceiro"} {
		if enqueueErr := store.EnqueueChunk("sess-1", content); enqueueErr != nil {
			t.Fatalf("enqueue %q: %v", content, enqueueErr)
		}
		time.Sleep(2 * time.Millisecond) // garante created_at distinto
	}
	claimed, claimErr := store.ClaimNextBatch(10)
	if claimErr != nil {
		t.Fatalf("ClaimNextBatch: %v", claimErr)
	}
	if len(claimed) != 3 {
		t.Fatalf("reivindicados = %d, want 3", len(claimed))
	}
	for index, expected := range []string{"primeiro", "segundo", "terceiro"} {
		if claimed[index].Content != expected {
			t.Errorf("posicao %d = %q, want %q — a ordem cronologica E a conversa",
				index, claimed[index].Content, expected)
		}
	}
}

// TestFailedItemComesBackWithReasonAndBackoff is the direct answer to the 52
// log lines that said "flush still failing" without ever saying how long, how
// many times, or why.
func TestFailedItemComesBackWithReasonAndBackoff(t *testing.T) {
	store := newTestQueueStore(t)
	if enqueueErr := store.EnqueueChunk("sess-1", "conteudo"); enqueueErr != nil {
		t.Fatalf("enqueue: %v", enqueueErr)
	}
	claimed, _ := store.ClaimNextBatch(10)
	if len(claimed) != 1 {
		t.Fatalf("reivindicados = %d, want 1", len(claimed))
	}
	if failErr := store.MarkFailed(claimed[0].ID, claimed[0].RetryCount,
		errors.New("authentication failed: invalid API key")); failErr != nil {
		t.Fatalf("MarkFailed: %v", failErr)
	}

	// O backoff tira o item do proximo dreno imediato — sem isso, um endpoint
	// morto faz o plugin martelar a rede a cada persist.
	immediatelyAfter, _ := store.ClaimNextBatch(10)
	if len(immediatelyAfter) != 0 {
		t.Errorf("item reaparece antes do backoff (%d itens) — endpoint morto viraria martelo",
			len(immediatelyAfter))
	}

	summary, summarizeErr := store.Summarize()
	if summarizeErr != nil {
		t.Fatalf("Summarize: %v", summarizeErr)
	}
	if summary.PendingCount != 1 {
		t.Errorf("PendingCount = %d, want 1 — item que falhou NUNCA e descartado", summary.PendingCount)
	}
	if summary.LastError == "" {
		t.Error("LastError vazio: a pergunta 'por que esta preso?' precisa ter resposta")
	}
}

// TestQuarantineIsNeverRetried pins that quarantine is terminal for the
// automatic loop — a human decides, the plugin never guesses.
func TestQuarantineIsNeverRetried(t *testing.T) {
	store := newTestQueueStore(t)
	store.EnqueueChunk("sess-1", "sem marcador de sessao confiavel")
	claimed, _ := store.ClaimNextBatch(10)
	if quarantineErr := store.Quarantine(claimed[0].ID, "sessao nao identificavel"); quarantineErr != nil {
		t.Fatalf("Quarantine: %v", quarantineErr)
	}
	afterQuarantine, _ := store.ClaimNextBatch(10)
	if len(afterQuarantine) != 0 {
		t.Error("item em quarentena foi reivindicado — quarentena tem de ser terminal para o loop")
	}
	summary, _ := store.Summarize()
	if summary.QuarantinedCount != 1 {
		t.Errorf("QuarantinedCount = %d, want 1 — quarentena tem de ser VISIVEL", summary.QuarantinedCount)
	}
}

// TestDeliveredItemDisappears: delivered is delivered. Keeping them would turn
// the waiting room into a second archive with its own retention problem.
func TestDeliveredItemDisappears(t *testing.T) {
	store := newTestQueueStore(t)
	store.EnqueueChunk("sess-1", "conteudo")
	claimed, _ := store.ClaimNextBatch(10)
	if deliverErr := store.MarkDelivered(claimed[0].ID); deliverErr != nil {
		t.Fatalf("MarkDelivered: %v", deliverErr)
	}
	summary, _ := store.Summarize()
	if summary.PendingCount != 0 {
		t.Errorf("PendingCount = %d apos entrega, want 0", summary.PendingCount)
	}
}

// TestStaleSendingIsReclaimed covers the crash-mid-send case: the hook is a
// short-lived process that can be killed at any moment. Without reclaim, an item
// caught in "sending" at that instant would be stuck forever — precisely the
// failure this table exists to end.
func TestStaleSendingIsReclaimed(t *testing.T) {
	store := newTestQueueStore(t)
	store.EnqueueChunk("sess-1", "conteudo")
	claimed, _ := store.ClaimNextBatch(10)

	// Simula o processo morto: envelhece o carimbo do estado para alem do timeout.
	staleStamp := time.Now().UTC().Add(-2 * queueSendingTimeout).Format(time.RFC3339Nano)
	if _, execErr := store.database.Exec(
		`UPDATE pending_memory SET state_changed_at = ? WHERE id = ?`, staleStamp, claimed[0].ID,
	); execErr != nil {
		t.Fatalf("envelhecendo o item: %v", execErr)
	}

	reclaimed, reclaimErr := store.ClaimNextBatch(10)
	if reclaimErr != nil {
		t.Fatalf("ClaimNextBatch: %v", reclaimErr)
	}
	if len(reclaimed) != 1 {
		t.Fatal("item preso em 'sending' por processo morto NAO foi reivindicado — ficaria preso para sempre")
	}
}

// TestBackoffGrowsAndIsCapped pins both halves: it must grow (so a dead endpoint
// is not hammered) and it must stop growing (so recovery is not delayed by
// hours after a long outage).
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	first := backoffForAttempt(1)
	later := backoffForAttempt(5)
	if later <= first {
		t.Errorf("backoff nao cresce: tentativa 1 = %v, tentativa 5 = %v", first, later)
	}
	if capped := backoffForAttempt(999); capped != queueMaxBackoff {
		t.Errorf("backoff da tentativa 999 = %v, want o teto %v — sem teto, a recuperacao demoraria horas",
			capped, queueMaxBackoff)
	}
}

// TestSummarizeOnEmptyStoreIsNotAnError: a fresh install has an empty queue, and
// asking about it must not look like a failure.
func TestSummarizeOnEmptyStoreIsNotAnError(t *testing.T) {
	store := newTestQueueStore(t)
	summary, summarizeErr := store.Summarize()
	if summarizeErr != nil {
		t.Fatalf("Summarize numa fila vazia devolveu erro: %v", summarizeErr)
	}
	if summary.PendingCount != 0 || summary.QuarantinedCount != 0 {
		t.Errorf("fila nova nao esta vazia: %+v", summary)
	}
}
