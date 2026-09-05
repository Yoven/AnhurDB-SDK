"""
The version of this SDK must be ONE number, and it must be the number that
travels on the wire (Python SDK, 2026-09-05).

Why this file exists: before 2.1.0 the package carried four mutually
inconsistent claims — ``pyproject.toml`` said 2.0.0, the ``User-Agent`` said
2.1, the README pinned a 2.0.12 wheel and the newest CHANGELOG heading said
2.0.2. Nothing failed, because nothing compared them. The release pipeline
rewrites ``pyproject.toml`` with ``sed`` (``.github/workflows/release-python.yml``),
so the manifest can be bumped without anyone touching ``anhurdb/version.py``;
this test is what turns that silent drift into a red build.

No network: the User-Agent assertion runs against an in-process aiohttp mock
server, the same pattern tests/test_http_mock.py uses.
"""

import os
import sys
import tomllib
import unittest
from pathlib import Path

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase

import anhurdb
from anhurdb.client import Memory
from anhurdb.version import USER_AGENT, __version__

PACKAGE_ROOT = Path(__file__).resolve().parent.parent


class TestVersionIsOneNumber(unittest.TestCase):
    """The manifest, the package attribute and the User-Agent must agree."""

    def test_pyproject_version_matches_package_version(self):
        with open(PACKAGE_ROOT / "pyproject.toml", "rb") as manifest_file:
            manifest = tomllib.load(manifest_file)
        manifest_version = manifest["tool"]["poetry"]["version"]
        self.assertEqual(
            manifest_version,
            __version__,
            "pyproject.toml and anhurdb/version.py disagree — the release "
            "pipeline rewrites the manifest, so bump anhurdb/version.py too.",
        )

    def test_package_exposes_dunder_version(self):
        self.assertEqual(anhurdb.__version__, __version__)
        self.assertIn("__version__", anhurdb.__all__)

    def test_user_agent_is_derived_not_retyped(self):
        self.assertEqual(USER_AGENT, f"AnhurSDK-Python/{__version__}")

    def test_py_typed_marker_is_present_and_declared(self):
        """PEP 561: without the marker file, every annotation in this SDK is
        invisible to the consumer's type checker — and without the manifest
        entry, the marker never reaches the wheel."""
        self.assertTrue((PACKAGE_ROOT / "anhurdb" / "py.typed").is_file())
        with open(PACKAGE_ROOT / "pyproject.toml", "rb") as manifest_file:
            manifest = tomllib.load(manifest_file)
        included_paths = [
            entry["path"] for entry in manifest["tool"]["poetry"].get("include", [])
        ]
        self.assertIn("anhurdb/py.typed", included_paths)


async def handle_health_capture(request):
    """Stashes the User-Agent header the SDK actually sent."""
    request.app["captured_user_agent"] = request.headers.get("User-Agent", "")
    return web.json_response({"status": "ok"})


class TestUserAgentReachesTheWire(AioHTTPTestCase):
    """A version constant nobody sends is not a version claim.

    Junior Tip: the assertion is on the SERVER side on purpose. Asserting on
    ``USER_AGENT`` alone would still pass if the header were dropped, renamed
    or overwritten by the session — which is exactly the class of defect an
    operator reading access logs would then be unable to explain.
    """

    async def get_application(self):
        app = web.Application()
        app.router.add_get("/api/v1/health", handle_health_capture)
        return app

    async def test_health_request_carries_the_versioned_user_agent(self):
        url = str(self.server.make_url("")).rstrip("/")
        async with Memory(api_key="test-key", url=url, user_id="u1") as memory_client:
            await memory_client.health()
        self.assertEqual(self.app["captured_user_agent"], USER_AGENT)
        self.assertTrue(self.app["captured_user_agent"].endswith(f"/{__version__}"))


if __name__ == "__main__":
    unittest.main()
