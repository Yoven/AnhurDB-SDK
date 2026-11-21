/**
 * The strict decode of the eight listing routes that always answer a JSON
 * object envelope (4.0.0).
 *
 * One domain in one sentence: check that a 2xx answer is the route's envelope
 * and hand back its list, or throw `AnhurUnexpectedResponseError`.
 *
 * Routes and keys (server truth, AnhurDB main):
 *
 *     GET /api/v1/sessions/stats         "sessions"  handler/record_session.go:135-142
 *     GET /api/v1/recent                 "records"   handler/record_session.go:455
 *     GET /api/v1/manifest               "records"   handler/search_aux.go:224
 *     GET /api/v1/chats/{uuid}/manifest  "records"   handler/record_session.go:305-311
 *     GET /api/v1/chats/{uuid}           "records"   handler/record_session.go:184
 *     GET /api/v1/entities               "entities"  handler/entity.go:254-257
 *     GET /api/v1/entities/list          "entities"  handler/entity.go:200-208
 *     GET /api/v1/records/{id}/entities  "entities"  handler/entity.go:561-565
 *
 * The rule, identical in Go (`client/envelope.go`) and Python
 * (`anhurdb/client/envelope.py`): the body must be a JSON object that HAS the
 * key, and the key's value must be an array or null (null = zero rows, the
 * server's own zero-hit answer). Anything else — a bare array, an empty body
 * (the transport turns it into `{}`), a missing key, a non-array value —
 * throws.
 *
 * Junior Tip [why "no rows" can never double as "I did not understand"]: 3.x
 * read `data.records ?? []`, so a bare array, a proxy page or a server bug
 * read as "this tenant has no memories". An empty list is a legitimate
 * answer; it cannot also be the signal for an unrecognised shape.
 *
 * @module
 */

import { AnhurUnexpectedResponseError } from "./errors.js";

/**
 * Return `data` as the envelope object, or throw.
 *
 * @param data  - The decoded body as the transport returned it.
 * @param route - The route TEMPLATE, for the cross-SDK message.
 * @param key   - The list key the envelope must carry.
 * @throws {AnhurUnexpectedResponseError} kind `"server"`.
 */
export function requireEnvelope<Envelope extends object>(
  data: unknown,
  route: string,
  key: string,
): Envelope {
  if (data === null || typeof data !== "object" || Array.isArray(data) || !(key in data)) {
    throw new AnhurUnexpectedResponseError(route, key);
  }
  const listValue = (data as Record<string, unknown>)[key];
  if (listValue !== null && !Array.isArray(listValue)) {
    throw new AnhurUnexpectedResponseError(route, key);
  }
  return data as Envelope;
}

/**
 * The envelope's list (`[]` when the server sent `null`), or throw.
 */
export function envelopeList<Row>(data: unknown, route: string, key: string): Row[] {
  const envelope = requireEnvelope<Record<string, Row[] | null>>(data, route, key);
  return envelope[key] ?? [];
}
