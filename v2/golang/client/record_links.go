package client

// record_links.go — the graph-link mutations on existing records: appending
// parent (main_ids) and peer (related_ids) links and setting the consolidate_id
// child → parent pointer. All are PATCH routes that union or set one column.

import (
	"context"
	"fmt"
)

// AppendMainIDs appends parent record IDs to the main_ids array of a single
// record via PATCH /api/v1/records/append-main-ids. Server-side the operation
// reads, deduplicates, and writes back — safe to call repeatedly with the
// same payload (idempotent on the union of existing + supplied IDs).
func (m *Memory) AppendMainIDs(ctx context.Context, recordID int64, mainIDs []int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return fmt.Errorf("AppendMainIDs: recordID must be > 0")
	}
	if len(mainIDs) == 0 {
		return nil // nothing to append, server would no-op too
	}
	payload := map[string]interface{}{
		"ids":                []int64{recordID},
		"main_ids_to_append": mainIDs,
	}
	_, patchErr := m.conn.Patch(ctx, "/api/v1/records/append-main-ids", payload)
	return patchErr
}

// AppendMainLinks appends parent record IDs to a batch of records via the same
// PATCH /api/v1/records/append-main-ids route. Prefer AppendMainIDs for a
// single child record.
//
// Junior Tip [batch form]: the wire payload already accepts multiple `ids`;
// this helper exposes that shape so Go matches Python append_main_links /
// TypeScript appendMainLinks without callers hand-rolling the map.
func (m *Memory) AppendMainLinks(ctx context.Context, ids []int64, mainIDsToAppend []int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if len(ids) == 0 || len(mainIDsToAppend) == 0 {
		return nil
	}
	payload := map[string]interface{}{
		"ids":                ids,
		"main_ids_to_append": mainIDsToAppend,
	}
	_, patchErr := m.conn.Patch(ctx, "/api/v1/records/append-main-ids", payload)
	return patchErr
}

// AppendRelatedIDs appends peer record IDs to the related_ids array of a single
// record via PATCH /api/v1/records/append-related-ids. Server-side the operation
// reads, deduplicates, and writes back — safe to call repeatedly with the same
// payload (idempotent on the union of existing + supplied IDs).
func (m *Memory) AppendRelatedIDs(ctx context.Context, recordID int64, relatedIDs []int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if recordID <= 0 {
		return fmt.Errorf("AppendRelatedIDs: recordID must be > 0")
	}
	if len(relatedIDs) == 0 {
		return nil // nothing to append, server would no-op too
	}
	payload := map[string]interface{}{
		"ids":                   []int64{recordID},
		"related_ids_to_append": relatedIDs,
	}
	_, patchErr := m.conn.Patch(ctx, "/api/v1/records/append-related-ids", payload)
	return patchErr
}

// LinkConsolidated sets the consolidate_id column on a batch of child
// records via PATCH /api/v1/records/consolidate-ids so subsequent queries
// can navigate child → parent in one column read.
func (m *Memory) LinkConsolidated(ctx context.Context, ids []int64, consolidateID int64) error {
	if m.conn == nil {
		return ErrEmptyAPIKey
	}
	if len(ids) == 0 {
		return nil
	}
	if consolidateID <= 0 {
		return fmt.Errorf("LinkConsolidated: consolidateID must be > 0")
	}
	payload := map[string]interface{}{
		"ids":            ids,
		"consolidate_id": consolidateID,
	}
	_, patchErr := m.conn.Patch(ctx, "/api/v1/records/consolidate-ids", payload)
	return patchErr
}
