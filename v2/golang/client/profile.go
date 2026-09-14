package client

// profile.go — one domain: GET /api/v1/profile.
//
// Split out of client.go on 2026-09-14. client.go was 1359 lines, far past the
// ~300-line house cut, and house law forbids GROWING a file already over it —
// so the domain being touched moved out before it changed. Its option type is
// in profile_options.go and its response shape in profile_types.go; the three
// files are the whole profile surface.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// Profile retrieves the memory profile for a container tag.
//
// With no options it reads the client's own derived container tag. Pass
// WithProfileTag to read a different tag inside the SAME tenant — the tenant
// always comes from the API key, never from the tag
// (server/handler/profile.go:63-66).
//
// Junior Tip [an unknown tag is an empty profile with HTTP 200, 2026-09-14]:
// a tag nobody has written under answers 200 with all-zero stats and
// last_active "". That is the server's answer and it is returned verbatim.
// Turning it into an error would dress a caller's typo as a failure and send
// them retrying a request that can never succeed.
//
// Junior Tip [why an empty tag is refused before the request leaves]: omitting
// the tag entirely is a guaranteed HTTP 400 ("tag: tag is required",
// live-verified 2026-09-14). Sending one anyway would spend a round trip to
// learn what the SDK already knew.
//
// If the server doesn't have a profile endpoint yet (OSS without agents),
// it returns an empty profile rather than failing — matching the Python
// SDK behaviour.
func (m *Memory) Profile(ctx context.Context, opts ...ProfileOption) (*ProfileResult, error) {
	if m.conn == nil {
		return nil, ErrEmptyAPIKey
	}

	cfg := applyProfileOptions(opts)
	requestedTag := cfg.tag
	if requestedTag == "" {
		requestedTag = m.containerTag
	}
	if requestedTag == "" {
		return nil, newValidationError("INVALID_PARAM: tag is required for Profile " +
			"(GET /api/v1/profile rejects an absent tag with HTTP 400)")
	}

	params := url.Values{}
	params.Set("tag", requestedTag)

	respBytes, err := m.conn.Get(ctx, "/api/v1/profile", params)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &ProfileResult{}, nil
		}
		return nil, err
	}

	var result ProfileResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parsing profile response: %w", err)
	}

	return &result, nil
}
