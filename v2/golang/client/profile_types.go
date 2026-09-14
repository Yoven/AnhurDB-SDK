package client

// profile_types.go — one domain: the response shape of GET /api/v1/profile.
//
// Split out of types.go on 2026-09-14 (types.go was 680 lines, far past the
// ~300-line house cut) so the three blocks below could be modelled concretely
// instead of staying map[string]interface{}.

// ProfileResult contains the memory profile for a container tag.
//
// The wire envelope has exactly three keys — static, dynamic, stats
// (server/handler/profile.go:27-51). It carries NO tag and NO status: those two
// fields lived on this struct until 2026-09-14 and never decoded from anything,
// because the server has never sent them. A field the server never sends reads
// like a safety net and is really a dead branch, so they were deleted.
type ProfileResult struct {
	Static  ProfileStatic  `json:"static"`
	Dynamic ProfileDynamic `json:"dynamic"`
	Stats   ProfileStats   `json:"stats"`
}

// ProfileStatic is the slow-moving half of a profile: what the tenant has
// established about itself. Every block is a list of summaries, never records.
type ProfileStatic struct {
	Facts       []string `json:"facts"`
	Preferences []string `json:"preferences"`
	Decisions   []string `json:"decisions"`
	Risks       []string `json:"risks"`
	Emotions    []string `json:"emotions"`
	Highlight   []string `json:"highlight"`
}

// ProfileDynamic is the fast-moving half: what the tenant is doing lately.
type ProfileDynamic struct {
	RecentTasks  []string `json:"recent_tasks"`
	RecentTopics []string `json:"recent_topics"`
}

// ProfileStats counts what the tag covers.
//
// Junior Tip [last_active here, last_activity on a session row]: the server
// genuinely spells this key two ways on two different objects — `last_active`
// on the profile stats block, `last_activity` on a SessionStats row. They are
// different objects from different handlers. Do NOT "fix" either spelling: the
// tag that does not match the wire decodes to "" in silence, and an empty
// timestamp reads as "never used" rather than as a decoding bug.
type ProfileStats struct {
	TotalRecords int    `json:"total_records"`
	Sessions     int    `json:"sessions"`
	LastActive   string `json:"last_active"`
}
