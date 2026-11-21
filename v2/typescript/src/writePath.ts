/**
 * AnhurDB TypeScript SDK — the two doors a memory is written through, as
 * plain functions over the transport.
 *
 * One domain in one sentence: build and send the body for `POST /api/v1/ingest`
 * (pipeline) or `POST /api/v1/records` (verbatim), and report which door ran.
 * `writeApi.ts` decides WHICH door; this file owns HOW each door is spoken to.
 * Split out of `memory.ts` (1532 lines, house cut ~300) in 4.0.0.
 *
 * Junior Tip [no door-to-door fallback, 4.0.0]: `add()` used to retry a 404
 * from `/ingest` on `/records` and then remember, for the client's lifetime,
 * that "this server has no ingest". `/api/v1/ingest` is mounted
 * unconditionally (`server/router.go:135`), so a 404 there now means a real
 * problem (wrong base URL, a proxy) and surfaces as the not-found error it is —
 * instead of quietly moving every later write onto a path with no extraction.
 */

import type { HttpClient } from "./client.js";
import type { MemoryType } from "./coreTypes.js";
import { truncateSummary } from "./summary.js";
import type { AddOptions, AddResult, IngestPayload, RecordPayload } from "./writeTypes.js";

/**
 * Wrap a container tag into the canonical metadata JSON envelope
 * `{"container_tag":"<tag>"}`, merging caller extras underneath it.
 *
 * The records path historically wrote `metadata` as the bare container_tag
 * string ("mem-3f9...") instead of a JSON object. On the server that poisoned
 * every downstream `JSON.parse(metadata)` — keep the wire value valid JSON.
 * Go and Python carry the identical rule (buildMetadataJSON /
 * _build_metadata_json); ALL THREE SDKs MUST stay byte-identical here.
 * Returns "{}" when the tag is empty and there are no extras.
 */
export function buildMetadataJson(
  containerTag: string,
  extra?: Record<string, unknown>): string {
  // Caller extras first, container_tag last, so a caller key named
  // "container_tag" can never clobber the SDK-owned tag.
  if (!containerTag && !extra) return "{}";
  const merged: Record<string, unknown> = { ...(extra ?? {}) };
  if (containerTag) merged.container_tag = containerTag;
  return JSON.stringify(merged);
}

/**
 * Does this `add()` call have to use the records door?
 *
 * Junior Tip [read the RAW intent]: `score`/`type`/`metadata` are tested with
 * `!== undefined` BEFORE any default collapses them; otherwise every add would
 * look "pinned" and never reach the ingest pipeline. The ingest body carries
 * none of those fields, so a pinned add routed to `/ingest` would silently drop
 * them. An EMPTY metadata object is not a pin — Go (`len(cfg.metadata) > 0`)
 * and Python (`bool(metadata)`) agree.
 */
export function pinsRecordsPath(options?: AddOptions): boolean {
  return (
    options?.mode === "regular" ||
    options?.score !== undefined ||
    options?.type !== undefined ||
    (options?.metadata !== undefined && Object.keys(options.metadata).length > 0)
  );
}

/** Everything one `POST /api/v1/records` write needs. */
export interface RecordWrite {
  text: string;
  score: number;
  type: MemoryType;
  sessionUuid: string;
  containerTag: string;
  metadata?: Record<string, unknown>;
  /** Written verbatim; "saved" when the caller does not say otherwise. */
  status?: string;
  relatedIds?: number[];
  validFrom?: string;
  validUntil?: string;
}

/**
 * Create exactly one record via `POST /api/v1/records`.
 *
 * Exactly one request. If the session has no episodic anchor yet the server
 * answers HTTP 422 ("create an episodic record first"); that error surfaces
 * unchanged — the SDK never fabricates a synthetic anchor.
 */
export async function writeRecord(
  httpClient: HttpClient,
  write: RecordWrite): Promise<AddResult> {
  const summary = truncateSummary(write.text);

  // Junior Tip [why valid_from goes INSIDE metadata, 2026-09-14]: on this route
  // the server reads the temporal window from the metadata
  // (`service/record_create.go:334`, `metaMap["valid_from"]`), never from a
  // top-level field. Sending it at the top answered 201 and stored no window —
  // silent loss on the WRITE path. Go (`client/parity.go`) and Python merge it
  // into metadata too; merging before buildMetadataJson keeps container_tag
  // with the last word.
  const temporalMetadata: Record<string, unknown> = { ...(write.metadata ?? {}) };
  if (write.validFrom) temporalMetadata.valid_from = write.validFrom;
  if (write.validUntil) temporalMetadata.valid_until = write.validUntil;

  const payload: RecordPayload = {
    uuid: write.sessionUuid,
    type: write.type,
    dimension: 0,
    prefix: "",
    weight: write.score / 10,
    score: write.score,
    vector: "",
    related_ids: write.relatedIds ?? [],
    main_ids: [],
    consolidate_id: 0,
    metadata: buildMetadataJson(write.containerTag, temporalMetadata),
    summary,
    content: write.text,
    consolidated: false,
    status: write.status ?? "saved",
  };
  const data = await httpClient.post<{ id?: number }>("/api/v1/records", payload);
  return {
    // The session the record actually landed in, so AddResult is truthful.
    sessionId: write.sessionUuid,
    records: [{ id: data.id ?? 0, type: write.type, summary }],
    mode: "records",
  };
}

/**
 * Send raw text through the ingest pipeline via `POST /api/v1/ingest`.
 *
 * The body is content + container_tag + the (required) session_id; the server
 * owns type/score on this path. Any HTTP error — 404 included — is thrown to
 * the caller as the typed error the transport built.
 */
export async function ingestText(
  httpClient: HttpClient,
  text: string,
  containerTag: string,
  sessionId: string): Promise<AddResult> {
  const payload: IngestPayload = {
    content: text,
    container_tag: containerTag,
    session_id: sessionId,
  };
  const data = await httpClient.post<{
    id?: number;
    records?: Array<{ id: number; type: string; summary: string }>;
  }>("/api/v1/ingest", payload);

  const records = data.records ?? [
    { id: data.id ?? 0, type: "episodic", summary: truncateSummary(text) },
  ];
  return {
    sessionId,
    records: records.map((recordRow) => ({
      id: recordRow.id,
      type: (recordRow.type ?? "episodic") as MemoryType,
      summary: recordRow.summary ?? truncateSummary(text),
    })),
    mode: "ingest",
  };
}
