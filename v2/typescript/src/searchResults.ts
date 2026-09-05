/**
 * AnhurDB TypeScript SDK — what a hybrid search ANSWERS.
 *
 * The hit, its graph expansion, its per-leg debug signals, the ADR-0012
 * retrieval diagnostics, the ADR-0031 per-leg score distributions, and the
 * object `searchWithRetrieval` hands back. Every interface here mirrors the
 * server's wire JSON verbatim — snake_case included — so a field that appears
 * in `AnhurDB/server/model` can be added here without a translation table.
 *
 * The other half of the contract, what a caller ASKS, is `searchTypes.ts`.
 * They are split because they change for different reasons: a request knob is
 * a decision the caller makes, a response field is a measurement the server
 * reports, and the two files together were 397 lines — past the house cut.
 *
 * @module
 */

import type { MemoryRecord } from "./types.js";

/**
 * A single search hit returned by every search method
 * (`search`/`searchSession`/`searchByType`/`recall`).
 *
 * the server emits —
 * `{ "record": {<full Record>}, "similarity": 0.63 }`. We nest the complete
 * {@link MemoryRecord} verbatim and carry the score in a SIBLING `similarity`
 * field (NOT inside the record). The previous flat shape
 * (`{id,type,summary,score,...}`) DROPPED every other record field
 * (uuid/weight/related_ids/main_ids/status/valid_from/...) — silent data loss.
 * All three SDKs must match: Python is `SearchResult(record=Record,
 * similarity=float)` (the reference), Go is `SearchResult{Record, Similarity}`.
 * NOTE the score key is `similarity`, NOT `score`.
 */
export interface SearchResult {
  /** The full memory record, verbatim (no fields dropped). */
  record: MemoryRecord;
  /** Similarity score (0-1), a sibling of `record` — not inside it. */
  similarity: number;
  /**
   * The plane that produced this hit (`sessions` | `tenant_shared` |
   * `client_shared`). Present on every hit — always sent by the server,
   * `omitempty` only trims it when the resolver could not label a leg.
   */
  provenance?: string;
  /**
   * Echoes the REQUEST scope (may read `shared_all` while {@link provenance}
   * names the specific leg that actually produced this hit).
   */
  scope?: string;
  /**
   * Per-hit ablation debug (fts/semantic/simhash ranks, RRF score, raw
   * cosine). Server only populates this when the request carries
   * `debug_signals=true` — a knob this SDK does not yet expose via
   * {@link SearchOptions} (query-string only today, `?debug_signals=true`).
   * Passing `signals` through here fixes the pre-existing bug where the SDK
   * silently dropped it even on requests that DID ask for it via raw query
   * string; it does not by itself make `search()` request debug signals.
   */
  signals?: SearchHitSignals;
  /**
   * Bounded summary of this hit's directly connected graph nodes (depth 1),
   * populated ONLY when the request set {@link SearchOptions.expandRelated}
   * to `true` (ADR-0021). Absent/undefined is not an error: it means either
   * `expand_related` was false/unset, the caller's SDK/server predates
   * ADR-0021, or the budget-bounded walk found nothing in scope for this hit
   * (an empty `[]` vs. `undefined` both read as "nothing to show"). Field
   * name kept snake_case (`related_nodes`, matching `provenance`/`scope`
   * above already being plain server keys and {@link SearchHitSignals}'s own
   * `fts_rank`-style fields) — this interface mirrors the server's wire JSON
   * verbatim, the same convention {@link MemoryRecord} uses for
   * `related_ids`/`main_ids`.
   */
  related_nodes?: RelatedNode[];
}

/**
 * A bounded, admission-filtered summary of one node connected to a search
 * hit's `related_ids`/`main_ids` graph edges (ADR-0021, `expand_related`).
 *
 * Deliberately excludes `content` and every internal `Record` field — this
 * is a SUMMARY projection, not a full {@link MemoryRecord}; attaching N full
 * records per hit would multiply the response payload. Mirrors
 * `model.RelatedNode` in `AnhurDB/server/model/record.go` and
 * AnhurCore's `RelatedNode` proto message field-for-field.
 */
export interface RelatedNode {
  id: number;
  type: string;
  summary: string;
  weight: number;
}

/**
 * Per-hit ablation debug attached to a {@link SearchResult} (ADR-0012).
 * Mirrors `model.SearchHitSignals` in `AnhurDB/server/model/record.go`
 * exactly — server field names, snake_case, all `omitempty`.
 */
export interface SearchHitSignals {
  fts_rank?: number;
  semantic_rank?: number;
  simhash_rank?: number;
  simhash_hamming?: number;
  rrf_score?: number;
  semantic_cosine?: number;
  // ── The seven added by ADR-0031 Stage 2 (proto fields 7-13) ──
  /** Rank of this hit inside the HNSW (dense ANN) leg's own candidate list. */
  hnsw_rank?: number;
  /** Rank inside the BSQ (binary-quantised) leg's candidate list. */
  bsq_rank?: number;
  /** Rank inside the Parquet/cold-tier leg's candidate list. */
  parquet_rank?: number;
  /**
   * Rank inside the FTS5 leg. Distinct from {@link fts_rank}, which is the
   * fused lexical rank the RRF stage consumed — keep both, they answer
   * different questions ("where did FTS5 put it" vs "where did fusion put
   * it").
   */
  fts5_rank?: number;
  /** Rank inside the A-star graph-walk leg. */
  astar_rank?: number;
  /** Rank inside the entity-Jaccard leg. */
  entity_jaccard_rank?: number;
  /**
   * Sum of the weights of the legs that were actually ACTIVE for this hit —
   * the denominator behind its fused score. A hit reached by one leg out of
   * four does not deserve the same confidence as one reached by all four,
   * and this is the number that says which happened.
   */
  active_leg_weight_sum?: number;
}

/**
 * Pre-fusion score distribution of ONE retrieval leg (ADR-0031), returned
 * only when the request set {@link SearchOptions.debugSignals}.
 *
 * Mirrors `model.LegScoreSummary` in
 * `AnhurDB/server/model/search_leg_scores.go` field-for-field.
 *
 * Junior Tip [why this is NOT inside {@link RetrievalMeta}]: on the wire the
 * server puts `leg_scores` at the TOP LEVEL of the search response, as a
 * SIBLING of `retrieval` — see `handler/bundle.go:attachLegScores`, whose own
 * Junior Tip explains the choice (RetrievalMeta is mirrored field-for-field
 * in the proto and in all three SDKs; this block is research instrumentation
 * and was deliberately kept out of that contract). Declaring it inside
 * `RetrievalMeta` here would produce a field that is `undefined` forever —
 * a type that lies. It is surfaced as {@link SearchWithRetrievalResult.legScores}.
 */
export interface LegScoreSummary {
  /** Leg name, e.g. `fts5`, `hnsw`, `bsq`, `simhash`, `astar`. */
  leg: string;
  /** Size of the leg's COMPLETE candidate list, not of the kept top-K. */
  candidates: number;
  /** The largest scores, descending. Absent when `candidates === 0`. */
  top_scores?: number[];
  /** Mean over ALL candidates. */
  mean: number;
  /** Population standard deviation (divisor n) over ALL candidates. */
  stddev: number;
}

/**
 * Retrieval diagnostics for one search response (ADR-0012): which arms ran,
 * whether semantic degraded, and the RESOLVED weights actually used (after
 * any per-request {@link SearchOptions.astarWeight} /
 * {@link SearchOptions.entityJaccardWeight} override was applied). Mirrors
 * `model.RetrievalMeta` in `AnhurDB/server/model/record.go` field-for-field.
 *
 * Junior Tip [why this is not on `search()`'s return type]: `search()` keeps
 * returning `SearchResult[]` for backward compatibility — existing callers
 * destructure/iterate the array directly and would break if it became
 * `{results, retrieval}`. Use {@link Memory.searchWithRetrieval} instead when
 * this diagnostic block is needed.
 */
export interface RetrievalMeta {
  /**
   * The mode the server ACTUALLY ran, already normalised
   * (`fast`|`balanced`|`semantic`). Typed `string`, not
   * {@link SearchMode}, on purpose: this is a value the server chose, and a
   * future mode must not crash a TypeScript build here.
   *
   * Junior Tip [this field is the version detector]: a server at ADR-0031
   * Stage 2 or newer ALWAYS fills it, because `normalizeSearchMode` always
   * resolves. So "I asked for `semantic` and this came back `balanced`
   * (or the whole `retrieval` block is missing)" is the honest proof that
   * the server ignored {@link SearchOptions.mode}. Never trust the REQUEST
   * to tell you what ran.
   */
  mode: string;
  signals_used: string[];
  semantic_attempted: boolean;
  semantic_used: boolean;
  degraded: boolean;
  reason?: string;
  elapsed_ms: number;
  content_simhash_enabled: boolean;
  content_simhash_weight: number;
  astar_enabled: boolean;
  astar_weight: number;
  entity_jaccard_enabled: boolean;
  entity_jaccard_weight: number;
}

/**
 * What {@link SearchOptions}-driven search returns when the caller wants the
 * diagnostics as well as the hits.
 *
 * `retrieval` and `legScores` are BOTH optional and their absence is not an
 * error: an AnhurDB older than ADR-0012 sends no `retrieval` block, and
 * `leg_scores` only exists when the request set
 * {@link SearchOptions.debugSignals} against a server new enough to honour it.
 */
export interface SearchWithRetrievalResult {
  /** Same shape and order as the array `search()` returns. */
  results: SearchResult[];
  /** ADR-0012 diagnostics. `undefined` = the server omitted the block. */
  retrieval?: RetrievalMeta;
  /**
   * ADR-0031 pre-fusion per-leg score distributions, read from the response's
   * TOP-LEVEL `leg_scores` key (see {@link LegScoreSummary} for why it does
   * not live inside `retrieval`). `undefined` unless
   * {@link SearchOptions.debugSignals} was set AND the server supports it.
   */
  legScores?: LegScoreSummary[];
}
