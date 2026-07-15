"""
Unit tests for the AnhurDB FileStorage layer.

Tests path construction, path traversal protection, and validation
of tenant_id/uuid parameters against directory escape attacks.
"""

import unittest
import os
import sys

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb.storage.filesystem import FileStorage


class TestFileStorage(unittest.TestCase):
    """Tests for FileStorage path construction and security."""

    def setUp(self):
        self.fs = FileStorage("/data/storage")

    def test_build_path_normal(self):
        """Normal inputs produce expected path."""
        path = self.fs.build_path("tenant_123", "sess_abc", 42)
        expected = os.path.realpath(
            os.path.join("/data/storage", "tenant_123", "sess_abc", "42.gz")
        )
        self.assertEqual(path, expected)

    def test_build_path_hex_tenant(self):
        """Hex-encoded tenant ID (real-world format) works."""
        path = self.fs.build_path("269EEF09ECADD91D", "35EC6D37DBE1582D", 317)
        self.assertIn("269EEF09ECADD91D", path)
        self.assertTrue(path.endswith("317.gz"))

    # ── Path traversal protection ──────────────────────────────

    def test_rejects_dotdot_in_tenant_id(self):
        """Directory traversal via tenant_id must raise ValueError."""
        with self.assertRaises(ValueError) as ctx:
            self.fs.build_path("../../../etc", "sess", 1)
        self.assertIn("..", str(ctx.exception))

    def test_rejects_dotdot_in_uuid(self):
        """Directory traversal via uuid must raise ValueError."""
        with self.assertRaises(ValueError) as ctx:
            self.fs.build_path("tenant", "../../etc/passwd", 1)
        self.assertIn("..", str(ctx.exception))

    def test_rejects_slash_in_tenant_id(self):
        """Forward slash in tenant_id must raise."""
        with self.assertRaises(ValueError):
            self.fs.build_path("tenant/evil", "sess", 1)

    def test_rejects_backslash_in_uuid(self):
        """Backslash in uuid must raise."""
        with self.assertRaises(ValueError):
            self.fs.build_path("tenant", "sess\\evil", 1)

    def test_rejects_null_byte(self):
        """Null byte in tenant_id must raise."""
        with self.assertRaises(ValueError):
            self.fs.build_path("tenant\x00evil", "sess", 1)

    def test_rejects_empty_tenant_id(self):
        with self.assertRaises(ValueError):
            self.fs.build_path("", "sess", 1)

    def test_rejects_empty_uuid(self):
        with self.assertRaises(ValueError):
            self.fs.build_path("tenant", "", 1)

    def test_rejects_negative_record_id(self):
        with self.assertRaises(ValueError):
            self.fs.build_path("tenant", "sess", -1)

    def test_rejects_leading_dot_in_tenant_id(self):
        """Leading dot could be used for hidden directories."""
        with self.assertRaises(ValueError):
            self.fs.build_path(".hidden", "sess", 1)

    def test_rejects_absolute_path_in_tenant_id(self):
        """Absolute paths must be rejected."""
        with self.assertRaises(ValueError):
            self.fs.build_path("/etc/passwd", "sess", 1)

    def test_accepts_hyphens_and_underscores(self):
        """Valid characters in path components."""
        path = self.fs.build_path("tenant-123", "sess_abc-def", 42)
        self.assertIn("tenant-123", path)
        self.assertIn("sess_abc-def", path)


if __name__ == "__main__":
    unittest.main()
