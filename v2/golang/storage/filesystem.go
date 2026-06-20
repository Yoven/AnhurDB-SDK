package storage

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fallbackHTTPTimeout bounds the REST fallback request.
//
// Junior Tip [security/availability]: an http.Client with no Timeout will wait
// FOREVER on a stalled connection (no read/write deadline). A hung fallback
// silently blocks the caller's goroutine, which in the pipeline cascades into
// stuck workers and eventual data-loss-by-timeout upstream. 30s matches the
// Python (timeout=30) and TypeScript (AbortSignal.timeout(30_000)) SDKs so all
// three behave identically.
const fallbackHTTPTimeout = 30 * time.Second

// validatePathComponent rejects a path component that could escape the storage
// root via directory traversal.
//
// Junior Tip [security]: tenantID and uuid flow into a filesystem path. If a
// caller (or a compromised upstream) passes "../../etc" the join would resolve
// OUTSIDE BasePath and read/leak arbitrary files. We reject "..", path
// separators ("/" and "\\"), and null bytes BEFORE building any path. This
// mirrors the Python SDK's _validate_path_component so the 3 SDKs enforce the
// same contract. We fail LOUD (return an error) rather than silently sanitising
// — a traversal attempt is a bug or an attack, never normal input.
func validatePathComponent(value, name string) error {
	if value == "" {
		return fmt.Errorf("%s cannot be empty", name)
	}
	if strings.Contains(value, "\x00") {
		return fmt.Errorf("%s contains null byte", name)
	}
	if strings.Contains(value, "..") {
		return fmt.Errorf("%s contains directory traversal sequence '..'", name)
	}
	if strings.Contains(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("%s contains path separator", name)
	}
	return nil
}

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
//
// Junior Tip [security]: tenantID and uuid are validated for path traversal
// BEFORE the join (see validatePathComponent). Returning an error rather than a
// bare string is deliberate — callers MUST surface a traversal attempt instead
// of letting a malicious path silently resolve outside BasePath. Matches the
// Python SDK's build_path, which raises ValueError on the same conditions.
func (f *FileStorage) BuildPath(tenantID, uuid string, recordID int) (string, error) {
	if validateErr := validatePathComponent(tenantID, "tenantID"); validateErr != nil {
		return "", validateErr
	}
	if validateErr := validatePathComponent(uuid, "uuid"); validateErr != nil {
		return "", validateErr
	}
	if recordID < 0 {
		return "", fmt.Errorf("recordID must be non-negative, got %d", recordID)
	}
	return filepath.Join(f.BasePath, tenantID, uuid, fmt.Sprintf("%d.gz", recordID)), nil
}

// Read reads and decompresses a gzip file from the local filesystem.
// Returns the decompressed bytes.
func (f *FileStorage) Read(tenantID, uuid string, recordID int) ([]byte, error) {
	path, pathErr := f.BuildPath(tenantID, uuid, recordID)
	if pathErr != nil {
		return nil, pathErr
	}
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
	path, pathErr := f.BuildPath(tenantID, uuid, recordID)
	if pathErr != nil {
		return nil, pathErr
	}
	data, readErr := readGzipFile(path)
	if readErr == nil {
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

	// Junior Tip [security]: two hardening properties on this client.
	//  1. Timeout caps the whole request so a stalled server can never hang the
	//     caller forever (the default &http.Client{} has NO timeout).
	//  2. CheckRedirect refuses to follow 3xx responses. The default client
	//     follows redirects AND re-sends our headers — including the X-API-Key
	//     credential — to whatever Location the server names, which could be a
	//     different (attacker-controlled) origin. http.ErrUseLastResponse makes
	//     Do() return the 3xx response as-is instead of following it, so the
	//     credential never crosses an origin boundary. Matches the TypeScript
	//     SDK's `redirect: "error"` and the Python SDK's allow_redirects=False.
	client := &http.Client{
		Timeout: fallbackHTTPTimeout,
		CheckRedirect: func(redirectReq *http.Request, viaRequests []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, doErr := client.Do(req)
	if doErr != nil {
		return nil, doErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API fallback failed with status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
