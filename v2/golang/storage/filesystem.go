package storage

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// Read reads and decompresses a gzip file from the local filesystem.
// Returns the decompressed bytes.
func (f *FileStorage) Read(tenantID, uuid string, recordID int) ([]byte, error) {
	path := f.BuildPath(tenantID, uuid, recordID)
	return readGzipFile(path)
}

// readGzipFile opens a .gz file and returns decompressed content.
func readGzipFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	gr, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("reading gzip header %s: %w", path, err)
	}
	defer gr.Close()

	return io.ReadAll(gr)
}

// ReadWithFallback first attempts to read and decompress from disk.
// If the path is inaccessible (e.g., due to strict V8 Hex isolation),
// it falls back to the REST API.
func (f *FileStorage) ReadWithFallback(tenantID, uuid string, recordID int, apiURL, apiKey string) ([]byte, error) {
	path := f.BuildPath(tenantID, uuid, recordID)
	data, err := readGzipFile(path)
	if err == nil {
		// Found and decompressed locally
		return data, nil
	}

	// Local file not found or corrupted, fallback to REST over HTTP
	if apiURL == "" {
		return nil, fmt.Errorf("record not found locally at %s and no API URL provided for fallback", path)
	}

	url := fmt.Sprintf("%s/api/v1/records/%d/content", strings.TrimRight(apiURL, "/"), recordID)
	req, reqErr := http.NewRequest("GET", url, nil)
	if reqErr != nil {
		return nil, reqErr
	}

	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	if tenantID != "" {
		req.Header.Set("X-Tenant-ID", tenantID)
	}

	client := &http.Client{}
	resp, doErr := client.Do(req)
	if doErr != nil {
		return nil, doErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(fmt.Sprintf("API fallback failed with status %d", resp.StatusCode))
	}

	return io.ReadAll(resp.Body)
}
