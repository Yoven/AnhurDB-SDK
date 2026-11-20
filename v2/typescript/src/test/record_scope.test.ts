/**
 * By-id scope (ADR-0029, L2 tenant library) through the REAL transport — a
 * genuine `Memory` against a stubbed `globalThis.fetch` — plus the typed L2
 * error answers and the delete-by-file legs. Parity: Go
 * `client/record_scope_test.go` + `client/scope_errors_test.go`, Python
 * `tests/test_record_scope.py`.
 *
 * Server shapes copied from AnhurDB fix/l2-lib-scope:
 *  - ?scope= on by-id routes: server/router.go (rh.ScopedByID),
 *    server/handler/record_scoped_by_id.go:54,75; batch-content / supersede:
 *    server/handler/record_batch.go, server/handler/record_supersede.go.
 *  - upload status ?scope= and the ambiguous 409: server/handler/upload_status_scope.go:147,188-189.
 *  - collision sentence: server/service/search_scope_admit.go:29,57 (409 via
 *    handler/search_scope_request.go:35-44); upload 400: service/upload_destination.go:163-165.
 *  - partial delete: server/handler/record_delete_file_library.go:53-57, legs
 *    from server/service/record_delete_file_library.go:34-44.
 */

import { describe, it } from "node:test";
import * as assert from "node:assert/strict";
import { Memory } from "../memory.js";
import {
  AnhurError,
  AnhurLibraryDeletePartialError,
  AnhurQueryError,
  AnhurSessionRecordLimitError,
  AnhurTenantLibraryCollisionError,
  AnhurUploadStatusAmbiguousError,
  tenantSharedScopeForHit,
  uploadStatusScope,
} from "../index.js";
import type { RecordScope } from "../index.js";

const COLLISION_409 = JSON.stringify({ error: "store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared" });
const COLLISION_400 = JSON.stringify({ error: "tenant_shared upload: store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared" });
const AMBIGUOUS_409 = JSON.stringify({ error: "record 42 is a file upload in more than one plane; pass ?scope=sessions|tenant_shared|tenant_shared_legacy|client_shared" });
const PARTIAL_500 = JSON.stringify({
  error: "tenant library delete-by-file partially failed — retry is safe (idempotent): primary deleted=3 matched=3; legacy FAILED: " + "x".repeat(600),
  partial: true,
  legs: [
    { generation: "primary", session_uuid: "shared-c1-t1_lib", matched_count: 3, deleted_count: 3 },
    { generation: "legacy", session_uuid: "shared-c1-t1", matched_count: 0, deleted_count: 0, error: "database error" },
  ],
});
const DELETE_OK = {
  session_uuid: "shared-c1-t1_lib", ingest_key_prefix: "ef9976f1ef5d5176", matched_count: 3,
  deleted_count: 3, deleted_ids: [1, 2, 3], dry_run: false, purge: true,
  legs: [
    { generation: "primary", session_uuid: "shared-c1-t1_lib", matched_count: 3, deleted_count: 3 },
    { generation: "legacy", session_uuid: "shared-c1-t1", dispatch_skipped: true, matched_count: 0, deleted_count: 0 },
  ],
};

interface Seen { method: string; path: string; query: Record<string, string> }

/** Stub fetch with (status, body) and record every request. */
async function withStub<T>(
  status: number,
  body: string,
  exercise: (memory: Memory, seen: Seen[]) => Promise<T>,
): Promise<T> {
  const originalFetch = globalThis.fetch;
  const seen: Seen[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    seen.push({ method: init?.method ?? "GET", path: url.pathname, query: Object.fromEntries(url.searchParams) });
    return new Response(body, { status, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  try {
    return await exercise(new Memory({ apiKey: "test-key-000", userId: "u", url: "http://localhost:8000" }), seen);
  } finally {
    globalThis.fetch = originalFetch;
  }
}

async function caught(promise: Promise<unknown>): Promise<unknown> {
  try {
    await promise;
  } catch (thrown) {
    return thrown;
  }
  assert.fail("expected a throw");
}

async function allByIdCalls(memory: Memory, scope: RecordScope | undefined): Promise<void> {
  const options = scope ? { scope } : undefined;
  await memory.get(42, options);
  await memory.readContent(42, options);
  await memory.getContext(42, options);
  await memory.getGrounding(42, 2, options);
  await memory.batchReadContent([42, 43], options);
  await memory.update(42, { summary: "s" }, options);
  await memory.delete(42, options);
  await memory.supersede(42, 43, options);
  await memory.uploadStatus(42, options);
}

describe("by-id scope (ADR-0029)", () => {
  it("every by-id method sends ?scope= only when given", async () => {
    const expected = [
      "GET /api/v1/records/42", "GET /api/v1/records/42/content", "GET /api/v1/records/42/topology",
      "GET /api/v1/records/42/grounding", "POST /api/v1/records/batch-content", "PATCH /api/v1/records/42",
      "DELETE /api/v1/records/42", "POST /api/v1/records/supersede", "GET /api/v1/upload/42/status",
    ];
    for (const scope of [undefined, "sessions", "tenant_shared", "tenant_shared_legacy", "client_shared"] as const) {
      const seen = await withStub(200, "{}", async (memory, requests) => {
        await allByIdCalls(memory, scope);
        return requests;
      });
      assert.deepEqual(seen.map((request) => `${request.method} ${request.path}`), expected);
      for (const request of seen) {
        assert.equal(request.query.scope, scope, `${request.method} ${request.path}`);
        if (scope === undefined && !request.path.endsWith("/grounding")) {
          assert.deepEqual(request.query, {}, `no scope must leave ${request.path} unchanged`);
        }
      }
    }
  });

  it("refuses shared_all / unknown scopes before the wire", async () => {
    await withStub(200, "{}", async (memory, seen) => {
      for (const badScope of ["shared_all", "bogus"]) {
        const thrown = await caught(memory.get(42, { scope: badScope as RecordScope }));
        assert.ok(thrown instanceof AnhurQueryError);
        assert.equal(thrown.kind, "invalid_request");
        assert.equal(thrown.retryable, false);
        assert.ok(await caught(memory.supersede(1, 2, { scope: badScope as RecordScope })) instanceof AnhurQueryError);
      }
      assert.equal(seen.length, 0);
    });
  });

  it("upload records its plane and waitForUpload forwards it", async () => {
    const cases: Array<[Parameters<Memory["uploadFile"]>[2], RecordScope]> = [
      [undefined, "tenant_shared"],
      [{ mode: "client_shared" }, "client_shared"],
      [{ sessionId: "sess-1", linkedEpisodicId: 7 }, "sessions"],
    ];
    for (const [options, wantScope] of cases) {
      await withStub(200, JSON.stringify({ record_id: 42, status: "completed", completed: true }), async (memory, seen) => {
        const upload = await memory.uploadFile("a.txt", "hi", options);
        assert.equal(upload.scope, wantScope);
        await memory.waitForUpload(42, { scope: upload.scope });
        assert.equal(seen[seen.length - 1].query.scope, wantScope);
      });
    }
  });

  it("tenantSharedScopeForHit / uploadStatusScope mirror the server", () => {
    assert.equal(tenantSharedScopeForHit("shared-c1-t1_lib"), "tenant_shared");
    assert.equal(tenantSharedScopeForHit(" shared-c1-t1_lib "), "tenant_shared");
    assert.equal(tenantSharedScopeForHit("shared-c1-t1"), "tenant_shared_legacy");
    assert.equal(tenantSharedScopeForHit("shared-c1-t1_libx"), "tenant_shared_legacy");
    // server/service/upload_destination.go:58-110
    assert.equal(uploadStatusScope(undefined, undefined), "tenant_shared");
    assert.equal(uploadStatusScope("", "sess-1"), "sessions");
    assert.equal(uploadStatusScope("client_shared"), "client_shared");
    assert.equal(uploadStatusScope("controlplane"), "tenant_shared");
  });
});

describe("L2 typed errors (ADR-0029)", () => {
  it("ambiguous upload status is typed and ends the wait at once", async () => {
    await withStub(409, AMBIGUOUS_409, async (memory) => {
      const thrown = await caught(memory.uploadStatus(42));
      assert.ok(thrown instanceof AnhurUploadStatusAmbiguousError);
      assert.equal(thrown.statusCode, 409);
      assert.equal(thrown.retryable, false);
      const startedAt = Date.now();
      assert.ok(await caught(memory.waitForUpload(42, { timeoutMs: 5000, intervalMs: 1000 })) instanceof AnhurUploadStatusAmbiguousError);
      assert.ok(Date.now() - startedAt < 500);
    });
  });

  it("collision is typed on 409 (search, by-id, delete) and 400 (upload)", async () => {
    await withStub(409, COLLISION_409, async (memory) => {
      for (const call of [
        memory.search("q", ["*"], { scope: "tenant_shared" }),
        memory.get(42, { scope: "tenant_shared" }),
        memory.deleteFile("", "ef9976f1ef5d5176", { scope: "tenant_shared" }),
      ]) {
        const thrown = await caught(call);
        assert.ok(thrown instanceof AnhurTenantLibraryCollisionError, String(thrown));
        assert.equal(thrown.statusCode, 409);
        assert.equal(thrown.kind, "conflict");
        assert.equal(thrown.retryable, false);
        assert.ok(!(thrown instanceof AnhurSessionRecordLimitError));
      }
    });
    await withStub(400, COLLISION_400, async (memory) => {
      const thrown = await caught(memory.uploadFile("a.txt", "hi", { mode: "tenant_shared" }));
      assert.ok(thrown instanceof AnhurTenantLibraryCollisionError);
      assert.equal(thrown.statusCode, 400);
      assert.equal(thrown.kind, "invalid_request");
      assert.equal(thrown.retryable, false);
    });
  });

  it("session limit and a generic 409 stay distinct", async () => {
    await withStub(409, JSON.stringify({ error: "session has reached the maximum of 500 records", code: "session_record_limit" }), async (memory) => {
      assert.ok(await caught(memory.get(1)) instanceof AnhurSessionRecordLimitError);
    });
    await withStub(409, JSON.stringify({ error: "record already superseded" }), async (memory) => {
      const thrown = await caught(memory.get(1));
      assert.ok(thrown instanceof AnhurError);
      assert.ok(!(thrown instanceof AnhurTenantLibraryCollisionError));
      assert.ok(!(thrown instanceof AnhurUploadStatusAmbiguousError));
      assert.ok(!(thrown instanceof AnhurSessionRecordLimitError));
    });
  });

  it("deleteFile exposes legs, sends scope, and validates it", async () => {
    await withStub(200, JSON.stringify(DELETE_OK), async (memory, seen) => {
      const result = await memory.deleteFile("", "ef9976f1ef5d5176", { scope: "tenant_shared" });
      assert.deepEqual(seen[0].query, { ingest_key_prefix: "ef9976f1ef5d5176", dry_run: "false", scope: "tenant_shared" });
      assert.deepEqual(result.legs?.map((leg) => leg.generation), ["primary", "legacy"]);
      assert.equal(result.legs?.[1].dispatch_skipped, true);
      for (const badScope of ["sessions", "tenant_shared_legacy", "shared_all"]) {
        assert.ok(await caught(memory.deleteFile("s1", "ef9976f1ef5d5176", { scope: badScope as "client_shared" })) instanceof AnhurQueryError);
      }
      assert.ok(await caught(memory.deleteFile("", "ef9976f1ef5d5176", { scope: "client_shared" })) instanceof Error);
      assert.equal(seen.length, 1);
    });
  });

  it("a partial library delete carries every leg past the 500-char excerpt", async () => {
    await withStub(500, PARTIAL_500, async (memory) => {
      const thrown = await caught(memory.deleteFile("", "ef9976f1ef5d5176", { scope: "tenant_shared" }));
      assert.ok(thrown instanceof AnhurLibraryDeletePartialError, String(thrown));
      assert.deepEqual(thrown.legs.map((leg) => leg.generation), ["primary", "legacy"]);
      assert.equal(thrown.legs[1].error, "database error");
      assert.equal(thrown.statusCode, 500);
      assert.equal(thrown.retryable, true);
      assert.ok(thrown.message.length < 700);
    });
  });
});
