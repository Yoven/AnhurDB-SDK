"""Cross-SDK alignments of 2026-11-21 (audit rows 18-21) — Python side.

Go (``client/parity_nits_test.go``) and TypeScript
(``src/test/parity_nits.test.ts``) pin the same answers:
  - an unknown upload mode is a typed invalid_request refusal, never sent;
  - a 404 reads ``Resource not found (HTTP 404): <path>`` with kind not_found.
"""
import pytest
from aiohttp import web

from anhurdb.client import Memory
from anhurdb.client.exceptions import AnhurQueryError


@pytest.fixture
def anyio_backend():
    return "asyncio"


async def _start_server(app: web.Application) -> tuple:
    """Start ``app`` on an ephemeral port and return (runner, base_url)."""
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = site._server.sockets[0].getsockname()[1]
    return runner, f"http://127.0.0.1:{port}"


@pytest.mark.anyio
async def test_upload_unknown_mode_is_a_typed_local_refusal():
    server_was_called = {"hit": False}

    async def handle(request: web.Request) -> web.Response:
        server_was_called["hit"] = True
        return web.json_response({}, status=202)

    app = web.Application()
    app.router.add_post("/api/v1/upload", handle)
    runner, base_url = await _start_server(app)
    try:
        async with Memory(api_key="test-key", url=base_url) as mem:
            with pytest.raises(AnhurQueryError) as refusal:
                await mem.upload_file("a.txt", b"hi", mode="Library")
    finally:
        await runner.cleanup()

    assert refusal.value.status_code == 400
    assert refusal.value.kind == "invalid_request"
    assert refusal.value.retryable is False
    assert str(refusal.value) == (
        "INVALID_PARAM: upload: mode 'Library' is not an upload mode "
        "(want chat|tenant_shared|client_shared)"
    )
    assert server_was_called["hit"] is False


@pytest.mark.anyio
async def test_not_found_message_quotes_the_path():
    async def handle(request: web.Request) -> web.Response:
        return web.Response(status=404, text="Not Found")

    app = web.Application()
    app.router.add_get("/api/v1/records/42", handle)
    runner, base_url = await _start_server(app)
    try:
        async with Memory(api_key="test-key", url=base_url) as mem:
            with pytest.raises(AnhurQueryError) as not_found:
                await mem.get(42)
    finally:
        await runner.cleanup()

    assert not_found.value.status_code == 404
    assert not_found.value.kind == "not_found"
    assert str(not_found.value) == "Resource not found (HTTP 404): /api/v1/records/42"
