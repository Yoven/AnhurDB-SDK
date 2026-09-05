/**
 * AnhurDB TypeScript SDK — the MECHANISM of `POST /api/v1/search`.
 *
 * Domain, in one sentence: assemble the request body, and nest the response
 * back into the shape callers see.
 *
 * One place assembles the request body, one place reads the response, and one
 * place decides whether the server actually honoured what was asked. The
 * public methods in `search.ts` do nothing but call these; the wire types they
 * speak live in `searchTypes.ts`.
 *
 * Junior Tip [why the assembly lives alone, away from the methods]: this SDK
 * has already shipped the same bug twice — `searchSession` and `searchByType`
 * each grew their OWN payload builder and each quietly dropped fields the
 * caller had set (the session filter in ADR-0014's case, every ablation knob
 * in `searchSession`'s). A knob that a method forgets to forward is
 * indistinguishable, from the caller's side, from a knob the server ignored.
 * One builder, called by every path, is the only structural cure.
 *
 * Junior Tip [what moved out on 2026-09-05]: this file was 348 lines against
 * the ~300-line house cut. `mode`, its validators and the cross-version
 * honesty check moved to `searchMode.ts` — the same filename the Go and Python
 * SDKs already use for that domain. Assembly and parsing stayed.
 *
 * @module
 */

import type { MemoryRecord } from "./types.js";
// The mode vocabulary and its two validators live in searchMode.ts, beside the
// response-side check that proves the server honoured them (2026-09-05 split).
import { validateSearchMode, validateSemanticTimeoutMs } from "./searchMode.js";
import type {
  SearchOptions,
  SearchPayload,
} from "./searchTypes.js";
import type {
  LegScoreSummary,
  RelatedNode,
  RetrievalMeta,
  SearchHitSignals,
  SearchResult,
} from "./searchResults.js";
/** One hit exactly as the server serialises it, before nesting. */
export interface RawSearchHit {
  record?: Record<string, unknown>;
  similarity?: number;
  provenance?: string;
  scope?: string;
  signals?: SearchHitSignals;
  related_nodes?: RelatedNode[];
}

/**
 * The full search response envelope.
 *
 * `leg_scores` sits at the TOP LEVEL, a sibling of `retrieval` — that is the
 * server's shape (`handler/bundle.go:attachLegScores`), not a convenience of
 * this SDK. Reading it from inside `retrieval` would return `undefined`
 * forever against a perfectly healthy server.
 */
export interface RawSearchResponse {
  results?: RawSearchHit[];
  retrieval?: RetrievalMeta;
  leg_scores?: LegScoreSummary[];
}

/**
 * Build the body of `POST /api/v1/search`.
 *
 * Every optional knob follows one of two disciplines, and which one is not a
 * style choice:
 *
 * - **falsy-omit** (`skip_query_embed`, `skip_cognitive_rerank`,
 *   `expand_related`, `debug_signals`): booleans whose server default is
 *   `false`. Omitting them keeps the wire body byte-identical for callers who
 *   never opt in.
 * - **nil-vs-zero** (`astar_weight`, `entity_jaccard_weight`): the server
 *   models these as `*float64`, so `0` is a REAL instruction ("switch this
 *   leg's contribution off for this query") and must survive as JSON `0`.
 *   Testing `!== undefined` rather than truthiness is what makes that work.
 *
 * `semantic_timeout_ms` is the nil-vs-zero shape with a twist: `0` already
 * MEANS "server default", so sending an explicit `0` says nothing the absent
 * field does not, and we omit it.
 *
 * @param query    - Non-empty query text; the caller has already checked it.
 * @param sessions - Already normalised session filter (ADR-0014).
 * @param options  - The caller's options bag, possibly undefined.
 */
export function buildSearchPayload(
  query: string,
  sessions: string[],
  options?: SearchOptions,
): SearchPayload {
  const payload: SearchPayload = {
    text: query,
    limit: options?.limit ?? 10,
    scope: options?.scope ?? "sessions",
    sessions,
  };
  if (options?.typeFilter) {
    payload.type_filter = options.typeFilter;
  }
  // Knobs de ablação (paridade REST/MCP/Go/Py, 2026-08-07): omitidos quando
  // falsos para preservar o wire default do servidor.
  if (options?.skipQueryEmbed) {
    payload.skip_query_embed = true;
  }
  if (options?.skipCognitiveRerank) {
    payload.skip_cognitive_rerank = true;
  }
  if (options?.astarWeight !== undefined) {
    payload.astar_weight = options.astarWeight;
  }
  if (options?.entityJaccardWeight !== undefined) {
    payload.entity_jaccard_weight = options.entityJaccardWeight;
  }
  if (options?.expandRelated) {
    payload.expand_related = true;
  }
  // ── ADR-0031 ──
  // `""` is "not set", not a mode — the Go SDK's validateSearchMode accepts it
  // for the same reason, and the server resolves an empty mode to balanced.
  // validateSearchMode returns the NORMALISED value (trimmed, lowercased), and
  // that is deliberately what goes on the wire: the response check compares
  // retrieval.mode against what was asked for, so both sides have to speak one
  // alphabet or a healthy server looks like an ancient one.
  if (options?.mode !== undefined) {
    const normalizedMode = validateSearchMode(options.mode);
    if (normalizedMode !== "") {
      payload.mode = normalizedMode;
    }
  }
  // Validate BEFORE the omit-unless-set gate: a negative budget would slip
  // through `> 0` and vanish silently. 0 stays the "use the server default"
  // sentinel and is still omitted. See validateSemanticTimeoutMs.
  if (options?.semanticTimeoutMs !== undefined) {
    const semanticTimeoutMs = validateSemanticTimeoutMs(options.semanticTimeoutMs);
    if (semanticTimeoutMs > 0) {
      payload.semantic_timeout_ms = semanticTimeoutMs;
    }
  }
  if (options?.debugSignals) {
    payload.debug_signals = true;
  }
  return payload;
}

/**
 * Nest each raw hit into the {@link SearchResult} shape, keeping every field
 * the server sent.
 *
 * Junior Tip [this function has caused two data-loss bugs, both the same
 * shape]: the first version copied the record's `id/type/summary/score` into
 * a FLAT object and dropped `uuid/weight/related_ids/main_ids/status/...`.
 * The second copied only `record`/`similarity` off the hit and dropped
 * `provenance`/`scope`/`signals`, so a `shared_all` caller could not tell
 * which plane produced a hit. The rule that came out of both: copy what the
 * server sends, do not curate it. `signals`, `related_nodes` and friends stay
 * `undefined` when absent — that is "the server did not send it", never "the
 * SDK threw it away".
 */
export function nestSearchResults(results?: RawSearchHit[]): SearchResult[] {
  return (results ?? []).map((rawHit) => ({
    record: (rawHit.record ?? {}) as unknown as MemoryRecord,
    similarity: rawHit.similarity ?? 0,
    ...(rawHit.provenance !== undefined ? { provenance: rawHit.provenance } : {}),
    ...(rawHit.scope !== undefined ? { scope: rawHit.scope } : {}),
    ...(rawHit.signals !== undefined ? { signals: rawHit.signals } : {}),
    ...(rawHit.related_nodes !== undefined ? { related_nodes: rawHit.related_nodes } : {}),
  }));
}
