"""delete_file — apagar TODO o rastro de um arquivo ingerido (paridade 3 SDKs).

O contrato deste endpoint é a URL: ``DELETE /api/v1/records/by-file`` com
``session``, ``ingest_key_prefix`` e ``dry_run`` na query. Por isso os testes
sobem um servidor real e asseguram o que chegou ao fio — não a intenção do
código. A contagem devolvida também é contrato: "apaguei 0" precisa ser
visível, nunca um sucesso mudo.

Mesmo desenho dos testes Go (httptest) e TypeScript (fetch stub).
"""
import pytest
from aiohttp import web

from anhurdb.client import Memory
from anhurdb.client.exceptions import AnhurQueryError


@pytest.fixture
def anyio_backend():
    return "asyncio"


async def _start_server(handler) -> tuple:
    """Sobe um servidor local em porta efêmera e devolve (runner, base_url)."""
    app = web.Application()
    app.router.add_delete("/api/v1/records/by-file", handler)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "127.0.0.1", 0)
    await site.start()
    port = site._server.sockets[0].getsockname()[1]
    return runner, f"http://127.0.0.1:{port}"


def _recording_handler(captured: dict, payload: dict, status: int = 200):
    """Handler que registra a query recebida e responde ``payload``."""

    async def handle(request: web.Request) -> web.Response:
        captured["method"] = request.method
        captured["path"] = request.path
        captured["session"] = request.query.get("session")
        captured["ingest_key_prefix"] = request.query.get("ingest_key_prefix")
        captured["dry_run"] = request.query.get("dry_run")
        return web.json_response(payload, status=status)

    return handle


@pytest.mark.anyio
async def test_delete_file_wire_contract():
    captured: dict = {}
    runner, base_url = await _start_server(
        _recording_handler(
            captured,
            {
                "session_uuid": "chat-42",
                "ingest_key_prefix": "ef9976f1ef5d5176",
                "matched_count": 511,
                "deleted_count": 511,
                "deleted_ids": [1, 2, 3],
                "dry_run": False,
                "purge": False,
            },
        )
    )
    try:
        async with Memory(api_key="test-key", url=base_url) as mem:
            result = await mem.delete_file("chat-42", "ef9976f1ef5d5176")
    finally:
        await runner.cleanup()

    assert captured["method"] == "DELETE"
    assert captured["path"] == "/api/v1/records/by-file"
    assert captured["session"] == "chat-42"
    assert captured["ingest_key_prefix"] == "ef9976f1ef5d5176"
    assert captured["dry_run"] == "false"

    # A contagem é a resposta ao usuário — perdê-la na decodificação devolveria
    # "sucesso" sem dizer o que aconteceu.
    assert result.matched_count == 511
    assert result.deleted_count == 511
    assert result.deleted_ids == [1, 2, 3]
    # raft_index is json:"-" on the server; the deprecated field stays 0.
    assert result.raft_index == 0
    assert result.dry_run is False


@pytest.mark.anyio
async def test_delete_file_dry_run_sends_flag_and_omits_ids():
    captured: dict = {}
    # Envelope de dry-run: o servidor manda deleted_ids com
    # ``omitempty``, então as chaves simplesmente não existem.
    runner, base_url = await _start_server(
        _recording_handler(
            captured,
            {
                "session_uuid": "chat-42",
                "ingest_key_prefix": "ef9976f1ef5d5176",
                "matched_count": 511,
                "deleted_count": 0,
                "dry_run": True,
            },
        )
    )
    try:
        async with Memory(api_key="test-key", url=base_url) as mem:
            result = await mem.delete_file(
                "chat-42", "ef9976f1ef5d5176", dry_run=True
            )
    finally:
        await runner.cleanup()

    assert captured["dry_run"] == "true"
    assert result.dry_run is True
    assert result.matched_count == 511
    assert result.deleted_count == 0
    assert result.deleted_ids == []
    assert result.raft_index == 0


@pytest.mark.anyio
async def test_delete_file_trims_arguments():
    captured: dict = {}
    runner, base_url = await _start_server(
        _recording_handler(captured, {"matched_count": 0, "deleted_count": 0})
    )
    try:
        async with Memory(api_key="test-key", url=base_url) as mem:
            await mem.delete_file("  chat-42 ", " ef9976f1ef5d5176\n")
    finally:
        await runner.cleanup()

    assert captured["session"] == "chat-42"
    assert captured["ingest_key_prefix"] == "ef9976f1ef5d5176"


@pytest.mark.anyio
@pytest.mark.parametrize(
    "session_uuid,ingest_key_prefix",
    [
        ("", "ef9976f1ef5d5176"),
        ("   ", "ef9976f1ef5d5176"),
        ("chat-42", ""),
        ("chat-42", "\t"),
    ],
)
async def test_delete_file_local_validation_never_hits_the_server(
    session_uuid, ingest_key_prefix
):
    server_was_called = {"hit": False}

    async def handle(request: web.Request) -> web.Response:
        server_was_called["hit"] = True
        return web.json_response({})

    runner, base_url = await _start_server(handle)
    try:
        async with Memory(api_key="test-key", url=base_url) as mem:
            with pytest.raises(ValueError):
                await mem.delete_file(session_uuid, ingest_key_prefix)
    finally:
        await runner.cleanup()

    assert server_was_called["hit"] is False


@pytest.mark.anyio
async def test_delete_file_short_prefix_surfaces_server_400():
    """A regra de tamanho/charset do prefixo vive no SERVIDOR (fonte única).
    O SDK precisa deixá-la subir como erro, nunca devolver resultado vazio."""
    captured: dict = {}
    runner, base_url = await _start_server(
        _recording_handler(
            captured,
            {"error": 'ingest key prefix "abc" is too short (minimum 8 characters)'},
            status=400,
        )
    )
    try:
        async with Memory(api_key="test-key", url=base_url) as mem:
            with pytest.raises(AnhurQueryError):
                await mem.delete_file("chat-42", "abc")
    finally:
        await runner.cleanup()

    assert captured["ingest_key_prefix"] == "abc"


# Byte-for-byte what service.DeleteFileResult marshals to when the prefix matched
# nothing in the requested session but exists in two others
# (server/service/record_delete_file.go:26-62; element type
# database.FileSessionCandidate, server/database/file_record_sessions.go:38-48).
# Same string as Go client/delete_file_alternatives_test.go.
ZERO_MATCH_SERVER_JSON = (
    '{"session_uuid":"chat-wrong","ingest_key_prefix":"ef9976f1ef5d5176",'
    '"matched_count":0,"deleted_count":0,"dry_run":false,"purge":false,'
    '"alternative_sessions":['
    '{"session_uuid":"chat-42","matched_count":511,'
    '"root_checksum":"ef9976f1ef5d51760a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",'
    '"root_filename":"lei-8666.pdf"},'
    '{"session_uuid":"chat-7","matched_count":3}]}'
)


def test_delete_file_result_parses_alternative_sessions_offline():
    from anhurdb import DeleteFileResult, FileSessionCandidate

    result = DeleteFileResult.model_validate_json(ZERO_MATCH_SERVER_JSON)
    assert result.matched_count == 0
    assert result.raft_index == 0
    assert result.alternative_sessions == [
        FileSessionCandidate(
            session_uuid="chat-42",
            matched_count=511,
            root_checksum="ef9976f1ef5d51760a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef",
            root_filename="lei-8666.pdf",
        ),
        FileSessionCandidate(session_uuid="chat-7", matched_count=3),
    ]
    assert result.alternative_sessions[1].root_checksum == ""


def test_delete_file_result_without_alternatives_is_empty_list():
    from anhurdb import DeleteFileResult

    result = DeleteFileResult.model_validate_json(
        '{"session_uuid":"chat-42","ingest_key_prefix":"ef9976f1ef5d5176",'
        '"matched_count":511,"deleted_count":511,"dry_run":false,"purge":false}'
    )
    assert result.alternative_sessions == []


@pytest.mark.parametrize("purge", [True, False])
def test_delete_file_result_parses_purge_offline(purge):
    from anhurdb import DeleteFileResult

    result = DeleteFileResult.model_validate_json(
        '{"session_uuid":"s_shared","ingest_key_prefix":"ef9976f1ef5d5176",'
        '"matched_count":2,"deleted_count":2,"deleted_ids":[7,8],"dry_run":false,'
        f'"purge":{"true" if purge else "false"}}}'
    )
    assert result.purge is purge
