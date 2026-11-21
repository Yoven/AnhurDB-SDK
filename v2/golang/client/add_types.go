package client

// add_types.go — the inputs and outputs of Memory.Add: the AddOption knobs
// (score, type, metadata, session, write mode), AddResult, and the two wire
// response shapes Add decodes (/ingest and /records).

// AddResult is returned by Memory.Add, Memory.Create and CreateInSession. It
// contains the created record(s) and Mode, the write door that ran.
type AddResult struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id,omitempty"`
	Records   []RecordSummary `json:"records"`
	Status    string          `json:"status"`
	// Mode is "ingest" (POST /api/v1/ingest) or "records" (POST /api/v1/records).
	Mode string `json:"mode"`
}

// The two AddResult.Mode values: the route that wrote the record.
//
// Junior Tip [renamed in 4.0.0]: they used to be "cloud" and "oss", names that
// described the removed 404 → /records fallback for servers without /ingest.
// The value now names the door, which is the only thing a caller can act on:
// "ingest" means extraction satellites will follow, "records" means exactly
// one record was written and nothing else will be.
const (
	// addModeIngest: written by POST /api/v1/ingest.
	addModeIngest = "ingest"
	// addModeRecords: written by POST /api/v1/records.
	addModeRecords = "records"
)

// RecordSummary is a lightweight descriptor of a created record.
type RecordSummary struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// AddOption configures a single Memory.Add call.
type AddOption func(*addConfig)

// addConfig holds the per-call overrides for Memory.Add. A nil pointer field
// means "caller did not specify — use the server/SDK default", which is how we
// keep score/type from being silently forced when the caller omits them.
type addConfig struct {
	score     *int
	memType   *string
	metadata  map[string]interface{}
	sessionID string
	// writeMode selects ingest (POST /api/v1/ingest) or regular (POST
	// /api/v1/records). Empty means ingest — the historical default.
	writeMode string
}

// WithScore sets the salience score (typically 0-10) on the record being added.
// When omitted, the SDK falls back to the historical default of 5.
func WithScore(score int) AddOption {
	return func(cfg *addConfig) {
		cfg.score = &score
	}
}

// WithType sets the memory type (e.g. "episodic", "semantic", "procedural").
// When omitted, the SDK falls back to the historical default of "episodic".
func WithType(memType string) AddOption {
	return func(cfg *addConfig) {
		cfg.memType = &memType
	}
}

// WithMetadata merges caller-supplied keys into the record metadata. The SDK
// always sets container_tag; caller keys are layered on top (caller wins on a
// key collision, except container_tag which the SDK owns).
func WithMetadata(metadata map[string]interface{}) AddOption {
	return func(cfg *addConfig) {
		cfg.metadata = metadata
	}
}

// WithSessionID pins the SESSION (uuid) the ingested record lands in. The tenant
// comes from the API key; the session is the caller's own unit of conversation.
func WithSessionID(sessionID string) AddOption {
	return func(cfg *addConfig) {
		cfg.sessionID = sessionID
	}
}

// WithMode selects the write path for Memory.Add. Valid values are "ingest"
// (POST /api/v1/ingest — default) and "regular" (POST /api/v1/records as
// episodic). Callers must register the session via CreateSession before
// either path succeeds on session-first servers.
func WithMode(writeMode string) AddOption {
	return func(cfg *addConfig) {
		cfg.writeMode = writeMode
	}
}

// ingestResponse is the wire format returned by POST /api/v1/ingest.
type ingestResponse struct {
	SessionID string          `json:"session_id"`
	Records   []RecordSummary `json:"records"`
	ID        int64           `json:"id"`
}

// recordCreateResponse is the wire format returned by POST /api/v1/records.
type recordCreateResponse struct {
	ID int64 `json:"id"`
}
