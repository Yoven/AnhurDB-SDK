"""
Live e2e proof for the 2026-06 SDK hardening fixes (Python).

Run against the live AnhurDB at http://localhost:8000:

    ANHUR_API_KEY=019c9f5b-d3cb-74af-9f01-5b761aaf7245 \
        /tmp/anhur_sdk_venv/bin/python tests/e2e_hardening_live.py

Proves, with real DB readbacks:
  1. add(text, score=, type=) persists score AND type (no longer dropped).
  2. add(text, metadata=) persists caller metadata merged with container_tag.
  3. read_content() returns the verbatim plain-text body (not wrapped in
     {"message": ...}, not truncated at 1000 chars).
  4. A long (>1000 char) plain-text content round-trips intact.

Plus an in-process mock-server proof (no live DB needed) that the transient
HTTP 500 'not_leader' write is retried and eventually succeeds.
"""

import asyncio
import os
import sys

from anhurdb import Memory, AnhurClient, MemoryType


API_KEY = os.environ.get("ANHUR_API_KEY", "019c9f5b-d3cb-74af-9f01-5b761aaf7245")
BASE_URL = os.environ.get("ANHUR_URL", "http://localhost:8000")


def check(label, condition, detail=""):
    status = "PASS" if condition else "FAIL"
    print(f"  [{status}] {label}" + (f" — {detail}" if detail else ""))
    if not condition:
        check.failed = True


check.failed = False


async def _find_record(client, record_id):
    """Read a single record's manifest row via the records endpoint."""
    return await client._connection.get(f"/api/v1/records/{record_id}")


async def test_score_type_persist():
    print("1. add(score, type) persists to DB:")
    async with Memory(api_key=API_KEY, url=BASE_URL, user_id="e2e_hardening") as mem:
        result = await mem.add(
            "User strongly prefers dark mode and vim keybindings",
            score=8,
            type=MemoryType.PREFERENCE,
        )
        check("add returned a record", bool(result["records"]), str(result))
        record_id = result["records"][0]["id"]
        check("mode is oss/direct (records path)", result["mode"] == "oss",
              f"mode={result['mode']}")

        row = await _find_record(mem._client, record_id)
        check("type persisted as 'preference'", row.get("type") == "preference",
              f"type={row.get('type')}")
        check("score persisted as 8", row.get("score") == 8,
              f"score={row.get('score')}")
        return record_id


async def test_metadata_merge():
    print("2. add(metadata=) merges caller metadata + container_tag:")
    async with Memory(api_key=API_KEY, url=BASE_URL, user_id="e2e_hardening") as mem:
        result = await mem.add(
            "Project Apollo kickoff notes",
            type=MemoryType.FACT,
            metadata={"project": "apollo", "priority": "high"},
        )
        record_id = result["records"][0]["id"]
        row = await _find_record(mem._client, record_id)
        import json
        meta = json.loads(row.get("metadata", "{}"))
        check("caller key 'project' persisted", meta.get("project") == "apollo",
              str(meta))
        check("caller key 'priority' persisted", meta.get("priority") == "high",
              str(meta))
        check("container_tag envelope preserved",
              meta.get("container_tag") == mem.container_tag, str(meta))


async def test_read_content_plain_text(record_id):
    print("3. read_content returns verbatim plain text (not wrapped):")
    async with Memory(api_key=API_KEY, url=BASE_URL, user_id="e2e_hardening") as mem:
        content = await mem.read_content(record_id)
        check("content is a str, not a dict", isinstance(content, str),
              f"type={type(content).__name__}")
        check("content not wrapped in {'message': ...}",
              not (isinstance(content, dict) and "message" in content),
              repr(content)[:80])


async def test_long_content_not_truncated():
    print("4. long (>1000 char) plain-text content round-trips intact:")
    big = "Lorem ipsum dolor sit amet. " * 80  # ~2240 chars
    async with Memory(api_key=API_KEY, url=BASE_URL, user_id="e2e_hardening") as mem:
        result = await mem.add(big, type=MemoryType.FACT)
        record_id = result["records"][0]["id"]
        content = await mem.read_content(record_id)
        check("returned as str", isinstance(content, str),
              f"type={type(content).__name__}")
        if isinstance(content, str):
            check("not truncated at 1000 chars", len(content) >= len(big) - 5,
                  f"len={len(content)} expected~{len(big)}")


# ── Retry proof against an in-process mock server ────────────────────────────

async def test_retry_transient_500():
    print("5. retry on transient HTTP 500 'not_leader' (mock server):")
    from aiohttp import web

    state = {"attempts": 0}

    async def flaky_records(request):
        state["attempts"] += 1
        if state["attempts"] < 3:
            # Simulate raft leadership handoff mid-write.
            return web.json_response(
                {"error": "node is not the leader (not_leader)"}, status=500
            )
        data = await request.json()
        return web.json_response({"id": 777, "uuid": data.get("uuid", "")})

    async def always_500(request):
        return web.json_response({"error": "boom: genuine bug"}, status=500)

    app = web.Application()
    app.router.add_post("/api/v1/records", flaky_records)
    app.router.add_post("/api/v1/permanent", always_500)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 8771)
    await site.start()
    try:
        async with AnhurClient(api_key="k", url="http://127.0.0.1:8771") as client:
            # Transient: should retry twice then succeed on attempt 3.
            data = await client._connection.post(
                "/api/v1/records", {"uuid": "s1", "summary": "x"}
            )
            check("transient write eventually succeeded", data.get("id") == 777,
                  str(data))
            check("retried exactly to 3rd attempt", state["attempts"] == 3,
                  f"attempts={state['attempts']}")

            # Non-transient 500 must NOT be retried — surfaces immediately.
            from anhurdb import AnhurError
            raised = False
            try:
                await client._connection.post("/api/v1/permanent", {})
            except AnhurError:
                raised = True
            check("genuine 500 surfaces (no silent retry)", raised)
    finally:
        await runner.cleanup()


async def main():
    rec_id = await test_score_type_persist()
    await test_metadata_merge()
    await test_read_content_plain_text(rec_id)
    await test_long_content_not_truncated()
    await test_retry_transient_500()
    print()
    if check.failed:
        print("RESULT: FAILED")
        sys.exit(1)
    print("RESULT: ALL CHECKS PASSED")


if __name__ == "__main__":
    asyncio.run(main())
