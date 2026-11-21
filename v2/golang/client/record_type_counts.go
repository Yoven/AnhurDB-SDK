package client

// record_type_counts.go — the record-type taxonomy: ListTypes returns the
// canonical types locally (no HTTP) and CountByType tallies the tenant's
// manifest by type (paging ManifestGlobal).

import (
	"context"
	"fmt"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/models"
)

// CountByType returns a {type: count} map by paging the global manifest and
// tallying each record's "type" field. It is the MCP count_by_type contract.
func (m *Memory) CountByType(ctx context.Context, opts ...ReadOption) (map[string]int, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	counts := map[string]int{}
	const pageSize = 1000 // match Python/TS; server hard-caps manifest limit at 1000.
	offset := 0
	for {
		page, pageErr := m.ManifestGlobal(ctx, "", pageSize, offset, opts...)
		if pageErr != nil {
			return nil, fmt.Errorf("CountByType: paging manifest at offset %d: %w", offset, pageErr)
		}
		for _, record := range page.Records {
			counts[string(record.Type)]++
		}
		// Stop when the page is short OR the server says there is no more. We
		// require BOTH an empty/short page AND !has_more so the exactly-full
		// last-page false-positive on has_more cannot loop forever: a follow-up
		// page that returns 0 records always terminates the loop.
		if len(page.Records) == 0 || !page.HasMore {
			break
		}
		offset += len(page.Records)
	}
	return counts, nil
}

// ListTypes returns the canonical record-type taxonomy as a local, static slice
// — NO network call. It mirrors the MCP list_types tool, which exposes the same
func (m *Memory) ListTypes() []models.MemoryType {
	return []models.MemoryType{
		models.TypeEpisodic,
		models.TypeFact,
		models.TypePreference,
		models.TypeDecision,
		models.TypeTask,
		models.TypeRisk,
		models.TypeReasoning,
		models.TypeIdea,
		models.TypeEmotion,
		models.TypeConsolidated,
		models.TypeHub,
		models.TypeFile,
		models.TypeRouter,
	}
}
