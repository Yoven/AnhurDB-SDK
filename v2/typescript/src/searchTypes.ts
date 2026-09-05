/**
 * AnhurDB TypeScript SDK — what a caller ASKS of the hybrid search.
 *
 * The scope planes, the ranking modes, the options bag, and the JSON body
 * those options become. What the server ANSWERS is `searchResults.ts`;
 * behaviour (payload assembly, validation, the cross-version guard) is
 * `searchRequest.ts`; the public methods are `search.ts`.
 *
 * Junior Tip [why these types left `types.ts`]: `types.ts` was already at 981
 * lines — far past the house ~300-line cut — and ADR-0031 needed to ADD three
 * request knobs, seven per-hit signals and a new response block. House law
 * forbids growing a file that is already over the cut: split the domain you
 * are about to touch FIRST, then change it. `types.ts` re-exports every name
 * below, so no import in user code or in this SDK had to move.
 *
 * @module
 */

import type { MemoryType } from "./types.js";

// ── search() ─────────────────────────────────────────────────

/**
 * Ranking effort for one search request (ADR-0012 / ADR-0031).
 *
 * - `fast` — lexical legs only; the query is never embedded.
 * - `balanced` — the default: try the semantic leg, DEGRADE to the lexical
 *   legs when the embedder is slow or down, and say so in
 *   {@link RetrievalMeta.degraded} / {@link RetrievalMeta.reason}.
 * - `semantic` — semantics or nothing: a server that cannot embed answers
 *   503/504 instead of quietly returning lexical hits.
 *
 * Mirrors `handler.SearchMode{Fast,Balanced,Semantic}` in
 * `AnhurDB/server/handler/search_mode.go`; Go `WithMode()` / Python
 * `mode=` are the same knob in the other two SDKs.
 */
export type SearchMode = "fast" | "balanced" | "semantic";

/** Search scope planes for POST /api/v1/search. */
export type SearchScope =
  | "sessions"
  | "tenant_shared"
  | "client_shared"
  | "shared_all";

/** Options for `Memory.search()`. */
export interface SearchOptions {
  /** Maximum results to return (default 10). */
  limit?: number;
  /** Filter by memory type. */
  typeFilter?: MemoryType;
  /** Search plane (default `sessions`). */
  scope?: SearchScope;
  /**
   * Desliga a perna vetorial da consulta (busca só léxico+SimHash) — a perna
   * "keyword" das medições. Paridade REST/MCP/Go/Py (2026-08-07).
   */
  skipQueryEmbed?: boolean;
  /**
   * Desliga o rerank cognitivo (recência/tipo/peso — ADR-0011 A3), mantendo o
   * score RRF puro.
   */
  skipCognitiveRerank?: boolean;
  /**
   * Overrides `ANHUR_SEARCH_ASTAR_WEIGHT` for THIS query only (ablation / A-B
   * sweep, no server restart needed). `undefined` (the default — field not
   * sent) keeps the server's configured weight; an explicit `0` rescales the
   * A* leg to zero contribution for this query only, WITHOUT touching the
   * server-wide default. Whether the A* arm runs AT ALL still comes from the
   * server-side `ANHUR_SEARCH_ASTAR` toggle — this can only rescale an
   * already-enabled leg, never turn it on for a tenant where it is off.
   * Mirrors `model.SearchRequest.AstarWeight` (`*float64`, same nil-vs-zero
   * contract) in `AnhurDB/server/model/record.go`.
   */
  astarWeight?: number;
  /**
   * Overrides `ANHUR_ENTITY_JACCARD_WEIGHT` for THIS query only. Same
   * nil-vs-zero contract as {@link astarWeight}: `undefined` preserves the
   * server default, `0` explicitly zeroes the leg for this query. Only takes
   * effect when `ANHUR_ENTITY_JACCARD_ENABLED` is already true server-side.
   */
  entityJaccardWeight?: number;
  /**
   * Opt-in, budget-bounded expansion (ADR-0021): when `true`, each top-K hit
   * that survives final ranking gets a small summary of its DIRECTLY
   * connected graph nodes (depth 1, `related_ids`/`main_ids`) attached as
   * {@link SearchResult.related_nodes}. Reuses the same A-star / WalkAdmission
   * session/plane boundary the A* RRF leg already enforces — it can never
   * surface a record outside the caller's session scope. Omitted from the
   * wire payload when `false`/unset (default false = zero extra cost, byte
   * -identical response to today). Mirrors `model.SearchRequest.ExpandRelated`
   * in `AnhurDB/server/model/record.go`; Go `WithExpandRelated()` / Python
   * `expand_related=True` are the equivalent knobs in the other two SDKs.
   */
  expandRelated?: boolean;
  /**
   * Ranking effort for THIS query (ADR-0031). Unset/empty = the server's
   * `balanced` default; the SDK never sends the field when it is unset, so
   * the wire body stays byte-identical for callers who do not opt in.
   *
   * An unknown value is rejected CLIENT-SIDE (`AnhurError`), not forwarded:
   * `handler.normalizeSearchMode` folds anything it does not recognise into
   * `balanced`, so a typo like `"semanitc"` would otherwise buy silent
   * lexical results under a semantic-looking request.
   *
   * Junior Tip [`semantic` is a promise the SERVER has to keep]: asking for
   * `semantic` against an AnhurDB older than ADR-0031 lands the field in
   * protobuf/JSON limbo and is IGNORED — that server runs balanced and
   * answers 200 with lexical hits. This SDK detects that from the RESPONSE
   * ({@link RetrievalMeta.mode}) and throws rather than let a caller believe
   * it got strict semantics. See `searchRequest.ts`.
   */
  mode?: SearchMode;
  /**
   * Per-query budget, in milliseconds, for the semantic leg (embed + HNSW).
   * `0`/unset = the server's default (`handler.defaultSemanticTimeoutMs`,
   * 700 ms today). Blowing the budget degrades in `balanced`
   * (`reason="embedding_timeout"`) and fails in `semantic`. Mirrors
   * `model.SearchRequest.SemanticTimeoutMs`.
   */
  semanticTimeoutMs?: number;
  /**
   * Ask the server for research instrumentation: {@link SearchResult.signals}
   * on every hit and the pre-fusion {@link LegScoreSummary} array. Off by
   * default because it costs the server extra bookkeeping per leg.
   */
  debugSignals?: boolean;
}

/**
 * Payload sent to POST /api/v1/search (scope boundary + session subset).
 *
 * `sessions` is required on the wire (ADR-0014) and is ALWAYS an array, even
 * for the wildcard: one type, one code path, no `oneOf` in the schema — an
 * agent generating JSON from the schema errs less when the type is fixed. The
 * retired singular `uuid` is deliberately absent: it could only express "one
 * session or none" and two of the four search paths dropped it in silence.
 */
export interface SearchPayload {
  text: string;
  limit: number;
  scope: SearchScope;
  /** `["*"]` for every session in the scope, or up to 1000 explicit uuids. */
  sessions: string[];
  type_filter?: string;
  /** Omitido quando false — preserva o default do servidor. */
  skip_query_embed?: boolean;
  skip_cognitive_rerank?: boolean;
  /**
   * Omitido quando `undefined` — mesma disciplina nil-vs-zero de
   * `model.SearchRequest.AstarWeight` no servidor (Go `*float64`): um `0`
   * explícito precisa chegar como `0` no JSON, nunca ser confundido com
   * "campo não enviado".
   */
  astar_weight?: number;
  /** Omitido quando `undefined`. Mesmo contrato nil-vs-zero acima. */
  entity_jaccard_weight?: number;
  /** Omitido quando `false`/`undefined` — default false = zero custo extra. */
  expand_related?: boolean;
  /**
   * ADR-0031. Omitido quando `undefined` — o servidor resolve `""` para
   * `balanced`, então mandar o campo vazio não teria significado próprio.
   */
  mode?: SearchMode;
  /** ADR-0031. Omitido quando `undefined`; `0` já significa "default do servidor". */
  semantic_timeout_ms?: number;
  /** ADR-0031. Omitido quando `false`/`undefined` — mesma disciplina falsy-omit. */
  debug_signals?: boolean;
}

/**
 * Payload sent to POST /api/v1/search for a single-session query.
 *
 * Same wire shape as {@link SearchPayload} with `scope` pinned to `sessions`;
 * the one-chat case is just `sessions: [uuid]`.
 *
 * @deprecated Since 2.1.0 `searchSession()` assembles its body through the
 * SAME {@link SearchPayload} builder as `search()`. Two assembly sites for one
 * endpoint is exactly how `searchSession` came to silently drop every knob
 * except `limit`/`type_filter` — including, before this was unified, the
 * ADR-0031 `mode`. The interface is kept exported so no caller's import
 * breaks; nothing in the SDK constructs it any more.
 */
export interface SearchSessionPayload {
  sessions: string[];
  text?: string;
  type_filter?: string;
  limit?: number;
  scope: "sessions";
}
