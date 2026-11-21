package client

// memory_options.go — the constructor options of NewMemory: server URL, user id
// (container tag), tenant header, timeout and an injected *http.Client.

import (
	"net/http"
	"time"
)

// Option configures the Memory client at construction time.
// Functional options let us add new config knobs without breaking
// existing callers.
type Option func(*memoryConfig)

// memoryConfig holds all constructor parameters for Memory.
type memoryConfig struct {
	url        string
	userID     string
	tenantID   string
	timeout    time.Duration
	httpClient *http.Client
}

// WithURL sets the AnhurDB server URL (default: https://anhurdb.yoven.ai).
func WithURL(url string) Option {
	return func(cfg *memoryConfig) {
		cfg.url = url
	}
}

// WithUserID sets an explicit user identifier used as the container_tag.
// If not set, the SDK derives a stable tag from the API key hash.
func WithUserID(id string) Option {
	return func(cfg *memoryConfig) {
		cfg.userID = id
	}
}

// WithTenantID sets the tenant ID header for multi-tenant deployments.
func WithTenantID(id string) Option {
	return func(cfg *memoryConfig) {
		cfg.tenantID = id
	}
}

// WithTimeout sets the HTTP client timeout (default: 30s).
func WithTimeout(d time.Duration) Option {
	return func(cfg *memoryConfig) {
		cfg.timeout = d
	}
}

// WithHTTPClient injects the *http.Client used for every request — e.g. one
// whose Transport dials through an SSRF guard, or a proxy. The SDK still
// enforces its no-redirect policy (X-API-Key must never follow a 3xx to
// another origin): CheckRedirect is set on a shallow copy, so the caller's
// client is not mutated. Timeout falls back to WithTimeout/30s when zero.
func WithHTTPClient(c *http.Client) Option {
	return func(cfg *memoryConfig) {
		cfg.httpClient = c
	}
}
