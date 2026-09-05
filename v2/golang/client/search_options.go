package client

// search_options.go — the request side of a read: ReadOption/SearchOption, the
// searchConfig they fold into, and every With* constructor.
//
// Domain: "what the caller can ask for", separate from search_types.go's "what
// the server sends back". Split out of types.go on 2026-09-05 for the ADR-0031
// knobs — types.go was already 1042 lines, and house law forbids growing a file
// that is already past the ~300-line cut, so the touched domain moved out first.

// ReadOption configures a single read call. It is the idiomatic Go surface
// for search shaping and bi-temporal filters, and is the SAME type used by
// Search (so WithLimit / WithTypeFilter all compose on a read).
type ReadOption func(*searchConfig)

// SearchOption is retained as an exported alias of ReadOption for backward
// compatibility — existing code using client.SearchOption / WithLimit /
// WithTypeFilter is unaffected.
type SearchOption = ReadOption

// searchConfig holds parameters for a read request: search shaping (limit,
// typeFilter), the optional bi-temporal window (asOf/since/until), and
// goal-directed semantic-walk parameters.
type searchConfig struct {
	limit      int
	typeFilter string
	// scope is the search plane for POST /api/v1/search (sessions,
	// tenant_shared, client_shared, shared_all). Empty means the Search
	// method applies its own default ("sessions").
	scope string
	// keyword is an optional free-text filter (query param "q") honoured by
	// SearchByType. Empty means omit.
	keyword string
	// asOf / since / until are optional RFC3339 UTC bi-temporal filters honoured
	// by the manifest reads. asOf is mutually exclusive with since/until — the
	// server returns HTTP 400 on a violation, which the SDK surfaces verbatim.
	asOf  string
	since string
	until string
	// Goal-directed semantic-walk parameters (2026-07-03). These are honoured
	// ONLY by WalkSemantic; every other read method ignores them — the exact
	// same shared-ReadOption pattern already used by asOf/since/until (fields
	// meaningful only to the manifest reads). The zero value of each field means
	// "caller did not set it": walkTarget "" and walkMaxCost 0 both omit their
	// JSON key so the server falls through to its plain-Dijkstra default,
	// preserving the historical behaviour of an option-free WalkSemantic call.
	//
	walkTarget     string
	walkGoalVector []byte
	walkTargetTag  string
	walkMaxCost    float64
	// skipQueryEmbed / skipCognitiveRerank são os knobs de ablação do
	// /api/v1/search (paridade com REST e MCP, 2026-08-07 — antes o SDK só
	// sabia rodar o híbrido default e um agente não conseguia medir as pernas).
	// Zero value = omitir a chave = comportamento default do servidor.
	skipQueryEmbed      bool
	skipCognitiveRerank bool
	// expandRelated / astarWeight / entityJaccardWeight são os 3 campos novos
	// do ADR-0021 (2026-08-10, paridade REST↔SDK-Go pro precedente skip_query_embed,
	// commit ff7f803). expandRelated segue o MESMO padrão bool-omitido-quando-false
	// de skipQueryEmbed/skipCognitiveRerank acima.
	//
	// astarWeight / entityJaccardWeight são *float64, NÃO float64, pelo mesmo
	// motivo documentado em server/model.SearchRequest.AstarWeight: o servidor
	// distingue "não mandou" (usa o default do env) de "mandou 0" (zera a perna
	// só nesta query, sem tocar o default do env) — um float64 puro não
	// consegue expressar essa diferença porque o zero value dele já é 0.
	// nil = omitir a chave do payload = deixa o servidor decidir.
	expandRelated       bool
	astarWeight         *float64
	entityJaccardWeight *float64
	// searchMode / semanticTimeoutMs / debugSignals are the three ADR-0031
	// Stage 2 controls (2026-09-05). They follow the same omit-unless-set
	// discipline as everything above: the empty string / 0 / false all mean
	// "did not ask", the key is left out of the payload, and the server applies
	// its own default (balanced, 700ms, off). searchMode is validated at call
	// time by validateSearchMode — see search_mode.go for why the SDK is
	// stricter here than the server is.
	searchMode        string
	semanticTimeoutMs int
	debugSignals      bool
}

// applyReadOptions folds a variadic ReadOption slice into a searchConfig.
// Centralised so every read method resolves options identically. The default
// limit is 0 here (read methods that need a different default set it before
// applying options — e.g. Search seeds limit=10).
func applyReadOptions(opts []ReadOption) searchConfig {
	cfg := searchConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// WithLimit sets the maximum number of search results.
func WithLimit(n int) SearchOption {
	return func(cfg *searchConfig) {
		cfg.limit = n
	}
}

// WithTypeFilter restricts search results to a specific memory type.
func WithTypeFilter(t string) SearchOption {
	return func(cfg *searchConfig) {
		cfg.typeFilter = t
	}
}

// WithSkipQueryEmbed desliga a perna vetorial da consulta: a busca roda só
// léxico (FTS5) + Content SimHash. É a perna "keyword" das ablação/medições —
// mesmo knob do REST (skip_query_embed) e do MCP (lexical_only).
func WithSkipQueryEmbed() SearchOption {
	return func(cfg *searchConfig) {
		cfg.skipQueryEmbed = true
	}
}

// WithSkipCognitiveRerank desliga o rerank cognitivo (recência/tipo/peso/
// valid_until — ADR-0011 A3), mantendo o score RRF puro. Serve para medir o
// efeito do rerank e para agentes que querem ranking neutro.
func WithSkipCognitiveRerank() SearchOption {
	return func(cfg *searchConfig) {
		cfg.skipCognitiveRerank = true
	}
}

// WithExpandRelated asks the server to attach a bounded summary of the
// graph-connected nodes (depth 1, budget-bounded) to each surviving top-K hit
// (ADR-0021, 2026-08-10). The expansion reuses the same A*/WalkAdmission the
// ranking stage already built — it never widens the rerank pool. Omitted
// (default false) means the wire payload does not carry "expand_related" at
// all, matching the WithSkipQueryEmbed/WithSkipCognitiveRerank precedent
// above. Populates SearchResult.RelatedNodes on the response.
func WithExpandRelated() SearchOption {
	return func(cfg *searchConfig) {
		cfg.expandRelated = true
	}
}

// WithAstarWeight overrides ANHUR_SEARCH_ASTAR_WEIGHT for a single query
// (ablation / eval-harness A/B — ADR-0021). A weight of 0 is a legal,
// meaningful value (disable the A* leg for THIS query only, without touching
// the server-wide env default) and is distinct from never calling this
// option at all, which omits "astar_weight" from the payload and leaves the
// server's configured default untouched. Whether the arm is enabled at all
// still comes from the server-wide ANHUR_SEARCH_ASTAR flag — this only
// rescales its contribution.
func WithAstarWeight(weight float64) SearchOption {
	return func(cfg *searchConfig) {
		cfg.astarWeight = &weight
	}
}

// WithEntityJaccardWeight overrides ANHUR_ENTITY_JACCARD_WEIGHT for a single
// query (ADR-0021). Same nil-vs-zero contract as WithAstarWeight: calling
// this with 0 explicitly zeroes the leg for this query; never calling it
// omits "entity_jaccard_weight" and leaves the server default in place. Only
// takes effect when ANHUR_ENTITY_JACCARD_ENABLED is already true
// server-side — this cannot turn the arm on for a tenant where it is off.
func WithEntityJaccardWeight(weight float64) SearchOption {
	return func(cfg *searchConfig) {
		cfg.entityJaccardWeight = &weight
	}
}

// WithScope selects the search plane for POST /api/v1/search and
// GET /api/v1/search/smart (sessions | tenant_shared | client_shared | shared_all).
func WithScope(scope string) SearchOption {
	return func(cfg *searchConfig) {
		cfg.scope = scope
	}
}

// WithKeyword sets an optional free-text filter (query param "q"), honoured by
// SearchByType. Empty string is a no-op.
func WithKeyword(keyword string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.keyword = keyword
	}
}

// WithAsOf scopes a temporal-aware read (the manifests) to a bi-temporal
// snapshot instant — an RFC3339 UTC timestamp, e.g. "2026-03-15T12:00:00Z".
// Mutually exclusive with WithSince/WithUntil; the server rejects the
// combination with HTTP 400, which the read method surfaces.
func WithAsOf(asOf string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.asOf = asOf
	}
}

// WithSince scopes a temporal-aware read to records whose created_at is >= the
// supplied RFC3339 UTC lower bound. Combine with WithUntil for a window.
func WithSince(since string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.since = since
	}
}

// WithUntil scopes a temporal-aware read to records whose created_at is <= the
// supplied RFC3339 UTC upper bound. Combine with WithSince for a window.
func WithUntil(until string) ReadOption {
	return func(cfg *searchConfig) {
		cfg.until = until
	}
}

// WithSearchMode selects the retrieval budget for one search (ADR-0012 modes,
// put on the wire by ADR-0031 Stage 2): SearchModeFast, SearchModeBalanced or
// SearchModeSemantic. An empty mode is omitted and the server uses balanced.
//
// Junior Tip [why this is NOT called WithMode]: WithMode already exists and
// means something else entirely — it picks the WRITE path for Memory.Add
// ("ingest" vs "regular"). Reusing the name would compile (both are just
// functions) and would silently let a caller pass a write mode to a search or
// vice versa, because Go would happily accept whichever option type matched.
// Two different concepts, two different names.
//
// mode=SearchModeSemantic is a PROMISE, not a preference: the server answers
// 503/504 rather than quietly returning lexical results when the embedding
// cannot be resolved. Because an older server ignores the field entirely, the
// SDK verifies the promise against the response and fails loud when it was not
// kept — see verifySearchKnobsHonoured.
func WithSearchMode(searchMode string) SearchOption {
	return func(cfg *searchConfig) {
		cfg.searchMode = searchMode
	}
}

// WithSemanticTimeoutMs caps how long the server waits on Embed+HNSW for this
// one query, in milliseconds. 0 (the default) omits the key and the server
// applies its own budget of 700ms.
//
// Junior Tip [this is a budget, not a deadline]: exceeding it does not fail the
// query in balanced mode — it DEGRADES it. The NOW legs (FTS5 + SimHash) still
// answer, and retrieval.degraded/reason report "embedding_timeout". Read them
// before concluding a low-recall answer means the corpus is thin.
func WithSemanticTimeoutMs(semanticTimeoutMs int) SearchOption {
	return func(cfg *searchConfig) {
		cfg.semanticTimeoutMs = semanticTimeoutMs
	}
}

// WithDebugSignals asks the server to attach per-hit SearchHitSignals and the
// per-leg LegScoreSummary distributions to the response.
//
// Junior Tip [why it is opt-in]: the signals block multiplies the response size
// and exists for ablation measurement, not for production ranking. Nothing in
// it is a calibrated similarity, and none of it is comparable across queries.
// Read SearchOutcome.LegScores and SearchResult.Signals only to answer "why did
// this record win?", never to build a score of your own.
func WithDebugSignals() SearchOption {
	return func(cfg *searchConfig) {
		cfg.debugSignals = true
	}
}
