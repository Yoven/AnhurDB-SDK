"""Session filter (ADR-0014) tests for the AnhurDB Python SDK.

Two layers:
  1. ``normalize_sessions`` — the pure contract (wildcard, cap, rejections),
     table-driven, no I/O.
  2. The search family over a mock HTTP server — proves the resolved filter
     reaches the wire (JSON body for POST, repeated query key for GET) and that
     a rejected filter never produces a request at all.

Stdlib ``unittest`` only for layer 1; layer 2 uses the same aiohttp test server
already used by ``test_http_mock.py``.
"""

import os
import sys
import unittest

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb.client import Memory
from anhurdb.client.exceptions import AnhurError
from anhurdb.client.session_filter import (
    MAX_SESSION_FILTER_UUIDS,
    SESSION_WILDCARD,
    normalize_sessions,
    sessions_all,
)


class TestNormalizeSessionsContract(unittest.TestCase):
    """Pins ADR-0014 D1/D2 on the client side."""

    def test_accepted_filters(self):
        at_the_cap = [f"session-{index}" for index in range(MAX_SESSION_FILTER_UUIDS)]
        accepted_cases = [
            ("one session", ["session-a"], ["session-a"]),
            (
                "many sessions",
                ["session-a", "session-b", "session-c"],
                ["session-a", "session-b", "session-c"],
            ),
            ("wildcard", [SESSION_WILDCARD], [SESSION_WILDCARD]),
            ("wildcard helper", sessions_all(), ["*"]),
            ("surrounding whitespace is trimmed", ["  session-a  "], ["session-a"]),
            (
                "duplicates collapse",
                ["session-a", "session-a", "session-b"],
                ["session-a", "session-b"],
            ),
            ("exactly at the cap is allowed", at_the_cap, at_the_cap),
            ("a tuple is a sequence too", ("session-a",), ["session-a"]),
        ]

        for case_name, raw_sessions, expected_wire in accepted_cases:
            with self.subTest(case=case_name):
                self.assertEqual(normalize_sessions(raw_sessions), expected_wire)

    def test_rejected_filters(self):
        over_the_cap = [
            f"session-{index}" for index in range(MAX_SESSION_FILTER_UUIDS + 1)
        ]
        rejected_cases = [
            (
                "absent",
                None,
                "INVALID_PARAM: 'sessions' is required; "
                'use ["*"] for every session in scope',
            ),
            (
                "empty list",
                [],
                "INVALID_PARAM: 'sessions' cannot be empty; "
                'use ["*"] for every session in scope',
            ),
            (
                "empty entry",
                ["session-a", "   "],
                "INVALID_PARAM: 'sessions' contains an empty entry",
            ),
            (
                "wildcard mixed with an explicit session",
                [SESSION_WILDCARD, "session-a"],
                'INVALID_PARAM: \'sessions\' mixes "*" with 1 explicit session(s); '
                "the wildcard must stand alone",
            ),
            (
                "above the cap",
                over_the_cap,
                f"INVALID_PARAM: at most {MAX_SESSION_FILTER_UUIDS} sessions per "
                f'request (got {MAX_SESSION_FILTER_UUIDS + 1}); use ["*"] for all',
            ),
            (
                # A bare string is iterable in Python — accepting it would turn
                # "conv-a" into six single-character session ids.
                "a bare string is not a list",
                "session-a",
                "INVALID_PARAM: 'sessions' must be a list of strings",
            ),
            (
                "a non-string entry",
                [42],
                "INVALID_PARAM: 'sessions' must be a list of strings",
            ),
        ]

        for case_name, raw_sessions, expected_message in rejected_cases:
            with self.subTest(case=case_name):
                with self.assertRaises(AnhurError) as raised:
                    normalize_sessions(raw_sessions)
                self.assertEqual(str(raised.exception), expected_message)


class TestSessionFilterOnTheWire(AioHTTPTestCase):
    """Proves the filter reaches the server on every search shape."""

    async def get_application(self):
        app = web.Application()
        app["request_count"] = 0

        async def capture_search(request):
            app["request_count"] += 1
            app["captured_body"] = await request.json()
            return web.json_response({"results": []})

        async def capture_smart(request):
            app["request_count"] += 1
            app["captured_sessions"] = request.query.getall("sessions", [])
            return web.json_response({"results": [], "count": 0})

        async def capture_by_type(request):
            app["request_count"] += 1
            app["captured_sessions"] = request.query.getall("sessions", [])
            return web.json_response({"records": [], "count": 0})

        app.router.add_post("/api/v1/search", capture_search)
        app.router.add_get("/api/v1/search/smart", capture_smart)
        app.router.add_get("/api/v1/search/type", capture_by_type)
        return app

    @unittest_run_loop
    async def test_search_sends_sessions_and_drops_legacy_uuid(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="key", url=url, user_id="u1") as mem:
            await mem.search("hello", ["session-a", "session-b"])
        body = self.app["captured_body"]
        self.assertEqual(body["sessions"], ["session-a", "session-b"])
        self.assertNotIn("uuid", body)

    @unittest_run_loop
    async def test_search_session_sends_singleton_filter(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="key", url=url, user_id="u1") as mem:
            await mem.search_session("hello", session_uuid="conv-42")
        body = self.app["captured_body"]
        self.assertEqual(body["sessions"], ["conv-42"])
        self.assertNotIn("uuid", body)

    @unittest_run_loop
    async def test_wildcard_reaches_the_wire_verbatim(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="key", url=url, user_id="u1") as mem:
            await mem.search("hello", sessions_all())
        self.assertEqual(self.app["captured_body"]["sessions"], ["*"])

    @unittest_run_loop
    async def test_get_searches_repeat_the_sessions_query_key(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="key", url=url, user_id="u1") as mem:
            await mem.smart_search("engineering", ["conv-a", "conv-b"])
            self.assertEqual(self.app["captured_sessions"], ["conv-a", "conv-b"])

            await mem.search_by_type("fact", sessions_all())
            self.assertEqual(self.app["captured_sessions"], ["*"])

    @unittest_run_loop
    async def test_rejected_filters_never_reach_the_network(self):
        url = f"http://localhost:{self.server.port}"
        bad_filters = {
            "absent": None,
            "empty": [],
            "mixed": [SESSION_WILDCARD, "conv-a"],
            "empty entry": [""],
        }

        async with Memory(api_key="key", url=url, user_id="u1") as mem:
            callers = {
                "search": lambda bad: mem.search("q", bad),
                "search_sessions": lambda bad: mem.search_sessions("q", bad),
                "search_tenant_shared": lambda bad: mem.search_tenant_shared("q", bad),
                "search_client_shared": lambda bad: mem.search_client_shared("q", bad),
                "search_shared": lambda bad: mem.search_shared("q", bad),
                "recall": lambda bad: mem.recall("q", bad, 5),
                "smart_search": lambda bad: mem.smart_search("q", bad, limit=5),
                "search_by_type": lambda bad: mem.search_by_type("fact", bad, 5),
            }
            for filter_name, bad_filter in bad_filters.items():
                for method_name, call_method in callers.items():
                    with self.subTest(method=method_name, filter=filter_name):
                        with self.assertRaises(AnhurError) as raised:
                            await call_method(bad_filter)
                        self.assertTrue(
                            str(raised.exception).startswith("INVALID_PARAM: "),
                            f"{method_name}/{filter_name}: {raised.exception}",
                        )

        self.assertEqual(
            self.app["request_count"],
            0,
            "a rejected session filter still produced an HTTP request",
        )


if __name__ == "__main__":
    unittest.main()
