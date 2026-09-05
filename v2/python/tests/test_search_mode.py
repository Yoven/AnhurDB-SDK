"""
ADR-0031 search controls in the Python SDK (2026-09-05).

Three things are under test, and they fail for three different reasons:

  1. REQUEST — ``mode`` / ``semantic_timeout_ms`` / ``debug_signals`` reach the
     wire only when the caller set them, and an unknown value is refused HERE,
     before the round trip. The server normalises an unknown mode to
     ``balanced`` on purpose (so gRPC and REST can never disagree about a
     typo), which means the server can never report the typo back — the client
     is the last layer that still knows what the caller meant.

  2. CROSS-VERSION — the hazard recorded in ADR-0031's own amendment: proto3
     additive fields are compatible on the WIRE, not in the SEMANTICS. A server
     that predates the ADR drops ``mode`` into unknown fields, runs balanced,
     and answers 200 with lexical hits while the caller believes it asked for
     strict semantic retrieval. The honest detector is the RESPONSE: an
     ADR-0031 server always fills ``retrieval.mode``.

  3. RESPONSE — the 7 new per-hit signals (13 total) and the ``leg_scores``
     array must be READABLE, not silently dropped by ``extra="ignore"``.

Field names and shapes were read from AnhurDB/server/model/record.go and
AnhurDB/server/model/search_leg_scores.go during this change, not guessed.
No network: payload/contract tests use an in-process aiohttp mock server,
parsing tests call the pure parse functions directly.
"""

import os
import sys
import unittest
import warnings

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase

from anhurdb.client import Memory, _parse_search_response
from anhurdb.client.exceptions import AnhurError
from anhurdb.client.session_filter import sessions_all
from anhurdb.models import LegScoreSummary, SearchHitSignals


# ── Mock server ──────────────────────────────────────────────────────────
#
# ``retrieval_mode`` on the app decides which SERVER GENERATION is simulated:
# a string = an ADR-0031 server echoing the mode it resolved; None = a server
# that predates the ADR and attaches no retrieval block at all.

async def handle_search_capture(request):
    body = await request.json()
    request.app.setdefault("captured_payloads", []).append(body)
    envelope = {"results": []}
    served_mode = request.app.get("retrieval_mode")
    if served_mode is not None:
        envelope["retrieval"] = {"mode": served_mode, "signals_used": []}
    return web.json_response(envelope)


class SearchModeTestCase(AioHTTPTestCase):
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


class TestSearchModeRequestShape(SearchModeTestCase):
    """The three knobs are opt-in and omitted when unset."""

    async def test_defaults_omit_all_three_adr0031_knobs(self):
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            await memory_client.search("q", sessions_all())
        payload = self.last_payload()
        self.assertNotIn("mode", payload)
        self.assertNotIn("semantic_timeout_ms", payload)
        self.assertNotIn("debug_signals", payload)

    async def test_knobs_reach_the_wire_when_set(self):
        self.app["retrieval_mode"] = "fast"
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            await memory_client.search(
                "q",
                sessions_all(),
                mode="fast",
                semantic_timeout_ms=250,
                debug_signals=True,
            )
        payload = self.last_payload()
        self.assertEqual(payload["mode"], "fast")
        self.assertEqual(payload["semantic_timeout_ms"], 250)
        self.assertIs(payload["debug_signals"], True)

    async def test_zero_semantic_timeout_stays_off_the_wire(self):
        """0 means "use the server default", which is what omitting says."""
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            await memory_client.search("q", sessions_all(), semantic_timeout_ms=0)
        self.assertNotIn("semantic_timeout_ms", self.last_payload())

    async def test_unknown_mode_is_refused_before_the_round_trip(self):
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with self.assertRaises(AnhurError) as raised:
                await memory_client.search("q", sessions_all(), mode="sematic")
        # EXACT equality, not a substring: this message is a pinned cross-SDK
        # invariant (2026-09-05). Go's client/search_mode.go and TypeScript's
        # src/searchRequest.ts produce this byte-for-byte, and the Go test
        # compares it with exact equality too — same precedent as the
        # session_filter INVALID_PARAM text. A substring assertion would let
        # the three wordings drift apart again, which is how they diverged in
        # the first place.
        self.assertEqual(
            str(raised.exception),
            'INVALID_PARAM: \'mode\' "sematic" is not supported; '
            'use "fast", "balanced" or "semantic"',
        )
        # Nothing was sent: the typo never became a balanced search.
        self.assertEqual(self.app.get("captured_payloads", []), [])

    async def test_negative_semantic_timeout_is_refused(self):
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with self.assertRaises(AnhurError) as raised:
                await memory_client.search("q", sessions_all(), semantic_timeout_ms=-1)
        # Same pinned cross-SDK text as Go's search_mode.go and TypeScript's
        # searchMode.ts (2026-09-05).
        self.assertEqual(
            str(raised.exception),
            "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0",
        )
        self.assertEqual(self.app.get("captured_payloads", []), [])

    async def test_recall_forwards_the_knobs(self):
        """``recall`` used to take a FIXED keyword set — a TypeError in Python
        only, while the identical call worked in Go and TypeScript.

        Junior Tip [what this test would have caught]: ``recall`` delegates to
        ``search()``, so the knobs were implemented and reachable — just not
        through this door. A knob that raises TypeError in one SDK and works in
        the other two is the same defect class as a knob the server ignores:
        the caller's intent never reaches the wire."""
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            await memory_client.recall(
                "q", sessions_all(), 5, mode="fast", semantic_timeout_ms=250,
                debug_signals=True,
            )
        payload = self.last_payload()
        self.assertEqual(payload["mode"], "fast")
        self.assertEqual(payload["semantic_timeout_ms"], 250)
        self.assertEqual(payload["debug_signals"], True)
        self.assertEqual(payload["limit"], 5)

    async def test_search_session_forwards_the_knobs(self):
        """``search_session`` had the same fixed keyword set as ``recall``."""
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            await memory_client.search_session(
                "q", session_uuid="sess-1", mode="fast", debug_signals=True,
            )
        payload = self.last_payload()
        self.assertEqual(payload["mode"], "fast")
        self.assertEqual(payload["debug_signals"], True)
        self.assertEqual(payload["sessions"], ["sess-1"])


class TestSearchModeCrossVersionContract(SearchModeTestCase):
    """mode='semantic' is a promise: semantic retrieval, or an error."""

    async def test_old_server_ignoring_mode_semantic_fails_loud(self):
        """The pre-ADR-0031 server: 200, lexical results, no honoured mode."""
        self.app["retrieval_mode"] = None
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with self.assertRaises(AnhurError) as raised:
                await memory_client.search("q", sessions_all(), mode="semantic")
        self.assertIn("SERVER_TOO_OLD", str(raised.exception))
        self.assertFalse(raised.exception.retryable)

    async def test_server_answering_a_different_mode_fails_loud(self):
        self.app["retrieval_mode"] = "balanced"
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with self.assertRaises(AnhurError) as raised:
                await memory_client.search_with_retrieval(
                    "q", sessions_all(), mode="semantic"
                )
        self.assertIn("SERVER_TOO_OLD", str(raised.exception))
        self.assertIn("balanced", str(raised.exception))

    async def test_current_server_honouring_semantic_passes(self):
        self.app["retrieval_mode"] = "semantic"
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            hits = await memory_client.search("q", sessions_all(), mode="semantic")
        self.assertEqual(hits, [])

    async def test_shared_all_cannot_be_verified_so_it_warns_instead(self):
        """A CURRENT server also returns an empty retrieval.mode for
        shared_all (handler/record_search_shared_all.go builds a RetrievalMeta
        with no Mode), so raising here would reject a healthy server."""
        self.app["retrieval_mode"] = None
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                await memory_client.search(
                    "q", sessions_all(), scope="shared_all", mode="semantic"
                )
        self.assertTrue(any("shared_all" in str(entry.message) for entry in caught))

    async def test_old_server_only_warns_for_the_two_soft_knobs(self):
        self.app["retrieval_mode"] = None
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                await memory_client.search(
                    "q", sessions_all(), semantic_timeout_ms=50, debug_signals=True
                )
        messages = [str(entry.message) for entry in caught]
        self.assertTrue(any("semantic_timeout_ms" in message for message in messages))
        self.assertTrue(any("debug_signals" in message for message in messages))

    async def test_current_server_without_the_soft_knobs_is_silent(self):
        self.app["retrieval_mode"] = "balanced"
        async with Memory(api_key="k", url=self.base_url(), user_id="u1") as memory_client:
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                await memory_client.search("q", sessions_all())
        # Filter to this SDK's own category: aiohttp's test server emits its
        # own DeprecationWarning/NotAppKeyWarning noise, and asserting on the
        # raw list would make this test fail for someone else's reason.
        sdk_warnings = [
            str(entry.message)
            for entry in caught
            if issubclass(entry.category, RuntimeWarning)
        ]
        self.assertEqual(sdk_warnings, [])


class TestRicherResponseIsReadable(unittest.TestCase):
    """extra='ignore' drops unknown keys SILENTLY — so every new field has to
    be declared to be readable at all. These assert values, not just names."""

    def test_all_thirteen_hit_signals_are_readable(self):
        envelope = {
            "results": [
                {
                    "record": {"id": 1, "summary": "s"},
                    "similarity": 0.5,
                    "signals": {
                        "fts_rank": 1,
                        "semantic_rank": 2,
                        "simhash_rank": 3,
                        "simhash_hamming": 4,
                        "rrf_score": 0.5,
                        "semantic_cosine": 0.6,
                        "hnsw_rank": 7,
                        "bsq_rank": 8,
                        "parquet_rank": 9,
                        "fts5_rank": 10,
                        "astar_rank": 11,
                        "entity_jaccard_rank": 12,
                        "active_leg_weight_sum": 1.75,
                    },
                }
            ]
        }
        signals = _parse_search_response(envelope).results[0].signals
        self.assertIsInstance(signals, SearchHitSignals)
        assert signals is not None  # narrows the Optional for a type checker
        # A distinct sentinel per field: a conversor that assigns the wrong
        # field, or drops one, comes back as a zero and fails BY NAME.
        self.assertEqual(signals.hnsw_rank, 7)
        self.assertEqual(signals.bsq_rank, 8)
        self.assertEqual(signals.parquet_rank, 9)
        self.assertEqual(signals.fts5_rank, 10)
        self.assertEqual(signals.astar_rank, 11)
        self.assertEqual(signals.entity_jaccard_rank, 12)
        self.assertEqual(signals.active_leg_weight_sum, 1.75)
        # The original six must not have been disturbed by the widening.
        self.assertEqual(signals.fts_rank, 1)
        self.assertEqual(signals.semantic_cosine, 0.6)

    def test_leg_scores_are_parsed(self):
        envelope = {
            "results": [],
            "leg_scores": [
                {
                    "leg": "fts5",
                    "candidates": 42,
                    "top_scores": [0.9, 0.8],
                    "mean": 0.85,
                    "stddev": 0.05,
                }
            ],
        }
        leg_scores = _parse_search_response(envelope).leg_scores
        assert leg_scores is not None
        self.assertEqual(len(leg_scores), 1)
        self.assertIsInstance(leg_scores[0], LegScoreSummary)
        self.assertEqual(leg_scores[0].leg, "fts5")
        self.assertEqual(leg_scores[0].candidates, 42)
        self.assertEqual(leg_scores[0].top_scores, [0.9, 0.8])
        self.assertEqual(leg_scores[0].stddev, 0.05)

    def test_absent_and_empty_leg_scores_are_told_apart(self):
        """None = the server never told us; [] = it told us there were none."""
        self.assertIsNone(_parse_search_response({"results": []}).leg_scores)
        self.assertEqual(
            _parse_search_response({"results": [], "leg_scores": []}).leg_scores, []
        )


if __name__ == "__main__":
    unittest.main()
