package storage_test

import (
	"testing"
	"path/filepath"
	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/storage"
)

func TestBuildPath(t *testing.T) {
	fileStorage := storage.NewFileStorage("/data/storage")
	path, buildErr := fileStorage.BuildPath("tenant_123", "sess_abc", 42)
	if buildErr != nil {
		t.Fatalf("Unexpected error building path: %v", buildErr)
	}

	expected := filepath.Join("/data/storage", "tenant_123", "sess_abc", "42.gz")

	if path != expected {
		t.Errorf("Expected path %s, got %s", expected, path)
	}
}

// TestBuildPathRejectsTraversal verifies the path-traversal guard fails LOUD on
// malicious components instead of resolving outside BasePath.
func TestBuildPathRejectsTraversal(t *testing.T) {
	fileStorage := storage.NewFileStorage("/data/storage")

	traversalCases := []struct {
		name     string
		tenantID string
		uuid     string
	}{
		{"dotdot in tenant", "../etc", "sess_abc"},
		{"slash in tenant", "tenant/x", "sess_abc"},
		{"backslash in uuid", "tenant_123", "sess\\abc"},
		{"dotdot in uuid", "tenant_123", ".."},
		{"empty tenant", "", "sess_abc"},
	}

	for _, traversalCase := range traversalCases {
		t.Run(traversalCase.name, func(t *testing.T) {
			_, buildErr := fileStorage.BuildPath(traversalCase.tenantID, traversalCase.uuid, 1)
			if buildErr == nil {
				t.Errorf("Expected error for %q/%q, got nil", traversalCase.tenantID, traversalCase.uuid)
			}
		})
	}
}
