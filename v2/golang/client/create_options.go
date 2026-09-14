package client

// create_options.go — one domain: the per-call options of Memory.Create
// (POST /api/v1/records).
//
// Split out of types.go on 2026-09-14 (types.go was past the ~300-line house
// cut) so WithCreateValidUntil could be added without growing an oversized
// file.

// CreateOption configures a single Memory.Create call. It is the full-fidelity
// counterpart to AddOption: Create always POSTs to /api/v1/records (no ingest
// worker override), so every option below is written to the record verbatim.
type CreateOption func(*createConfig)

// createConfig holds the per-call overrides for Memory.Create. Pointer fields
// give the same nil/set-to-zero/set three-state as addConfig: score 0 and ""
// type/status are LEGAL explicit values, so the zero value cannot double as the
// "unset" sentinel.
type createConfig struct {
	memType    *string
	score      *int
	status     *string
	relatedIDs []int64
	metadata   map[string]interface{}
	// validFrom is an RFC3339 UTC instant folded into the metadata envelope —
	// REST create reads valid_from from metadata only.
	// "" means "not supplied".
	validFrom string
	// validUntil is the closing half of the bi-temporal window, folded into the
	// same metadata envelope for the same reason. "" means "not supplied".
	validUntil string
}

// WithCreateType sets the record type (e.g. "fact","semantic","decision").
// Defaults to "episodic" when omitted.
func WithCreateType(memType string) CreateOption {
	return func(cfg *createConfig) {
		cfg.memType = &memType
	}
}

// WithCreateScore sets the salience score (typically 0-10). Defaults to 5.
func WithCreateScore(score int) CreateOption {
	return func(cfg *createConfig) {
		cfg.score = &score
	}
}

// WithCreateStatus sets the lifecycle status (e.g. "saved","processing").
// Defaults to "saved".
func WithCreateStatus(status string) CreateOption {
	return func(cfg *createConfig) {
		cfg.status = &status
	}
}

// WithCreateRelatedIDs sets the related_ids horizontal-edge array. The server
// still enforces graph topology on top of these (see service.enforceGraphTopology).
func WithCreateRelatedIDs(relatedIDs []int64) CreateOption {
	return func(cfg *createConfig) {
		cfg.relatedIDs = relatedIDs
	}
}

// WithCreateMetadata merges caller-supplied keys into the record metadata. The
// SDK always sets container_tag (it wins on a collision); caller keys are
// layered on top, identical to Add's WithMetadata.
func WithCreateMetadata(metadata map[string]interface{}) CreateOption {
	return func(cfg *createConfig) {
		cfg.metadata = metadata
	}
}

// WithCreateValidFrom sets the bi-temporal valid_from instant (RFC3339 UTC) for
// the new record. It is delivered inside the metadata JSON; the REST create
// route reads valid_from from metadata only.
func WithCreateValidFrom(validFrom string) CreateOption {
	return func(cfg *createConfig) {
		cfg.validFrom = validFrom
	}
}

// WithCreateValidUntil sets the bi-temporal valid_until instant (RFC3339 UTC)
// after which the new record stops being true. Like valid_from it is delivered
// inside the metadata JSON — service.createRecord reads BOTH keys out of
// metadata when the dedicated input fields are empty
// (server/service/record_create.go:329-344), and the REST create route never
// fills those dedicated fields.
//
// Junior Tip [why this arrived late, 2026-09-14]: TypeScript CreateOptions and
// Python CreateRequest both accepted validUntil from the start; Go's
// createConfig carried only validFrom. So the same three-language call wrote a
// record with an open-ended validity in Go and a bounded one everywhere else —
// a divergence invisible at the call site, because the Go caller simply had no
// option to pass and no error to read.
func WithCreateValidUntil(validUntil string) CreateOption {
	return func(cfg *createConfig) {
		cfg.validUntil = validUntil
	}
}
