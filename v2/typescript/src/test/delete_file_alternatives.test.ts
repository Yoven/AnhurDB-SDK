/**
 * deleteFile zero-match answer: `alternative_sessions` names the sessions that
 * DO hold the file. Offline: `fetch` returns the server's exact bytes.
 */
import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import type { DeleteFileResult } from "../types.js";

/**
 * Byte-for-byte what `service.DeleteFileResult` marshals to when the prefix
 * matched nothing in the requested session but exists in two others
 * (`server/service/record_delete_file.go:26-62`; element type
 * `database.FileSessionCandidate`, `server/database/file_record_sessions.go:38-48`).
 * Same string as Go `client/delete_file_alternatives_test.go` and Python
 * `tests/test_delete_file.py`.
 */
const ZERO_MATCH_SERVER_JSON =
  '{"session_uuid":"chat-wrong","ingest_key_prefix":"ef9976f1ef5d5176",' +
  '"matched_count":0,"deleted_count":0,"dry_run":false,"purge":false,' +
  '"alternative_sessions":[' +
  '{"session_uuid":"chat-42","matched_count":511,' +
  '"root_checksum":"ef9976f1ef5d51760a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",' +
  '"root_filename":"lei-8666.pdf"},' +
  '{"session_uuid":"chat-7","matched_count":3}]}';

/** Run `deleteFile` against a stubbed fetch that answers `rawBody` verbatim. */
async function deleteFileAgainst(rawBody: string): Promise<DeleteFileResult | null> {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async () =>
    new Response(rawBody, {
      status: 200,
      headers: { "content-type": "application/json" },
    })) as unknown as typeof fetch;
  try {
    const mem = new Memory({ apiKey: "key", userId: "u", url: "http://localhost:8000" });
    return await mem.deleteFile("chat-wrong", "ef9976f1ef5d5176");
  } finally {
    globalThis.fetch = originalFetch;
  }
}

describe("DeleteFileResult.alternative_sessions", () => {
  it("decodes both candidates from the exact server JSON", async () => {
    const result = await deleteFileAgainst(ZERO_MATCH_SERVER_JSON);
    assert.equal(result?.matched_count, 0);
    assert.equal(result?.raft_index, undefined);
    assert.deepEqual(result?.alternative_sessions, [
      {
        session_uuid: "chat-42",
        matched_count: 511,
        root_checksum: "ef9976f1ef5d51760a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
        root_filename: "lei-8666.pdf",
      },
      { session_uuid: "chat-7", matched_count: 3 },
    ]);
  });

  for (const purge of [true, false]) {
    it(`decodes purge=${purge}`, async () => {
      const result = await deleteFileAgainst(
        '{"session_uuid":"s_shared","ingest_key_prefix":"ef9976f1ef5d5176",' +
          `"matched_count":2,"deleted_count":2,"deleted_ids":[7,8],"dry_run":false,"purge":${purge}}`,
      );
      assert.equal(result?.purge, purge);
    });
  }

  it("stays absent when the server omits it", async () => {
    const result = await deleteFileAgainst(
      '{"session_uuid":"chat-42","ingest_key_prefix":"ef9976f1ef5d5176",' +
        '"matched_count":511,"deleted_count":511,"dry_run":false,"purge":false}',
    );
    assert.equal(result?.alternative_sessions, undefined);
  });
});
