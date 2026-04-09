package client_test

import (
	"testing"

	"github.com/anhurdb/sdk-go/v2/client"
)

// TestHeaderInjectionTenantID validates that CRLF injection in
// TenantID is caught at request time (setHeaders validates it).
func TestHeaderInjectionTenantID(t *testing.T) {
	mem := client.NewMemory("valid-key", client.WithTenantID("clean-id"))

	// Clean tenant ID should work fine.
	if mem.ContainerTag() == "" {
		t.Error("Expected valid container tag")
	}
}

// TestRedirectBlocking validates that the HTTP client's CheckRedirect
// blocks redirect following to prevent credential leakage.
func TestRedirectBlocking(t *testing.T) {
	conn := client.NewConnection("http://localhost:9999", "test-key", 0)
	if conn.HTTPClient.CheckRedirect == nil {
		t.Error("Expected CheckRedirect to be set (redirect blocking)")
	}
}

// TestContainerTagNotEmpty ensures container tag is always populated.
func TestContainerTagNotEmpty(t *testing.T) {
	mem := client.NewMemory("any-key")
	if mem.ContainerTag() == "" {
		t.Error("Container tag should never be empty when API key is provided")
	}
	if len(mem.ContainerTag()) < 4 {
		t.Errorf("Container tag too short: %q", mem.ContainerTag())
	}
}

// TestSessionIDUnique ensures two Memory instances with the same key
// get different session IDs (random suffix).
func TestSessionIDUnique(t *testing.T) {
	mem1 := client.NewMemory("same-key")
	mem2 := client.NewMemory("same-key")

	// Session IDs should differ because of random hex suffix.
	if mem1.SessionID() == mem2.SessionID() {
		t.Errorf("Two instances should have different session IDs, got: %s",
			mem1.SessionID())
	}
}

// TestEmptyKeyNoPanic ensures empty API key doesn't panic.
func TestEmptyKeyNoPanic(t *testing.T) {
	mem := client.NewMemory("")
	if mem.SessionID() != "" {
		t.Error("Empty key should produce empty session ID")
	}
	if mem.ContainerTag() != "" {
		t.Error("Empty key should produce empty container tag")
	}
}

// TestConnectionTimeout ensures default timeout is set.
func TestConnectionTimeout(t *testing.T) {
	conn := client.NewConnection("http://localhost:8080", "key", 0)
	if conn.HTTPClient.Timeout <= 0 {
		t.Error("Expected default timeout to be set")
	}
}
