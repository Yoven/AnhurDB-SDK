"""The strict decode of the eight listing routes that always answer a JSON
object envelope (4.0.0).

One domain in one sentence: check that a 2xx answer is the route's envelope and
hand back its list, or raise ``AnhurUnexpectedResponseError``.

Routes and keys (server truth, AnhurDB main):

  GET /api/v1/sessions/stats         "sessions"  handler/record_session.go:135-142
  GET /api/v1/recent                 "records"   handler/record_session.go:455
  GET /api/v1/manifest               "records"   handler/search_aux.go:224
  GET /api/v1/chats/{uuid}/manifest  "records"   handler/record_session.go:305-311
  GET /api/v1/chats/{uuid}           "records"   handler/record_session.go:184
  GET /api/v1/entities               "entities"  handler/entity.go:254-257
  GET /api/v1/entities/list          "entities"  handler/entity.go:200-208
  GET /api/v1/records/{id}/entities  "entities"  handler/entity.go:561-565

The rule, identical in Go (``client/envelope.go``) and TypeScript
(``src/envelope.ts``): the body must be a JSON object that HAS the key, and the
key's value must be a list or null (null = zero rows, the server's own zero-hit
answer). Anything else — a bare array, an empty body (the transport turns it
into ``{}``), a missing key, a non-list value — raises.

Junior Tip [why "no rows" can never double as "I did not understand"]: 3.x
read a bare array or a non-dict as an empty page. An empty list is a
legitimate answer, so using it as the signal for an unrecognised shape made a
proxy page or a server bug read as "this tenant has no memories".
"""

from typing import Any, Dict, List

from .exceptions import AnhurUnexpectedResponseError


def require_envelope(data: Any, route: str, key: str) -> Dict[str, Any]:
    """Return ``data`` as the envelope dict, or raise.

    Args:
        data:  The decoded body as the transport returned it.
        route: The route TEMPLATE, for the cross-SDK message.
        key:   The list key the envelope must carry.

    Raises:
        AnhurUnexpectedResponseError: kind ``"server"``.
    """
    if not isinstance(data, dict) or key not in data:
        raise AnhurUnexpectedResponseError(route, key)
    if data[key] is not None and not isinstance(data[key], list):
        raise AnhurUnexpectedResponseError(route, key)
    return data


def envelope_list(data: Any, route: str, key: str) -> List[Any]:
    """The envelope's list (``[]`` when the server sent ``null``), or raise."""
    rows = require_envelope(data, route, key)[key]
    return list(rows) if rows is not None else []


__all__ = ["require_envelope", "envelope_list"]
