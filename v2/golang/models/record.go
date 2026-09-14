/*
Package models defines the data structures for AnhurDB memory records.

These types match the Go server's JSON serialisation exactly and are
shared across the Memory client and any custom integrations.

Junior Tip [what is NOT here, and why, 2026-09-14]: this package once also
declared CreateRequest, SearchResult and SessionStats. None of the three was
ever constructed or returned by anything — client.Create builds its payload
inline from createConfig, search returns the richer client.SearchResult, and
sessions return client.SessionStats. All three were deleted. A type that
describes a contract nobody speaks is worse than no type: a reader finds it,
believes it is the shape, and writes code against a wire format that does not
exist.
*/
package models

import "time"

// Record represents a unified cognitive memory record returned by AnhurDB.
//
// This struct covers every field the server may return across different
// endpoints (search, topology, manifest, content). Fields that don't
// apply to a particular endpoint will use their zero values.
type Record struct {
	ID   int        `json:"id,omitempty"`
	UUID string     `json:"uuid"`
	Type MemoryType `json:"type"`

	// Graph edges. Junior Tip [read-tag fix, 2026-06-18]: the server serializes
	// these as related_ids / main_ids on every READ response (search, manifest,
	// query, chats). The tags previously read related_json / main_json — which the
	// server never emits on reads — so RelatedIDs/MainIDs silently decoded to nil
	// and the graph edges were dropped on the new Query/ListChat/Manifest read
	// paths. The SDK never marshals Record for writes (verified), so correcting
	// the read tags is safe and stops the silent edge loss.
	RelatedIDs []int `json:"related_ids,omitempty"`
	MainIDs    []int `json:"main_ids,omitempty"`

	// Junior Tip [SDK weight/score parity, 2026-07-04]: the server emits weight (Ebbinghaus
	// decay strength) and score (client relevance) on every read; without these fields they
	// silently decoded to zero, so Go SDK callers lost the ranking signal Python/TS keep.
	// Canonical record shape across SDKs (weight float64, score int).
	Weight float64 `json:"weight"`
	Score  int     `json:"score"`

	// Consolidation pointers.
	ConsolidateID int          `json:"consolidate_id"`
	Consolidated  bool         `json:"consolidated"`
	Archived      bool         `json:"archived"`
	Status        MemoryStatus `json:"status"`

	// Content fields.
	Metadata string `json:"metadata"`
	Summary  string `json:"summary"`

	// Storage references.
	FilePath string `json:"file_path,omitempty"`
	Checksum string `json:"checksum,omitempty"`

	// Temporal versioning (v6).
	SupersededBy *int       `json:"superseded_by,omitempty"`
	ValidFrom    *time.Time `json:"valid_from,omitempty"`
	ValidUntil   *time.Time `json:"valid_until,omitempty"`

	// Timestamps.
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`

	// Binary vector (not usually returned in plain queries).
	Vector string `json:"vector,omitempty"`

	// Full payload content from FileStorage (not from DB directly).
	Content any `json:"content,omitempty"`
}
