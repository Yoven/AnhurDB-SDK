"""update() puts metadata on the wire as the JSON STRING the server decodes.

A dict keeps ``None`` as null so a merge patch (AnhurDB ADR-0035) can delete
keys; a string travels byte-for-byte as before. Parity: Go
``client/record_metadata_patch_test.go``, TypeScript
``src/test/record_metadata_patch.test.ts`` — same cases, same expectations.
"""

import json
import os
import sys

from aiohttp import web
from aiohttp.test_utils import AioHTTPTestCase, unittest_run_loop

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb import AnhurQueryError  # noqa: E402  public export
from anhurdb.client import Memory  # noqa: E402


class TestUpdateMetadataWire(AioHTTPTestCase):
    async def get_application(self):
        app = web.Application()
        self.recorded_bodies = []

        async def record_patch(request):
            self.recorded_bodies.append(await request.text())
            return web.Response(
                text=json.dumps({"message": "record updated", "last_raft_index": 1}),
                content_type="application/json",
            )

        app.router.add_route("*", "/{tail:.*}", record_patch)
        return app

    def _client(self):
        return Memory(url=f"http://localhost:{self.server.port}", api_key="test-key")

    async def _patch_body(self, metadata_value):
        caller_fields = {"summary": "s", "metadata": metadata_value}
        async with self._client() as client:
            await client.update(42, **caller_fields)
        self.assertEqual(len(self.recorded_bodies), 1)
        wire_body = json.loads(self.recorded_bodies[0])
        self.assertEqual(wire_body["summary"], "s", "other fields must travel unchanged")
        self.assertIsInstance(
            wire_body["metadata"], str,
            "the server field is a string; a nested object is a 400",
        )
        return wire_body["metadata"]

    @unittest_run_loop
    async def test_dict_keeps_none_as_null_so_the_server_deletes_the_key(self):
        metadata_patch = {"keep": "x", "drop_me": None, "nested": {"inner": None}}
        wire_metadata = await self._patch_body(metadata_patch)
        self.assertEqual(json.loads(wire_metadata), metadata_patch)
        self.assertIn('"drop_me": null', wire_metadata)

    @unittest_run_loop
    async def test_empty_dict_is_sent_as_the_empty_object(self):
        wire_metadata = await self._patch_body({})
        self.assertEqual(json.loads(wire_metadata), {})

    @unittest_run_loop
    async def test_string_metadata_passes_through_byte_for_byte(self):
        wire_metadata = await self._patch_body('{"a":1,"b":null}')
        self.assertEqual(wire_metadata, '{"a":1,"b":null}')

    @unittest_run_loop
    async def test_unencodable_dict_is_refused_before_the_wire(self):
        async with self._client() as client:
            with self.assertRaises(AnhurQueryError) as raised:
                await client.update(42, metadata={"ratio": float("nan")})
        self.assertEqual(raised.exception.status_code, 400)
        self.assertIn("INVALID_PARAM: update: metadata cannot be encoded as JSON", str(raised.exception))
        self.assertEqual(self.recorded_bodies, [], "a refused dict must not reach the wire")


def test_caller_dict_is_not_mutated():
    from anhurdb.client.record_metadata_patch import encode_update_metadata

    caller_fields = {"metadata": {"drop_me": None}}
    encoded_fields = encode_update_metadata("update", caller_fields)
    assert isinstance(caller_fields["metadata"], dict)
    assert encoded_fields["metadata"] == '{"drop_me": null}'
