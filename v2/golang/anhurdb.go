/*
Package anhurdb provides the top-level entry point for the AnhurDB Go SDK.

This is a convenience re-export so callers can write:

	mem := anhurdb.NewMemory("key")
	mem.Add(ctx, "some memory")

instead of importing the client sub-package directly.

All real logic lives in client/. This file is a thin facade so the
import path stays clean for end users.

Junior Tip: The Go SDK has ZERO external dependencies — it uses only
net/http, crypto/sha256, encoding/json, and other stdlib packages.
*/
package anhurdb

import (
	"github.com/anhurdb/sdk-go/v2/client"
)

// NewMemory creates a new Memory instance connected to AnhurDB.
//
// The apiKey is required. Use functional options to configure URL,
// user ID, and tenant ID:
//
//	mem := anhurdb.NewMemory("key", anhurdb.WithURL("http://localhost:8000"))
//
// See client.NewMemory for full documentation.
func NewMemory(apiKey string, opts ...client.Option) *client.Memory {
	return client.NewMemory(apiKey, opts...)
}

// Re-export option constructors so callers don't need a separate import.

// WithURL sets the AnhurDB server URL (default: https://api.anhurdb.com).
var WithURL = client.WithURL

// WithUserID sets an explicit container tag (user identifier).
var WithUserID = client.WithUserID

// WithTenantID sets the X-Tenant-ID header for multi-tenant deployments.
var WithTenantID = client.WithTenantID

// WithLimit sets the maximum number of search results.
var WithLimit = client.WithLimit

// WithTypeFilter restricts search results to a specific memory type.
var WithTypeFilter = client.WithTypeFilter

// WithTimeout sets the HTTP client timeout.
var WithTimeout = client.WithTimeout
