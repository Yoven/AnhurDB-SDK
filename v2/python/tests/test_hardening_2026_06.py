"""
Unit tests for the 2026-06 SDK hardening fixes (mock-server based, no live DB).

Covers:
  1. add(score=, type=, metadata=) routes to /api/v1/records and SENDS the
     score, type, and merged metadata in the payload (the cloud-ingest path
     dropped them silently).
  2. _request is a transparent pipe: it makes exactly ONE request and surfaces
     every 5xx immediately (including a 'not_leader' body) with NO retry — the
     router is the single retry owner in the stack.
  3. read_content returns a verbatim plain-text body instead of wrapping it in
     {"message": text[:1000]} and truncating at 1000 chars.
"""

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

from anhurdb import Memory, AnhurClient, MemoryType, AnhurError


# ── Mock handlers that capture what the SDK sends ────────────────────────────

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
            result = await mem.add(
                "likes dark mode", score=8, type=MemoryType.PREFERENCE
            )
            # Routed to the records (oss) path, not cloud ingest.
            self.assertEqual(result["mode"], "oss")
            # The record sent is the preference, carrying score+type.
            sent = self.app["last_record"]
            self.assertEqual(sent["type"], "preference")
            self.assertEqual(sent["score"], 8)
            # Anchor-seed REMOVED (2026-07-06): a derived add() issues exactly
            # ONE request — the SDK no longer fabricates a synthetic episodic
            # anchor client-side. The server auto-links the real anchor (Rule 3a)
            # and returns an honest 422 when the session has none.
            types = [r["type"] for r in self.app["records"]]
            self.assertEqual(types, ["preference"])
            # No client-side anchor link is fabricated — related_ids stays empty
            # and the server does the linking.
            self.assertEqual(sent.get("related_ids", []), [])

    @unittest_run_loop
    async def test_metadata_merged_with_container_tag(self):
        import json
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="k", url=url, user_id="u1") as mem:
            await mem.add(
                "kickoff", type=MemoryType.FACT, metadata={"project": "apollo"}
            )
            meta = json.loads(self.app["last_record"]["metadata"])
            self.assertEqual(meta["project"], "apollo")
            self.assertEqual(meta["container_tag"], "u1")

    @unittest_run_loop
    async def test_plain_add_still_uses_ingest(self):
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="k", url=url, user_id="u1") as mem:
            result = await mem.add("plain text, no options")
            self.assertEqual(result["mode"], "cloud")
            # Ingest payload only carries content + container_tag (server drops
            # the rest), so we must NOT have sent a score/type there.
            self.assertEqual(
                set(self.app["last_ingest"].keys()),
                {"content", "container_tag"},
            )

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
    every 5xx surfaced immediately. The router — not the SDK transport — is the
    single retry owner in the stack. A former transport retry re-fired reads and
    could mask genuine bugs behind silent replays; it was removed 2026-07-06.
    """

    async def get_application(self):
        app = web.Application()
        app["leader_attempts"] = 0
        app["perm_attempts"] = 0

        async def leadership_500(request):
            # Even a 'not_leader' body — the classic transient marker — is now
            # surfaced immediately, NOT retried. The router owns that replay.
            request.app["leader_attempts"] += 1
            return web.json_response(
                {"error": "node is not the leader (not_leader)"}, status=500
            )

        async def permanent(request):
            request.app["perm_attempts"] += 1
            return web.json_response({"error": "genuine bug"}, status=500)

        app.router.add_post("/api/v1/records", leadership_500)
        app.router.add_post("/api/v1/permanent", permanent)
        return app

    @unittest_run_loop
    async def test_leadership_500_surfaced_without_retry(self):
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(api_key="k", url=url) as client:
            with self.assertRaises(AnhurError):
                await client._connection.post(
                    "/api/v1/records", {"uuid": "s1", "summary": "x"}
                )
            # Exactly one attempt — the transport does NOT replay the write.
            self.assertEqual(self.app["leader_attempts"], 1)

    @unittest_run_loop
    async def test_non_transient_500_surfaced_without_retry(self):
        url = f"http://localhost:{self.server.port}"
        async with AnhurClient(api_key="k", url=url) as client:
            with self.assertRaises(AnhurError):
                await client._connection.post("/api/v1/permanent", {})
            # Exactly one attempt — no silent retry of a real failure.
            self.assertEqual(self.app["perm_attempts"], 1)
