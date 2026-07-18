"""
Unit tests for the 2026-06 SDK hardening fixes (mock-server based, no live DB).

Covers:
  1. add(mode="regular", score=, type=, metadata=) routes to /api/v1/records
     and SENDS score, type, and merged metadata in the payload.
  2. add(mode="ingest") always sends session_id on /api/v1/ingest.
  3. _request is a transparent pipe: it makes exactly ONE request and surfaces
     every 5xx immediately with NO client-side retry.
  4. read_content returns a verbatim plain-text body instead of wrapping it in
     {"message": text[:1000]} and truncating at 1000 chars.
"""

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

from anhurdb import Memory, AnhurClient, MemoryType, AnhurError


# ── Mock handlers that capture what the SDK sends ────────────────────────────

async def handle_sessions_create(request):
    data = await request.json() if request.can_read_body and request.content_length else {}
    session_id = data.get("session_id") or "server-generated"
    request.app.setdefault("sessions_created", []).append(session_id)
    return web.json_response({"session_id": session_id, "metadata": {}}, status=201)


async def handle_records_capture(request):
    body = await request.json()
    request.app["last_record"] = body
    # Reply with a stable id keyed by type (10 episodic / 20 derived) so tests
    # can assert exactly which records the SDK sent.
    is_episodic = body.get("type", "episodic") == "episodic"
    request.app.setdefault("records", []).append(body)
    rec_id = 10 if is_episodic else 20
    return web.json_response({"id": rec_id, "uuid": body.get("uuid", "")})


async def handle_ingest_capture(request):
    body = await request.json()
    request.app["last_ingest"] = body
    return web.json_response({
        "id": 1,
        "records": [{"id": 1, "type": "episodic", "summary": body["content"][:50]}],
    })


async def handle_plain_content(request):
    # 2500-char plain-text body; NOT JSON.
    return web.Response(text="X" * 2500, content_type="text/plain")


def app_capture():
    app = web.Application()
    app.router.add_post("/api/v1/sessions", handle_sessions_create)
    app.router.add_post("/api/v1/ingest", handle_ingest_capture)
    app.router.add_post("/api/v1/records", handle_records_capture)
    app.router.add_get("/api/v1/records/{id}/content", handle_plain_content)
    return app


class TestAddPersistsScoreType(AioHTTPTestCase):
    async def get_application(self):
        return app_capture()

    @unittest_run_loop
    async def test_explicit_score_type_routes_to_records(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="k", url=url, user_id="u1") as mem:
            await mem.create_session()
            result = await mem.add(
                "likes dark mode",
                mode="regular",
                score=8,
                type=MemoryType.PREFERENCE,
            )
            # Routed to the records (oss) path, not cloud ingest.
            self.assertEqual(result["mode"], "oss")
            # The record sent is the preference, carrying score+type.
            sent = self.app["last_record"]
            self.assertEqual(sent["type"], "preference")
            self.assertEqual(sent["score"], 8)
            types = [record["type"] for record in self.app["records"]]
            self.assertEqual(types, ["preference"])
            self.assertEqual(sent.get("related_ids", []), [])

    @unittest_run_loop
    async def test_metadata_merged_with_container_tag(self):
        import json
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="k", url=url, user_id="u1") as mem:
            await mem.create_session()
            await mem.add(
                "kickoff",
                mode="regular",
                type=MemoryType.FACT,
                metadata={"project": "apollo"},
            )
            meta = json.loads(self.app["last_record"]["metadata"])
            self.assertEqual(meta["project"], "apollo")
            self.assertEqual(meta["container_tag"], "u1")

    @unittest_run_loop
    async def test_plain_add_still_uses_ingest(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="k", url=url, user_id="u1") as mem:
            await mem.create_session()
            result = await mem.add("plain text, no options")
            self.assertEqual(result["mode"], "cloud")
            ingest_payload = self.app["last_ingest"]
            self.assertEqual(
                set(ingest_payload.keys()),
                {"content", "container_tag", "session_id"},
            )
            self.assertEqual(ingest_payload["session_id"], mem.session_id)
            self.assertEqual(ingest_payload["container_tag"], "u1")

    @unittest_run_loop
    async def test_score_type_with_default_mode_still_uses_ingest(self):
        """mode defaults to ingest — score/type are ignored, not rerouted."""
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="k", url=url, user_id="u1") as mem:
            await mem.create_session()
            result = await mem.add(
                "plain with pins", score=8, type=MemoryType.PREFERENCE
            )
            self.assertEqual(result["mode"], "cloud")
            self.assertIn("last_ingest", self.app)
            self.assertNotIn("last_record", self.app)

    @unittest_run_loop
    async def test_read_content_plain_text_verbatim(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="k", url=url, user_id="u1") as mem:
            content = await mem.read_content(42)
            self.assertIsInstance(content, str)
            self.assertEqual(len(content), 2500)  # not truncated at 1000


# ── Transparent pipe: no transport-level retry ───────────────────────────────

class TestNoTransportRetry(AioHTTPTestCase):
    """
    The connection layer is a transparent pipe: exactly ONE request per call,
    every 5xx surfaced immediately with no client-side retry.
    """

    async def get_application(self):
        app = web.Application()
        app["transient_attempts"] = 0
        app["perm_attempts"] = 0

        async def transient_500(request):
            # 5xx responses surface immediately — no client-side retry.
            request.app["transient_attempts"] += 1
            return web.json_response(
                {"error": "transient server error"}, status=500
            )

        async def permanent(request):
            request.app["perm_attempts"] += 1
            return web.json_response({"error": "genuine bug"}, status=500)

        app.router.add_post("/api/v1/records", transient_500)
        app.router.add_post("/api/v1/permanent", permanent)
        return app

    @unittest_run_loop
    async def test_transient_500_surfaced_without_retry(self):
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(api_key="k", url=url) as client:
            with self.assertRaises(AnhurError):
                await client._connection.post(
                    "/api/v1/records", {"uuid": "s1", "summary": "x"}
                )
            # Exactly one attempt — the transport does NOT replay the write.
            self.assertEqual(self.app["transient_attempts"], 1)

    @unittest_run_loop
    async def test_non_transient_500_surfaced_without_retry(self):
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(api_key="k", url=url) as client:
            with self.assertRaises(AnhurError):
                await client._connection.post("/api/v1/permanent", {})
            # Exactly one attempt — no silent retry of a real failure.
            self.assertEqual(self.app["perm_attempts"], 1)
