"""Regression test for the 2026-07-30 silent-truncation incident.

aiohttp's StreamReader.read(n) with n > 0 returns as soon as the internal
buffer is non-empty — NOT the full body. A multi-chunk search response was
truncated at a random offset, json parsing failed, and search returned []
with no error: intermittent silent loss (5-7 of 20 e2e runs), with the server
proven correct. _read_capped_body must read to EOF in a loop.
"""
import asyncio
import json
import pytest
from aiohttp import web

from anhurdb.client.connection import HTTPConnection
from anhurdb.client.exceptions import AnhurError


@pytest.fixture
def anyio_backend():
    return "asyncio"


def _large_search_payload() -> dict:
    # Big enough to guarantee multiple TCP chunks (~1.5 MB).
    return {
        "count": 3,
        "results": [
            {"record": {"id": i, "summary": "x" * 500_000}, "similarity": 0.9}
            for i in range(3)
        ],
    }


async def _chunked_handler(request: web.Request) -> web.StreamResponse:
    # Stream the JSON body in small chunks with explicit flushes + yields so
    # the client's first read() fires long before the body is complete —
    # the exact shape that truncated the old single-read implementation.
    body = json.dumps(_large_search_payload()).encode()
    response = web.StreamResponse(status=200, headers={"Content-Type": "application/json"})
    await response.prepare(request)
    for offset in range(0, len(body), 8192):
        await response.write(body[offset:offset + 8192])
        await asyncio.sleep(0)  # yield so chunks land separately
    await response.write_eof()
    return response


async def _start_server():
    app = web.Application()
    app.router.add_post("/api/v1/search", _chunked_handler)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = site._server.sockets[0].getsockname()[1]
    return runner, f"http://127.0.0.1:{port}"


@pytest.mark.anyio
async def test_chunked_response_is_read_to_eof():
    runner, base_url = await _start_server()

    conn = HTTPConnection(base_url, api_key="test-key")
    async with conn:
        result = await conn.post("/api/v1/search", {"text": "q", "sessions": ["*"]})

    # The old single-read implementation returned a truncated body here and the
    # caller saw [] silently. Reading to EOF must yield the complete payload.
    assert result["count"] == 3
    assert len(result["results"]) == 3
    assert len(result["results"][2]["record"]["summary"]) == 500_000
    await runner.cleanup()


@pytest.mark.anyio
async def test_size_cap_still_enforced():
    runner, base_url = await _start_server()

    conn = HTTPConnection(base_url, api_key="test-key",
                          max_response_size=64 * 1024)
    async with conn:
        with pytest.raises(AnhurError, match="maximum size"):
            await conn.post("/api/v1/search", {"text": "q", "sessions": ["*"]})
    await runner.cleanup()
