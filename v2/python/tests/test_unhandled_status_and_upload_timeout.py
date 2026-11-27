"""Regression for the 2026-11-27 SDK audit: an unhandled 4xx (e.g. 413 too_large)
must raise instead of parsing as an empty success, and a multipart upload timeout
must raise AnhurConnectionError(kind=timeout) instead of a bare TimeoutError.
Parity with Go (response.go default case) and TypeScript (!response.ok)."""
import asyncio

import pytest
from aiohttp import web

from anhurdb.client import Memory
from anhurdb.client.connection import HTTPConnection
from anhurdb.client.exceptions import AnhurConnectionError, AnhurError


async def _start(app):
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = site._server.sockets[0].getsockname()[1]
    return runner, f"http://127.0.0.1:{port}"


async def test_upload_413_raises_with_status():
    async def too_large(request):
        await request.read()
        return web.json_response({"error": "file too large", "code": "too_large"}, status=413)

    app = web.Application()
    app.router.add_post("/api/v1/upload", too_large)
    runner, base = await _start(app)
    try:
        async with Memory(api_key="k", url=base) as memory_client:
            with pytest.raises(AnhurError) as raised:
                await memory_client.upload_file("a.pdf", b"%PDF-1.4 x", mode="tenant_shared")
            assert raised.value.status_code == 413
    finally:
        await runner.cleanup()


async def test_json_path_405_raises_with_status():
    async def method_not_allowed(request):
        return web.json_response({"error": "method not allowed"}, status=405)

    app = web.Application()
    app.router.add_get("/api/v1/recent", method_not_allowed)
    runner, base = await _start(app)
    connection = HTTPConnection(api_key="k", base_url=base)
    await connection.connect()
    try:
        with pytest.raises(AnhurError) as raised:
            await connection.get("/api/v1/recent")
        assert raised.value.status_code == 405
    finally:
        await connection.close()
        await runner.cleanup()


async def test_multipart_timeout_is_typed():
    async def slow(request):
        await asyncio.sleep(3)
        return web.json_response({})

    app = web.Application()
    app.router.add_post("/api/v1/upload", slow)
    runner, base = await _start(app)
    connection = HTTPConnection(api_key="k", base_url=base, timeout=1)
    await connection.connect()
    try:
        with pytest.raises(AnhurConnectionError) as raised:
            await connection.post_multipart("/api/v1/upload", "file", b"x", "a.txt")
        assert raised.value.kind == AnhurError.KIND_TIMEOUT
        assert str(raised.value) != ""
    finally:
        await connection.close()
        await runner.cleanup()
