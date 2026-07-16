package client

import (
	"encoding/json"
	"testing"
)

// TestEntity_UnmarshalEntityTypeWireKey locks the AnhurDB list/search wire
// contract: entity bodies use entity_type, never bare type.
func TestEntity_UnmarshalEntityTypeWireKey(testHandle *testing.T) {
	rawJSON := []byte(`{
		"id": 42,
		"name": "chrome",
		"entity_type": "product",
		"summary": "browser",
		"attributes": {},
		"dimension": 3,
		"first_seen": "2026-07-01T00:00:00Z",
		"last_seen": "2026-07-16T00:00:00Z",
		"mention_count": 7,
		"weight": 1
	}`)

	var entity Entity
	if unmarshalErr := json.Unmarshal(rawJSON, &entity); unmarshalErr != nil {
		testHandle.Fatalf("unmarshal: %v", unmarshalErr)
	}
	if entity.EntityType != "product" {
		testHandle.Errorf("EntityType = %q, want product (json tag must be entity_type)", entity.EntityType)
	}
	if entity.Name != "chrome" {
		testHandle.Errorf("Name = %q, want chrome", entity.Name)
	}
	if entity.MentionCount != 7 {
		testHandle.Errorf("MentionCount = %d, want 7", entity.MentionCount)
	}
}

// TestEntityResult_UnmarshalUpsertResponse locks upsert response key parity
// with list/search (entity_type, not type).
func TestEntityResult_UnmarshalUpsertResponse(testHandle *testing.T) {
	rawJSON := []byte(`{"id":9,"name":"chrome","entity_type":"product"}`)
	var result EntityResult
	if unmarshalErr := json.Unmarshal(rawJSON, &result); unmarshalErr != nil {
		testHandle.Fatalf("unmarshal: %v", unmarshalErr)
	}
	if result.EntityType != "product" {
		testHandle.Errorf("EntityType = %q, want product", result.EntityType)
	}
}
