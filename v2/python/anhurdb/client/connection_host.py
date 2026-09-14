"""What each transport mixin REQUIRES from the connection that hosts it.

Domain, in one sentence: the typed contract ``HTTPConnection`` owes to the three
behaviour mixins it composes — one protocol per mixin, listing only what that
mixin actually reads.

Junior Tip [why a contract instead of an annotation, 2026-09-14]: when
``connection.py`` was split (2026-09-05) the behaviour moved into mixins, but
the STATE stayed on the host. A mixin body writes ``self.base_url`` while
``self`` is, statically, just ``RequestExecutionMixin`` — which owns nothing —
so mypy reported 13 ``has no attribute`` errors and the SDK CI went red.

There are two ways to make that green, and only one of them is worth having:

  * Declare the attributes ON the mixin (``base_url: str`` in the class body).
    mypy goes quiet — and STAYS quiet the day someone deletes ``base_url`` from
    ``HTTPConnection``, because the mixin now swears the attribute exists. The
    check becomes decoration.
  * Declare them on a PROTOCOL and use it as the mixin's ``self`` type, which is
    what this file does. The claim being checked changes from "I have these" to
    "I am only ever mixed into something that has these", and mypy then verifies
    it from BOTH sides: the mixin body against the protocol here, and
    ``HTTPConnection`` against the protocol at the conformance proof at the
    bottom of ``connection.py``. Remove ``base_url`` from the host and that
    proof fails by name.

Junior Tip [why three protocols and not one]: precision is the whole point. The
MCP tunnel never touches ``api_key``; the multipart upload never touches
``_before_request``. A single fat ``ConnectionHost`` would let any mixin reach
for any piece of the connection without the type checker noticing that it had
quietly grown a new dependency on its host. One protocol per mixin keeps each
dependency list honest and small enough to read in one breath.
"""

from typing import Any, Dict, Optional, Protocol

import aiohttp

from .connection_guards import QueryParams


class RequestExecutionHost(Protocol):
    """What ``RequestExecutionMixin`` reads off the connection it lives in.

    Junior Tip [``_before_request`` is ``Optional[Any]`` on purpose]: it holds
    an async callable installed by the impersonation path, and the host stores
    it with exactly this type. A protocol member is INVARIANT — narrowing it
    here to ``Optional[Callable[[], Awaitable[None]]]`` would not tighten the
    host, it would simply stop matching it.
    """

    base_url: str
    _session: Optional[aiohttp.ClientSession]
    _max_response_size: int
    _before_request: Optional[Any]


class MultipartUploadHost(Protocol):
    """What ``MultipartUploadMixin`` reads off the connection it lives in.

    Junior Tip [why ``api_key``/``tenant_id`` and not ``headers``]: the upload
    rebuilds its headers from the raw credentials instead of reusing the
    session's, because a session-level ``Content-Type: application/json`` makes
    aiohttp send a FormData body as JSON and AnhurDB answers HTTP 400. The
    contract lists the raw fields because that is what the code touches — if the
    upload is ever rewritten to reuse ``headers``, this list must change with it.
    """

    base_url: str
    api_key: str
    tenant_id: str
    _session: Optional[aiohttp.ClientSession]
    _max_response_size: int


class McpTunnelHost(Protocol):
    """The connection as ``McpTunnelMixin`` sees it from the inside.

    ``_request`` is the part that comes from the HOST: the tunnel is not a
    second transport, it wraps a normal REST round trip to
    ``/api/v1/mcp/direct``. Delete ``_request`` from the composition and the
    conformance proof in ``connection.py`` fails.

    Junior Tip [why the two tables are listed here too]: typing ``self`` as this
    protocol REPLACES the mixin's static view of itself, so the tool map and the
    declared-argument table — which the mixin owns — have to be part of the
    shape as well, or the mixin could no longer read its own constants. They
    cost two lines and they do not weaken the check that matters: ``_request``
    is still only satisfiable by the host.
    """

    _MCP_TOOL_MAP: Dict[str, str]
    _MCP_TOOL_DECLARED_ARGS: Dict[str, frozenset]

    async def _request(
        self,
        method: str,
        path: str,
        body: Any = None,
        params: Optional[QueryParams] = None,
        raw_text: bool = False,
    ) -> Any:
        """One HTTP round trip, provided by ``RequestExecutionMixin``."""
        ...
