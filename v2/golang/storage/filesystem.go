package storage

import (
	"path/filepath"
	"fmt"
)

// FileStorage handles reading and writing compressed cognitive payload files.
type FileStorage struct {
	BasePath string
}

// NewFileStorage creates a new FileStorage reference.
func NewFileStorage(basePath string) *FileStorage {
	return &FileStorage{
		BasePath: basePath,
	}
}

// BuildPath constructs the expected file path for a tenant record.
func (f *FileStorage) BuildPath(tenantID, uuid string, recordID int) string {
	return filepath.Join(f.BasePath, tenantID, uuid, fmt.Sprintf("%d.gz", recordID))
}
