package client

// profile_options.go — one domain: the ONE parameter GET /api/v1/profile parses.
//
// Created 2026-09-14. Profile used to take `opts ...ReadOption` and write
// `_ = opts`, so a caller could pass WithLimit, WithScope or WithSince and get a
// compiling call that changed nothing.
//
// Junior Tip [why a narrow option type instead of reusing ReadOption]:
// ReadOption carries ~20 knobs and EXACTLY ZERO of them apply to this endpoint —
// the handler (server/handler/profile.go:63-66) resolves the tenant from the
// auth middleware and then reads one query key, `tag`. Reusing the wide type is
// how the fourteen `_ = opts` sites happened in the first place: a shared
// option type makes every wrong call compile. ProfileOption can only ever be
// constructed by WithProfileTag, so the compiler now enforces what the handler
// enforces.

// ProfileOption configures a single Memory.Profile call.
type ProfileOption func(*profileConfig)

// profileConfig holds the resolved per-call overrides for Profile.
// An empty tag means "use the client's own derived container tag".
type profileConfig struct {
	tag string
}

// WithProfileTag queries the profile of an explicit container tag instead of
// the client's own derived tag.
//
// The tag is an IN-TENANT filter, never a tenant selector: the server takes the
// tenant from the API key and uses `tag` only to narrow within it. Live-verified
// 2026-09-14 against production — an arbitrary tag belonging to the same tenant
// answers HTTP 200 with that tag's numbers, and there is no cross-tenant reach
// and no wildcard ("*" is a literal tag that matches nothing).
//
// Junior Tip [an unknown tag is an EMPTY profile, not a 404]: a tag nobody ever
// wrote under returns HTTP 200 with all-zero stats and last_active "". The SDK
// passes that through verbatim. Translating it into an error would dress a
// caller's typo up as a server failure, and the caller would then retry a
// request that can never succeed.
func WithProfileTag(tag string) ProfileOption {
	return func(cfg *profileConfig) {
		cfg.tag = tag
	}
}

// applyProfileOptions folds a variadic ProfileOption slice into a profileConfig.
func applyProfileOptions(opts []ProfileOption) profileConfig {
	cfg := profileConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}
