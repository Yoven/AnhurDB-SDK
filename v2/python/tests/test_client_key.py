"""ClientAPI (client-key surface, ADR-0034) against the fake in
``client_key_fake.py``. Same cases as Go ``client/client_key*_test.go`` and
TypeScript ``src/test/client_key.test.ts``."""

import asyncio
from datetime import datetime, timezone

import pytest
from aiohttp import web

from anhurdb import (
    AgentProfile,
    AnhurClientAPIError,
    AnhurClientAPIResponseError,
    ClientAPI,
    KeyScope,
)
from anhurdb.client.exceptions import AnhurConnectionError, AnhurError, AnhurQueryError
from client_key_fake import FakeClientServer

CHILD_ID = "9b1d2c3e-4f5a-4b6c-8d7e-0f1a2b3c4d5e"


@pytest.fixture
def anyio_backend():
    return "asyncio"


@pytest.fixture
async def fake():
    server = await FakeClientServer().start()
    yield server
    await server.stop()


def _api(fake: FakeClientServer) -> ClientAPI:
    return ClientAPI("client_key_secret", url=fake.base_url)


@pytest.mark.anyio
async def test_impersonate_child_wire(fake):
    async with _api(fake) as api:
        token = await api.impersonate_child(CHILD_ID, "geral", scope=KeyScope.WRITE, l1_only=True, ttl_seconds=900)
    sent = fake.requests[0]
    assert (sent["method"], sent["path"]) == ("POST", f"/api/v1/client/children/{CHILD_ID}/impersonate")
    assert sent["api_key"] == "client_key_secret" and not sent["has_tenant_header"]
    assert sent["body"] == {"tenant_id": "geral", "scope": "write", "l1_only": True, "expires_in": 900, "ttl_seconds": 900}
    assert (token.api_key, token.key_id, token.scope, token.l1_only, token.client_id) == (
        "anhur_secret", "ik_42", "write", True, CHILD_ID)
    assert token.expires_in == 900
    assert token.expires_at == datetime(2026, 11, 21, 13, 0, tzinfo=timezone.utc)
    assert "anhur_secret" not in repr(token)


@pytest.mark.anyio
async def test_plain_impersonate_omits_optional_fields(fake):
    async with _api(fake) as api:
        token = await api.impersonate("it-process")
    assert fake.requests[0]["path"] == "/api/v1/client/impersonate"
    assert fake.requests[0]["body"] == {"tenant_id": "it-process"}
    assert token.scope == "admin" and token.l1_only is False


@pytest.mark.anyio
@pytest.mark.parametrize("call", [
    lambda api: api.create_child("  "),
    lambda api: api.suspend_child("../tenants"),
    lambda api: api.list_child_tenants(""),
    lambda api: api.create_tenant(" "),
    lambda api: api.impersonate("*"),
    lambda api: api.impersonate("geral", scope="root"),
    lambda api: api.impersonate_child(CHILD_ID, "geral", scope=""),
    lambda api: api.impersonate("geral", ttl_seconds=-1),
    lambda api: api.create_tenant("geral", profile=AgentProfile(max_rpm=0)),
    lambda api: api.revoke_impersonation(""),
])
async def test_local_refusals_send_nothing(fake, call):
    async with _api(fake) as api:
        with pytest.raises(AnhurQueryError) as raised:
            await call(api)
    assert raised.value.status_code == 400 and raised.value.retryable is False
    assert str(raised.value).startswith("INVALID_PARAM: client API: ")
    assert fake.requests == []


_BASE_TOKEN = {"api_key": "anhur_secret", "key_id": "ik_42", "tenant_id": "geral", "kind": "impersonation",
               "expires_in": 900, "expires_at": "2026-11-21T13:00:00Z"}


@pytest.mark.anyio
@pytest.mark.parametrize("scope,l1_only,override,want_code", [
    ("write", True, {"scope": "write"}, "l1_only_not_honoured"),
    ("write", True, {"scope": "write", "l1_only": False}, "l1_only_not_honoured"),
    ("read_only", False, {"scope": "admin", "l1_only": False}, "scope_not_honoured"),
    ("write", False, {"l1_only": False}, "scope_not_honoured"),
    ("admin", False, {"scope": "write", "l1_only": False}, None),
    ("write", False, {"scope": "write", "api_key": ""}, "unexpected_response"),
])
async def test_fails_closed_on_weak_echo(fake, scope, l1_only, override, want_code):
    fake.impersonate_override = lambda body: dict(_BASE_TOKEN, **override)
    async with _api(fake) as api:
        if want_code is None:
            token = await api.impersonate_child(CHILD_ID, "geral", scope=scope, l1_only=l1_only)
            assert token.scope == "write"
            return
        with pytest.raises(AnhurClientAPIResponseError) as raised:
            await api.impersonate_child(CHILD_ID, "geral", scope=scope, l1_only=l1_only)
    assert raised.value.code == want_code and raised.value.retryable is False
    if want_code != "unexpected_response":
        assert raised.value.key_id == "ik_42"


@pytest.mark.anyio
async def test_create_child_idempotent(fake):
    async with _api(fake) as api:
        first = await api.create_child("3f2c9a1e-ref")
        second = await api.create_child("3f2c9a1e-ref")
        children = await api.list_children()
    assert first.created is True and second.created is False
    assert first.child_client_id == second.child_client_id
    assert fake.requests[0]["body"] == {"external_ref": "3f2c9a1e-ref"}
    assert [child.child_client_id for child in children] == [first.child_client_id]


@pytest.mark.anyio
async def test_create_child_tenant_sends_profile(fake):
    async with _api(fake) as api:
        tenant = await api.create_child_tenant(
            CHILD_ID, "contabilidade", description="agent",
            profile=AgentProfile(config={"search_top_k": "8"}, max_rpm=5000))
        empty_profile = await api.create_tenant("geral", profile=AgentProfile())
        await api.create_tenant("geral")
    assert fake.requests[0]["body"] == {"tenant_id": "contabilidade", "description": "agent",
                                        "profile": {"config": {"search_top_k": "8"}, "max_rpm": 5000}}
    assert tenant.max_rpm == 1200 and tenant.client_id == CHILD_ID
    assert tenant.composite_tenant == f"{CHILD_ID}_contabilidade"
    assert fake.requests[1]["body"] == {"tenant_id": "geral", "profile": {}} and empty_profile.max_rpm is None
    assert fake.requests[2]["body"] == {"tenant_id": "geral"}


@pytest.mark.anyio
async def test_suspend_resume_paths(fake):
    async with _api(fake) as api:
        child = await api.create_child("ref")
        suspended = await api.suspend_child(child.child_client_id)
        resumed = await api.resume_child(child.child_client_id)
        child_tenants = await api.list_child_tenants(child.child_client_id)
    assert (suspended.status, resumed.status) == ("suspended", "active")
    assert fake.requests[1]["path"] == f"/api/v1/client/children/{child.child_client_id}/suspend"
    assert fake.requests[2]["path"] == f"/api/v1/client/children/{child.child_client_id}/resume"
    assert child_tenants == ["contabilidade", "geral"]


@pytest.mark.anyio
async def test_plain_list_and_revoke_wire(fake):
    async with _api(fake) as api:
        tenant_ids = await api.list_tenants()
        revoked = await api.revoke_impersonation("ik_42")
        with pytest.raises(AnhurClientAPIError) as raised:
            await api.revoke_impersonation("ik_missing")
    assert tenant_ids == ["contabilidade", "geral"]
    assert (fake.requests[0]["method"], fake.requests[0]["path"]) == ("GET", "/api/v1/client/tenants")
    assert fake.requests[1]["body"] == {"key_id": "ik_42"} and revoked.revoked and revoked.key_id == "ik_42"
    assert raised.value.code == "key_not_found" and raised.value.status_code == 404 and raised.value.kind == "not_found"


_IMPERSONATE_PATH = f"POST /api/v1/client/children/{CHILD_ID}/impersonate"


@pytest.mark.anyio
@pytest.mark.parametrize("status,content_type,body,code,retryable,not_leader,message", [
    (403, "application/json", '{"error":"child_suspended","detail":"child client is suspended"}',
     "child_suspended", False, False,
     f"Client API error (HTTP 403) on {_IMPERSONATE_PATH}: child_suspended: child client is suspended"),
    (404, "application/json", '{"error":"child_not_found","detail":"x"}', "child_not_found", False, False, None),
    (429, "application/json", '{"error":"child_limit","detail":"x"}', "child_limit", False, False, None),
    (403, "text/plain", '{"error":"client API key required"}\n', "client API key required", False, False, None),
    (503, "application/json", '{"error":"not the leader (node-2)","error_type":"not_leader","detail":"retry"}',
     "not the leader (node-2)", True, True, None),
    (502, "text/html", "<html>bad gateway</html>", "", True, False,
     f"Client API error (HTTP 502) on {_IMPERSONATE_PATH}: <html>bad gateway</html>"),
])
async def test_error_code_exposed(fake, status, content_type, body, code, retryable, not_leader, message):
    fake.static_answer = web.Response(status=status, text=body, headers={"Content-Type": content_type})
    async with _api(fake) as api:
        with pytest.raises(AnhurClientAPIError) as raised:
            await api.impersonate_child(CHILD_ID, "geral", scope=KeyScope.WRITE)
    error = raised.value
    assert (error.code, error.status_code, error.retryable, error.is_not_leader) == (code, status, retryable, not_leader)
    assert isinstance(error, AnhurError)
    if message is not None:
        assert str(error) == message


@pytest.mark.anyio
async def test_construction_and_not_connected(fake):
    with pytest.raises(ValueError):
        ClientAPI("")
    with pytest.raises(ValueError):
        ClientAPI("bad\r\nkey")
    with pytest.raises(AnhurConnectionError):
        await _api(fake).list_tenants()


@pytest.mark.anyio
async def test_concurrent_use(fake):
    async with _api(fake) as api:
        tokens = await asyncio.gather(*[
            api.impersonate_child(CHILD_ID, f"tenant-{index}", scope=KeyScope.READ_ONLY) for index in range(16)])
    assert len(tokens) == 16 and len(fake.requests) == 16
