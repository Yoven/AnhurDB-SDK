"""
Unit tests for the MCP tunnel transport (``HTTPConnection._mcp_tunnel``).

The MCP server closes every tool schema (ADR-0013 D1 strict params,
``additionalProperties: false``) and rejects any undeclared argument with
INVALID_PARAM. The REST payload for ``POST /api/v1/records`` carries
REST-only fields (``prefix`` / ``main_ids`` / ``consolidated`` /
``consolidate_id``) that the ``create_memory`` tool does not declare, so the
tunnel must project the payload down to the declared fields before sending.

These tests pin the projection contract without a running server by stubbing
``HTTPConnection._request`` and capturing the wire payload:
  1. Empty REST-only defaults are dropped (the historical AUTH/INVALID_PARAM
     100%-failure path becomes a clean call).
  2. Declared fields pass through untouched.
  3. A NON-empty undeclared field raises AnhurQueryError (fail loud — never
     silently lose caller data).
  4. Tools without an allowlist entry (``query``) are passed through
     unchanged (out of the projection's scope).
  5. The api_key never travels in the JSON body (header-only auth; the
     gateway injects it server-side).
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb.client.connection import HTTPConnection
from anhurdb.client.exceptions import AnhurQueryError
from anhurdb.models import CreateRequest, MemoryType


# The wire shape of a successful MCP direct response.
_MCP_OK_RESULT = {"content": [{"type": "text", "text": '{"id": 42, "status": "saved"}'}]}


def _tunnel_connection():
    """Build an MCP-mode connection whose ``_request`` records the payload."""
    connection = HTTPConnection("http://localhost:9092", "test_key", mode="mcp")
    captured = {}

    async def fake_request(method, path, params=None, body=None, raw_text=False):
        captured["method"] = method
        captured["path"] = path
        captured["body"] = body
        return _MCP_OK_RESULT

    connection._request = fake_request  # type: ignore[method-assign]
    return connection, captured


class TestMCPTunnelStrictProjection(unittest.IsolatedAsyncioTestCase):
    """The tunnel must send ONLY fields the tool schema declares."""

    async def test_create_memory_drops_empty_undeclared_defaults(self):
        """A full CreateRequest.model_dump() (the real REST payload) must
        tunnel WITHOUT the undeclared prefix/main_ids/consolidated/
        consolidate_id defaults that the strict schema rejects."""
        connection, captured = _tunnel_connection()
        rest_payload = CreateRequest(
            session_id="session-1",
            type=MemoryType.FACT,
            summary="s",
            content="c",
        ).model_dump(exclude_none=True)
        # Preconditions: the REST payload really does carry the offenders.
        for undeclared_field in ("prefix", "main_ids", "consolidated", "consolidate_id"):
            self.assertIn(undeclared_field, rest_payload)

        result = await connection.post("/api/v1/records", rest_payload)

        self.assertEqual(result, {"id": 42, "status": "saved"})
        sent_args = captured["body"]["args"]
        for undeclared_field in ("prefix", "main_ids", "consolidated", "consolidate_id"):
            self.assertNotIn(undeclared_field, sent_args)

    async def test_create_memory_keeps_declared_fields(self):
        connection, captured = _tunnel_connection()
        rest_payload = CreateRequest(
            session_id="session-1",
            type=MemoryType.FACT,
            summary="the summary",
            content="the content",
            score=8,
            related_ids=[7],
        ).model_dump(exclude_none=True)

        await connection.post("/api/v1/records", rest_payload)

        sent_args = captured["body"]["args"]
        self.assertEqual(captured["body"]["tool"], "create_memory")
        self.assertEqual(sent_args["session_id"], "session-1")
        self.assertEqual(sent_args["summary"], "the summary")
        self.assertEqual(sent_args["content"], "the content")
        self.assertEqual(sent_args["score"], 8)
        self.assertEqual(sent_args["related_ids"], [7])

    async def test_create_memory_rejects_non_empty_undeclared_field(self):
        """A caller-set REST-only value must fail LOUD, never be dropped."""
        connection, captured = _tunnel_connection()
        rest_payload = CreateRequest(
            session_id="session-1",
            content="c",
            main_ids=[1, 2],
        ).model_dump(exclude_none=True)

        with self.assertRaises(AnhurQueryError) as raised:
            await connection.post("/api/v1/records", rest_payload)
        self.assertIn("main_ids", str(raised.exception))
        # Nothing may have been sent.
        self.assertNotIn("body", captured)

    async def test_query_tool_args_pass_through_unchanged(self):
        """Tools without an allowlist entry are NOT projected."""
        connection, captured = _tunnel_connection()
        ast_payload = {"filters": {"type": {"$eq": "fact"}}, "pagination": {"limit": 5}}

        await connection.post("/api/v1/query", dict(ast_payload))

        self.assertEqual(captured["body"]["tool"], "query")
        self.assertEqual(captured["body"]["args"], ast_payload)

    async def test_api_key_never_in_tunnel_body(self):
        """Auth is header-only; the gateway injects args.api_key server-side."""
        connection, captured = _tunnel_connection()
        rest_payload = CreateRequest(session_id="session-1", content="c").model_dump(
            exclude_none=True
        )

        await connection.post("/api/v1/records", rest_payload)

        self.assertNotIn("api_key", captured["body"]["args"])


if __name__ == "__main__":
    unittest.main()
