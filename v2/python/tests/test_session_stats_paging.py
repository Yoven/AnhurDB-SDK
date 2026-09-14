"""``list_sessions()`` must return the WHOLE tenant, not the server's first page.

WHY THIS FILE EXISTS, and why every case serves at least TWO pages: the defect
it pins shipped precisely because the only coverage was single-page.
``GET /api/v1/sessions/stats`` defaults to ``limit=50`` and announces the
truncation in ``has_more``/``next_offset``; Python sent no params and read only
``data.get("sessions", data)``, so a tenant with 99 sessions answered 50 and
looked complete. A one-page test AGREES with that bug. Only a test whose server
insists there is more can tell a paging client from a truncating one.

Each case asserts the UNION across pages, and additionally asserts the exact
query string the SDK put on the wire — "returned 3 rows" is also what a client
that guessed ``limit=1000`` in one shot would report, and the three SDKs are
meant to page identically.
"""

from __future__ import annotations

import os
import sys
import unittest
from typing import Any, Dict, List

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb.client import Memory  # noqa: E402
from anhurdb.client.exceptions import AnhurError  # noqa: E402
from anhurdb.client.session_stats import (  # noqa: E402
    SESSION_STATS_MAX_PAGES,
    SESSION_STATS_PAGE_LIMIT,
)


def envelope_page(uuids: List[str], has_more: bool, next_offset: int, offset: int) -> Dict[str, Any]:
    """An envelope page shaped exactly like the live server's."""
    return {
        "count": len(uuids),
        "limit": SESSION_STATS_PAGE_LIMIT,
        "offset": offset,
        "has_more": has_more,
        "next_offset": next_offset,
        "sessions": [
            {"uuid": uuid, "record_count": 1, "last_activity": "2026-09-14T00:00:00Z"}
            for uuid in uuids
        ],
    }


class SessionStatsPagingTest(AioHTTPTestCase):
    """Drive a real ``Memory`` against a real aiohttp server that PAGES."""

    # Set by each test before the request runs; the handler reads it.
    reply_mode = "two_pages"

    async def get_application(self) -> web.Application:
        self.recorded_requests: List[Dict[str, Any]] = []
        application = web.Application()
        application.router.add_get("/api/v1/sessions/stats", self._handle_sessions_stats)
        return application

    async def _handle_sessions_stats(self, request: web.Request) -> web.Response:
        limit_param = request.query.get("limit")
        offset_param = request.query.get("offset")
        self.recorded_requests.append({"limit": limit_param, "offset": offset_param})

        if self.reply_mode == "bare_array":
            return web.json_response([{"uuid": "legacy", "record_count": 3}])
        if self.reply_mode == "stuck_next_offset":
            # The trap: has_more stays true and next_offset never moves.
            return web.json_response(envelope_page(["stuck"], True, 0, 0))
        if self.reply_mode == "empty_tenant":
            return web.json_response(envelope_page([], False, 0, 0))
        if self.reply_mode == "last_page_nonzero_next_offset":
            return web.json_response(envelope_page(["only"], False, 9999, 0))

        # Default: two pages.
        if offset_param in (None, "0"):
            return web.json_response(
                envelope_page(["page0-a", "page0-b"], True, SESSION_STATS_PAGE_LIMIT, 0)
            )
        return web.json_response(
            envelope_page(["page1-a"], False, 0, SESSION_STATS_PAGE_LIMIT)
        )

    def _memory(self) -> Memory:
        """A Memory pointed at the fake server; use as ``async with``.

        Junior Tip [why the context manager is not optional]: HTTPConnection
        refuses to send before ``connect()`` opens the aiohttp session, so a
        bare ``Memory(...)`` raises AnhurConnectionError before any paging
        logic runs — a green-looking failure that proves nothing about pages.
        """
        return Memory(api_key="test-key", url=str(self.server.make_url("")).rstrip("/"))

    async def test_returns_union_of_two_pages_not_the_first_page(self) -> None:
        self.reply_mode = "two_pages"
        async with self._memory() as memory:
            sessions = await memory.list_sessions()

        self.assertEqual(
            [session.uuid for session in sessions],
            ["page0-a", "page0-b", "page1-a"],
            "list_sessions must concatenate every page the server offers",
        )
        self.assertEqual(len(self.recorded_requests), 2, "the second page must be fetched")
        self.assertEqual(
            [request["offset"] for request in self.recorded_requests],
            ["0", str(SESSION_STATS_PAGE_LIMIT)],
            "the SDK must follow next_offset",
        )

    async def test_every_page_asks_for_the_go_page_size(self) -> None:
        self.reply_mode = "two_pages"
        async with self._memory() as memory:
            await memory.list_sessions()

        self.assertEqual(SESSION_STATS_PAGE_LIMIT, 500, "Go's listSessionsPageLimit is 500")
        for request in self.recorded_requests:
            self.assertEqual(request["limit"], "500", "every page must use the Go page size")

    async def test_has_more_false_ends_the_loop_even_with_next_offset_set(self) -> None:
        self.reply_mode = "last_page_nonzero_next_offset"
        async with self._memory() as memory:
            sessions = await memory.list_sessions()

        self.assertEqual([session.uuid for session in sessions], ["only"])
        self.assertEqual(len(self.recorded_requests), 1, "has_more is the authority")

    async def test_stuck_next_offset_terminates_loudly_instead_of_spinning(self) -> None:
        self.reply_mode = "stuck_next_offset"

        with self.assertRaises(AnhurError) as raised:
            async with self._memory() as memory:
                await memory.list_sessions()

        self.assertEqual(raised.exception.kind, AnhurError.KIND_SERVER)
        self.assertEqual(
            len(self.recorded_requests),
            SESSION_STATS_MAX_PAGES,
            "the ceiling, not the server, must end the loop",
        )
        offsets_seen = [int(request["offset"]) for request in self.recorded_requests]
        self.assertEqual(
            offsets_seen,
            sorted(set(offsets_seen)),
            "offset must strictly increase even when next_offset is stuck",
        )

    async def test_legacy_bare_array_is_one_complete_page(self) -> None:
        self.reply_mode = "bare_array"
        async with self._memory() as memory:
            sessions = await memory.list_sessions()

        self.assertEqual([session.uuid for session in sessions], ["legacy"])
        self.assertEqual(
            len(self.recorded_requests), 1, "a bare array carries no has_more: it is the answer"
        )

    async def test_empty_tenant_returns_empty_list(self) -> None:
        self.reply_mode = "empty_tenant"
        async with self._memory() as memory:
            self.assertEqual(await memory.list_sessions(), [])


if __name__ == "__main__":
    unittest.main()
