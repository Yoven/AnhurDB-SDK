/**
 * AnhurDB TypeScript SDK — what `GET /api/v1/search/smart` ANSWERS.
 *
 * Its own file, not part of `searchResults.ts`, because it is a DIFFERENT
 * contract: hybrid search answers `{record, similarity}` (a nested full record
 * scored by cosine), smart search answers a FLAT DuckDB/Parquet projection
 * scored by lexical `relevance`. The two look alike and are not interchangeable
 * — keeping them in separate files is what stops the next reader from assuming
 * a shared shape and reading `undefined`.
 */

/**
 * One row of the `GET /api/v1/search/smart` envelope.
 *
 * Mirrors `duckdb.SearchResult` in
 * `AnhurDB/server/duckdb/engine_lifecycle.go` plus the three fields
 * `handler.SmartSearchResponseRow` embeds on the shared planes — server field
 * names, snake_case, verbatim.
 *
 * Junior Tip [why this is a FLAT row and not a {@link SearchResult}]: smart
 * search answers from the DuckDB/Parquet lexical leg, whose row is a projection
 * (id/summary/metadata/relevance), NOT a nested full record. Declaring it with
 * `record`/`similarity` would describe a shape the server never sends, and the
 * caller would read `undefined` forever. Note the score key is `relevance`
 * (BM25 × cognitive decay), not `similarity`: it is a lexical score and is NOT
 * comparable with the cosine `similarity` that {@link SearchResult} carries.
 */
export interface SmartSearchHit {
  id: number;
  uuid: string;
  type: string;
  summary: string;
  /** Raw metadata JSON string, exactly as stored. */
  metadata: string;
  score: number;
  weight: number;
  status: string;
  /** Lexical relevance after decay. Not a cosine — see the Junior Tip above. */
  relevance: number;
  /** UNDECAYED BM25. Server sends it `omitempty`, and only the FTS leg fills it. */
  bm25?: number;
  created_at: string;
  updated_at: string;
  /** Which plane produced the hit; shared planes only (`omitempty`). */
  provenance?: string;
  /** Echoes the plane label on shared-plane rows (`omitempty`). */
  scope?: string;
  /** The row's score inside its OWN leg, before cross-leg RRF overwrote it. */
  leg_relevance?: number;
}

/**
 * The full envelope of `GET /api/v1/search/smart`.
 *
 * Junior Tip [why `results` may be `null` and that is not a bug]: the handler
 * marshals a Go slice, and a nil slice serialises as JSON `null`, not `[]`. A
 * type that promised `SmartSearchHit[]` would make `response.results.length`
 * compile and then throw at runtime on the perfectly ordinary "no matches"
 * answer. Declaring the null is the honest contract; callers use
 * `response.results ?? []`.
 */
export interface SmartSearchResponse {
  results: SmartSearchHit[] | null;
  count: number;
  /** The plane the server RESOLVED (may differ from the requested label). */
  scope: string;
  /** Order-preserving, tenant-local hash of the returned id sequence. */
  bundle_hash: string;
  /** Always `"smart_relevance"` for this endpoint. */
  bundle_ordering: string;
}
