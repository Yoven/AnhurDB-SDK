"""By-id scope (ADR-0029, L2 tenant library) through the REAL transport — a
genuine ``Memory`` against a stub aiohttp server — plus the typed L2
error answers and the delete-by-file legs. Parity: Go
``client/record_scope_test.go`` + ``client/scope_errors_test.go``, TypeScript
``src/test/record_scope.test.ts``.

Server shapes copied from AnhurDB fix/l2-lib-scope:
  - ?scope= on by-id routes: server/router.go (rh.ScopedByID) and
    server/handler/record_scoped_by_id.go:54,75; batch-content / supersede:
    server/handler/record_batch.go, server/handler/record_supersede.go.
  - upload status ?scope= and the ambiguous 409: server/handler/upload_status_scope.go:147,188-189.
  - collision (4574c4d): server/service/search_scope_admit.go sentence + code
    "tenant_library_collision", HTTP 409 on every door including upload
    (handler/search_scope_request.go writeScopeLegError, handler/upload.go);
    Pre-4574c4d bodies (no code; 400 on upload) are NOT typed since 4.0.0 —
    the tests below prove they reach the caller as the generic errors.
    Registry unavailable on upload: HTTP 503.
  - partial delete: server/handler/record_delete_file_library.go:53-57, legs
    from server/service/record_delete_file_library.go:34-44.
  - upload 202 body: server/handler/upload.go (writeJSON StatusAccepted).
"""

import json
import os
import sys

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb import (  # noqa: E402  public exports
    AnhurLibraryDeletePartialError,
    AnhurSessionRecordLimitError,
    AnhurTenantLibraryCollisionError,
    AnhurUploadStatusAmbiguousError,
    sessions_all,
    tenant_shared_scope_for_hit,
    upload_status_scope,
)
from anhurdb.client import Memory  # noqa: E402
from anhurdb.client.exceptions import AnhurError, AnhurQueryError  # noqa: E402

COLLISION_409 = json.dumps({"error": "store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared", "code": "tenant_library_collision"})
COLLISION_UPLOAD_409 = json.dumps({"error": "tenant_shared upload: store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared", "code": "tenant_library_collision"})
LEGACY_COLLISION_409 = json.dumps({"error": "store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared"})
LEGACY_COLLISION_400 = json.dumps({"error": "tenant_shared upload: store c1_t1_lib: tenant library store id is a registered tenant; rename one of the two tenants before using tenant_shared"})
REGISTRY_DOWN_503 = json.dumps({"error": "tenant_shared upload: tenant library store c1_t1_lib: tenant registry unavailable: database is locked"})
AMBIGUOUS_409 = json.dumps({"error": "record 42 is a file upload in more than one plane; pass ?scope=sessions|tenant_shared|tenant_shared_legacy|client_shared"})
LIMIT_409 = json.dumps({"error": "session has reached the maximum of 500 records", "code": "session_record_limit", "limit": 500, "count": 500, "session_uuid": "s1"})
PARTIAL_500 = json.dumps({
    "error": "tenant library delete-by-file partially failed — retry is safe (idempotent): primary deleted=3 matched=3; legacy FAILED: " + "x" * 600,
    "partial": True,
    "legs": [
        {"generation": "primary", "session_uuid": "shared-c1-t1_lib", "matched_count": 3, "deleted_count": 3},
        {"generation": "legacy", "session_uuid": "shared-c1-t1", "matched_count": 0, "deleted_count": 0, "error": "database error"},
    ],
})
DELETE_OK = json.dumps({
    "session_uuid": "shared-c1-t1_lib", "ingest_key_prefix": "ef9976f1ef5d5176",
    "matched_count": 3, "deleted_count": 3, "deleted_ids": [1, 2, 3], "dry_run": False, "purge": True,
    "legs": [
        {"generation": "primary", "session_uuid": "shared-c1-t1_lib", "matched_count": 3, "deleted_count": 3},
        {"generation": "legacy", "session_uuid": "shared-c1-t1", "dispatch_skipped": True, "matched_count": 0, "deleted_count": 0},
    ],
})
UPLOAD_ACCEPT = json.dumps({
    "message": "File accepted for processing", "record_id": 42, "uuid": "shared-c1-t1_lib",
    "filename": "a.txt", "mime": "text/plain", "mime_detected": "text/plain",
    "extension": ".txt", "size_bytes": 2, "status": "processing",
})
UPLOAD_DONE = json.dumps({"record_id": 42, "uuid": "shared-c1-t1_lib", "status": "completed", "type": "file", "summary": "", "metadata": "{}", "completed": True})


class TestRecordScope(AioHTTPTestCase):
    async def get_application(self):
        app = web.Application()
        self.seen = []

        async def ok(request):
            self.seen.append((request.method, request.path, dict(request.query)))
            if request.path.endswith("/upload"):
                return web.Response(status=202, text=UPLOAD_ACCEPT, content_type="application/json")
            if request.path.endswith("/status"):
                return web.Response(text=UPLOAD_DONE, content_type="application/json")
            if request.path.endswith("/by-file"):
                return web.Response(text=DELETE_OK, content_type="application/json")
            return web.Response(text="{}", content_type="application/json")

        def fixed(status, body):
            async def handle(_request):
                return web.Response(status=status, text=body, content_type="application/json")

            return handle

        app.router.add_route("*", "/ok/{tail:.*}", ok)
        app.router.add_route("*", "/collide/{tail:.*}", fixed(409, COLLISION_409))
        app.router.add_route("*", "/collideupload/{tail:.*}", fixed(409, COLLISION_UPLOAD_409))
        app.router.add_route("*", "/reworded/{tail:.*}", fixed(409, json.dumps({"error": "library store is taken", "code": "tenant_library_collision"})))
        app.router.add_route("*", "/othercode/{tail:.*}", fixed(409, json.dumps({"error": "tenant library store id is a registered tenant", "code": "something_else"})))
        app.router.add_route("*", "/legacy409/{tail:.*}", fixed(409, LEGACY_COLLISION_409))
        app.router.add_route("*", "/legacy400/{tail:.*}", fixed(400, LEGACY_COLLISION_400))
        app.router.add_route("*", "/registrydown/{tail:.*}", fixed(503, REGISTRY_DOWN_503))
        app.router.add_route("*", "/ambiguous/{tail:.*}", fixed(409, AMBIGUOUS_409))
        app.router.add_route("*", "/limit/{tail:.*}", fixed(409, LIMIT_409))
        app.router.add_route("*", "/conflict/{tail:.*}", fixed(409, json.dumps({"error": "record already superseded"})))
        app.router.add_route("*", "/partial/{tail:.*}", fixed(500, PARTIAL_500))
        return app

    def _client(self, prefix):
        return Memory(url=f"http://localhost:{self.server.port}/{prefix}", api_key="test-key")

    async def _by_id_calls(self, client, scope):
        await client.get(42, scope=scope)
        await client.read_content(42, scope=scope)
        await client.get_context(42, scope=scope)
        await client.get_grounding(42, max_depth=2, scope=scope)
        await client.batch_read_content([42, 43], scope=scope)
        await client.update(42, scope=scope, summary="s")
        await client.delete(42, scope=scope)
        await client.supersede(42, 43, scope=scope)
        await client.upload_status(42, scope=scope)

    @unittest_run_loop
    async def test_every_by_id_method_sends_scope_only_when_given(self):
        expected = [
            ("GET", "/ok/api/v1/records/42"), ("GET", "/ok/api/v1/records/42/content"),
            ("GET", "/ok/api/v1/records/42/topology"), ("GET", "/ok/api/v1/records/42/grounding"),
            ("POST", "/ok/api/v1/records/batch-content"), ("PATCH", "/ok/api/v1/records/42"),
            ("DELETE", "/ok/api/v1/records/42"), ("POST", "/ok/api/v1/records/supersede"),
            ("GET", "/ok/api/v1/upload/42/status"),
        ]
        for scope in (None, "sessions", "tenant_shared", "tenant_shared_legacy", "client_shared"):
            self.seen.clear()
            async with self._client("ok") as client:
                await self._by_id_calls(client, scope)
            self.assertEqual([(method, path) for method, path, _ in self.seen], expected)
            for method, path, query in self.seen:
                self.assertEqual(query.get("scope"), scope, f"{method} {path}")
                if scope is None and not path.endswith("/grounding"):
                    self.assertEqual(query, {}, f"no scope must leave {path} unchanged")

    @unittest_run_loop
    async def test_non_addressable_scope_is_refused_before_the_wire(self):
        for bad_scope in ("shared_all", "bogus"):
            self.seen.clear()
            async with self._client("ok") as client:
                with self.assertRaises(AnhurQueryError) as caught:
                    await client.get(42, scope=bad_scope)
                self.assertEqual(caught.exception.kind, AnhurError.KIND_INVALID_REQUEST)
                self.assertFalse(caught.exception.retryable)
                with self.assertRaises(AnhurQueryError):
                    await client.supersede(1, 2, scope=bad_scope)
            self.assertEqual(self.seen, [])

    @unittest_run_loop
    async def test_upload_records_plane_and_wait_forwards_it(self):
        cases = [
            ({}, "tenant_shared"),
            ({"mode": "client_shared"}, "client_shared"),
            ({"session_id": "sess-1", "linked_episodic_id": 7}, "sessions"),
        ]
        async with self._client("ok") as client:
            for kwargs, want_scope in cases:
                result = await client.upload_file("a.txt", b"hi", **kwargs)
                self.assertEqual(result.scope, want_scope)
                self.assertNotIn("scope", result.model_dump())
                await client.wait_for_upload(result.record_id, scope=result.scope)
                self.assertEqual(self.seen[-1][2].get("scope"), want_scope)

    @unittest_run_loop
    async def test_ambiguous_upload_status_is_typed_and_ends_the_wait(self):
        async with self._client("ambiguous") as client:
            with self.assertRaises(AnhurUploadStatusAmbiguousError) as caught:
                await client.upload_status(42)
            self.assertEqual(caught.exception.status_code, 409)
            self.assertFalse(caught.exception.retryable)
            with self.assertRaises(AnhurUploadStatusAmbiguousError):
                await client.wait_for_upload(42, timeout=5, interval=1)

    @unittest_run_loop
    async def test_collision_is_typed_on_409_on_every_door(self):
        async with self._client("collide") as client:
            with self.assertRaises(AnhurTenantLibraryCollisionError) as on_search:
                await client.search("q", sessions=sessions_all(), scope="tenant_shared")
            with self.assertRaises(AnhurTenantLibraryCollisionError):
                await client.get(42, scope="tenant_shared")
            with self.assertRaises(AnhurTenantLibraryCollisionError):
                await client.delete_file("", "ef9976f1ef5d5176", scope="tenant_shared")
        self.assertEqual((on_search.exception.status_code, on_search.exception.kind), (409, "conflict"))
        self.assertFalse(on_search.exception.retryable)
        self.assertNotIsInstance(on_search.exception, AnhurSessionRecordLimitError)
        async with self._client("collideupload") as client:
            with self.assertRaises(AnhurTenantLibraryCollisionError) as on_upload:
                await client.upload_file("a.txt", b"hi", mode="tenant_shared")
        self.assertEqual((on_upload.exception.status_code, on_upload.exception.kind), (409, "conflict"))
        self.assertFalse(on_upload.exception.retryable)

    @unittest_run_loop
    async def test_collision_code_wins_over_text(self):
        async with self._client("reworded") as client:
            with self.assertRaises(AnhurTenantLibraryCollisionError):
                await client.get(42, scope="tenant_shared")
        async with self._client("othercode") as client:
            with self.assertRaises(AnhurQueryError) as caught:
                await client.get(42)
        self.assertNotIsInstance(caught.exception, AnhurTenantLibraryCollisionError)

    @unittest_run_loop
    async def test_collision_sentence_without_code_is_generic_409(self):
        """4.0.0 removal: the sentence alone no longer types the refusal."""
        async with self._client("legacy409") as client:
            with self.assertRaises(AnhurQueryError) as caught:
                await client.get(42, scope="tenant_shared")
        self.assertNotIsInstance(caught.exception, AnhurTenantLibraryCollisionError)
        self.assertEqual((caught.exception.status_code, caught.exception.kind), (409, "conflict"))

    @unittest_run_loop
    async def test_collision_sentence_on_upload_400_is_generic_400(self):
        """4.0.0 removal: the pre-4574c4d upload 400 branch is gone."""
        async with self._client("legacy400") as client:
            with self.assertRaises(AnhurQueryError) as caught:
                await client.upload_file("a.txt", b"hi", mode="tenant_shared")
        self.assertNotIsInstance(caught.exception, AnhurTenantLibraryCollisionError)
        self.assertEqual((caught.exception.status_code, caught.exception.kind), (400, "invalid_request"))

    @unittest_run_loop
    async def test_collision_sentence_on_json_400_is_generic_400(self):
        async with self._client("legacy400") as client:
            with self.assertRaises(AnhurQueryError) as caught:
                await client.get(42, scope="tenant_shared")
        self.assertNotIsInstance(caught.exception, AnhurTenantLibraryCollisionError)
        self.assertEqual(caught.exception.status_code, 400)

    @unittest_run_loop
    async def test_upload_registry_unavailable_is_plain_unavailable(self):
        async with self._client("registrydown") as client:
            with self.assertRaises(AnhurError) as caught:
                await client.upload_file("a.txt", b"hi", mode="tenant_shared")
        self.assertNotIsInstance(caught.exception, AnhurTenantLibraryCollisionError)
        self.assertEqual((caught.exception.status_code, caught.exception.kind), (503, "unavailable"))
        self.assertTrue(caught.exception.retryable)

    @unittest_run_loop
    async def test_session_limit_and_generic_conflict_stay_distinct(self):
        async with self._client("limit") as client:
            with self.assertRaises(AnhurSessionRecordLimitError):
                await client.get(1)
        async with self._client("conflict") as client:
            with self.assertRaises(AnhurQueryError) as caught:
                await client.get(1)
        self.assertNotIsInstance(caught.exception, (AnhurTenantLibraryCollisionError, AnhurUploadStatusAmbiguousError, AnhurSessionRecordLimitError))
        self.assertEqual(caught.exception.status_code, 409)

    @unittest_run_loop
    async def test_delete_file_exposes_legs_and_scope(self):
        async with self._client("ok") as client:
            result = await client.delete_file("", "ef9976f1ef5d5176", scope="tenant_shared")
        self.assertEqual(self.seen[-1][2], {"ingest_key_prefix": "ef9976f1ef5d5176", "dry_run": "false", "scope": "tenant_shared"})
        self.assertEqual([leg.generation for leg in result.legs], ["primary", "legacy"])
        self.assertEqual(result.legs[0].deleted_count, 3)
        self.assertTrue(result.legs[1].dispatch_skipped)

    @unittest_run_loop
    async def test_delete_file_scope_validation(self):
        async with self._client("ok") as client:
            for bad_scope in ("sessions", "tenant_shared_legacy", "shared_all"):
                with self.assertRaises(AnhurQueryError):
                    await client.delete_file("s1", "ef9976f1ef5d5176", scope=bad_scope)
            with self.assertRaises(AnhurQueryError):
                await client.delete_file("", "ef9976f1ef5d5176", scope="client_shared")

    @unittest_run_loop
    async def test_partial_library_delete_carries_every_leg(self):
        async with self._client("partial") as client:
            with self.assertRaises(AnhurLibraryDeletePartialError) as caught:
                await client.delete_file("", "ef9976f1ef5d5176", scope="tenant_shared")
        partial_error = caught.exception
        self.assertEqual([leg.generation for leg in partial_error.legs], ["primary", "legacy"])
        self.assertEqual(partial_error.legs[1].error, "database error")
        self.assertEqual((partial_error.status_code, partial_error.kind, partial_error.retryable), (500, "server", True))
        self.assertLess(len(str(partial_error)), 700)


def test_tenant_shared_scope_for_hit():
    assert tenant_shared_scope_for_hit("shared-c1-t1_lib") == "tenant_shared"
    assert tenant_shared_scope_for_hit(" shared-c1-t1_lib ") == "tenant_shared"
    assert tenant_shared_scope_for_hit("shared-c1-t1") == "tenant_shared_legacy"
    assert tenant_shared_scope_for_hit("shared-c1-t1_libx") == "tenant_shared_legacy"
    assert tenant_shared_scope_for_hit("") == "tenant_shared_legacy"


def test_upload_status_scope_mirrors_resolve_upload_destination():
    # server/service/upload_destination.go:58-110
    assert upload_status_scope(None, None) == "tenant_shared"
    assert upload_status_scope("", "sess-1") == "sessions"
    assert upload_status_scope("chat", "sess-1") == "sessions"
    assert upload_status_scope(" Tenant_Shared ", None) == "tenant_shared"
    assert upload_status_scope("client_shared", None) == "client_shared"
    assert upload_status_scope("controlplane", None) == "tenant_shared"
