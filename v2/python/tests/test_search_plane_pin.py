"""The plane wrappers answer from the plane their NAME promises.

Junior Tip [why this file exists, 2026-09-06]: until this date
``search_tenant_shared(q, s, scope="client_shared")`` did not return
client_shared results and did not return tenant_shared results either — it
raised a bare ``TypeError: search() got multiple values for keyword argument
'scope'``, an error with no ``.kind`` and no ``.retryable``, outside the
``AnhurError`` contract every caller writes their handling against. Go silently
honoured the caller's scope and TypeScript silently honoured the pin: one knob,
three behaviours. Reading the three sources did not catch it — each looked
right on its own. Only the WIRE BODY is an honest witness, so every wrapper is
driven against a real aiohttp server here and the assertion is on the JSON that
actually left the process.
"""

import unittest
from typing import Any, Dict

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

from anhurdb import Memory
from anhurdb.client.session_filter import sessions_all


# A plane that is never the right answer for the wrappers under test that are
# not named for it, so a leak is unambiguous instead of accidentally equal to
# the expected value.
INTRUDER_SCOPE = "client_shared"

# wrapper method name -> the scope its NAME promises on the wire.
PLANE_WRAPPERS: Dict[str, str] = {
    "search_sessions": "sessions",
    "search_tenant_shared": "tenant_shared",
    "search_client_shared": "client_shared",
    "search_shared": "shared_all",
}


class TestPlaneWrapperScopePin(AioHTTPTestCase):
    """Every plane wrapper pins its own scope; a caller-supplied one is ignored."""

    async def get_application(self) -> web.Application:
        app = web.Application()
        app["captured"] = {}

        async def capture_search(request: web.Request) -> web.Response:
            body: Dict[str, Any] = await request.json()
            app["captured"] = body
            return web.json_response({"results": [], "scope": body.get("scope")})

        app.router.add_post("/api/v1/search", capture_search)
        return app

    async def _drive(self, wrapper_name: str, caller_scope: str) -> str:
        """Call one wrapper and return the scope that reached the wire."""
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="key", url=url, user_id="u1") as memory:
            wrapper = getattr(memory, wrapper_name)
            if caller_scope:
                await wrapper("q", sessions_all(), scope=caller_scope)
            else:
                await wrapper("q", sessions_all())
        return self.app["captured"].get("scope", "<absent>")

    @unittest_run_loop
    async def test_plane_wrappers_pin_scope_without_caller_scope(self) -> None:
        for wrapper_name, expected_scope in PLANE_WRAPPERS.items():
            with self.subTest(wrapper=wrapper_name, caller_scope=""):
                wire_scope = await self._drive(wrapper_name, "")
                print(
                    f'WIRE python | {wrapper_name:<20} | caller_scope="" '
                    f'| wire_scope="{wire_scope}"'
                )
                self.assertEqual(wire_scope, expected_scope)

    @unittest_run_loop
    async def test_plane_wrappers_pin_scope_over_caller_supplied_scope(self) -> None:
        """The pin WINS and the caller's scope is silently overridden, never raised."""
        for wrapper_name, expected_scope in PLANE_WRAPPERS.items():
            with self.subTest(wrapper=wrapper_name, caller_scope=INTRUDER_SCOPE):
                wire_scope = await self._drive(wrapper_name, INTRUDER_SCOPE)
                print(
                    f'WIRE python | {wrapper_name:<20} '
                    f'| caller_scope="{INTRUDER_SCOPE}" | wire_scope="{wire_scope}"'
                )
                self.assertEqual(
                    wire_scope,
                    expected_scope,
                    f"{wrapper_name} must pin {expected_scope!r}; a caller-supplied "
                    f"scope is overridden, and it must never raise a bare TypeError",
                )

    @unittest_run_loop
    async def test_other_knobs_still_reach_the_wire(self) -> None:
        """The scope pop must not eat the rest of the caller's kwargs.

        Junior Tip: ``kwargs.pop("scope", None)`` is a mutation of the caller's
        keyword bag. A pop that took the wrong key, or a future refactor that
        replaced it with a whole-bag rebuild, would silently drop ADR-0031 knobs
        — exactly the class of defect that made these wrappers forward
        ``**kwargs`` in the first place.
        """
        url = f"http://localhost:{self.server.port}"
        async with Memory(api_key="key", url=url, user_id="u1") as memory:
            await memory.search_tenant_shared(
                "q", sessions_all(), scope=INTRUDER_SCOPE, limit=7
            )
        captured = self.app["captured"]
        print(f"WIRE python | search_tenant_shared + limit=7 | body={captured}")
        self.assertEqual(captured.get("scope"), "tenant_shared")
        self.assertEqual(captured.get("limit"), 7)


if __name__ == "__main__":
    unittest.main()
