"""File upload: accept, poll, and wait.

One domain: ``POST /api/v1/upload`` and ``GET /api/v1/upload/{id}/status``,
plus the polling loop built on top of them. Split out of
``client/__init__.py`` in 3.0.0 when both returns became typed models; the
file was already past the 300-line house cut.
"""

import asyncio
from typing import Dict, Optional

from .connection import HTTPConnection
from .exceptions import AnhurQueryError, AnhurUploadWaitTimeout
from ..models.upload import UploadResult, UploadStatusResult


class UploadMixin:
    """Document upload and its asynchronous completion protocol."""

    _connection: HTTPConnection

    async def upload_file(
        self,
        filename: str,
        content: bytes,
        session_id: Optional[str] = None,
        linked_episodic_id: Optional[int] = None,
        mode: Optional[str] = None,
    ) -> UploadResult:
        """Upload a document for async ingestion.

        Supported formats: PDF, JPEG, PNG, WEBP, GIF, TXT, Markdown,
        HTML, DOCX.

        Planes:
            * ``mode="chat"`` (or ``session_id`` set) — attach to a chat
              session. Requires ``linked_episodic_id``; the file root hangs
              as a sub-tree of that episodic and the server sets
              ``has_file=true`` on it.
            * ``mode="tenant_shared"`` / ``mode="client_shared"`` — Shared Data
              (no session / episodic).

        The server processes the file asynchronously — use
        ``upload_status()`` to poll for completion.

        Args:
            filename: Original filename (used for format detection).
            content: Raw file bytes.
            session_id: From ``create_session()`` when uploading via chat.
            linked_episodic_id: Required for chat — episodic turn record id.
            mode: ``chat`` | ``tenant_shared`` | ``client_shared``.

        Returns:
            ``UploadResult`` (HTTP **202**, accepted-not-finished). Poll with
            ``record_id`` — there is no ``id`` field on this response, and a
            poll keyed on a phantom id 404s for a file that uploaded fine.

        Example::

            session_id = await mem.create_session()
            episodic = await mem.add("see attached report", mode="ingest",
                                     session_id=session_id)
            with open("report.pdf", "rb") as handle:
                result = await mem.upload_file(
                    "report.pdf", handle.read(),
                    session_id=session_id,
                    linked_episodic_id=episodic.records[0].id,
                )
            record_id = result.record_id"""
        extra: Dict[str, str] = {}
        resolved_mode = (mode or "").strip().lower()
        if session_id and not resolved_mode:
            resolved_mode = "chat"
        if resolved_mode == "chat":
            if not session_id:
                raise ValueError(
                    "session_id is required — create a session first "
                    "(await create_session())"
                )
            if linked_episodic_id is None or int(linked_episodic_id) <= 0:
                raise ValueError(
                    "linked_episodic_id is required for chat uploads — "
                    "attach the file to the episodic turn"
                )
            extra["mode"] = "chat"
            extra["session_id"] = session_id
            extra["linked_episodic_id"] = str(int(linked_episodic_id))
        elif resolved_mode in ("tenant_shared", "client_shared"):
            extra["mode"] = resolved_mode
        elif resolved_mode:
            raise ValueError(
                f"invalid mode {mode!r} "
                "(want chat|tenant_shared|client_shared)"
            )
        data = await self._connection.post_multipart(
            "/api/v1/upload",
            file_field="file",
            file_data=content,
            filename=filename,
            extra_fields=extra or None,
        )
        return UploadResult.model_validate(data if isinstance(data, dict) else {})

    async def upload_status(
        self,
        upload_id: int,
    ) -> UploadStatusResult:
        """Check the processing status of a file upload.

        Args:
            upload_id: The ``record_id`` returned by ``upload_file()``.

        Returns:
            ``UploadStatusResult``. ``status`` is one of ``processing`` /
            ``completed`` / ``saved`` / ``failed``; ``completed`` is the
            server's own terminal flag and is ``False`` for a ``failed``
            upload (failed is terminal for YOU, not "completed")."""
        data = await self._connection.get(
            f"/api/v1/upload/{upload_id}/status"
        )
        return UploadStatusResult.model_validate(data if isinstance(data, dict) else {})

    async def wait_for_upload(
        self,
        upload_id: int,
        timeout: float = 120.0,
        interval: float = 2.0,
        not_found_grace: float = 10.0,
    ) -> UploadStatusResult:
        """Poll ``upload_status`` until the upload reaches a terminal state.

        Junior Tip [por que 404 vira "pendente" no começo — medido
        2026-08-07]: as leituras do AnhurDB são load-balanced; logo após o
        202 de aceite, um nó que ainda não aplicou o Raft index devolve um
        404 legítimo por alguns segundos (read-your-writes). Dentro de
        ``not_found_grace`` esse 404 é tratado como "ainda não aplicado";
        depois dele, volta a ser erro de verdade.

        Args:
            upload_id: The ``record_id`` returned by ``upload_file()``.
            timeout: Total wait budget in seconds.
            interval: Seconds between polls.
            not_found_grace: How long an HTTP 404 counts as "not applied yet".

        Returns:
            The final ``UploadStatusResult`` — INCLUDING ``status="failed"``
            (a failed ingest is terminal data the caller must inspect, not a
            transport error).

            Junior Tip [a failed ingest is reported through ``status`` and
            ONLY through ``status``, 2026-09-14]: this loop used to also treat
            a truthy ``payload["error"]`` as terminal. The server has never
            sent an ``error`` key on this route — the body is a fixed seven-key
            map (``handler/upload.go:220-236``). That branch was unreachable,
            and reading it as a safety net hid the fact that the real failure
            signal was already in ``status``.

        Raises:
            AnhurUploadWaitTimeout: no terminal status within ``timeout``.
            AnhurQueryError: a 404 after ``not_found_grace``, or any other
                HTTP failure."""
        import time as _time

        started_at = _time.monotonic()
        last_status = "never-seen"
        while True:
            try:
                status_payload = await self.upload_status(upload_id)
            except AnhurQueryError as query_error:
                if query_error.status_code != 404:
                    raise
                if _time.monotonic() - started_at >= not_found_grace:
                    raise
                last_status = "not-found-yet"
            else:
                status_text = (status_payload.status or "").lower()
                if status_payload.completed or status_text in (
                    "completed",
                    "saved",
                    "done",
                    "failed",
                ):
                    return status_payload
                if status_text:
                    last_status = status_text

            if _time.monotonic() - started_at + interval > timeout:
                raise AnhurUploadWaitTimeout(
                    f"upload {upload_id} not terminal after {timeout}s "
                    f"(last={last_status})"
                )
            await asyncio.sleep(interval)


__all__ = ["UploadMixin"]
