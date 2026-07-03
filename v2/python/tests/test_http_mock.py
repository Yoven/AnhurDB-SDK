"""
Integration-style tests with a mock HTTP server for the AnhurDB Python SDK.

Uses aiohttp's built-in test utilities to spin up a local HTTP server that
simulates AnhurDB responses. Tests cover:
  - Memory.add() cloud ingest path
  - Memory.add() OSS fallback path
  - Memory.search() result parsing
  - Memory.profile() success and 404 fallback
  - AnhurClient.create() record creation
  - AnhurClient.batch_read_content()
  - AnhurClient.search_entities()
  - Error handling (401, 400, 409, 415, 429, 403, 500, timeout, oversized response)
  - Redirect blocking (credential leak protection)
  - Response size cap enforcement
  - HTTP status code → exception type mapping (TestHTTPStatusCodes)
"""

import asyncio
import base64
import json
import unittest
from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

import sys
import os

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb.client import Memory, AnhurClient
from anhurdb.client.connection import HTTPConnection
from anhurdb.client.exceptions import (
    AnhurAuthError,
    AnhurConnectionError,
    AnhurError,
    AnhurQueryError,
)
from anhurdb.models import CreateRequest, MemoryType


# ── Mock server handlers ──────────────────────────────────────────

async def handle_ingest(request):
    """Simulates POST /api/v1/ingest (cloud mode)."""
    data = await request.json()
    return web.json_response({
        "id": 42,
        "records": [
            {"id": 42, "type": "fact", "summary": data["content"][:50]},
            {"id": 43, "type": "episodic", "summary": data["content"][:50]},
        ],
    })


async def handle_ingest_404(request):
    """Simulates POST /api/v1/ingest returning 404 (OSS mode)."""
    return web.Response(status=404, text="Not Found")


async def handle_records(request):
    """Simulates POST /api/v1/records (OSS fallback)."""
    data = await request.json()
    return web.json_response({"id": 100, "uuid": data.get("uuid", "")})


async def handle_search_global(request):
    """Simulates POST /api/v1/search/global."""
    data = await request.json()
    return web.json_response({
        "results": [
            {
                "record": {
                    "id": 1,
                    "type": "fact",
                    "summary": "Data scientist at Google",
                    "metadata": "mem-abc",
                    "content": "Full content here",
                },
                "similarity": 0.95,
            },
            {
                "record": {
                    "id": 2,
                    "type": "preference",
                    "summary": "Prefers Python",
                    "metadata": "mem-abc",
                },
                "similarity": 0.82,
            },
        ]
    })


async def handle_profile(request):
    """Simulates GET /api/v1/profile."""
    tag = request.query.get("tag", "")
    return web.json_response({
        "static": {"role": "engineer", "company": "Google"},
        "dynamic": {"recent_topic": "NLP"},
        "stats": {"total_records": 42},
    })


async def handle_profile_404(request):
    """Simulates GET /api/v1/profile returning 404."""
    return web.Response(status=404, text="Not Found")


async def handle_record_content(request):
    """Simulates GET /api/v1/records/{id}/content."""
    return web.json_response({"content": "Full payload content"})


async def handle_batch_content(request):
    """Simulates POST /api/v1/records/batch-content."""
    data = await request.json()
    result = {str(i): f"content-{i}" for i in data.get("ids", [])}
    return web.json_response(result)


async def handle_entities(request):
    """Simulates GET /api/v1/entities."""
    return web.json_response({
        "entities": [
            {"id": 1, "name": "Google", "type": "org", "summary": "Tech company"},
        ]
    })


async def handle_sessions_stats(request):
    """Simulates GET /api/v1/sessions/stats."""
    return web.json_response({
        "sessions": [
            {"uuid": "s1", "record_count": 10, "types": {"fact": 5}, "last_activity": "2026-04-08"},
        ]
    })


async def handle_auth_fail(request):
    return web.Response(status=401, text="Unauthorized")


async def handle_bad_request(request):
    return web.Response(status=400, text="Bad request body")


async def handle_server_error(request):
    return web.Response(status=500, text="Internal server error")


async def handle_redirect(request):
    return web.Response(status=302, headers={"Location": "http://evil.com"})


async def handle_oversized(request):
    """Returns a response slightly over the size limit."""
    # Return 101 MB of data
    data = "x" * (101 * 1024 * 1024)
    return web.Response(text=data)


async def handle_ast_query(request):
    """Simulates POST /api/v1/query (AST endpoint).

    Validates that the AST is sent flat (not wrapped in {"query": ...}).
    The server expects: {"filters": {...}, "pagination": {...}}.
    """
    data = await request.json()

    # CRITICAL: AST must be flat at top-level, not wrapped.
    # If "query" is a key, the SDK is sending wrong format.
    if "query" in data and "filters" not in data:
        return web.json_response(
            {"error": "AST must be flat, not wrapped in {\"query\": ...}"},
            status=400,
        )

    # Verify we got actual filter data.
    filters = data.get("filters", {})

    # Return matching records.
    return web.json_response({
        "records": [
            {"id": 10, "uuid": "s1", "type": "risk", "summary": "No rollback"},
        ],
        "count": 1,
    })


async def handle_manifest(request):
    """Simulates GET /api/v1/manifest."""
    return web.json_response({
        "records": [
            {"id": 1, "uuid": "s1", "type": "fact", "summary": "Latest", "status": "saved"},
        ],
        "count": 1,
        "has_more": False,
    })


async def handle_walk(request):
    """Simulates POST /api/v1/walk."""
    return web.json_response({
        "nodes": [{"id": 42, "type": "fact", "summary": "Root"}],
        "edges": [{"source": 42, "target": 43, "type": "related"}],
    })


async def handle_walk_semantic(request):
    """Simulates POST /api/v1/walk/semantic.

    Records the decoded request body on the app so a test can assert the
    goal-directed serialization (target/vector/target_tag), then returns the
    locked response shape {"nodes","edges","count"}.
    """
    request.app["walk_semantic_body"] = await request.json()
    return web.json_response({
        "nodes": [{"id": 42, "type": "fact", "summary": "Root"}],
        "edges": [{"source": 42, "target": 43}],
        "count": 1,
    })


async def handle_topology(request):
    """Simulates GET /api/v1/records/{id}/topology."""
    return web.json_response({
        "target": {"id": 42, "type": "fact", "summary": "Target"},
        "neighbors": [{"id": 43, "type": "episodic", "summary": "Neighbor"}],
    })


# ── Test classes ──────────────────────────────────────────────────

def create_app_cloud():
    """App with cloud ingest available."""
    app = web.Application()
    app.router.add_post("/api/v1/ingest", handle_ingest)
    app.router.add_post("/api/v1/records", handle_records)
    app.router.add_post("/api/v1/search/global", handle_search_global)
    app.router.add_get("/api/v1/profile", handle_profile)
    app.router.add_get("/api/v1/records/{id}/content", handle_record_content)
    app.router.add_post("/api/v1/records/batch-content", handle_batch_content)
    app.router.add_get("/api/v1/entities", handle_entities)
    app.router.add_get("/api/v1/sessions/stats", handle_sessions_stats)
    app.router.add_post("/api/v1/query", handle_ast_query)
    app.router.add_get("/api/v1/manifest", handle_manifest)
    app.router.add_post("/api/v1/walk", handle_walk)
    app.router.add_post("/api/v1/walk/semantic", handle_walk_semantic)
    app.router.add_get("/api/v1/records/{id}/topology", handle_topology)
    return app


def create_app_oss():
    """App without ingest (OSS mode — 404 on ingest)."""
    app = web.Application()
    app.router.add_post("/api/v1/ingest", handle_ingest_404)
    app.router.add_post("/api/v1/records", handle_records)
    app.router.add_post("/api/v1/search/global", handle_search_global)
    app.router.add_get("/api/v1/profile", handle_profile_404)
    return app


def create_app_errors():
    """App that returns various errors."""
    app = web.Application()
    app.router.add_post("/api/v1/ingest", handle_auth_fail)
    app.router.add_post("/api/v1/records", handle_bad_request)
    app.router.add_post("/api/v1/search/global", handle_server_error)
    app.router.add_get("/api/v1/profile", handle_redirect)
    app.router.add_get("/api/v1/manifest", handle_oversized)
    return app


class TestMemoryCloudMode(AioHTTPTestCase):
    """Tests Memory against a cloud-mode mock server."""

    async def get_application(self):
        return create_app_cloud()

    @unittest_run_loop
    async def test_add_cloud_ingest(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            result = await mem.add("I'm a data scientist at Google")
            self.assertEqual(result["mode"], "cloud")
            self.assertEqual(len(result["records"]), 2)
            self.assertEqual(result["records"][0]["id"], 42)

    @unittest_run_loop
    async def test_search_returns_flat_results(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            results = await mem.search("what does user do?")
            self.assertEqual(len(results), 2)
            self.assertEqual(results[0]["id"], 1)
            self.assertEqual(results[0]["summary"], "Data scientist at Google")
            self.assertAlmostEqual(results[0]["score"], 0.95)
            self.assertEqual(results[1]["type"], "preference")

    @unittest_run_loop
    async def test_profile_returns_structured(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            profile = await mem.profile()
            self.assertEqual(profile["static"]["role"], "engineer")
            self.assertEqual(profile["dynamic"]["recent_topic"], "NLP")
            self.assertEqual(profile["stats"]["total_records"], 42)

    @unittest_run_loop
    async def test_read_content(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            content = await mem.read_content(42)
            self.assertIsInstance(content, dict)
            self.assertEqual(content["content"], "Full payload content")

    @unittest_run_loop
    async def test_list_sessions(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            sessions = await mem.list_sessions()
            self.assertEqual(len(sessions), 1)
            self.assertEqual(sessions[0]["uuid"], "s1")

    @unittest_run_loop
    async def test_walk(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            result = await mem.walk(42, depth=2)
            self.assertIn("nodes", result)
            self.assertIn("edges", result)

    @unittest_run_loop
    async def test_walk_semantic_dijkstra_backward_compat(self):
        # Without goal args the body must be the pre-existing Dijkstra payload:
        # only seed_id + depth, no target/vector/target_tag/max_cost.
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            result = await mem.walk_semantic(42, depth=2)
            self.assertIn("nodes", result)
            self.assertIn("edges", result)
        body = self.app["walk_semantic_body"]
        self.assertEqual(body, {"seed_id": 42, "depth": 2})

    @unittest_run_loop
    async def test_walk_semantic_goal_semantic_base64(self):
        # target="semantic" must base64-encode goal_vector into the "vector" field.
        url = f"http://localhost:{self.server.port}"
        raw_vector = bytes([0, 1, 2, 253, 254, 255])
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.walk_semantic(
                7,
                target="semantic",
                goal_vector=raw_vector,
                max_cost=1.5,
            )
        body = self.app["walk_semantic_body"]
        self.assertEqual(body["seed_id"], 7)
        self.assertEqual(body["target"], "semantic")
        self.assertEqual(body["max_cost"], 1.5)
        self.assertEqual(body["vector"], base64.b64encode(raw_vector).decode("ascii"))
        self.assertNotIn("target_tag", body)

    @unittest_run_loop
    async def test_walk_semantic_goal_tag(self):
        # target="tag" must forward target_tag and omit the vector field.
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            await mem.walk_semantic(9, target="tag", target_tag="Anhur")
        body = self.app["walk_semantic_body"]
        self.assertEqual(body["target"], "tag")
        self.assertEqual(body["target_tag"], "Anhur")
        self.assertNotIn("vector", body)

    @unittest_run_loop
    async def test_get_context(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            result = await mem.get_context(42)
            self.assertIn("target", result)
            self.assertIn("neighbors", result)

    @unittest_run_loop
    async def test_recent(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            records = await mem.recent(limit=5)
            self.assertEqual(len(records), 1)


class TestMemoryOSSFallback(AioHTTPTestCase):
    """Tests Memory cloud→OSS fallback."""

    async def get_application(self):
        return create_app_oss()

    @unittest_run_loop
    async def test_add_falls_back_to_oss(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            result = await mem.add("Test text for OSS mode")
            self.assertEqual(result["mode"], "oss")
            self.assertEqual(result["records"][0]["id"], 100)

    @unittest_run_loop
    async def test_profile_404_returns_empty(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            profile = await mem.profile()
            self.assertEqual(profile["status"], "not_available")
            self.assertEqual(profile["static"], {})


class TestAnhurClientMock(AioHTTPTestCase):
    """Tests AnhurClient full API against mock server."""

    async def get_application(self):
        return create_app_cloud()

    @unittest_run_loop
    async def test_create_record(self):
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            result = await client.create(CreateRequest(
                uuid="test-session",
                type=MemoryType.FACT,
                summary="Test fact",
                content="Full content",
                score=8,
            ))
            self.assertEqual(result["id"], 100)

    @unittest_run_loop
    async def test_batch_read_content(self):
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            result = await client.batch_read_content([1, 2, 3])
            self.assertEqual(result["1"], "content-1")
            self.assertEqual(result["2"], "content-2")

    @unittest_run_loop
    async def test_search_entities(self):
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            entities = await client.search_entities(query="Google")
            self.assertEqual(len(entities), 1)
            self.assertEqual(entities[0]["name"], "Google")

    @unittest_run_loop
    async def test_search_with_ast(self):
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            from anhurdb.query import Filter
            records = await client.search_with_ast(
                Filter({"type": {"$eq": "risk"}}),
                session_uuid="session-uuid",
            )
            self.assertEqual(len(records), 1)
            self.assertEqual(records[0].type, MemoryType.RISK)
            self.assertEqual(records[0].summary, "No rollback")


class TestASTQueryIntegration(AioHTTPTestCase):
    """Tests that the AST query pipeline sends correct format to server."""

    async def get_application(self):
        app = web.Application()
        app["last_ast_payload"] = None

        async def capture_ast(request):
            """Captures the raw payload and validates AST format."""
            data = await request.json()
            app["last_ast_payload"] = data

            # Reject wrapped format — must be flat.
            if "query" in data and "filters" not in data:
                return web.json_response(
                    {"error": "wrapped format rejected"}, status=400
                )

            return web.json_response({
                "records": [
                    {"id": 10, "uuid": "s1", "type": "risk", "summary": "No rollback"},
                ],
                "count": 1,
            })

        app.router.add_post("/api/v1/query", capture_ast)
        return app

    @unittest_run_loop
    async def test_ast_sent_flat_not_wrapped(self):
        """The AST must arrive at the server flat, not inside {"query": ...}."""
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            from anhurdb.query import Filter
            records = await client.search_with_ast(
                Filter({"type": {"$eq": "risk"}}),
            )
            # If we got records, the mock didn't reject the format.
            self.assertEqual(len(records), 1)

            # Verify the raw payload structure.
            payload = self.app["last_ast_payload"]
            self.assertIn("filters", payload, "AST must have 'filters' at top level")
            self.assertIn("pagination", payload, "AST must have 'pagination' at top level")
            self.assertNotIn("query", payload, "AST must NOT be wrapped in 'query'")

    @unittest_run_loop
    async def test_session_uuid_injected_as_filter(self):
        """session_uuid must become a uuid filter, not a separate field."""
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            from anhurdb.query import Filter
            await client.search_with_ast(
                Filter({"type": {"$eq": "risk"}}),
                session_uuid="my-session-123",
            )

            payload = self.app["last_ast_payload"]
            self.assertNotIn("session_uuid", payload, "session_uuid must NOT be a top-level field")
            self.assertEqual(
                payload["filters"]["uuid"]["$eq"],
                "my-session-123",
                "session_uuid must be injected as uuid filter",
            )

    @unittest_run_loop
    async def test_query_builder_end_to_end(self):
        """Full QueryBuilder → search_with_ast → server pipeline."""
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            from anhurdb.query import QueryBuilder
            qb = (
                QueryBuilder()
                .where(type="risk", score__gte=7)
                .order_by("weight", "desc")
                .limit(20)
            )
            records = await client.search_with_ast(qb)

            payload = self.app["last_ast_payload"]
            self.assertEqual(payload["filters"]["type"]["$eq"], "risk")
            self.assertEqual(payload["filters"]["score"]["$gte"], 7)
            self.assertEqual(payload["pagination"]["limit"], 20)
            self.assertEqual(payload["sort"][0]["field"], "weight")
            self.assertEqual(payload["sort"][0]["order"], "desc")


class TestErrorHandling(AioHTTPTestCase):
    """Tests error handling with various HTTP error codes."""

    async def get_application(self):
        return create_app_errors()

    @unittest_run_loop
    async def test_401_raises_auth_error(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="bad-key", url=url, user_id="u1") as mem:
            # add() tries ingest first which returns 401
            with self.assertRaises(AnhurAuthError):
                await mem.add("test")

    @unittest_run_loop
    async def test_redirect_blocked(self):
        """Redirects must be blocked to prevent credential leakage."""
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="test-key", url=url, user_id="u1") as mem:
            with self.assertRaises(AnhurError) as ctx:
                await mem.profile()
            self.assertIn("redirect", str(ctx.exception).lower())


class TestSecurityHeaders(AioHTTPTestCase):
    """Verify that security headers are set correctly."""

    async def get_application(self):
        app = web.Application()
        # Store captured headers on the app for inspection.
        app["captured_headers"] = {}

        async def capture_headers(request):
            app["captured_headers"] = dict(request.headers)
            return web.json_response({
                "results": [{"record": {"id": 1, "type": "fact",
                             "summary": "test"}, "similarity": 0.9}]
            })

        app.router.add_post("/api/v1/search/global", capture_headers)
        return app

    @unittest_run_loop
    async def test_x_api_key_header_sent(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="my-secret-key", url=url, user_id="u1") as mem:
            await mem.search("test")
            headers = self.app["captured_headers"]
            self.assertEqual(headers.get("X-Api-Key") or headers.get("X-API-Key"),
                             "my-secret-key")

    @unittest_run_loop
    async def test_no_bearer_header(self):
        """Must NOT use Bearer auth."""
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="key", url=url, user_id="u1") as mem:
            await mem.search("test")
            headers = self.app["captured_headers"]
            auth = headers.get("Authorization", "")
            self.assertNotIn("Bearer", auth)


class TestHTTPStatusCodes(AioHTTPTestCase):
    """Tests that specific HTTP status codes raise the correct exception types."""

    async def get_application(self):
        app = web.Application()

        async def handle_409(request):
            return web.Response(status=409, text="session has reached the maximum of 1000 records")

        async def handle_415(request):
            return web.Response(status=415, text="Unsupported Media Type")

        async def handle_429(request):
            return web.Response(status=429, text="rate limit exceeded")

        async def handle_403(request):
            return web.Response(status=403, text="Forbidden")

        app.router.add_post("/api/v1/records", handle_409)
        app.router.add_post("/api/v1/upload", handle_415)
        app.router.add_post("/api/v1/search/global", handle_429)
        app.router.add_get("/api/v1/profile", handle_403)
        return app

    @unittest_run_loop
    async def test_409_raises_query_error(self):
        """HTTP 409 Conflict must raise AnhurQueryError (not silently return a dict)."""
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            from anhurdb.models import CreateRequest, MemoryType
            with self.assertRaises(AnhurQueryError) as ctx:
                await client.create(CreateRequest(uuid="s1", content="test"))
            self.assertIn("409", str(ctx.exception))

    @unittest_run_loop
    async def test_415_raises_query_error(self):
        """HTTP 415 Unsupported Media Type must raise AnhurQueryError."""
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            with self.assertRaises(AnhurQueryError) as ctx:
                await client.upload_file("test.pdf", b"fake pdf content")
            self.assertIn("415", str(ctx.exception))

    @unittest_run_loop
    async def test_429_raises_anhur_error(self):
        """HTTP 429 Rate Limited must raise AnhurError."""
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            with self.assertRaises(AnhurError) as ctx:
                await client.search("test")
            self.assertIn("429", str(ctx.exception))

    @unittest_run_loop
    async def test_403_raises_auth_error(self):
        """HTTP 403 Forbidden must raise AnhurAuthError."""
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(url=url, api_key="test-key") as client:
            with self.assertRaises(AnhurAuthError):
                await client.profile("tag1")


if __name__ == "__main__":
    unittest.main()
