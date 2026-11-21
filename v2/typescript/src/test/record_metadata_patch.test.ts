/**
 * update() puts metadata on the wire as the JSON STRING the server decodes.
 *
 * An object keeps `null` so a merge patch (AnhurDB ADR-0035) can delete keys;
 * a string travels byte-for-byte as before. Parity: Go
 * `client/record_metadata_patch_test.go`, Python
 * `tests/test_record_metadata_patch.py` — same cases, same expectations.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import { AnhurQueryError } from "../index.js";
import { expectNoRequest, recordWire } from "./wireRecorder.js";

const UPDATED = { message: "record updated", last_raft_index: 1 };

/** Run one update and return the metadata STRING that reached the wire. */
async function wireMetadata(metadataValue: string | Record<string, unknown>): Promise<string> {
  const memory = new Memory({ apiKey: "test-key", url: "http://stub.invalid" });
  const callerFields = { summary: "s", metadata: metadataValue };
  const { requests } = await recordWire(UPDATED, () => memory.update(42, callerFields));
  assert.equal(requests.length, 1);
  const wireBody = requests[0].body as Record<string, unknown>;
  assert.equal(wireBody.summary, "s", "other fields must travel unchanged");
  assert.equal(typeof wireBody.metadata, "string", "the server field is a string; a nested object is a 400");
  assert.equal(callerFields.metadata, metadataValue, "the caller's object must not be mutated");
  return wireBody.metadata as string;
}

describe("update metadata wire shape (ADR-0035 merge patch)", () => {
  it("an object keeps null so the server deletes the key", async () => {
    const metadataPatch = { keep: "x", drop_me: null, nested: { inner: null } };
    const sentMetadata = await wireMetadata(metadataPatch);
    assert.deepEqual(JSON.parse(sentMetadata), metadataPatch);
    assert.ok(sentMetadata.includes('"drop_me":null'));
  });

  it("an empty object is sent as the empty object", async () => {
    assert.deepEqual(JSON.parse(await wireMetadata({})), {});
  });

  it("string metadata passes through byte-for-byte", async () => {
    assert.equal(await wireMetadata('{"a":1,"b":null}'), '{"a":1,"b":null}');
  });

  it("a non-finite number is refused before the wire (plain JSON would send null = delete)", async () => {
    const memory = new Memory({ apiKey: "test-key", url: "http://stub.invalid" });
    const { requestCount, thrown } = await expectNoRequest(
      () => memory.update(42, { metadata: { ratio: Number.NaN } }));
    assert.equal(requestCount, 0, "a refused object must not reach the wire");
    assert.ok(thrown instanceof AnhurQueryError, `got ${String(thrown)}`);
    assert.equal((thrown as AnhurQueryError).statusCode, 400);
    assert.match((thrown as Error).message, /^INVALID_PARAM: update: metadata cannot be encoded as JSON/);
  });
});
