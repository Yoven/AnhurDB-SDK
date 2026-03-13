package models

import "time"

// CreateRequest represents a High-Level input from a Client to AnhurDB.
// The SDK hides cognitive params (weight, dimension, vectors) so the Server handles it.
type CreateRequest struct {
	UUID        string            `json:"uuid"`
	Type        MemoryType        `json:"type"`
	Content     string            `json:"content"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Record ensures interface compatibility for backwards references
type Record struct {
	ID             int          `json:"id,omitempty"`
	UUID           string       `json:"uuid"`
	Type           MemoryType   `json:"type"`
	
	RelatedIDs     []int        `json:"related_json,omitempty"`
	MainIDs        []int        `json:"main_json,omitempty"`
	
	ConsolidateID  int          `json:"consolidate_id"`
	Consolidated   bool         `json:"consolidated"`
	Archived       bool         `json:"archived"`
	Status         MemoryStatus `json:"status"`
	
	Metadata       string       `json:"metadata"`
	Summary        string       `json:"summary"`
	
	FilePath       string       `json:"file_path,omitempty"`
	Checksum       string       `json:"checksum,omitempty"`
	
	CreatedAt      *time.Time   `json:"created_at,omitempty"`
	UpdatedAt      *time.Time   `json:"updated_at,omitempty"`
	
	Vector         string       `json:"vector,omitempty"`
	
	// Payload content from FileStorage (not from DB directly)
	Content        any          `json:"content,omitempty"`
}

type SearchResult struct {
	Record      Record  `json:"record"`
	Similarity  float64 `json:"similarity"`
}
