"""How ``update()`` puts ``metadata`` on the wire.

One domain in one sentence: turn the caller's ``metadata`` value into the JSON
STRING the server's ``PATCH /api/v1/records/{id}`` decodes, so a dict can carry
``None`` values (= delete that key) under the server's merge-patch contract.

Server contract (AnhurDB feat/metadata-merge-patch, ADR-0035): record update
metadata is an RFC 7396 JSON merge patch — present keys overwrite, a null value
deletes the key, absent keys are untouched, nested objects merge, arrays are
replaced whole. A patch that is not a JSON object is refused (400 /
InvalidArgument), and so is an update over stored metadata that is not a JSON
object. The REST handler decodes the field as a Go string
(``server/handler/record_crud.go``, ``Metadata string `json:"metadata"```), so
the body must hold a JSON-encoded string, never a nested object.

Parity: Go ``client/record_metadata_patch.go``, TypeScript
``src/recordMetadataPatch.ts`` — same accepted shapes, same refusal.
"""

import json
from collections.abc import Mapping
from typing import Any, Dict

from .exceptions import AnhurQueryError


def encode_update_metadata(method: str, fields: Dict[str, Any]) -> Dict[str, Any]:
    """Return the body ``update()`` should send.

    A ``metadata`` value that is a ``Mapping`` is JSON-encoded to a string with
    ``None`` kept as ``null``; every other value (a ``str``, as in 3.x/4.0.0, or
    anything else) is sent unchanged so the server stays the judge of it. The
    caller's dict is never mutated: a shallow copy is returned when the metadata
    value had to be encoded.

    Junior Tip [why the dict is encoded HERE and not left to the transport]: the
    transport serialises the whole body, which would put metadata on the wire as
    a nested JSON object. The server field is a string, so that body is a 400
    "invalid request body" — the dict form never worked before this change. The
    ``None`` values are the point of accepting a dict: under merge-patch a key
    the caller wants REMOVED must travel as null; dropping it would leave the
    stored key untouched and the caller would believe it was deleted.

    Raises:
        AnhurQueryError: HTTP-400-classified (invalid_request, not retryable)
            when the mapping cannot be encoded as JSON (a non-serialisable
            value, or NaN/Infinity, which JSON does not have) — before any
            request.
    """
    metadata_value = fields.get("metadata")
    if not isinstance(metadata_value, Mapping):
        return fields
    try:
        encoded_patch = json.dumps(dict(metadata_value), allow_nan=False)
    except (TypeError, ValueError) as encode_error:
        raise AnhurQueryError(
            f"INVALID_PARAM: {method}: metadata cannot be encoded as JSON: {encode_error}",
            status_code=400,
        ) from encode_error
    encoded_fields = dict(fields)
    encoded_fields["metadata"] = encoded_patch
    return encoded_fields


__all__ = ["encode_update_metadata"]
