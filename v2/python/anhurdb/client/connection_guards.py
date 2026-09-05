"""Transport guards: what the HTTP layer REFUSES, and why.

Domain, in one sentence: this module owns the two hard limits that stand
between a caller (or a server) and the connection — a header value that could
split the response, and a response body too large to hold in memory.

Junior Tip [why these left ``connection.py``, 2026-09-05]: ``connection.py``
had grown to 593 lines, far past this project's ~300-line cut, and adding one
more import pushed it further. The cut is not arithmetic: a file is one
responsibility a junior can name in one sentence. "Speak HTTP to AnhurDB" and
"refuse input that could hurt us" are two sentences, so they are two files. The
guards moved out first because they are the part with NO dependency on the
connection's state — they are pure functions over a value and a stream, which
makes them the only piece that can be tested without a server.
"""

import re
from typing import Any, Dict, Sequence, Tuple, Union

from .exceptions import AnhurError

# Query-string arguments accepted by the read verbs.
#
# Junior Tip [why a pair sequence is allowed alongside the mapping]: the
# ADR-0014 ``sessions`` filter is multi-valued and MUST reach the server as a
# repeated key (``?sessions=a&sessions=b``). A ``Dict[str, str]`` cannot express
# a repeated key, and joining the uuids with a separator would break the day a
# session id contains that character. ``urlencode`` accepts either shape as-is,
# so widening the type costs nothing and every existing dict call site keeps
# working untouched.
QueryParams = Union[Dict[str, str], Sequence[Tuple[str, str]]]

# Maximum response body size: 100 MB.
# Prevents memory exhaustion from malicious or misconfigured servers.
MAX_RESPONSE_SIZE = 100 * 1024 * 1024

# Regex for validating header values — rejects CRLF injection attempts.
_HEADER_SAFE = re.compile(r"^[\x20-\x7E]+$")

# The transport issues exactly ONE HTTP request per call and surfaces the
# outcome verbatim — no client-side retry loop.
# ClientSession. aiohttp merges session defaults into every request; a sticky
# application/json made FormData uploads arrive as JSON and AnhurDB returned
# HTTP 400 "failed to parse multipart form". REST JSON bodies still get the
# correct type via the ``json=`` kwarg on ``session.request``.


def validate_header_value(value: str, name: str) -> None:
    """Validate a string is safe to use as an HTTP header value.

    Rejects any string containing control characters (CR, LF, null)
    that could enable HTTP header injection (response splitting).

    Args:
        value: The header value to validate.
        name:  Human-readable field name for error messages.

    Raises:
        ValueError: If the value contains unsafe characters."""
    if not value:
        return
    if not _HEADER_SAFE.match(value):
        raise ValueError(
            f"{name} contains invalid characters for HTTP header. "
            f"Only printable ASCII (0x20-0x7E) is allowed."
        )


async def read_capped_body(response: Any, max_response_size: int) -> bytes:
    """Read the ENTIRE response body, enforcing the size cap.

    Args:
        response:          The live ``aiohttp`` response whose stream to drain.
        max_response_size: Byte ceiling; exceeding it raises instead of
            allocating further. Passed in rather than read off a connection so
            this stays a pure function the tests can drive with a fake stream.

    Returns:
        The complete body as bytes.

    Raises:
        AnhurError: when the body grows past ``max_response_size``.

    Junior Tip [o read(n) do aiohttp NAO le o corpo inteiro — incidente
    2026-07-30]: StreamReader.read(n) com n > 0 espera apenas o buffer
    interno ficar nao-vazio e devolve O QUE JA CHEGOU — nao o corpo
    completo. Em respostas que atravessam mais de um chunk TCP/TLS, o JSON
    vinha truncado em offset aleatorio, o parse falhava, e a busca devolvia
    [] SEM ERRO — perda silenciosa intermitente (5-7 de 20 execucoes no e2e
    scope-planes; o servidor respondia count:3 valido, provado por curl).
    Go e TS nunca tiveram o bug (leem ate EOF), por isso este fix e so do
    Python — a paridade aqui e de COMPORTAMENTO, nao de diff. Ler em loop
    ate EOF preserva o cap de seguranca sem depender do tamanho do primeiro
    chunk: read() devolve b"" somente no fim do stream.
    """
    received_body = bytearray()
    while True:
        body_chunk = await response.content.read(65536)
        if not body_chunk:
            return bytes(received_body)
        received_body.extend(body_chunk)
        if len(received_body) > max_response_size:
            raise AnhurError(
                f"Response exceeds maximum size ({max_response_size // (1024*1024)} MB)"
            )
