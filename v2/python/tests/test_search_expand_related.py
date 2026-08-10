"""
Tests for the ADR-0021 search surface additions (Python SDK, 2026-08-10).

Covers two things:
  1. Request payload construction — expand_related / astar_weight /
     entity_jaccard_weight must only appear on the wire when the caller
     actually set them, and astar_weight=0.0 / entity_jaccard_weight=0.0
     (explicit) must be told apart from "never set" (omitted). Same
     omit-when-unset discipline already covered for skip_query_embed /
     skip_cognitive_rerank (see docs/decisions ADR-0021, commit ff7f803
     precedent).
  2. Response parsing — _parse_search_results / _parse_search_response must
     stop discarding provenance/scope/signals (pre-existing débito técnico,
     ADR-0021 "Decisão pendente do dono" #2) and must populate
     related_nodes when the server sends it.

Input source: hand-built JSON fixtures shaped exactly like the Go structs in
server/model/record.go (SearchRequest, SearchResult, SearchHitSignals,
RetrievalMeta) — read directly from that file during this change, not
guessed. No network, no external services; the payload-shape tests use an
in-process aiohttp mock server (matches the existing tests/test_http_mock.py
pattern), the parsing tests call the SDK's pure parse functions directly.
"""

import sys
import os
import unittest

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

from anhurdb.client import Memory
from anhurdb.client.session_filter import sessions_all
from anhurdb.client import _parse_search_results, _parse_search_response
from anhurdb.models import RelatedNode, RetrievalMeta, SearchHitSignals, SearchResult


# ── Mock server: captures the exact JSON body the SDK sent ──────────

async def handle_sessions_create(request):
    return web.json_response({"session_id": "s1", "metadata": {}}, status=201)


async def handle_search_capture(request):
    """Echoes back a minimal valid envelope, and stashes the parsed request
    body on the app so the test can assert on exactly what was sent."""
    body = await request.json()
    request.app.setdefault("captured_payloads", []).append(body)
    return web.json_response({"results": []})


def create_capture_app():
    app = web.Application()
    app.router.add_post("/api/v1/sessions", handle_sessions_create)
    app.router.add_post("/api/v1/search", handle_search_capture)
    return app


class TestSearchPayloadKnobOmission(AioHTTPTestCase):
    """POST /api/v1/search body must omit expand_related/astar_weight/
    entity_jaccard_weight entirely when the caller did not set them —
    never send an explicit "off" value the server would have to interpret."""

    async def get_application(self):
        return create_capture_app()

    @unittest_run_loop
    async def test_defaults_omit_all_three_new_knobs(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.search("q", sessions_all())
        payload = self.app["captured_payloads"][-1]
        self.assertNotIn("expand_related", payload)
        self.assertNotIn("astar_weight", payload)
        self.assertNotIn("entity_jaccard_weight", payload)
        # Existing ablation knobs keep the same discipline (regression guard).
        self.assertNotIn("skip_query_embed", payload)
        self.assertNotIn("skip_cognitive_rerank", payload)

    @unittest_run_loop
    async def test_expand_related_true_is_sent(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.search("q", sessions_all(), expand_related=True)
        payload = self.app["captured_payloads"][-1]
        self.assertIs(payload["expand_related"], True)

    @unittest_run_loop
    async def test_expand_related_false_still_omitted(self):
        # explicit False must behave identically to the unset default.
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.search("q", sessions_all(), expand_related=False)
        payload = self.app["captured_payloads"][-1]
        self.assertNotIn("expand_related", payload)

    @unittest_run_loop
    async def test_astar_weight_explicit_zero_is_sent_not_omitted(self):
        # The whole point of Optional[float]=None: 0.0 is a legitimate
        # "disable this arm for this query" request, distinct from "unset".
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.search("q", sessions_all(), astar_weight=0.0)
        payload = self.app["captured_payloads"][-1]
        self.assertIn("astar_weight", payload)
        self.assertEqual(payload["astar_weight"], 0.0)

    @unittest_run_loop
    async def test_entity_jaccard_weight_explicit_zero_is_sent_not_omitted(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.search("q", sessions_all(), entity_jaccard_weight=0.0)
        payload = self.app["captured_payloads"][-1]
        self.assertIn("entity_jaccard_weight", payload)
        self.assertEqual(payload["entity_jaccard_weight"], 0.0)

    @unittest_run_loop
    async def test_astar_weight_none_default_is_omitted(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.search("q", sessions_all(), astar_weight=None)
        payload = self.app["captured_payloads"][-1]
        self.assertNotIn("astar_weight", payload)

    @unittest_run_loop
    async def test_all_three_knobs_together(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.search(
                "q",
                sessions_all(),
                expand_related=True,
                astar_weight=0.42,
                entity_jaccard_weight=0.13,
            )
        payload = self.app["captured_payloads"][-1]
        self.assertIs(payload["expand_related"], True)
        self.assertAlmostEqual(payload["astar_weight"], 0.42)
        self.assertAlmostEqual(payload["entity_jaccard_weight"], 0.13)

    @unittest_run_loop
    async def test_search_with_retrieval_uses_identical_payload_builder(self):
        # search_with_retrieval() must honor the same omit-when-unset
        # discipline as search() — they share _build_search_payload, this
        # guards against the two ever being hand-edited out of sync.
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.search_with_retrieval("q", sessions_all())
        payload = self.app["captured_payloads"][-1]
        self.assertNotIn("expand_related", payload)
        self.assertNotIn("astar_weight", payload)
        self.assertNotIn("entity_jaccard_weight", payload)


# ── Response parsing: provenance/scope/signals/related_nodes/retrieval ──

# Shaped exactly like server/model/record.go's SearchResult/SearchHitSignals/
# RetrievalMeta JSON tags (read directly from that file on 2026-08-10).
_SAMPLE_SERVER_ENVELOPE = {
    "results": [
        {
            "record": {
                "id": 1,
                "type": "fact",
                "summary": "Data scientist at Google",
                "metadata": "mem-abc",
            },
            "similarity": 0.95,
            "provenance": "tenant_shared",
            "scope": "shared_all",
            "signals": {
                "fts_rank": 2,
                "semantic_rank": 1,
                "simhash_rank": 3,
                "simhash_hamming": 7,
                "rrf_score": 0.031,
                "semantic_cosine": 0.88,
            },
            "related_nodes": [
                {"id": 123, "type": "fact", "summary": "Works remotely", "weight": 0.7},
                {"id": 124, "type": "preference", "summary": "Prefers Python", "weight": 0.4},
            ],
        },
        {
            # Second hit deliberately omits provenance/scope/signals/related_nodes —
            # exactly what a server that never ran expand_related/debug_signals sends.
            "record": {"id": 2, "type": "preference", "summary": "Prefers Python"},
            "similarity": 0.82,
        },
    ],
    "retrieval": {
        "mode": "balanced",
        "signals_used": ["fts", "semantic", "simhash"],
        "semantic_attempted": True,
        "semantic_used": True,
        "degraded": False,
        "elapsed_ms": 42,
        "content_simhash_enabled": True,
        "content_simhash_weight": 0.2,
        "astar_enabled": True,
        "astar_weight": 0.42,
        "entity_jaccard_enabled": False,
        "entity_jaccard_weight": 0.0,
    },
}


class TestParseSearchResults(unittest.TestCase):
    """_parse_search_results must no longer discard provenance/scope/signals,
    and must populate related_nodes when the server sends it."""

    def test_first_hit_full_fields(self):
        results = _parse_search_results(_SAMPLE_SERVER_ENVELOPE)
        self.assertEqual(len(results), 2)
        first = results[0]
        self.assertIsInstance(first, SearchResult)
        self.assertEqual(first.record.id, 1)
        self.assertAlmostEqual(first.similarity, 0.95)
        self.assertEqual(first.provenance, "tenant_shared")
        self.assertEqual(first.scope, "shared_all")
        self.assertIsInstance(first.signals, SearchHitSignals)
        self.assertEqual(first.signals.fts_rank, 2)
        self.assertEqual(first.signals.semantic_rank, 1)
        self.assertAlmostEqual(first.signals.rrf_score, 0.031)
        self.assertAlmostEqual(first.signals.semantic_cosine, 0.88)
        self.assertIsNotNone(first.related_nodes)
        self.assertEqual(len(first.related_nodes), 2)
        self.assertIsInstance(first.related_nodes[0], RelatedNode)
        self.assertEqual(first.related_nodes[0].id, 123)
        self.assertEqual(first.related_nodes[0].summary, "Works remotely")
        self.assertAlmostEqual(first.related_nodes[0].weight, 0.7)

    def test_second_hit_absent_fields_default_sanely(self):
        results = _parse_search_results(_SAMPLE_SERVER_ENVELOPE)
        second = results[1]
        self.assertEqual(second.provenance, "")
        self.assertEqual(second.scope, "")
        self.assertIsNone(second.signals)
        # related_nodes must stay None (not []) when the server never sent
        # the key — None means "didn't ask/server doesn't support it", []
        # would wrongly mean "asked, and got zero neighbours back".
        self.assertIsNone(second.related_nodes)

    def test_empty_or_non_dict_payload_returns_empty_list(self):
        self.assertEqual(_parse_search_results({}), [])
        self.assertEqual(_parse_search_results(None), [])
        self.assertEqual(_parse_search_results([]), [])

    def test_malformed_hit_missing_record_raises_loudly(self):
        # No silent data loss: a hit without "record" must fail loudly
        # (KeyError), never synthesize a placeholder record.
        with self.assertRaises(KeyError):
            _parse_search_results({"results": [{"similarity": 0.5}]})


class TestParseSearchResponse(unittest.TestCase):
    """_parse_search_response wraps results + the optional retrieval block."""

    def test_full_envelope(self):
        response = _parse_search_response(_SAMPLE_SERVER_ENVELOPE)
        self.assertEqual(len(response.results), 2)
        self.assertIsInstance(response.retrieval, RetrievalMeta)
        self.assertEqual(response.retrieval.mode, "balanced")
        self.assertEqual(
            response.retrieval.signals_used, ["fts", "semantic", "simhash"]
        )
        self.assertTrue(response.retrieval.semantic_used)
        self.assertFalse(response.retrieval.degraded)
        self.assertEqual(response.retrieval.elapsed_ms, 42)
        self.assertAlmostEqual(response.retrieval.astar_weight, 0.42)
        self.assertFalse(response.retrieval.entity_jaccard_enabled)

    def test_retrieval_omitted_by_server_is_none(self):
        envelope_without_retrieval = {"results": _SAMPLE_SERVER_ENVELOPE["results"]}
        response = _parse_search_response(envelope_without_retrieval)
        self.assertEqual(len(response.results), 2)
        self.assertIsNone(response.retrieval)

    def test_non_dict_payload_returns_empty_response(self):
        response = _parse_search_response(None)
        self.assertEqual(response.results, [])
        self.assertIsNone(response.retrieval)


class TestSearchWithRetrievalEndToEnd(AioHTTPTestCase):
    """Memory.search_with_retrieval() against a mock server returning the
    full ADR-0012 envelope — exercises the real HTTP round trip, not just
    the pure parse functions above."""

    async def get_application(self):
        app = web.Application()

        async def handle_search_full(request):
            return web.json_response(_SAMPLE_SERVER_ENVELOPE)

        app.router.add_post("/api/v1/search", handle_search_full)
        return app

    @unittest_run_loop
    async def test_search_with_retrieval_returns_envelope(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            response = await mem.search_with_retrieval("q", sessions_all())
        self.assertEqual(len(response.results), 2)
        self.assertEqual(response.results[0].provenance, "tenant_shared")
        self.assertIsNotNone(response.retrieval)
        self.assertEqual(response.retrieval.mode, "balanced")

    @unittest_run_loop
    async def test_search_still_returns_plain_list_unchanged(self):
        # search() keeps its List[SearchResult] return type — the whole
        # point of adding search_with_retrieval() instead of retyping it.
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            results = await mem.search("q", sessions_all())
        self.assertIsInstance(results, list)
        self.assertEqual(len(results), 2)
        self.assertEqual(results[0].provenance, "tenant_shared")
        self.assertEqual(len(results[0].related_nodes), 2)
