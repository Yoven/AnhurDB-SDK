"""The session-record-limit refusal (ADR-0033) arrives TYPED through the real
transport — a genuine ``Memory`` call against a stub aiohttp server — for the
structured body on the JSON and multipart paths. A generic 409 must stay a
plain ``AnhurQueryError``.

4.0.0 removal proofs: the pre-ADR-0033 text-only bodies (JSON or plain text,
no ``code``) and a body carrying the code but missing a required field are the
GENERIC 409 now — the sentence prefix match and the "read N back from the
sentence" regex are gone, and ``limit``/``count``/``session_uuid`` are never
invented.

gRPC is not covered because this SDK has no gRPC transport.
"""

import json
import os
import sys

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb import AnhurSessionRecordLimitError  # noqa: E402  public export
from anhurdb.client import Memory  # noqa: E402
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
CODE_MISSING_COUNT_BODY = json.dumps(
    {
        "error": "session has reached the maximum of 500 records",
        "code": "session_record_limit",
        "limit": 500,
        "session_uuid": "s-full-1",
    }
)


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
        app.router.add_post("/nocount/api/v1/records", responder(CODE_MISSING_COUNT_BODY))
        app.router.add_post("/new/api/v1/upload", responder(NEW_BODY))
        return app

    async def _create_error(self, prefix):
        url = f"http://localhost:{self.server.port}/{prefix}"
        async with Memory(url=url, api_key="test-key") as client:
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
    async def test_exact_message_layout(self):
        """Byte-identical to the TypeScript SDK's layout."""
        exc = await self._create_error("new")
        self.assertEqual(
            str(exc),
            "Conflict (HTTP 409): session has reached the maximum of 500 records "
            "(refused) (session record limit: session=s-full-1, limit=500, count=503)",
        )

    @unittest_run_loop
    async def test_old_text_only_json_body_is_generic_409(self):
        exc = await self._create_error("oldjson")
        self.assertNotIsInstance(exc, AnhurSessionRecordLimitError)
        self._assert_non_retryable_conflict(exc)
        self.assertNotIn("session record limit", str(exc))

    @unittest_run_loop
    async def test_old_plain_text_body_is_generic_409(self):
        exc = await self._create_error("oldplain")
        self.assertNotIsInstance(exc, AnhurSessionRecordLimitError)
        self._assert_non_retryable_conflict(exc)

    @unittest_run_loop
    async def test_code_with_missing_field_is_generic_409(self):
        """A count the server did not send is never invented as 0."""
        exc = await self._create_error("nocount")
        self.assertNotIsInstance(exc, AnhurSessionRecordLimitError)
        self._assert_non_retryable_conflict(exc)

    @unittest_run_loop
    async def test_other_conflict_stays_generic(self):
        exc = await self._create_error("other")
        self.assertNotIsInstance(exc, AnhurSessionRecordLimitError)
        self._assert_non_retryable_conflict(exc)

    @unittest_run_loop
    async def test_multipart_path_is_typed(self):
        url = f"http://localhost:{self.server.port}/new"
        async with Memory(url=url, api_key="test-key") as client:
            with self.assertRaises(AnhurSessionRecordLimitError) as ctx:
                await client.upload_file("note.txt", b"hello")
        self.assertEqual(ctx.exception.count, 503)
        self.assertFalse(ctx.exception.retryable)
