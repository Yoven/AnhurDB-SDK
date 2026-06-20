/*
Package models defines the data structures for AnhurDB memory records.

These types match the Go server's JSON serialisation exactly and are
shared across the Memory client and any custom integrations.
*/
package models

import "time"

// CreateRequest represents a high-level input from a client to AnhurDB.
//
// The SDK hides cognitive params (weight, dimension, vectors) so the
// server handles embedding and classification automatically.
type CreateRequest struct {
	UUID     string            `json:"uuid"`
	Type     MemoryType        `json:"type"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

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

// SearchResult wraps a Record with its relevance score from search.
type SearchResult struct {
	Record     Record  `json:"record"`
	Similarity float64 `json:"similarity"`
}
