"""
Unit tests for the AnhurDB Python SDK client layer.

Tests constructor validation, header setup, session management,
and the Memory / AnhurClient facades — all without a running server.
"""

import unittest
import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb.client.connection import HTTPConnection
from anhurdb.client import Memory, AnhurClient
from anhurdb.client.exceptions import AnhurError, AnhurAuthError


class TestHTTPConnection(unittest.TestCase):
    """Tests for the HTTP connection layer."""

    def test_init_strips_trailing_slash(self):
        conn = HTTPConnection("http://localhost:8080/", "test_key")
        self.assertEqual(conn.base_url, "http://localhost:8080")

    def test_init_sets_x_api_key_header(self):
        """Must use X-API-Key (not Bearer) to match the server middleware."""
        conn = HTTPConnection("http://localhost:8080", "test_key")
        self.assertEqual(conn.headers["X-API-Key"], "test_key")
        self.assertNotIn("Authorization", conn.headers)

    def test_init_sets_user_agent(self):
        conn = HTTPConnection("http://localhost:8080", "test_key")
        self.assertIn("AnhurSDK-Python", conn.headers["User-Agent"])

    def test_init_sets_tenant_id(self):
        conn = HTTPConnection("http://localhost:8080", "key", tenant_id="t1")
        self.assertEqual(conn.headers["X-Tenant-ID"], "t1")

    def test_init_no_tenant_id(self):
        conn = HTTPConnection("http://localhost:8080", "key")
        self.assertNotIn("X-Tenant-ID", conn.headers)

    def test_mode_default_is_rest(self):
        conn = HTTPConnection("http://localhost:8080", "key")
        self.assertEqual(conn.mode, "rest")

    def test_mode_can_be_mcp(self):
        conn = HTTPConnection("http://localhost:8080", "key", mode="mcp")
        self.assertEqual(conn.mode, "mcp")

    def test_rejects_crlf_in_tenant_id(self):
        """Header injection protection: CRLF in tenant_id must raise."""
        with self.assertRaises(ValueError):
            HTTPConnection("http://localhost:8080", "key", tenant_id="bad\r\nInjected: true")

    def test_rejects_crlf_in_api_key(self):
        """Header injection protection: CRLF in api_key must raise."""
        with self.assertRaises(ValueError):
            HTTPConnection("http://localhost:8080", "bad\nkey")

    def test_rejects_null_byte_in_tenant_id(self):
        with self.assertRaises(ValueError):
            HTTPConnection("http://localhost:8080", "key", tenant_id="bad\x00id")


class TestMemory(unittest.TestCase):
    """Tests for the Memory (simple API) class."""

    def test_requires_api_key(self):
        with self.assertRaises(ValueError):
            Memory(api_key="")

    def test_env_fallback(self):
        """Falls back to ANHUR_API_KEY env var."""
        os.environ["ANHUR_API_KEY"] = "env-key-123"
        try:
            mem = Memory()
            self.assertIsNotNone(mem)
            self.assertIn("mem-", mem.container_tag)
        finally:
            del os.environ["ANHUR_API_KEY"]

    def test_container_tag_derived(self):
        """Container tag is derived from API key hash when no user_id."""
        mem = Memory(api_key="test-key-123")
        self.assertTrue(mem.container_tag.startswith("mem-"))
        self.assertEqual(len(mem.container_tag), 16)  # "mem-" + 12 hex

    def test_container_tag_deterministic(self):
        """Same key must always produce same container tag."""
        mem1 = Memory(api_key="deterministic-key")
        mem2 = Memory(api_key="deterministic-key")
        self.assertEqual(mem1.container_tag, mem2.container_tag)

    def test_container_tag_explicit_user_id(self):
        mem = Memory(api_key="key", user_id="custom-user")
        self.assertEqual(mem.container_tag, "custom-user")

    def test_session_id_starts_with_container_tag(self):
        mem = Memory(api_key="key", user_id="agent-x")
        self.assertTrue(mem.session_id.startswith("agent-x-"))

    def test_session_id_format(self):
        """Session UUID is container_tag-YYYYMMDD-HHMMSS-<6hex> (timestamp + random suffix)."""
        mem = Memory(api_key="key", user_id="u1")
        parts = mem.session_id.split("-", 1)
        self.assertEqual(parts[0], "u1")
        # Remainder is YYYYMMDD-HHMMSS-<6hex> — the 6-hex random suffix (2026-07-04 parity)
        # makes two sessions created in the same UTC second collision-safe.
        self.assertRegex(parts[1], r"^\d{8}-\d{6}-[0-9a-f]{6}$")

    def test_new_session_changes_uuid(self):
        mem = Memory(api_key="key", user_id="u1")
        old = mem.session_id
        import time
        time.sleep(1.1)  # timestamp granularity is 1 second
        new = mem.new_session()
        self.assertNotEqual(old, new)
        self.assertEqual(mem.session_id, new)

    def test_repr(self):
        mem = Memory(api_key="key", user_id="agent-x")
        r = repr(mem)
        self.assertIn("agent-x", r)
        self.assertIn("Memory(", r)


class TestAnhurClient(unittest.TestCase):
    """Tests for the AnhurClient (full API) class."""

    def test_requires_api_key(self):
        with self.assertRaises(ValueError):
            AnhurClient(api_key="")

    def test_default_url(self):
        client = AnhurClient(api_key="test-key")
        self.assertEqual(client._connection.base_url, "http://localhost:8080")

    def test_custom_url(self):
        client = AnhurClient(url="http://custom:9000", api_key="key")
        self.assertEqual(client._connection.base_url, "http://custom:9000")


if __name__ == "__main__":
    unittest.main()
