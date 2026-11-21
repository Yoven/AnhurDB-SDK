"""Proofs that the 4.0.0 legacy removals are REAL, not just undocumented.

Each test fails if the removed alias / fallback comes back. Wire-level proofs
for the 409 classifiers and the ingest 404 live next to their siblings
(``test_session_record_limit.py``, ``test_record_scope.py``,
``test_http_mock.py::TestIngest404IsAnError``); this file covers the API
surface and the bare-array envelopes through a real aiohttp stub server.
"""

import json
import os
import sys
import unittest

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

import anhurdb  # noqa: E402
import anhurdb.client as client_package  # noqa: E402
from anhurdb import CreateRequest, Memory, MemoryStatus  # noqa: E402
from anhurdb.client.exceptions import AnhurError  # noqa: E402
from anhurdb.client.scope_errors import scope_error_from_response  # noqa: E402
from anhurdb.client.session_limit import session_record_limit_from_response  # noqa: E402

REMOVED_METHODS = (
    "search_with_ast",
    "mark_consolidated",
    "link_to_consolidated",
    "update_consolidate_ids",
)
COLLISION_SENTENCE = "store c1_t1_lib: tenant library store id is a registered tenant"


class TestRemovedSurface(unittest.TestCase):
    def test_anhur_client_is_gone(self):
        self.assertNotIn("AnhurClient", anhurdb.__all__)
        self.assertFalse(hasattr(anhurdb, "AnhurClient"))
        self.assertFalse(hasattr(client_package, "AnhurClient"))
        self.assertFalse(hasattr(client_package, "_LEGACY_LOCAL_URL"))
        with self.assertRaises(ImportError):
            from anhurdb import AnhurClient  # noqa: F401

    def test_deprecated_method_aliases_are_gone(self):
        for method_name in REMOVED_METHODS:
            self.assertFalse(hasattr(Memory, method_name), method_name)
        # Their canonical replacements stay.
        for method_name in ("query", "batch_update_status", "link_consolidated"):
            self.assertTrue(callable(getattr(Memory, method_name)), method_name)

    def test_pending_record_status_is_gone(self):
        self.assertNotIn("pending", [member.value for member in MemoryStatus])
        self.assertIn("pending_judge", [member.value for member in MemoryStatus])

    def test_create_request_uuid_alias_raises(self):
        with self.assertRaises(ValueError) as caught:
            CreateRequest(uuid="s1", content="c")
        self.assertIn("session_id", str(caught.exception))
        request = CreateRequest(session_id="s1", content="c")
        self.assertNotIn("uuid", request.model_dump())


class TestClassifiersAreCodeOnly(unittest.TestCase):
    def test_collision_sentence_is_not_typed_on_any_status(self):
        for status in (400, 409):
            body = json.dumps({"error": COLLISION_SENTENCE})
            self.assertIsNone(scope_error_from_response(status, body), status)

    def test_collision_code_on_400_is_not_typed(self):
        body = json.dumps({"error": COLLISION_SENTENCE, "code": "tenant_library_collision"})
        self.assertIsNone(scope_error_from_response(400, body))

    def test_session_limit_sentence_without_code_is_not_typed(self):
        for body in (
            json.dumps({"error": "session has reached the maximum of 500 records"}),
            "session has reached the maximum of 500 records",
        ):
            self.assertIsNone(session_record_limit_from_response(409, body))

    def test_session_limit_fields_are_required_ints(self):
        typed = session_record_limit_from_response(409, json.dumps({
            "error": "full", "code": "session_record_limit",
            "limit": 5, "count": 5, "session_uuid": "s",
        }))
        assert typed is not None
        self.assertEqual((typed.limit, typed.count, typed.session_uuid), (5, 5, "s"))
        for broken in ({"limit": "5"}, {"count": None}, {"session_uuid": 7}, {"limit": True}):
            body = {"error": "full", "code": "session_record_limit",
                    "limit": 5, "count": 5, "session_uuid": "s"}
            body.update(broken)
            self.assertIsNone(session_record_limit_from_response(409, json.dumps(body)), broken)


class TestEnvelopeOnly(AioHTTPTestCase):
    """Routes whose bare-array fallback was removed now raise on a bare array."""

    async def get_application(self):
        app = web.Application()
        app["bodies"] = []

        async def bare_array(_request):
            return web.json_response([{"id": 1, "name": "x", "entity_type": "p"}])

        async def capture_record(request):
            request.app["bodies"].append(await request.json())
            return web.json_response({"id": 7, "status": "saved"})

        async def ingest_ok(request):
            body = await request.json()
            return web.json_response({"session_id": body["session_id"], "records": []})

        async def chat_null(_request):
            return web.json_response({"records": None, "count": 0})

        app.router.add_get("/api/v1/entities", bare_array)
        app.router.add_get("/api/v1/records/{id}/entities", bare_array)
        app.router.add_get("/api/v1/recent", bare_array)
        app.router.add_get("/api/v1/chats/{uuid}", chat_null)
        app.router.add_post("/api/v1/records", capture_record)
        app.router.add_post("/api/v1/ingest", ingest_ok)
        return app

    def _memory(self):
        return Memory(api_key="k", url=f"http://localhost:{self.server.port}", user_id="u1")

    @unittest_run_loop
    async def test_bare_arrays_raise(self):
        async with self._memory() as mem:
            for call in (mem.search_entities("x"), mem.get_record_entities(1), mem.recent()):
                with self.assertRaises(AnhurError) as caught:
                    await call
                self.assertEqual(caught.exception.kind, AnhurError.KIND_SERVER)

    @unittest_run_loop
    async def test_null_records_is_still_an_empty_list(self):
        async with self._memory() as mem:
            self.assertEqual(await mem.list_chat("s1"), [])

    @unittest_run_loop
    async def test_write_payloads_carry_session_id_never_uuid(self):
        async with self._memory() as mem:
            pinned = await mem.add("pinned", score=9, session_id="s-add")
            anchored = await mem.create_in_session("anchor", "s-anchor")
            created = await mem.create("s-create", "body")
            ingested = await mem.add("plain", session_id="s-ingest")
        sent = self.app["bodies"]
        self.assertEqual([body["session_id"] for body in sent], ["s-add", "s-anchor", "s-create"])
        for body in sent:
            self.assertNotIn("uuid", body)
        # mode names the door, with the 4.0.0 values.
        self.assertEqual({pinned.mode, anchored.mode, created.mode}, {"records"})
        self.assertEqual(ingested.mode, "ingest")
        self.assertEqual(ingested.records, [])  # nothing invented


if __name__ == "__main__":
    unittest.main()
