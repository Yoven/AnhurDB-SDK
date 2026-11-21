"""The 4.0.0 envelope rule (``anhurdb/client/envelope.py``) on all eight
listing routes, through a real aiohttp stub server.

Cross-SDK twins: Go ``client/envelope_only_test.go``, TypeScript
``src/test/envelope_rule.test.ts``. A bare array, an empty body, a missing key
or a non-list value raises ``AnhurUnexpectedResponseError`` (kind ``server``)
with the byte-identical message; a present key whose value is null is empty.
"""

import json
import os
import sys
import unittest

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb import AnhurUnexpectedResponseError, Memory  # noqa: E402
from anhurdb.client.session_limit import session_record_limit_from_response  # noqa: E402

BAD_BODIES = {
    "bare array": '[{"id": 7, "name": "n"}]',
    "empty body": "",
    "json null": "null",
    "missing key": '{"count": 0}',
    "non-list key": '{"records": {}, "sessions": {}, "entities": {}}',
}
NULL_BODY = '{"records": null, "sessions": null, "entities": null, "count": 0, "has_more": false}'

# (route template, key, call) — the same eight routes Go and TypeScript cover.
ROUTES = (
    ("/api/v1/sessions/stats", "sessions", lambda memory: memory.list_sessions()),
    ("/api/v1/recent", "records", lambda memory: memory.recent(10)),
    ("/api/v1/manifest", "records", lambda memory: memory.manifest_global()),
    ("/api/v1/chats/{uuid}/manifest", "records", lambda memory: memory.manifest_session("s1")),
    ("/api/v1/chats/{uuid}", "records", lambda memory: memory.list_chat("s1")),
    ("/api/v1/entities", "entities", lambda memory: memory.search_entities("q")),
    ("/api/v1/entities/list", "entities", lambda memory: memory.list_entities()),
    ("/api/v1/records/{id}/entities", "entities", lambda memory: memory.get_record_entities(7)),
)


class TestEnvelopeRule(AioHTTPTestCase):
    async def get_application(self):
        app = web.Application()
        self.current_body = ""

        async def fixed_body(_request):
            return web.Response(text=self.current_body, content_type="application/json")

        for path in (
            "/api/v1/sessions/stats",
            "/api/v1/recent",
            "/api/v1/manifest",
            "/api/v1/chats/{uuid}/manifest",
            "/api/v1/chats/{uuid}",
            "/api/v1/entities",
            "/api/v1/entities/list",
            "/api/v1/records/{id}/entities",
        ):
            app.router.add_get(path, fixed_body)
        return app

    def _memory(self) -> Memory:
        return Memory(api_key="k", url=f"http://localhost:{self.server.port}", user_id="u1")

    async def test_non_envelope_raises_on_every_route(self):
        async with self._memory() as memory:
            await self._assert_every_route_raises(memory)

    async def _assert_every_route_raises(self, memory: Memory) -> None:
        for route, key, call in ROUTES:
            for body_name, body in BAD_BODIES.items():
                with self.subTest(route=route, body=body_name):
                    self.current_body = body
                    with self.assertRaises(AnhurUnexpectedResponseError) as caught:
                        await call(memory)
                    self.assertEqual(
                        str(caught.exception),
                        f'unexpected response from GET {route}: expected a JSON object with "{key}"',
                    )
                    self.assertEqual(caught.exception.kind, "server")
                    self.assertIsNone(caught.exception.status_code)

    async def test_null_list_is_empty_on_every_route(self):
        self.current_body = NULL_BODY
        async with self._memory() as memory:
            for route, _key, call in ROUTES:
                with self.subTest(route=route):
                    await call(memory)


class TestSessionLimitCrossSDKMessage(unittest.TestCase):
    """Byte-identical to Go and TypeScript, including the 500-char excerpt."""

    def test_message_layout(self):
        sentence = "é" * 600
        body = json.dumps({"error": sentence, "code": "session_record_limit",
                           "limit": 3, "count": 4, "session_uuid": "s9"})
        refusal = session_record_limit_from_response(409, body)
        assert refusal is not None
        self.assertEqual(
            str(refusal),
            "Conflict (HTTP 409): " + "é" * 500
            + " (session record limit: session=s9, limit=3, count=4)",
        )
        self.assertEqual(refusal.server_message, sentence)

    def test_malformed_fields_are_not_typed(self):
        base = {"error": "x", "code": "session_record_limit", "limit": 5, "count": 5, "session_uuid": "s"}
        variants = {
            "limit missing": {k: v for k, v in base.items() if k != "limit"},
            "count missing": {k: v for k, v in base.items() if k != "count"},
            "session_uuid missing": {k: v for k, v in base.items() if k != "session_uuid"},
            "limit null": {**base, "limit": None},
            "limit as string": {**base, "limit": "5"},
            "count fractional": {**base, "count": 5.5},
            "session_uuid number": {**base, "session_uuid": 7},
        }
        for name, body in variants.items():
            with self.subTest(name):
                self.assertIsNone(session_record_limit_from_response(409, json.dumps(body)))


if __name__ == "__main__":
    unittest.main()
