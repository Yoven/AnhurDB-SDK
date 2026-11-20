package client

// record_scope.go — which STORE a record id names (ADR-0029, L2 tenant library).
//
// One domain in one sentence: "the closed set of planes a by-id call may address,
// the option that carries one, and the two rules that pick the right plane for a
// search hit or an upload".
//
// Junior Tip [why a by-id call needs a plane at all — server fix/l2-lib-scope,
// 2026-11-20]: a record id is a SQLite rowid PER STORE. Since tenant-library
// uploads land in {c}_{t}_lib, id 42 from a tenant_shared hit names a library row
// there — and the SAME 42 names an unrelated chat row in {c}_{t}. Without a scope
// the server reads (or PATCHes, archives, supersedes) the caller's own store, i.e.
// the wrong record, and answers 200. With a scope the server resolves the store from
// the authenticated caller and proves every id is a live row of that plane, or
// answers 404 (server/service/record_scoped_by_id.go:23-52). No scope keeps the old
// behaviour, so existing callers are unchanged.

import (
	"strings"
)

// RecordScope names the plane (store) a by-id call targets.
type RecordScope string

const (
	// RecordScopeSessions is the caller's own chat store minus the shared-* rows.
	RecordScopeSessions RecordScope = "sessions"
	// RecordScopeTenantShared is the tenant library {c}_{t}_lib (uploads after the L2 switch).
	RecordScopeTenantShared RecordScope = "tenant_shared"
	// RecordScopeTenantSharedLegacy is the pre-migration library rows still in {c}_{t}.
	RecordScopeTenantSharedLegacy RecordScope = "tenant_shared_legacy"
	// RecordScopeClientShared is the client-wide library {c}_shared.
	RecordScopeClientShared RecordScope = "client_shared"
)

// tenantLibrarySessionSuffix ends the session uuid of every row in the L2 PRIMARY
// library store (shared-{c}-{t}_lib). Server: service.tenantSharedLegs.
const tenantLibrarySessionSuffix = "_lib"

// ByIDOption configures a by-id call (Get, ReadContent, GetContext, GetGrounding,
// BatchReadContent, Update, Delete, Supersede, UploadStatus).
type ByIDOption func(*byIDConfig)

// byIDConfig is the resolved set of ByIDOptions.
type byIDConfig struct {
	scope RecordScope
}

// WithRecordScope addresses the store the id lives in. Pass the plane the id CAME
// from: the scope of the search that returned it (see TenantSharedScopeForHit for
// tenant_shared hits) or the scope of the upload (UploadResult.Scope).
func WithRecordScope(scope RecordScope) ByIDOption {
	return func(cfg *byIDConfig) {
		cfg.scope = scope
	}
}

// TenantSharedScopeForHit picks the by-id scope for a hit returned by a tenant_shared
// search: the hit's record UUID (its session) ends in "_lib" for a row of the L2
// library store, otherwise it is a pre-migration row of the legacy store.
//
// Junior Tip [why the search scope alone is not enough]: one tenant_shared search reads
// TWO stores and merges them, so "tenant_shared" does not say which store a given hit
// came from. The session uuid does, and the server uses the same suffix to admit a
// row into a leg (ScopeLeg.AdmitsRecordSession).
func TenantSharedScopeForHit(recordUUID string) RecordScope {
	if strings.HasSuffix(strings.TrimSpace(recordUUID), tenantLibrarySessionSuffix) {
		return RecordScopeTenantShared
	}
	return RecordScopeTenantSharedLegacy
}

// UploadStatusScope is the plane an upload lands in, mirroring the server's
// ResolveUploadDestination: chat (or a session without a mode) → sessions,
// client_shared → client_shared, anything else (default, tenant_shared) →
// tenant_shared.
func UploadStatusScope(mode, sessionID string) RecordScope {
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	if normalizedMode == "" && strings.TrimSpace(sessionID) != "" {
		normalizedMode = "chat"
	}
	switch normalizedMode {
	case "chat":
		return RecordScopeSessions
	case "client_shared":
		return RecordScopeClientShared
	default:
		return RecordScopeTenantShared
	}
}

// resolveByIDScope applies opts and validates the scope locally.
//
// Junior Tip [why validate here and not let the server say 400]: an unknown value is
// a programming error that can never succeed, and shared_all names TWO stores — the
// server refuses it for by-id too (ErrSharedAllNotAddressableByID). Refusing before
// the request leaves keeps the message about the SDK argument, not the wire.
func resolveByIDScope(method string, opts []ByIDOption) (RecordScope, error) {
	cfg := byIDConfig{}
	for _, applyOption := range opts {
		applyOption(&cfg)
	}
	trimmedScope := RecordScope(strings.TrimSpace(string(cfg.scope)))
	switch trimmedScope {
	case "", RecordScopeSessions, RecordScopeTenantShared, RecordScopeTenantSharedLegacy, RecordScopeClientShared:
		return trimmedScope, nil
	default:
		return "", newValidationError(
			"INVALID_PARAM: %s: scope %q is not addressable by id (want sessions|tenant_shared|tenant_shared_legacy|client_shared)",
			method, string(trimmedScope))
	}
}

// byIDPath appends ?scope= to path when a scope was given; no scope = path unchanged.
func byIDPath(path string, scope RecordScope) string {
	if scope == "" {
		return path
	}
	return path + "?scope=" + string(scope)
}
