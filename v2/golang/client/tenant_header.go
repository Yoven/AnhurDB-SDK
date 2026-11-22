package client

// tenant_header.go — the rule an X-Tenant-ID value must pass before it may leave
// this process.
//
// One domain: validating the caller-supplied tenant identifier. It lives apart
// from connection.go (already near the house 300-line cut) because it is a pure
// function over one string, testable without a server.

import "fmt"

// maxTenantIDLength mirrors database.MaxStoreTenantIDLength on the server
// (server/database/tenant_validation.go): the header names a COMPOSITE store,
// "<clientID>_<tenantID>" plus an optional "_lib", each part at most 64, so the
// whole name may reach 64+1+64+4 = 133. The master-key path refuses a longer
// X-Tenant-ID with HTTP 400 (server/middleware/auth_master_tenant.go).
//
// Junior Tip [incident 2026-11-22]: this was 64 for the WHOLE name. Agents
// address stores like "<36-char client>_kaveo-veterinaria-vacinacao-4d7d5ed7_lib"
// (78 chars), so every agent call for 50 tenants failed here before leaving the
// process and their uploaded files stayed at status=processing for hours.
const maxTenantIDLength = 133

// validateTenantIDHeader reports whether tenantID may be sent as X-Tenant-ID.
// Empty means "not configured" and is accepted.
//
// The rule is the server's identifier rule (database.validateIdentifierCharset):
// letters, digits, '-' and '_' only, at most 133 characters (a composite store name). The charset is
// checked BEFORE the length so the "(got N)" count is always an ASCII count —
// identical in Go, Python and TypeScript.
//
// Junior Tip [reject, never repair — why the SDK must not drop or strip]: the
// server's master-key path SANITIZES the header (strips every other character)
// and falls back to the "default" tenant when nothing is left. So "acme/prod"
// would silently become tenant "acmeprod", and a header the SDK quietly dropped
// (the old Go behaviour) would silently become "default". Either way the caller
// reads or writes ANOTHER tenant's store with no error anywhere. Failing loudly
// here, before any request, is the only point where the mistake is cheap.
// The error message is a pinned cross-SDK string (Python and TypeScript raise
// it byte-for-byte).
func validateTenantIDHeader(tenantID string) error {
	for position := 0; position < len(tenantID); position++ {
		character := tenantID[position]
		isLetter := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
		isDigit := character >= '0' && character <= '9'
		if !isLetter && !isDigit && character != '-' && character != '_' {
			return newValidationError(
				"INVALID_PARAM: tenant_id contains an invalid character — " +
					"allowed: letters, digits, '-' and '_'")
		}
	}
	if len(tenantID) > maxTenantIDLength {
		return newValidationError(
			"INVALID_PARAM: tenant_id exceeds %d characters (got %d)", maxTenantIDLength, len(tenantID))
	}
	return nil
}

// wrapRequestError keeps the SDK's typed validation error UNWRAPPED (so its
// pinned "INVALID_PARAM: ..." message and errors.As(*APIError) both survive) and
// wraps every other request-construction failure with its context.
func wrapRequestError(operation string, requestError error) error {
	if apiError, isAPIError := requestError.(*APIError); isAPIError {
		return apiError
	}
	return fmt.Errorf("%s: %w", operation, requestError)
}
