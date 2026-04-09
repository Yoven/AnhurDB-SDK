package client_test

import (
	"testing"

	"github.com/anhurdb/sdk-go/v2/client"
)

// Junior Tip: These tests verify the connection constructor logic
// without hitting a real server. They catch regressions in URL
// normalisation and header setup.

func TestNewConnection(t *testing.T) {
	conn := client.NewConnection("http://localhost:8080/", "test_key", 0)

	if conn.BaseURL != "http://localhost:8080" {
		t.Errorf("Expected base url http://localhost:8080, got %s", conn.BaseURL)
	}

	if conn.APIKey != "test_key" {
		t.Errorf("Expected APIKey test_key, got %s", conn.APIKey)
	}

	if conn.HTTPClient == nil {
		t.Error("Expected HTTPClient to be initialized")
	}
}

func TestNewConnectionTrimSlash(t *testing.T) {
	conn := client.NewConnection("https://api.anhurdb.com///", "key", 0)

	if conn.BaseURL != "https://api.anhurdb.com" {
		t.Errorf("Expected trailing slashes stripped, got %s", conn.BaseURL)
	}
}

func TestNewConnectionTenantID(t *testing.T) {
	conn := client.NewConnection("http://localhost:8000", "key", 0)
	conn.TenantID = "tenant-42"

	if conn.TenantID != "tenant-42" {
		t.Errorf("Expected TenantID tenant-42, got %s", conn.TenantID)
	}
}

func TestNewMemoryDefaults(t *testing.T) {
	mem := client.NewMemory("test-api-key-12345")

	if mem.ContainerTag() == "" {
		t.Error("Expected container tag to be derived from API key")
	}

	if mem.SessionID() == "" {
		t.Error("Expected session ID to be generated")
	}

	// Session ID must start with the container tag.
	tag := mem.ContainerTag()
	sid := mem.SessionID()
	if len(sid) < len(tag) || sid[:len(tag)] != tag {
		t.Errorf("Session ID %q should start with container tag %q", sid, tag)
	}
}

func TestNewMemoryWithUserID(t *testing.T) {
	mem := client.NewMemory("key", client.WithUserID("custom-user"))

	if mem.ContainerTag() != "custom-user" {
		t.Errorf("Expected container tag custom-user, got %s", mem.ContainerTag())
	}
}

func TestNewMemoryEmptyKey(t *testing.T) {
	mem := client.NewMemory("")

	// Should not panic, but all methods should return an error.
	if mem.ContainerTag() != "" {
		t.Errorf("Expected empty container tag for empty API key, got %s", mem.ContainerTag())
	}
}

func TestNewSession(t *testing.T) {
	mem := client.NewMemory("key")
	old := mem.SessionID()
	mem.NewSession()
	next := mem.SessionID()

	if old == next {
		t.Error("NewSession should generate a different session ID")
	}
}

func TestContainerTagDeterministic(t *testing.T) {
	// Junior Tip: Same API key must always produce the same container tag.
	// This is critical — it ensures memories are grouped consistently.
	mem1 := client.NewMemory("deterministic-key")
	mem2 := client.NewMemory("deterministic-key")

	if mem1.ContainerTag() != mem2.ContainerTag() {
		t.Errorf("Same key should produce same tag: %s vs %s",
			mem1.ContainerTag(), mem2.ContainerTag())
	}
}
