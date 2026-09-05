"""The cross-SDK divergences closed on 2026-09-05.

Domain: proof that this SDK behaves like the Go and TypeScript SDKs on the
ADR-0031 knobs. Each class below corresponds to one divergence an independent
probe measured by capturing the ACTUAL request body from every entry point in
every SDK.

Junior Tip [why every string here is compared with EXACT equality]: the previous
round pinned the ``INVALID_PARAM`` strings and asserted the response-side ones by
substring. ``"SERVER_TOO_OLD" in message`` is true for any wording at all, which
is exactly how the three SDKs ended up with three different sentences for the
same fact. A cross-SDK contract has to be compared the way a contract is
compared.

No network: an in-process aiohttp mock server captures the body and decides which
SERVER GENERATION is simulated (``retrieval_mode`` on the app: a string = an
ADR-0031 server echoing the mode it resolved; ``None`` = a server that predates
the ADR and attaches no retrieval block at all).
"""

import os
import sys
import warnings

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase

from anhurdb.client import Memory
from anhurdb.client.exceptions import AnhurError
from anhurdb.client.session_filter import sessions_all


async def handle_search_capture(request):
    body = await request.json()
    request.app.setdefault("captured_payloads", []).append(body)
    envelope = {"results": []}
    served_mode = request.app.get("retrieval_mode")
    if served_mode is not None:
        envelope["retrieval"] = {"mode": served_mode, "signals_used": []}
    return web.json_response(envelope)


class SearchParityTestCase(AioHTTPTestCase):
    """Shared mock-server wiring for every case below."""

    async def get_application(self):
        application = web.Application()
        application["retrieval_mode"] = "balanced"
        application.router.add_post("/api/v1/search", handle_search_capture)
        return application

    def base_url(self):
        return str(self.server.make_url("")).rstrip("/")

    def last_payload(self):
        return self.app["captured_payloads"][-1]

    def sdk_warnings(self, caught):
        """Only this SDK's own warnings.

        aiohttp's test server emits its own DeprecationWarning / NotAppKeyWarning
        noise, and asserting on the raw list would make these tests fail for
        someone else's reason.
        """
        return [
            str(entry.message)
            for entry in caught
            if issubclass(entry.category, RuntimeWarning)
        ]


class TestModeIsNormalisedBeforeValidation(SearchParityTestCase):
    """L4: case and whitespace were accepted here and refused in Go/TypeScript.

    The divergence is resolved toward LENIENCY in all three, because accepting
    more can break no caller, while tightening this SDK would break every caller
    already passing ``mode="Semantic"``.
    """

    async def test_uppercase_whitespace_and_title_case_all_reach_the_wire_lowercased(self):
        for requested_mode, expected_wire in (
            ("SEMANTIC", "semantic"),
            (" semantic ", "semantic"),
            ("Balanced", "balanced"),
        ):
            with self.subTest(requested_mode=requested_mode):
                # The server echoes the NORMALISED mode, which is what a real
                # server does — this also proves the read-back comparison uses
                # the normalised form and does not raise SERVER_TOO_OLD at a
                # perfectly healthy server.
                self.app["retrieval_mode"] = expected_wire
                async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
                    await memory_client.search("q", sessions_all(), mode=requested_mode)
                self.assertEqual(self.last_payload()["mode"], expected_wire)

    async def test_a_typo_still_echoes_what_the_caller_typed(self):
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with self.assertRaises(AnhurError) as raised:
                await memory_client.search("q", sessions_all(), mode=" SEMANITC ")
        self.assertEqual(
            str(raised.exception),
            'INVALID_PARAM: \'mode\' " SEMANITC " is not supported; '
            'use "fast", "balanced" or "semantic"',
        )


class TestPinnedCrossSDKStrings(SearchParityTestCase):
    """L2: SERVER_TOO_OLD and the shared_all warning are one contract, not three."""

    async def test_server_too_old_message_is_byte_identical_to_go_and_typescript(self):
        self.app["retrieval_mode"] = None
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with self.assertRaises(AnhurError) as raised:
                await memory_client.search("q", sessions_all(), mode="semantic")
        self.assertEqual(
            str(raised.exception),
            'SERVER_TOO_OLD: requested mode="semantic" but the server answered '
            'retrieval.mode="" — this server predates ADR-0031 and IGNORED the mode field, '
            "so these results came from the balanced budget and may be purely lexical. "
            'Upgrade the server, or drop to mode="balanced" and read retrieval.degraded '
            "yourself.",
        )

    async def test_shared_all_warning_is_byte_identical_to_go_and_typescript(self):
        self.app["retrieval_mode"] = None
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                await memory_client.search(
                    "q", sessions_all(), scope="shared_all", mode="semantic"
                )
        self.assertEqual(
            self.sdk_warnings(caught),
            [
                'anhurdb-sdk: warning: mode="semantic" cannot be CONFIRMED on '
                'scope="shared_all" — the server never echoes retrieval.mode for a two-leg '
                "merge, so a server too old for ADR-0031 looks identical to a current one "
                "here. Use a single scope to get the strict-semantic guarantee verified.",
            ],
        )


class TestModeIgnoredWarning(SearchParityTestCase):
    """L3: this SDK stayed SILENT when an old server ignored mode=fast/balanced.

    The soft-warning branch was gated on ``semantic_timeout_ms > 0 or
    debug_signals`` and did not mention ``requested_mode`` at all, so the same
    call against the same server told a Go or TypeScript operator that the mode
    had been dropped and told a Python operator nothing.
    """

    async def test_an_ignored_fast_mode_now_warns_with_the_shared_wording(self):
        self.app["retrieval_mode"] = None
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                await memory_client.search("q", sessions_all(), mode="fast")
        self.assertEqual(
            self.sdk_warnings(caught),
            [
                'anhurdb-sdk: warning: this AnhurDB server ignored mode="fast" (it predates '
                "ADR-0031) and ran its own balanced pipeline; the results are balanced results.",
            ],
        )

    async def test_the_two_soft_knobs_warn_one_line_each(self):
        self.app["retrieval_mode"] = None
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                await memory_client.search(
                    "q", sessions_all(), semantic_timeout_ms=900, debug_signals=True
                )
        self.assertEqual(
            self.sdk_warnings(caught),
            [
                "anhurdb-sdk: warning: this AnhurDB server ignored semantic_timeout_ms=900 "
                "(it predates ADR-0031); the server's own semantic budget (700ms) was used "
                "instead.",
                "anhurdb-sdk: warning: this AnhurDB server ignored debug_signals (it "
                "predates ADR-0031); per-hit signals and leg_scores are absent, not empty.",
            ],
        )

    async def test_a_current_server_says_nothing(self):
        self.app["retrieval_mode"] = "fast"
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                await memory_client.search(
                    "q", sessions_all(), mode="fast", debug_signals=True
                )
        self.assertEqual(self.sdk_warnings(caught), [])


class TestSearchSessionIsTheSameRequestAsSearch(SearchParityTestCase):
    """L1 (parity direction): the Go SDK's SearchSession was outside this path.

    These pin the behaviour Go had to be brought up to, so a future change that
    re-forks the session-scoped read fails here as well as there.
    """

    async def test_search_session_forwards_all_three_knobs(self):
        self.app["retrieval_mode"] = "semantic"
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            await memory_client.search_session(
                "q",
                session_uuid="sess-1",
                mode="semantic",
                semantic_timeout_ms=1500,
                debug_signals=True,
            )
        payload = self.last_payload()
        self.assertEqual(payload["mode"], "semantic")
        self.assertEqual(payload["semantic_timeout_ms"], 1500)
        self.assertIs(payload["debug_signals"], True)
        self.assertEqual(payload["scope"], "sessions")

    async def test_search_session_refuses_a_negative_budget_before_the_round_trip(self):
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with self.assertRaises(AnhurError) as raised:
                await memory_client.search_session(
                    "q", session_uuid="sess-1", semantic_timeout_ms=-1
                )
        self.assertEqual(
            str(raised.exception),
            "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0",
        )
        self.assertEqual(self.app.get("captured_payloads", []), [])

    async def test_a_caller_supplied_scope_is_overridden_not_a_crash(self):
        """The scope is pinned, and pinning it may not throw a bare TypeError.

        Go pins by appending ``WithScope`` last and TypeScript pins by writing
        ``scope`` after the spread; both silently override. Python collided its
        ``**kwargs`` with the pinned keyword and raised
        ``TypeError: search() got multiple values for keyword argument 'scope'``
        — an exception with no ``.kind`` and no ``.retryable``, so it fell
        outside the ``AnhurError`` contract callers handle.
        """
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            await memory_client.search_session(
                "q", session_uuid="sess-1", scope="shared_all"
            )
        self.assertEqual(self.last_payload()["scope"], "sessions")

    async def test_a_knobless_caller_still_sends_the_2_0_x_body(self):
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            await memory_client.search_session("q", session_uuid="sess-1")
        self.assertEqual(
            self.last_payload(),
            {"text": "q", "limit": 10, "scope": "sessions", "sessions": ["sess-1"]},
        )
