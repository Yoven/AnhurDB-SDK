package client

// search_session.go — SearchSession, the session-pinned search: sugar over
// runSearch with scope=sessions and exactly one session, never a widened
// tenant search.

import (
	"context"
	"strings"
)

// SearchSession runs a hybrid (vector + full-text) search scoped to a single
// session UUID via POST /api/v1/search with scope=sessions. It is sugar over
// Search(ctx, query, []string{sessionUUID}) — the MCP semantic_search(uuid=...)
// contract expressed in the ADR-0014 grammar.
//
// An empty sessionUUID falls back to the client's current session, matching the
// Python and TypeScript SDKs. When there is no current session either, the call
// FAILS instead of searching the whole tenant.
//
// Junior Tip [why the empty uuid stopped meaning "everything"]: this method used
// to send `uuid: ""`, which the server read as "no session filter" — a method
// named SearchSession silently searching every session was the exact defect
// ADR-0014 exists to kill. Widening is now spelled Search(..., SessionsAll()).
// Junior Tip [why this delegates instead of building its own body, 2026-09-05]:
// until this change SearchSession assembled its OWN payload and called
// conn.PostRead directly, which put it OUTSIDE the ADR-0031 path entirely. It
// compiled and looked correct because SearchOption is a type ALIAS of ReadOption
// (search_options.go), so WithSearchMode / WithSemanticTimeoutMs /
// WithDebugSignals were ACCEPTED by the signature and then discarded — three
// knobs silently dropped, a NEGATIVE budget accepted that the very same SDK
// refuses on Search, and the cross-version SERVER_TOO_OLD guard never running.
// TypeScript searchSession and Python search_session both delegate to their
// shared search implementation and therefore never had the defect.
//
// The rule this leaves behind: a second assembly site for the same request is
// not a shortcut, it is a divergence with a delay fuse. Any new session-scoped
// or scope-pinned read must end in runSearch, exactly like this one.
func (m *Memory) SearchSession(ctx context.Context, sessionUUID, query string, opts ...ReadOption) ([]SearchResult, error) {
	targetSession := strings.TrimSpace(sessionUUID)
	if targetSession == "" {
		targetSession = strings.TrimSpace(m.sessionUUID)
	}

	// Preserve the historical limit contract EXACTLY: this method has always
	// coerced a non-positive limit to 10, which runSearch's own default does not
	// do for an explicit WithLimit(0). Probing the caller's options first and
	// re-asserting the default only when needed keeps the wire byte-identical
	// for every caller that existed before this change.
	sessionOptions := make([]SearchOption, 0, len(opts)+2)
	sessionOptions = append(sessionOptions, opts...)
	if probedConfig := applyReadOptions(opts); probedConfig.limit <= 0 {
		sessionOptions = append(sessionOptions, WithLimit(10))
	}
	// Scope is PINNED and goes last so it wins over a caller-supplied WithScope —
	// a method named SearchSession that searches a shared plane would be the same
	// class of lie ADR-0014 exists to kill. TypeScript pins it the same way
	// (`{...options, scope: "sessions"}`), as does Python (`scope="sessions"`).
	sessionOptions = append(sessionOptions, WithScope(searchScopeSessions))

	outcome, searchErr := m.runSearch(ctx, query, []string{targetSession}, sessionOptions...)
	if searchErr != nil {
		return nil, searchErr
	}
	return outcome.Results, nil
}
