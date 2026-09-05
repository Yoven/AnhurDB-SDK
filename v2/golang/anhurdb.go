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
	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
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

// WithURL sets the AnhurDB server URL (default: https://anhurdb.yoven.ai).
var WithURL = client.WithURL

// WithUserID sets an explicit container tag (user identifier).
var WithUserID = client.WithUserID

// WithTenantID sets the X-Tenant-ID header for multi-tenant deployments.
var WithTenantID = client.WithTenantID

// WithLimit sets the maximum number of search results.
var WithLimit = client.WithLimit

// WithTypeFilter restricts search results to a specific memory type.
var WithTypeFilter = client.WithTypeFilter

// WithKeyword sets an optional free-text filter (query param "q") for SearchByType.
var WithKeyword = client.WithKeyword

// WithTimeout sets the HTTP client timeout.
var WithTimeout = client.WithTimeout

// Add-call options (SDK-parity write path): control the score, type, and
// metadata of a record stored via Memory.Add without breaking Add(ctx, text).

// WithScore sets the salience score (typically 0-10) on the added record.
var WithScore = client.WithScore

// WithType sets the memory type (e.g. "episodic", "semantic") on the record.
var WithType = client.WithType

// WithMetadata merges caller-supplied keys into the added record's metadata.
var WithMetadata = client.WithMetadata

// WithSessionID pins the session uuid for Add (must be registered via CreateSession).
var WithSessionID = client.WithSessionID

// WithMode selects Add write path: "ingest" (default) or "regular" (/records).
var WithMode = client.WithMode

// WithCreateSessionID registers an explicit session uuid in CreateSession.
var WithCreateSessionID = client.WithCreateSessionID

// WithCreateSessionMetadata sets optional metadata on CreateSession.
var WithCreateSessionMetadata = client.WithCreateSessionMetadata

// WithUploadMode selects UploadFile plane: tenant_shared or client_shared.
var WithUploadMode = client.WithUploadMode

// WithCreateType sets the record type for Memory.Create.
var WithCreateType = client.WithCreateType

// WithCreateScore sets the score for Memory.Create.
var WithCreateScore = client.WithCreateScore

// Session filter (ADR-0014) — every search call takes a mandatory `sessions`
// argument. Re-exported here so callers of the facade never need to import
// client/ just to spell the wildcard.

// SessionsAll returns []string{"*"} — every session inside the search scope.
var SessionsAll = client.SessionsAll

// SessionWildcard is the "every session in scope" token ("*").
const SessionWildcard = client.SessionWildcard

// MaxSessionFilterUUIDs is the cap on explicitly named sessions per request.
const MaxSessionFilterUUIDs = client.MaxSessionFilterUUIDs

// --------------------------------------------------------------------------
// ADR-0031 search controls (2026-09-05) — re-exported so a caller of the
// facade can shape a search without also importing client/.
// --------------------------------------------------------------------------

// WithSearchMode selects the retrieval budget of ONE search: SearchModeFast,
// SearchModeBalanced or SearchModeSemantic.
//
// Junior Tip [do not confuse it with WithMode above]: WithMode picks the WRITE
// path for Add ("ingest" / "regular"). Two unrelated concepts that would both
// have been called "mode" — hence the longer name here.
var WithSearchMode = client.WithSearchMode

// WithSemanticTimeoutMs caps the Embed+HNSW wait of one search, in ms.
// 0 (the default) lets the server apply its own 700ms budget.
var WithSemanticTimeoutMs = client.WithSemanticTimeoutMs

// WithDebugSignals asks the server for per-hit signals and per-leg score
// distributions, readable via Memory.SearchWithSignals.
var WithDebugSignals = client.WithDebugSignals

// The three legal search budgets, mirroring the server's handler.SearchMode*.
const (
	// SearchModeFast answers from the NOW legs only — never waits on the embedder.
	SearchModeFast = client.SearchModeFast
	// SearchModeBalanced embeds under a budget and degrades instead of failing.
	SearchModeBalanced = client.SearchModeBalanced
	// SearchModeSemantic promises strict semantics: 503/504 rather than a
	// quietly lexical answer. The SDK fails loud if the server ignored it.
	SearchModeSemantic = client.SearchModeSemantic
)

// Version is this SDK's semantic version — the same string its User-Agent
// reports on every request.
const Version = client.Version
