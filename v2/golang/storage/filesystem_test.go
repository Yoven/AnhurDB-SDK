package storage_test

import (
	"testing"
	"path/filepath"
	"github.com/yoven/anhurdb-sdk/v2/golang/v2/storage"
)

func TestBuildPath(t *testing.T) {
	fs := storage.NewFileStorage("/data/storage")
	path := fs.BuildPath("tenant_123", "sess_abc", 42)
	
	expected := filepath.Join("/data/storage", "tenant_123", "sess_abc", "42.gz")
	
	if path != expected {
		t.Errorf("Expected path %s, got %s", expected, path)
	}
}
