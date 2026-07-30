"""Durable on-disk queue for turns that could not reach AnhurDB.

A turn the plugin cannot persist is written to disk and retried on the next
turn — never dropped. This is the plugin's safety net, and the whole reason
it exists is the 2026-07-30 post-mortem: the previous safety net sat BEHIND
the same early-return that disabled writing, so it died together with the
thing it was supposed to protect. Here the queue depends on nothing but the
filesystem: no API key, no network, no SDK.

Standard library only, on purpose — see the Junior Tip at the top of
:mod:`config`.
"""

from __future__ import annotations

import itertools
import json
import logging
import os
import threading
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, List, Optional

logger = logging.getLogger(__name__)

# Envelope version. Written on every file so a future format change can be
# detected instead of silently mis-parsed.
QUEUE_ENVELOPE_VERSION = 1

_QUEUE_FILE_SUFFIX = ".json"

# Process-unique suffix source: two turns queued inside the same microsecond
# must never collide onto one filename.
#
# Junior Tip [colisão de nome É perda silenciosa — lição do plugin Go]: a
# versão antiga daquele plugin nomeava o arquivo com segundo + pid, e um
# persist multi-chunk (o caso NORMAL) sobrescrevia todos os pedaços menos o
# último. Um contador atômico custa nada e fecha a porta.
_queue_sequence = itertools.count()
_queue_sequence_lock = threading.Lock()


class QueueWriteError(RuntimeError):
    """Raised when a turn could NOT be written to disk — data is at risk."""


@dataclass(frozen=True)
class QueuedTurn:
    """One conversation turn waiting to be persisted."""

    session_id: str
    content: str
    queued_at: str
    path: Path


@dataclass
class BacklogSummary:
    """What is still stuck on disk after a drain attempt.

    Junior Tip [por que o backlog é DEVOLVIDO e não só logado]: um log
    registra, mas não alcança ninguém — nenhum humano faz tail de arquivo. O
    único canal que este plugin possui e que chega de fato a uma pessoa é o
    texto injetado no contexto do modelo (system prompt / prefetch). Por isso
    o resumo sobe até o provider, que o anuncia lá.
    """

    pending_count: int = 0
    oldest_pending: str = ""
    quarantined_count: int = 0
    oldest_quarantined: str = ""
    last_error: str = ""
    drained_count: int = 0

    @property
    def needs_human_attention(self) -> bool:
        return self.pending_count > 0 or self.quarantined_count > 0


@dataclass
class DurableTurnQueue:
    """Filesystem-backed queue of unpersisted turns.

    Layout::

        <queue_dir>/20260730T143012.004211-4711-3.json   # pending, retried
        <queue_dir>/quarantine/....json                  # NEVER retried

    Quarantine holds envelopes whose owning session cannot be proven. They are
    kept intact and never written anywhere, because writing them under any
    other session id would merge two conversations — see :meth:`quarantine`.
    """

    queue_dir: Path
    _write_lock: threading.Lock = field(default_factory=threading.Lock, repr=False)

    @property
    def quarantine_dir(self) -> Path:
        """Directory for envelopes with no provable session (never retried).

        It lives INSIDE ``queue_dir`` and :meth:`drain` skips directories, so a
        quarantined envelope can never be picked up by the retry loop again.
        """
        return self.queue_dir / "quarantine"

    # -- writing ------------------------------------------------------------

    def enqueue(self, session_id: str, content: str) -> Path:
        """Persist one turn to disk for later retry.

        Raises:
            QueueWriteError: when the envelope could not be written. The caller
                MUST log that loudly — at that point the turn exists only in
                memory and is about to be lost.
        """
        cleaned_session_id = (session_id or "").strip()
        if not cleaned_session_id:
            raise QueueWriteError(
                "refusing to enqueue a turn with no session id — use quarantine()"
            )
        envelope = {
            "version": QUEUE_ENVELOPE_VERSION,
            "session_id": cleaned_session_id,
            "content": content,
            "queued_at": _utc_now_iso(),
        }
        return self._write_envelope(self.queue_dir, envelope)

    def quarantine(self, content: str, reason: str) -> Optional[Path]:
        """Park a turn whose owning session cannot be proven. NEVER retried.

        Junior Tip [perder disponibilidade é aceitável; perder atribuição não
        é]: a regra "1 sessão do agente = 1 sessão AnhurDB" é inviolável. Se o
        turno não traz sessão, qualquer id que a gente escolhesse — o
        container, a sessão do turno anterior, a sessão "atual" do cliente —
        fundiria duas conversas em uma. Então o conteúdo fica intacto no disco,
        o erro sobe ALTO, e só um humano decide para onde ele vai.
        """
        envelope = {
            "version": QUEUE_ENVELOPE_VERSION,
            "session_id": "",
            "content": content,
            "queued_at": _utc_now_iso(),
            "quarantine_reason": reason,
        }
        try:
            return self._write_envelope(self.quarantine_dir, envelope)
        except QueueWriteError as write_error:
            logger.error(
                "anhurdb-memory: QUARANTINE WRITE FAILED (%s) — a turn is being "
                "lost right now: %s",
                reason,
                write_error,
            )
            return None

    def _write_envelope(self, target_dir: Path, envelope: dict) -> Path:
        with self._write_lock:
            sequence_number = next(_queue_sequence)
        # Junior Tip [o contador é ZERO-PADDED de propósito]: a ordem de drenagem
        # é a ordem alfabética dos nomes. Sem padding, "…-10" vem ANTES de "…-3"
        # e dois turnos gravados no mesmo microssegundo entrariam invertidos na
        # memória — um bug que só aparece sob rajada e é impossível de explicar
        # depois. Seis dígitos cobrem 1M de turnos enfileirados por processo.
        file_name = (
            f"{datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S.%f')}"
            f"-{os.getpid()}-{sequence_number:06d}{_QUEUE_FILE_SUFFIX}"
        )
        target_path = target_dir / file_name
        try:
            target_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
            # Atomic publish: write a sibling temp file and rename, so a crash
            # mid-write can never leave a half-envelope that drain() would
            # quarantine as corrupt.
            temporary_path = target_path.with_suffix(".tmp")
            temporary_path.write_text(
                json.dumps(envelope, ensure_ascii=False), encoding="utf-8"
            )
            os.chmod(temporary_path, 0o600)
            temporary_path.replace(target_path)
        except OSError as write_error:
            raise QueueWriteError(
                f"cannot write {target_path}: {type(write_error).__name__}: {write_error}"
            ) from write_error
        return target_path

    # -- reading / draining --------------------------------------------------

    def drain(self, send_turn: Callable[[str, str], None]) -> BacklogSummary:
        """Retry every pending turn, oldest first, and report what is left.

        ``send_turn(session_id, content)`` must raise on failure.

        Junior Tip [parar no primeiro erro preserva ordem E tempo]: se o
        AnhurDB está fora, insistir nos N arquivos custa N × timeout dentro do
        turno do usuário e não persiste nada mesmo. Parando na primeira falha,
        a fila mantém a ordem cronológica (nada é escrito "na frente" de um
        turno anterior) e o backlog restante continua contado e visível.
        """
        summary = BacklogSummary()
        pending_paths = self._sorted_files(self.queue_dir)
        transport_is_down = False

        for envelope_path in pending_paths:
            if transport_is_down:
                self._count_pending(summary, envelope_path)
                continue

            queued_turn = self._read_envelope(envelope_path)
            if queued_turn is None:
                # Unreadable or session-less: never guess an owner.
                self._quarantine_existing_file(
                    envelope_path, reason="unparseable or missing session_id"
                )
                continue

            try:
                send_turn(queued_turn.session_id, queued_turn.content)
            except Exception as send_error:  # noqa: BLE001 — reported, not swallowed
                summary.last_error = f"{type(send_error).__name__}: {send_error}"
                transport_is_down = True
                self._count_pending(summary, envelope_path)
                continue

            try:
                envelope_path.unlink()
                summary.drained_count += 1
            except OSError as unlink_error:
                # The turn IS persisted; failing to delete would replay it on
                # the next drain. Duplicate memory beats lost memory, but say so.
                logger.error(
                    "anhurdb-memory: persisted %s but could not remove it (%s) — "
                    "it will be re-sent on the next drain",
                    envelope_path,
                    unlink_error,
                )

        self._count_quarantined(summary)
        return summary

    def summarize(self) -> BacklogSummary:
        """Cheap, network-free view of what is stuck (used by prefetch)."""
        summary = BacklogSummary()
        for envelope_path in self._sorted_files(self.queue_dir):
            self._count_pending(summary, envelope_path)
        self._count_quarantined(summary)
        return summary

    # -- internals -----------------------------------------------------------

    def _sorted_files(self, directory: Path) -> List[Path]:
        """Return ``*.json`` files, oldest first.

        Names start with a lexicographically sortable UTC stamp, so sorting by
        name is sorting by time — no stat() calls, no clock skew from mtime.
        """
        try:
            entries = sorted(directory.iterdir())
        except (OSError, FileNotFoundError):
            return []
        return [
            entry
            for entry in entries
            if entry.is_file() and entry.name.endswith(_QUEUE_FILE_SUFFIX)
        ]

    def _read_envelope(self, envelope_path: Path) -> Optional[QueuedTurn]:
        try:
            raw_text = envelope_path.read_text(encoding="utf-8")
            envelope = json.loads(raw_text)
        except (OSError, ValueError) as read_error:
            logger.error(
                "anhurdb-memory: cannot read queued turn %s (%s)",
                envelope_path,
                read_error,
            )
            return None
        if not isinstance(envelope, dict):
            return None
        session_id = str(envelope.get("session_id", "") or "").strip()
        content = str(envelope.get("content", "") or "")
        if not session_id or not content:
            return None
        return QueuedTurn(
            session_id=session_id,
            content=content,
            queued_at=str(envelope.get("queued_at", "") or ""),
            path=envelope_path,
        )

    def _quarantine_existing_file(self, envelope_path: Path, reason: str) -> None:
        try:
            self.quarantine_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
            envelope_path.replace(self.quarantine_dir / envelope_path.name)
        except OSError as move_error:
            logger.error(
                "anhurdb-memory: quarantine move failed for %s (%s) — the turn "
                "stays in the queue and will be retried",
                envelope_path,
                move_error,
            )
            return
        logger.error(
            "anhurdb-memory: QUARANTINED %s (%s) — it will NOT be persisted "
            "automatically; only a human can decide which session it belongs to",
            envelope_path.name,
            reason,
        )

    def _count_pending(self, summary: BacklogSummary, envelope_path: Path) -> None:
        summary.pending_count += 1
        if not summary.oldest_pending:
            summary.oldest_pending = _queued_at_from_name(envelope_path.name)

    def _count_quarantined(self, summary: BacklogSummary) -> None:
        for quarantined_path in self._sorted_files(self.quarantine_dir):
            summary.quarantined_count += 1
            if not summary.oldest_quarantined:
                summary.oldest_quarantined = _queued_at_from_name(
                    quarantined_path.name
                )


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _queued_at_from_name(file_name: str) -> str:
    """Recover the UTC instant encoded in a queue filename.

    Falls back to the raw name when the format does not match, so a stray file
    can never cost us the warning itself.
    """
    stamp_end = file_name.find("-")
    if stamp_end <= 0:
        return file_name
    try:
        queued_at = datetime.strptime(file_name[:stamp_end], "%Y%m%dT%H%M%S.%f")
    except ValueError:
        return file_name
    return queued_at.replace(tzinfo=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
