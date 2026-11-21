/**
 * AnhurDB TypeScript SDK — how `update()` puts `metadata` on the wire.
 *
 * One domain in one sentence: turn the caller's `metadata` value into the JSON
 * STRING the server's `PATCH /api/v1/records/{id}` decodes, so an object can
 * carry `null` values (= delete that key) under the server's merge-patch
 * contract.
 *
 * Server contract (AnhurDB feat/metadata-merge-patch, ADR-0035): record update
 * metadata is an RFC 7396 JSON merge patch — present keys overwrite, a null
 * value deletes the key, absent keys are untouched, nested objects merge,
 * arrays are replaced whole. A patch that is not a JSON object is refused (400
 * / InvalidArgument), and so is an update over stored metadata that is not a
 * JSON object. The REST handler decodes the field as a Go string
 * (`server/handler/record_crud.go`, `Metadata string \`json:"metadata"\``), so
 * the body must hold a JSON-encoded string, never a nested object.
 *
 * Parity: Go `client/record_metadata_patch.go`, Python
 * `anhurdb/client/record_metadata_patch.py` — same accepted shapes, same
 * refusal.
 */

import { AnhurQueryError } from "./errors.js";

/** A metadata merge patch: `null` deletes the key, `undefined` is "absent". */
export type MetadataPatch = Record<string, unknown>;

/** True for a plain object (not an array, not null, not a class instance). */
function isPlainObject(candidate: unknown): candidate is MetadataPatch {
  if (candidate === null || typeof candidate !== "object" || Array.isArray(candidate)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(candidate);
  return prototype === Object.prototype || prototype === null;
}

/**
 * Return the body `update()` should send.
 *
 * A `metadata` value that is a plain object is JSON-encoded to a string with
 * `null` kept; every other value (a string, as in 3.x/4.0.0, or anything else
 * from plain JS) is sent unchanged so the server stays the judge of it. The
 * caller's object is never mutated: a shallow copy is returned when the
 * metadata value had to be encoded.
 *
 * Junior Tip [why the object is encoded HERE and not left to the transport]:
 * the transport stringifies the whole body, which would put metadata on the
 * wire as a nested JSON object. The server field is a string, so that body is
 * a 400 "invalid request body". The `null` values are the point of accepting an
 * object: under merge-patch a key the caller wants REMOVED must travel as null.
 * `undefined` is NOT a delete — `JSON.stringify` drops it, so the stored key is
 * left untouched (the same as not naming it).
 *
 * Junior Tip [why NaN/Infinity throw instead of becoming null]: plain
 * `JSON.stringify` turns them into `null`, which under merge-patch would
 * DELETE the key — a silent data loss Go and Python cannot produce (both
 * refuse the value). The replacer makes all three SDKs refuse it.
 *
 * @throws AnhurQueryError (400, invalid_request) before any request when the
 *         object cannot be encoded as JSON (non-finite number, BigInt, cycle).
 */
export function encodeUpdateMetadata<FieldsShape extends object>(
  method: string,
  fields: FieldsShape,
): FieldsShape {
  const metadataValue: unknown = (fields as { metadata?: unknown }).metadata;
  if (!isPlainObject(metadataValue)) {
    return fields;
  }
  let encodedPatch: string;
  try {
    encodedPatch = JSON.stringify(metadataValue, (_key, nestedValue: unknown) => {
      if (typeof nestedValue === "number" && !Number.isFinite(nestedValue)) {
        throw new TypeError(`${nestedValue} is not a JSON number`);
      }
      return nestedValue;
    });
  } catch (encodeError) {
    const reason = encodeError instanceof Error ? encodeError.message : String(encodeError);
    throw new AnhurQueryError(
      `INVALID_PARAM: ${method}: metadata cannot be encoded as JSON: ${reason}`,
      400,
    );
  }
  return { ...fields, metadata: encodedPatch };
}
