"""Delete-by-file answer models — ``DELETE /api/v1/records/by-file``.

One domain: what the server answers when the SDK erases every trace of one
ingested file (root + chapters + satellites): the counts, the zero-match
diagnostic (``FileSessionCandidate``) and the per-store legs of a
tenant-library delete (``DeleteFileLeg``).

Split out of ``models/record.py`` in 4.0.0 (house 300-line cut); still
re-exported from there.
"""

from typing import List

from pydantic import BaseModel, ConfigDict, Field


class DeleteFileResult(BaseModel):
    """
    Response of ``DELETE /api/v1/records/by-file`` — apagar TODO o rastro de um
    arquivo ingerido (root + capítulos + satélites) de uma sessão.

    ``matched_count`` é o que o prefixo ENCONTROU; ``deleted_count`` é o que o
    cluster realmente apagou. Em ``dry_run`` só ``matched_count`` é preenchido:
    nada foi escrito, então ``deleted_count`` fica 0 de propósito.

    Junior Tip [por que a contagem faz parte do contrato]: "apaguei 0 registros"
    tem de ser visível para o chamador. Um método que devolvesse apenas ``None``
    transformaria um prefixo errado em sucesso silencioso — e perda silenciosa é
    a falha número um deste projeto.

    ``deleted_ids`` chega com ``omitempty`` do servidor: em dry-run, ou quando
    nada casou, a chave não existe no JSON. Ausência aqui é informação
    legítima, não erro de parse — por isso tem default.

    ``alternative_sessions`` is filled ONLY when ``matched_count == 0``: the
    sessions of this SAME store that do hold records with the requested prefix
    (server ``database.FileSessionCandidate``,
    ``server/database/file_record_sessions.go:38-48``). Empty list = the prefix is
    not in this store at all; non-empty = the session was wrong — re-issue the
    strict delete with a candidate's ``session_uuid`` after checking its
    ``root_checksum``/``root_filename`` (a 64-bit prefix is weaker proof than the
    full sha256).

    There is no ``raft_index``: the server never serialises it (``json:"-"``
    at ``server/service/record_delete_file.go:42``), so the field removed in
    4.0.0 was always 0 — a number nobody had ever been sent.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    session_uuid: str = Field(default="")
    ingest_key_prefix: str = Field(default="")
    matched_count: int = Field(default=0)
    deleted_count: int = Field(default=0)
    deleted_ids: List[int] = Field(default_factory=list)
    dry_run: bool = Field(default=False)
    # True = gone for good (shared plane); False = restorable tombstone (chat
    # plane). Server: ``json:"purge"``, always sent — the caller must know
    # whether the delete can be undone.
    purge: bool = Field(default=False)
    alternative_sessions: List["FileSessionCandidate"] = Field(default_factory=list)
    # Present only for a tenant-library delete (one entry per library store,
    # generation "primary" = {c}_{t}_lib, "legacy" = pre-migration rows).
    legs: List["DeleteFileLeg"] = Field(default_factory=list)


class FileSessionCandidate(BaseModel):
    """One session of the queried store holding records of the file named by the
    ingest_key prefix (see ``DeleteFileResult.alternative_sessions``).

    ``root_checksum`` is the FULL sha256 the file_ingestor stores on the root
    record and ``root_filename`` its original name; both are ``""`` when the root
    no longer exists or predates the field (``omitempty`` on the server).
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    session_uuid: str = Field(default="")
    matched_count: int = Field(default=0)
    root_checksum: str = Field(default="")
    root_filename: str = Field(default="")


class DeleteFileLeg(BaseModel):
    """One library store's part of a delete-by-file answer.

    Server truth: ``service.DeleteFileLegOutcome``
    (``server/service/record_delete_file_library.go:34-44``).

    Junior Tip [why the legs matter]: during the L2 migration a library file can
    live in either store, and the two stores are two Raft groups with no shared
    transaction. ``skipped`` = the store does not exist on that node;
    ``dispatch_skipped`` = it held no match while the other leg did, so nothing
    was proposed there. A leg with ``error`` set failed — see
    ``AnhurLibraryDeletePartialError``.
    """

    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    generation: str = Field(default="")
    session_uuid: str = Field(default="")
    skipped: bool = Field(default=False)
    dispatch_skipped: bool = Field(default=False)
    matched_count: int = Field(default=0)
    deleted_count: int = Field(default=0)
    error: str = Field(default="")


DeleteFileResult.model_rebuild()
