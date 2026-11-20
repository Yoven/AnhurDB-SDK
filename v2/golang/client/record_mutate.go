package client

// record_mutate.go — the by-id WRITES: update, score, archive, supersede and the
// batch status update.
//
// One domain in one sentence: "change one record (or a batch) by id, in the store the
// caller names with WithRecordScope, or in the caller's own store when no scope is
// given".
//
// Moved out of client.go (past the house 300-line cut) on 2026-11-20 when Update,
// Delete and Supersede gained ...ByIDOption.
//
// Junior Tip [why a mutation needs the scope even more than a read]: an id taken from
// a tenant_shared hit names a row of {c}_{t}_lib; the same integer names an unrelated
// chat row in {c}_{t}. An unscoped PATCH/DELETE/supersede would change THAT chat row
// and answer 200 — a valid-looking success on the wrong record. With a scope the
// server proves the row lives in the plane or answers 404
// (server/handler/record_scoped_by_id.go:72-90, router.go PATCH/DELETE
// /api/v1/records/{id}; server/handler/record_supersede.go proves BOTH ids).

import (
	"context"
	"fmt"
)

// Update partially updates a record by ID.
//
// The updates map can contain any subset of record fields
// (e.g. {"summary": "new summary", "status": "archived"}).
//
// It REFUSES a "score" key. PATCH /api/v1/records/{id} has no score field, so
// the server answers 200 and drops it — the same shape as an earlier defect
// with "archived". Silently succeeding while writing nothing is worse than an
// error, so this returns one and names SetScore. Measured 2026-08-15.
func (m *Memory) Update(ctx context.Context, recordID int64, updates map[string]interface{}, opts ...ByIDOption) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if _, hasScore := updates["score"]; hasScore {
		return fmt.Errorf("Update: cannot change score — PATCH /api/v1/records/{id} has no score " +
			"field and drops the key while answering 200; use SetScore(ctx, recordID, score)")
	}
	scope, scopeErr := resolveByIDScope("Update", opts)
	if scopeErr != nil {
		return scopeErr
	}

	path := byIDPath(fmt.Sprintf("/api/v1/records/%d", recordID), scope)
	_, err := m.conn.Patch(ctx, path, updates)
	return err
}

// SetScore sets a record's importance score (1-10) durably.
//
// Update cannot do this — see its comment. This posts to
// /api/v1/records/set-score, which dispatches a replicated command and
// invalidates the read cache.
//
// Junior Tip [a correção não aparece na hora]: o score alimenta o termo de
// valor do peso cognitivo, e o peso escolhe a banda de fidelidade do embedding.
// A mudança só chega ao ranking quando a varredura de manutenção reequilibrar o
// registro — medir logo depois de escrever mostra o estado antigo.
func (m *Memory) SetScore(ctx context.Context, recordID int64, score int) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if score < 1 || score > 10 {
		return fmt.Errorf("SetScore: score must be between 1 and 10, got %d", score)
	}
	payload := map[string]interface{}{
		"ids":   []int64{recordID},
		"score": score,
	}
	_, err := m.conn.Post(ctx, "/api/v1/records/set-score", payload)
	return err
}

// Delete archives a record by ID via DELETE /api/v1/records/{id}.
//
// For soft delete, use Update with {"status": "archived"} instead.
func (m *Memory) Delete(ctx context.Context, recordID int64, opts ...ByIDOption) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	scope, scopeErr := resolveByIDScope("Delete", opts)
	if scopeErr != nil {
		return scopeErr
	}

	path := byIDPath(fmt.Sprintf("/api/v1/records/%d", recordID), scope)
	return m.conn.Delete(ctx, path)
}

// Forget is a placeholder for a future decay API. It always returns an error.
//
// Junior Tip [parity stub]: Python forget() and TypeScript forget() raise the
// same "not yet available" contract — use Delete or Update(..., archived).
func (m *Memory) Forget(ctx context.Context, memoryID int64) error {
	_ = ctx
	_ = memoryID
	return fmt.Errorf(
		"Forget is not yet available. Use Delete for hard removal or Update with status=archived for soft delete",
	)
}

// BatchUpdateStatus updates the status for multiple records at once.
//
// Useful for bulk operations like marking records as consolidated,
// archived, or hubbed.
func (m *Memory) BatchUpdateStatus(ctx context.Context, ids []int64, status string) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}

	payload := map[string]interface{}{"ids": ids, "status": status}
	_, err := m.conn.Patch(ctx, "/api/v1/records/mark-consolidated", payload)
	return err
}

// Supersede marks an old record as superseded by a new one.
//
// The old record remains in the graph but is annotated with superseded_by
// pointing to the new record. Search results prefer the newer version.
//
// With WithRecordScope BOTH ids must be live rows of that plane; otherwise the
// server answers 404 and supersedes nothing.
func (m *Memory) Supersede(ctx context.Context, oldID, newID int64, opts ...ByIDOption) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	scope, scopeErr := resolveByIDScope("Supersede", opts)
	if scopeErr != nil {
		return scopeErr
	}

	payload := map[string]interface{}{"old_id": oldID, "new_id": newID}
	_, err := m.conn.Post(ctx, byIDPath("/api/v1/records/supersede", scope), payload)
	return err
}
