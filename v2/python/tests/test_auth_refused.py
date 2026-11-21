"""Every 401/403 on a data route arrives as ``AnhurAuthRefusedError`` through
the REAL transport (a genuine ``Memory`` call against a stub aiohttp server),
carrying the server's code, and stays an ``AnhurAuthError``. Bodies are the
ones AnhurDB sends (server/middleware/auth*.go, handler/service_bridge.go),
including the ``text/plain`` JSON body of ``http.Error``. Both transport paths
(JSON and multipart) are covered. gRPC is not: this SDK has no gRPC transport.
"""

import os
import sys

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb import AnhurAuthError, AnhurAuthRefusedError, AnhurClientAPIError  # noqa: E402
from anhurdb.client import Memory  # noqa: E402
from anhurdb.client.auth_errors import decode_error_envelope  # noqa: E402
from anhurdb.client.exceptions import AnhurError  # noqa: E402

PREDICATES = (
    "is_l1_only",
    "is_child_suspended",
    "is_insufficient_scope",
    "is_impersonation_required",
    "is_invalid_key",
)

# (route prefix, status, content type, body, code, detail, message, predicate)
CASES = [
    ("l1", 403, "application/json",
     '{"error":"l1_only","detail":"token restricted to L1 sessions"}',
     "l1_only", "token restricted to L1 sessions",
     "Authentication failed (HTTP 403): l1_only: token restricted to L1 sessions", "is_l1_only"),
    ("suspended", 403, "text/plain",
     '{"error":"child_suspended","detail":"client is suspended"}\n',
     "child_suspended", "client is suspended",
     "Authentication failed (HTTP 403): child_suspended: client is suspended", "is_child_suspended"),
    ("scope", 403, "text/plain",
     '{"error":"insufficient scope — key does not have permission for this operation"}\n',
     "insufficient scope — key does not have permission for this operation", "",
     "Authentication failed (HTTP 403): insufficient scope — key does not have permission for this operation",
     "is_insufficient_scope"),
    ("impersonate", 403, "text/plain",
     '{"error":"client_key_requires_impersonation"}\n',
     "client_key_requires_impersonation", "",
     "Authentication failed (HTTP 403): client_key_requires_impersonation", "is_impersonation_required"),
    ("badkey", 401, "text/plain",
     '{"error":"invalid or inactive API key"}\n',
     "invalid or inactive API key", "",
     "Authentication failed (HTTP 401): invalid or inactive API key", "is_invalid_key"),
    ("plain", 403, "text/plain", "Forbidden", "", "",
     "Authentication failed (HTTP 403)", None),
    ("badtype", 401, "application/json", '{"error":5}', "", "",
     "Authentication failed (HTTP 401)", None),
]


class TestAuthRefused(AioHTTPTestCase):
    async def get_application(self):
        app = web.Application()

        def responder(status, content_type, body):
            async def handle(_request):
                return web.Response(status=status, text=body, content_type=content_type)

            return handle

        for prefix, status, content_type, body, *_rest in CASES:
            handler = responder(status, content_type, body)
            app.router.add_post(f"/{prefix}/api/v1/records", handler)
            app.router.add_post(f"/{prefix}/api/v1/upload", handler)
        return app

    async def _refusal(self, prefix, multipart=False):
        url = f"http://localhost:{self.server.port}/{prefix}"
        async with Memory(url=url, api_key="test-key") as client:
            try:
                if multipart:
                    await client.upload_file("note.txt", b"hello")
                else:
                    await client.create("s1", "turn text")
            except AnhurError as exc:
                return exc
        self.fail("expected the 401/403 to raise")

    def _assert_case(self, exc, status, code, detail, message, predicate):
        self.assertIsInstance(exc, AnhurAuthRefusedError)
        self.assertIsInstance(exc, AnhurAuthError)  # backward compatible
        self.assertEqual(exc.status_code, status)
        self.assertEqual(exc.code, code)
        self.assertEqual(exc.detail, detail)
        self.assertEqual(str(exc), message)
        self.assertEqual(exc.kind, AnhurError.KIND_AUTH)
        self.assertFalse(exc.retryable)
        true_predicates = [name for name in PREDICATES if getattr(exc, name)]
        self.assertEqual(true_predicates, [predicate] if predicate else [])

    @unittest_run_loop
    async def test_json_path_is_typed(self):
        for prefix, status, _content_type, _body, code, detail, message, predicate in CASES:
            with self.subTest(prefix=prefix):
                exc = await self._refusal(prefix)
                self._assert_case(exc, status, code, detail, message, predicate)

    @unittest_run_loop
    async def test_multipart_path_is_typed(self):
        """The multipart path used to drop status_code (kind read "transport")."""
        for prefix, status, _content_type, _body, code, detail, message, predicate in CASES:
            with self.subTest(prefix=prefix):
                exc = await self._refusal(prefix, multipart=True)
                self._assert_case(exc, status, code, detail, message, predicate)


def test_envelope_keeps_code_when_optional_field_is_mistyped():
    """Shared with the client-key surface; parity with Go's field-by-field decode."""
    cases = [
        ('{"error":"l1_only","detail":5}', ("l1_only", "")),
        ('{"error":"x","detail":"d","error_type":{}}', ("x", "d")),
        ('{"error":""}', None),
        ('["l1_only"]', None),
        ("null", None),
        ("", None),
    ]
    for body, expected in cases:
        envelope = decode_error_envelope(body)
        got = None if envelope is None else (envelope.code, envelope.detail)
        assert got == expected, body
        client_error = AnhurClientAPIError.from_body(403, "POST", "/api/v1/client/impersonate", body)
        assert client_error.code == (expected[0] if expected else ""), body
