package client

// read_option_support.go — one domain: deciding, at call time, whether the
// ReadOptions a caller passed are ones the target endpoint can actually honour.
//
// Created 2026-09-14 for the parity pass that removed the fourteen `_ = opts`
// sites. The three methods that KEEP `opts ...ReadOption` (Walk, SearchByType,
// SmartSearch) each honour a small named subset; everything else must be
// refused here rather than serialised into a request the handler ignores.
//
// Junior Tip [why a field sweep instead of tagging the option funcs]: a
// ReadOption is an opaque `func(*searchConfig)` — there is nothing to inspect
// on the value itself. The only honest way to learn what a caller asked for is
// to APPLY the options and then read which fields of the resolved searchConfig
// moved off their zero value. That is also why every option in this SDK keeps
// the omit-unless-set discipline: the zero value has to mean "did not ask", or
// this sweep (and the payload builders) would both lie.

// readOptionUse pairs the caller-visible constructor name with whether the
// resolved config shows that the caller set it.
type readOptionUse struct {
	optionName string
	isSet      bool
}

// readOptionUses enumerates EVERY ReadOption this SDK exposes against a
// resolved config.
//
// Junior Tip [this list must stay exhaustive]: a new With* constructor whose
// field is missing here becomes silently forwardable again — it would pass the
// guard on an endpoint that cannot honour it, which is precisely the bug the
// guard exists to end. When you add an option, add its row here in the same
// commit; TestReadOptionUsesCoversEverySearchConfigField fails otherwise.
func readOptionUses(cfg searchConfig) []readOptionUse {
	return []readOptionUse{
		{"WithLimit", cfg.limit != 0},
		{"WithTypeFilter", cfg.typeFilter != ""},
		{"WithScope", cfg.scope != ""},
		{"WithKeyword", cfg.keyword != ""},
		{"WithAsOf", cfg.asOf != ""},
		{"WithSince", cfg.since != ""},
		{"WithUntil", cfg.until != ""},
		{"WithTarget", cfg.walkTarget != ""},
		{"WithGoalVector", len(cfg.walkGoalVector) > 0},
		{"WithTargetTag", cfg.walkTargetTag != ""},
		{"WithMaxCost", cfg.walkMaxCost != 0},
		{"WithSkipQueryEmbed", cfg.skipQueryEmbed},
		{"WithSkipCognitiveRerank", cfg.skipCognitiveRerank},
		{"WithExpandRelated", cfg.expandRelated},
		{"WithAstarWeight", cfg.astarWeight != nil},
		{"WithEntityJaccardWeight", cfg.entityJaccardWeight != nil},
		{"WithSearchMode", cfg.searchMode != ""},
		{"WithSemanticTimeoutMs", cfg.semanticTimeoutMs != 0},
		{"WithDebugSignals", cfg.debugSignals},
	}
}

// rejectUnsupportedReadOptions returns an *UnsupportedOptionError for the first
// option the caller set that is not in honouredOptions.
//
// method is the SDK method name ("Walk"); honouredNote is the one-line truth
// about the endpoint ("POST /api/v1/walk honours as_of only") that the error
// message carries to the caller. honouredOptions lists the constructor names
// this method wires onto the wire.
//
// Junior Tip [first offender, not all of them]: the caller has to fix every bad
// option anyway, and naming one keeps the message the short, quotable sentence
// the other two SDKs also emit. Reporting a list would make the string harder
// to compare across arms for no diagnostic gain.
func rejectUnsupportedReadOptions(cfg searchConfig, method, honouredNote string, honouredOptions ...string) error {
	honoured := make(map[string]bool, len(honouredOptions))
	for _, optionName := range honouredOptions {
		honoured[optionName] = true
	}
	for _, use := range readOptionUses(cfg) {
		if use.isSet && !honoured[use.optionName] {
			return &UnsupportedOptionError{
				Option:   use.optionName,
				Method:   method,
				Honoured: honouredNote,
			}
		}
	}
	return nil
}
