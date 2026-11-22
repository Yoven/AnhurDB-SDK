"""X-Tenant-ID is refused at construction with the SDK's validation error.

Rule = the server's identifier rule (AnhurDB database.validateIdentifierCharset
+ MaxIdentifierLength): letters, digits, '-' and '_', at most 133. Messages are
pinned byte-for-byte with Go (client/tenant_header_test.go) and TypeScript
(src/test/tenant_id_validation.test.ts).
"""

import pytest

from anhurdb.client import Memory
from anhurdb.client.connection import HTTPConnection
from anhurdb.client.exceptions import AnhurQueryError

CHARSET_MESSAGE = (
    "INVALID_PARAM: tenant_id contains an invalid character — "
    "allowed: letters, digits, '-' and '_'"
)


@pytest.mark.parametrize(
    "tenant_id",
    ["", "1a99ce02-e9b6-442a-8c10-d67b9f3446b8_prod", "a" * 133, "1a99ce02-e9b6-442a-8c10-d67b9f3446b8_kaveo-veterinaria-vacinacao-4d7d5ed7_lib"],
)
def test_valid_tenant_ids_construct(tenant_id: str) -> None:
    connection = HTTPConnection("http://localhost:8080", "key", tenant_id=tenant_id)
    if tenant_id:
        assert connection.headers["X-Tenant-ID"] == tenant_id
    else:
        assert "X-Tenant-ID" not in connection.headers


@pytest.mark.parametrize(
    "tenant_id",
    ["acme/prod", "acme prod", "bad\r\nInjected: true", "bad\x00id", "açme"],
)
def test_invalid_charset_is_refused(tenant_id: str) -> None:
    with pytest.raises(AnhurQueryError) as raised:
        HTTPConnection("http://localhost:8080", "key", tenant_id=tenant_id)
    assert str(raised.value) == CHARSET_MESSAGE
    assert raised.value.kind == "invalid_request"
    assert raised.value.retryable is False


def test_over_long_tenant_is_refused() -> None:
    with pytest.raises(AnhurQueryError) as raised:
        HTTPConnection("http://localhost:8080", "key", tenant_id="a" * 134)
    assert str(raised.value) == "INVALID_PARAM: tenant_id exceeds 133 characters (got 134)"


def test_memory_refuses_invalid_tenant_at_construction() -> None:
    with pytest.raises(AnhurQueryError):
        Memory(api_key="key", url="http://localhost:8080", tenant_id="acme/prod")
