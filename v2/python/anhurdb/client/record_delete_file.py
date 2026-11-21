"""Delete EVERY record of one ingested file (``DELETE /api/v1/records/by-file``).

One domain in one sentence: remove a file's whole trace (root, chapters,
satellites) from one plane, and report what each library store did.

Moved out of ``client/__init__.py`` (past the house 300-line cut) on
2026-11-20 when it gained ``scope`` and the per-leg answer of the L2 tenant
library (ADR-0029). Parity: Go ``client/delete_file.go``, TypeScript
``src/recordsApi.ts`` ``deleteFile``.

Junior Tip [two library stores, one call]: during the L2 migration a library
file can live in ``{c}_{t}_lib`` (new uploads) or in ``{c}_{t}`` (older ones).
With ``scope="tenant_shared"`` — or a library session uuid — the server runs
the delete in BOTH stores and returns ``legs``; a failure in one leg is HTTP
500 ``partial: true`` (``AnhurLibraryDeletePartialError``), never a silent
half-delete.
"""

from typing import Dict, Optional

from .connection import HTTPConnection
from .exceptions import AnhurQueryError
from .record_scope import SCOPE_CLIENT_SHARED, SCOPE_TENANT_SHARED
from ..models.record import DeleteFileResult


class DeleteFileMixin:
    """``delete_file`` — the whole-file delete."""

    _connection: HTTPConnection

    async def delete_file(
        self,
        session_uuid: str,
        ingest_key_prefix: str,
        *,
        dry_run: bool = False,
        scope: Optional[str] = None,
    ) -> DeleteFileResult:
        """Remove EVERY record produced by one ingested file inside a session —
        root, chapters and satellites — via ``DELETE /api/v1/records/by-file``.

        Junior Tip [por que este método existe — o delete que mentia]:
        ``delete(id)`` apaga UM record. Um arquivo ingerido vira centenas deles
        (root + capítulos + satélites), então apagar a ficha do arquivo deixava
        o CONHECIMENTO vivo: a busca continuava respondendo com o livro
        "apagado". Sem ``delete_file`` não existe "trocar a edição velha pela
        nova" — as duas coexistiriam para sempre, disputando a mesma busca.

        A identidade do arquivo é o prefixo ``<checksum16>`` que o file_ingestor
        grava em ``metadata.ingest_key`` (root: ``"<c16>:root"``; capítulo:
        ``"<c16>:<total>:<índice>"``) e em ``metadata.chapter_ingest_key``
        (satélite). Um prefixo alcança as três formas.

        A remoção é soft-delete REPLICADO (tombstone via Raft): o registro fica
        archived/status=deleted, some da busca e do tier analítico, e continua
        restaurável — a auditoria não é perdida.

        Junior Tip [dry_run é a rede de segurança, não um detalhe de debug]: com
        ``dry_run=True`` o servidor resolve o MESMO conjunto e devolve
        ``matched_count`` sem escrever nada. A interface mostra "isto vai apagar
        511 registros" ANTES de o usuário confirmar; apagar um documento inteiro
        é operação de mão pesada, e a contagem prévia é o que separa "removi a
        edição velha da lei" de "removi a biblioteca".

        Args:
            session_uuid:      Session that owns the file's records.
            ingest_key_prefix: The file's ``<checksum16>`` identity.
            dry_run:           Count only, write nothing (default: False).
            scope:             ``"tenant_shared"`` (the tenant library: the
                               server deletes in BOTH library stores and
                               fills ``legs``; ``session_uuid`` may then be
                               empty) or ``"client_shared"``. ``None`` = your
                               own store — passing a library session uuid
                               also targets the library.

        Returns:
            ``DeleteFileResult`` with ``matched_count`` / ``deleted_count`` —
            a contagem real, para que "apaguei 0" nunca se disfarce de sucesso.

        Raises:
            AnhurQueryError: (status 400, ``kind == "invalid_request"``) if
                             either argument is empty/blank or the scope is
                             not a delete-by-file plane (fails locally,
                             without a round trip); the session may be blank
                             only with ``scope="tenant_shared"``.
            AnhurLibraryDeletePartialError: HTTP 500 ``partial: true`` — one
                             library store failed; ``legs`` has every leg and
                             retrying the same call is safe (idempotent).
            AnhurTenantLibraryCollisionError: HTTP 409, not retryable.
            AnhurQueryError: HTTP 400 when the prefix is shorter than 8
                             characters or carries invalid characters — essa
                             regra vive no servidor, fonte única da verdade.

        Example::

            preview = await mem.delete_file(session, "ef9976f1ef5d5176", dry_run=True)
            if preview.matched_count and confirmed:
                await mem.delete_file(session, "ef9976f1ef5d5176")"""
        # Validação local mínima: o vazio nunca merece uma ida ao servidor, e o
        # erro local nomeia o argumento do SDK. Tamanho mínimo e conjunto de
        # caracteres do prefixo continuam a ser do servidor — uma fonte da
        # verdade só, para os três SDKs não divergirem dela.
        delete_scope = (scope or "").strip()
        if delete_scope not in ("", SCOPE_TENANT_SHARED, SCOPE_CLIENT_SHARED):
            # Junior Tip [one store per scope]: the server's delete-by-file
            # resolves only tenant_shared (two legs, handled together) and
            # client_shared (one store); anything else would delete from a
            # store the caller did not mean (handler/record_delete_file.go:88-104).
            raise AnhurQueryError(
                f"INVALID_PARAM: delete_file: scope {delete_scope!r} is not a "
                "delete-by-file plane (want tenant_shared|client_shared, or none)",
                status_code=400,
            )
        trimmed_session_uuid = (session_uuid or "").strip()
        if not trimmed_session_uuid and delete_scope != SCOPE_TENANT_SHARED:
            raise AnhurQueryError(
                "INVALID_PARAM: delete_file: session_uuid is required", status_code=400
            )
        trimmed_ingest_key_prefix = (ingest_key_prefix or "").strip()
        if not trimmed_ingest_key_prefix:
            raise AnhurQueryError(
                "INVALID_PARAM: delete_file: ingest_key_prefix is required",
                status_code=400,
            )

        delete_params: Dict[str, str] = {
            "ingest_key_prefix": trimmed_ingest_key_prefix,
            "dry_run": "true" if dry_run else "false",
        }
        if trimmed_session_uuid:
            delete_params["session"] = trimmed_session_uuid
        if delete_scope:
            delete_params["scope"] = delete_scope
        response = await self._connection.delete(
            "/api/v1/records/by-file", params=delete_params
        )
        return DeleteFileResult.model_validate(response or {})


__all__ = ["DeleteFileMixin"]
