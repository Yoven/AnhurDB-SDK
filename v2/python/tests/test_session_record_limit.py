"""The session-record-limit refusal (ADR-0033) arrives TYPED through the real
transport — a genuine ``AnhurClient`` call against a stub aiohttp server — for
the new structured body, the old JSON text-only body, the old plain-text body,
and the multipart path. A generic 409 must stay a plain ``AnhurQueryError``.

gRPC is not covered because this SDK has no gRPC transport.
"""

import json
import os
import sys

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb import AnhurSessionRecordLimitError  # noqa: E402  public export
from anhurdb.client import AnhurClient  # noqa: E402
from anhurdb.client.exceptions import AnhurError, AnhurQueryError  # noqa: E402

NEW_BODY = json.dumps(
    {
        "error": "session has reached the maximum of 500 records (refused)",
        "code": "session_record_limit",
        "limit": 500,
        "count": 503,
        "session_uuid": "s-full-1",
    }
)
OLD_JSON_BODY = json.dumps({"error": "session has reached the maximum of 500 records"})
OLD_PLAIN_BODY = "session has reached the maximum of 1000 records"
OTHER_CONFLICT_BODY = json.dumps({"error": "record already superseded"})


class TestSessionRecordLimit(AioHTTPTestCase):
    async def get_application(self):
        app = web.Application()

        def responder(body, content_type="application/json"):
            async def handle(_request):
                return web.Response(status=409, text=body, content_type=content_type)

            return handle

        app.router.add_post("/new/api/v1/records", responder(NEW_BODY))
        app.router.add_post("/oldjson/api/v1/records", responder(OLD_JSON_BODY))
        app.router.add_post("/oldplain/api/v1/records", responder(OLD_PLAIN_BODY, "text/plain"))
        app.router.add_post("/other/api/v1/records", responder(OTHER_CONFLICT_BODY))
        app.router.add_post("/new/api/v1/upload", responder(NEW_BODY))
        return app

    async def _create_error(self, prefix):
        url = f"http://localhost:{self.server.port}/{prefix}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            try:
                await client.create("s1", "turn text")
            except AnhurError as exc:
                return exc
        self.fail("expected the 409 to raise")

    def _assert_non_retryable_conflict(self, exc):
        self.assertIsInstance(exc, AnhurQueryError)  # backward compatible
        self.assertEqual(exc.status_code, 409)
        self.assertEqual(exc.kind, AnhurError.KIND_CONFLICT)
        self.assertFalse(exc.retryable)

    @unittest_run_loop
    async def test_new_structured_body(self):
        exc = await self._create_error("new")
        self.assertIsInstance(exc, AnhurSessionRecordLimitError)
        self._assert_non_retryable_conflict(exc)
        self.assertEqual(exc.session_uuid, "s-full-1")
        self.assertEqual(exc.limit, 500)
        self.assertEqual(exc.count, 503)
        self.assertEqual(exc.code, "session_record_limit")
        self.assertIn("session=s-full-1, limit=500, count=503", str(exc))

    @unittest_run_loop
    async def test_old_text_only_json_body(self):
        exc = await self._create_error("oldjson")
        self.assertIsInstance(exc, AnhurSessionRecordLimitError)
        self._assert_non_retryable_conflict(exc)
        self.assertIsNone(exc.session_uuid)
        self.assertEqual(exc.limit, 500)  # read back from the server's sentence
        self.assertIsNone(exc.count)
        self.assertIn("session=unknown, limit=500, count=unknown", str(exc))

    @unittest_run_loop
    async def test_old_plain_text_body(self):
        exc = await self._create_error("oldplain")
        self.assertIsInstance(exc, AnhurSessionRecordLimitError)
        self.assertEqual(exc.limit, 1000)
        self.assertIsNone(exc.count)

    @unittest_run_loop
    async def test_other_conflict_stays_generic(self):
        exc = await self._create_error("other")
        self.assertNotIsInstance(exc, AnhurSessionRecordLimitError)
        self._assert_non_retryable_conflict(exc)

    @unittest_run_loop
    async def test_multipart_path_is_typed(self):
        url = f"http://localhost:{self.server.port}/new"
        async with AnhurClient(url=url, api_key="test-key") as client:
            with self.assertRaises(AnhurSessionRecordLimitError) as ctx:
                await client.upload_file("note.txt", b"hello")
        self.assertEqual(ctx.exception.count, 503)
        self.assertFalse(ctx.exception.retryable)
