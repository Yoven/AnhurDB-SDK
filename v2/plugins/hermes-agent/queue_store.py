"""A fila de memória não entregue, com ESTADO EXPLÍCITO (SQLite da stdlib).

Par exato de ``plugins/core/queue_store.go`` + ``queue_bridge.go``. Mesmos
estados, mesma ordem, mesmo backoff, mesma regra de quarentena — porque os dois
plugins prometem a mesma coisa ao usuário e uma divergência silenciosa entre eles
seria descoberta por um cliente, não por nós.

Junior Tip [por que sair do nome de arquivo]: a fila anterior guardava tudo no
NOME do arquivo (timestamp-pid-seq.json). Um turno que falhava ficava
indistinguível de um recém-chegado: sem estado, sem contagem de tentativas, sem
motivo. Foi assim que 52 chunks ficaram "still failing" indefinidamente do lado
do Claude, e assim que a rotação de chave de 2026-07-14 deixou órfãos que ninguém
conseguia diagnosticar. Com estado explícito, "o que está preso e por quê" vira
uma pergunta com resposta.

Junior Tip [sqlite3 é STDLIB, e isso não é detalhe]: o host do Hermes engole
falhas de import em ``logger.debug``. Um plugin que dependesse de um pacote
externo simplesmente não carregaria — em silêncio — e o usuário veria "sem
memória" sem nenhuma pista. Nada aqui importa fora da biblioteca padrão.

Junior Tip [o arquivo achatado continua sendo o PISO]: se o SQLite não abrir
(disco cheio, permissão, arquivo corrompido), tudo cai para o ``DurableTurnQueue``
de sempre. A lição de 2026-07-30 foi exatamente essa: a rede de segurança nunca
pode depender daquilo que ela protege — o arquivo lossless morreu porque estava
atrás da mesma guarda que deveria compensar.
"""

from __future__ import annotations

import json
import logging
import sqlite3
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Callable, List, Optional

from .memory_queue import BacklogSummary, DurableTurnQueue, QueueWriteError

logger = logging.getLogger(__name__)

# Estados. Deliberadamente poucos: cada estado extra é uma transição a mais para
# alguém errar.
STATE_PENDING = "pending"
STATE_SENDING = "sending"
STATE_QUARANTINED = "quarantined"

# Junior Tip [por que existe SENDING]: o processo pode morrer no meio do envio.
# Sem esta reivindicação, um turno marcado "sending" no instante da morte ficaria
# preso para sempre — o modo de falha que esta tabela existe para acabar.
SENDING_TIMEOUT = timedelta(minutes=5)

# Teto do adiamento entre tentativas. Sem ele, um endpoint fora do ar leva o
# plugin a martelar a rede a cada turno. O que NUNCA acontece é desistir: não há
# limite de tentativas, porque "tentei demais" jamais pode virar "joguei fora".
MAX_BACKOFF = timedelta(minutes=30)

# Teto de itens por dreno, para o dreno não virar operação ilimitada dentro do
# turno do usuário. Quando é atingido, o excedente é REGISTRADO (nunca truncado
# em silêncio) e sai no dreno seguinte.
DRAIN_LIMIT = 500

_SCHEMA = """
CREATE TABLE IF NOT EXISTS pending_memory (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id       TEXT    NOT NULL,
    content          TEXT    NOT NULL,
    state            TEXT    NOT NULL DEFAULT 'pending',
    retry_count      INTEGER NOT NULL DEFAULT 0,
    last_error       TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL,
    next_attempt_at  TEXT    NOT NULL,
    state_changed_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pending_drain
    ON pending_memory(state, next_attempt_at, created_at);
"""


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


def _stamp(instant: datetime) -> str:
    return instant.astimezone(timezone.utc).isoformat()


def _readable_stamp(stored: str) -> str:
    """Reduz o carimbo interno ao formato que o usuário lê.

    Um carimbo ilegível volta como está: a data é detalhe do aviso, e nunca pode
    custar o aviso em si.
    """
    if not stored:
        return ""
    try:
        return (
            datetime.fromisoformat(stored)
            .astimezone(timezone.utc)
            .strftime("%Y-%m-%dT%H:%M:%SZ")
        )
    except ValueError:
        return stored


def backoff_for_attempt(attempt_number: int) -> timedelta:
    """Dobra a espera a cada tentativa, com teto."""
    exponent = max(0, min(attempt_number - 1, 12))
    candidate = timedelta(minutes=2**exponent)
    return candidate if candidate < MAX_BACKOFF else MAX_BACKOFF


class StateQueue:
    """Fila com estado explícito, com o ``DurableTurnQueue`` como piso.

    Apresenta a MESMA interface que o ``DurableTurnQueue`` (``enqueue``,
    ``quarantine``, ``drain``, ``summarize``), de modo que o provider não precisa
    saber qual das duas está ativa — só que a memória não se perde.
    """

    def __init__(self, queue_dir: Path) -> None:
        self.queue_dir = queue_dir
        self.database_path = queue_dir.parent / "queue.db"
        # O piso é construído SEMPRE, não sob demanda: um caminho de recuperação
        # que só nasce no momento da falha é um caminho que nunca foi exercitado.
        self.floor = DurableTurnQueue(queue_dir=queue_dir)
        self._connection: Optional[sqlite3.Connection] = None
        self._open_error: Optional[Exception] = None
        self._open()

    # -- ciclo de vida -------------------------------------------------------

    def _open(self) -> None:
        try:
            self.database_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            # check_same_thread=False: o provider chama de threads diferentes do
            # runtime; todo acesso aqui é serializado pelo próprio SQLite.
            connection = sqlite3.connect(
                str(self.database_path), check_same_thread=False, timeout=5.0
            )
            # WAL sobrevive melhor a morte abrupta; FULL garante que cada COMMIT
            # chegue ao disco antes de retornar — a durabilidade não pode depender
            # de um desligamento limpo, porque ele é justamente o que não acontece.
            connection.execute("PRAGMA journal_mode=WAL")
            connection.execute("PRAGMA synchronous=FULL")
            connection.executescript(_SCHEMA)
            connection.commit()
            self._connection = connection
            self.database_path.chmod(0o600)
            self._migrate_legacy_files()
        except (sqlite3.Error, OSError) as open_error:
            self._connection = None
            self._open_error = open_error
            logger.error(
                "anhurdb-memory: state queue unavailable (%s) — falling back to "
                "flat files; memory is still safe",
                open_error,
            )

    @property
    def available(self) -> bool:
        return self._connection is not None

    @property
    def quarantine_dir(self) -> Path:
        """Mantido para compatibilidade com quem inspeciona o piso."""
        return self.floor.quarantine_dir

    def close(self) -> None:
        if self._connection is not None:
            self._connection.close()
            self._connection = None

    # -- migração ------------------------------------------------------------

    def _migrate_legacy_files(self) -> None:
        """Move os envelopes .json antigos para a fila com estado.

        Junior Tip [o arquivo só some DEPOIS de o novo lar estar durável]: a ordem
        é insere-commita-e-só-então-remove. Invertida, uma falha entre as duas
        operações apagaria a única cópia. Nada é abandonado: o que não migrar fica
        no disco e o piso continua sabendo drená-lo.
        """
        migrated_count = 0
        for envelope_path in self.floor._sorted_files(self.queue_dir):
            queued_turn = self.floor._read_envelope(envelope_path)
            if queued_turn is None:
                # Junior Tip [preservar o CONTEÚDO, não o invólucro, 2026-07-31]:
                # ``_read_envelope`` devolve None em DOIS casos distintos — o arquivo
                # não é JSON válido, ou é válido mas não tem sessão. No segundo, o
                # conteúdo da conversa está ali, legível, e guardar o JSON inteiro
                # obrigaria quem for resolver a quarentena a desembrulhar o envelope à
                # mão para reconhecer a origem. Só quando nem parsear dá é que o texto
                # bruto vira o melhor registro possível — aí ele é tudo o que temos.
                # (O par Go grava .txt puro, então lá o conteúdo já é o conteúdo; foi o
                # harness de paridade que expôs a diferença.)
                try:
                    raw_content = envelope_path.read_text(encoding="utf-8")
                except OSError:
                    continue
                quarantined_content = raw_content
                quarantine_reason = "unparseable envelope (migrated from flat file)"
                try:
                    parsed_envelope = json.loads(raw_content)
                    if isinstance(parsed_envelope, dict) and isinstance(
                        parsed_envelope.get("content"), str
                    ):
                        quarantined_content = parsed_envelope["content"]
                        quarantine_reason = (
                            "no session id in envelope (migrated from flat file)"
                        )
                except (ValueError, TypeError):
                    pass
                self._insert(
                    "(unknown)",
                    quarantined_content,
                    STATE_QUARANTINED,
                    quarantine_reason,
                    _utc_now(),
                )
            else:
                # O instante vem do ENVELOPE, não de agora: os turnos são pedaços de
                # conversa e o dreno os entrega por created_at. Carimbar tudo com o
                # instante da migração embaralharia turnos que estavam corretos.
                self._insert(
                    queued_turn.session_id,
                    queued_turn.content,
                    STATE_PENDING,
                    "",
                    _parse_queued_at(queued_turn.queued_at),
                )
            try:
                envelope_path.unlink()
            except OSError as unlink_error:
                logger.warning(
                    "anhurdb-memory: migrated %s but could not remove it (%s) — "
                    "it may be migrated again and duplicate",
                    envelope_path,
                    unlink_error,
                )
            migrated_count += 1
        if migrated_count:
            logger.info(
                "anhurdb-memory: migrated %d queued turn(s) into the state queue",
                migrated_count,
            )

    # -- escrita -------------------------------------------------------------

    def _insert(
        self,
        session_id: str,
        content: str,
        state: str,
        reason: str,
        created_at: datetime,
    ) -> None:
        assert self._connection is not None
        created_stamp = _stamp(created_at)
        self._connection.execute(
            "INSERT INTO pending_memory (session_id, content, state, last_error,"
            " created_at, next_attempt_at, state_changed_at)"
            " VALUES (?, ?, ?, ?, ?, ?, ?)",
            (
                session_id,
                content,
                state,
                reason[:300],
                created_stamp,
                created_stamp,
                _stamp(_utc_now()),
            ),
        )
        self._connection.commit()

    def enqueue(self, session_id: str, content: str) -> None:
        """Guarda um turno não entregue. Sessão vazia é recusada ALTO."""
        cleaned_session_id = (session_id or "").strip()
        if not cleaned_session_id:
            raise QueueWriteError(
                "refusing to enqueue a turn with no session id — use quarantine()"
            )
        if not self.available:
            self.floor.enqueue(cleaned_session_id, content)
            return
        try:
            self._insert(cleaned_session_id, content, STATE_PENDING, "", _utc_now())
        except sqlite3.Error as insert_error:
            logger.error(
                "anhurdb-memory: state queue write failed (%s) — using flat file",
                insert_error,
            )
            self.floor.enqueue(cleaned_session_id, content)

    def quarantine(self, content: str, reason: str) -> None:
        """Guarda um turno cuja sessão não pôde ser provada. NUNCA reenviado.

        Junior Tip [perder disponibilidade é aceitável; perder atribuição não é]:
        a regra "1 sessão do agente = 1 sessão AnhurDB" é inviolável. Sem sessão,
        qualquer id que escolhêssemos — o container, a sessão do turno anterior, a
        sessão "atual" do cliente — fundiria duas conversas. O conteúdo fica
        intacto, o erro sobe ALTO, e só um humano decide para onde ele vai.
        """
        if not self.available:
            self.floor.quarantine(content, reason)
            return
        try:
            self._insert("(unknown)", content, STATE_QUARANTINED, reason, _utc_now())
        except sqlite3.Error as insert_error:
            logger.error(
                "anhurdb-memory: quarantine write failed (%s) — using flat file",
                insert_error,
            )
            self.floor.quarantine(content, reason)

    # -- dreno ---------------------------------------------------------------

    def drain(self, send_turn: Callable[[str, str], None]) -> BacklogSummary:
        """Entrega o que está represado, em ordem, e devolve o que continua preso.

        ``send_turn(session_id, content)`` deve levantar exceção em caso de falha.

        Junior Tip [bloqueio POR SESSÃO, não global]: dentro de uma sessão a ordem
        É a conversa — entregar o turno 7 antes do 5 escreve a conversa errada e
        nenhuma consolidação conserta. Entre sessões diferentes não há ordem a
        preservar, e bloquear tudo seria pior: uma rejeição permanente numa sessão
        (HTTP 409 "session has reached the maximum of 500 records" — o caso real de
        2026-07-03) travaria a fila inteira para sempre.
        """
        if not self.available:
            return self.floor.drain(send_turn)

        summary = BacklogSummary()
        claimed_items = self._claim_batch(DRAIN_LIMIT)
        if len(claimed_items) == DRAIN_LIMIT:
            logger.info(
                "anhurdb-memory: drain hit the %d-item limit; the remainder goes "
                "out on the next turn",
                DRAIN_LIMIT,
            )

        blocked_sessions: set = set()
        for item_id, session_id, content, retry_count in claimed_items:
            if session_id in blocked_sessions:
                # Soltar sem punir: este turno nem chegou a ser tentado. Marcá-lo
                # como falha inflaria o backoff por um erro que não foi dele.
                self._set_state(item_id, STATE_PENDING)
                continue
            try:
                send_turn(session_id, content)
            except Exception as send_error:  # noqa: BLE001 — reportado, não engolido
                summary.last_error = f"{type(send_error).__name__}: {send_error}"
                blocked_sessions.add(session_id)
                self._mark_failed(item_id, retry_count, summary.last_error)
                continue
            self._delete(item_id)
            summary.drained_count += 1

        self._fill_summary(summary)
        # "Assim que voltar, descarregar tudo que tem local e limpar o local pra não
        # ter risco" — depois de o backlog já ter sido lido, para a limpeza nunca
        # apagar o próprio aviso.
        if summary.pending_count == 0 and summary.quarantined_count == 0:
            self.purge_if_drained(announce=summary.drained_count > 0)
        return summary

    def _claim_batch(self, maximum_items: int) -> List[tuple]:
        assert self._connection is not None
        self._reclaim_stale_sending()
        now_stamp = _stamp(_utc_now())
        rows = self._connection.execute(
            "SELECT id, session_id, content, retry_count FROM pending_memory"
            " WHERE state = ? AND next_attempt_at <= ?"
            " ORDER BY created_at ASC, id ASC LIMIT ?",
            (STATE_PENDING, now_stamp, maximum_items),
        ).fetchall()
        for row in rows:
            self._set_state(row[0], STATE_SENDING)
        return rows

    def _reclaim_stale_sending(self) -> None:
        """Devolve para pending o que ficou em "sending" além do timeout.

        Junior Tip [pode reenviar, e o que REALMENTE acontece então — corrigido
        2026-07-31]: se o processo morreu DEPOIS de o servidor gravar mas ANTES de
        marcar entregue, isto reenvia. Este comentário dizia que "a consolidação
        deduplica por checksum" e isso era FALSO: aquele checksum
        (AnhurAgents/cmd/consolidation/source_hash.go) existe para PULAR uma chamada
        de LLM quando o conjunto-fonte não mudou — ele nunca funde dois registros.

        A proteção real mora no servidor: CreateRecord tem idempotência por
        (uuid, type, checksum) dentro de ANHUR_DEDUP_WINDOW (30s por padrão) e o
        /ingest passa por ela (service/ingest.go:157). Isso cobre a rajada — a
        retentativa em voo de deliver_with_retry, com 0,3s/1,2s de espera, cai dentro
        da janela. NÃO cobre este caminho: um reenvio da fila chega minutos depois,
        fora dos 30s, e o servidor o trata como evento distinto legítimo, por decisão
        explícita dele. Medição de 2026-07-31 no banco real: 3 chunks byte-idênticos
        duplicados em 120, com 17m e 24m de intervalo — esta assinatura.

        A escolha continua certa: duplicar é recuperável, perder não é. Mas agora
        está registrada pelo que é — dívida conhecida, não problema resolvido por
        outro. Fechá-la exige idempotência ponta-a-ponta no AnhurDB, não aqui.
        """
        assert self._connection is not None
        stale_before = _stamp(_utc_now() - SENDING_TIMEOUT)
        self._connection.execute(
            "UPDATE pending_memory SET state = ?, state_changed_at = ?"
            " WHERE state = ? AND state_changed_at < ?",
            (STATE_PENDING, _stamp(_utc_now()), STATE_SENDING, stale_before),
        )
        self._connection.commit()

    def _set_state(self, item_id: int, state: str) -> None:
        assert self._connection is not None
        self._connection.execute(
            "UPDATE pending_memory SET state = ?, state_changed_at = ? WHERE id = ?",
            (state, _stamp(_utc_now()), item_id),
        )
        self._connection.commit()

    def _mark_failed(self, item_id: int, retry_count: int, cause: str) -> None:
        """Devolve para pending com backoff e a causa. NUNCA descarta."""
        assert self._connection is not None
        next_attempt = _utc_now() + backoff_for_attempt(retry_count + 1)
        self._connection.execute(
            "UPDATE pending_memory SET state = ?, retry_count = ?, last_error = ?,"
            " next_attempt_at = ?, state_changed_at = ? WHERE id = ?",
            (
                STATE_PENDING,
                retry_count + 1,
                cause[:300],
                _stamp(next_attempt),
                _stamp(_utc_now()),
                item_id,
            ),
        )
        self._connection.commit()

    def _delete(self, item_id: int) -> None:
        """Entregue é entregue.

        Junior Tip [por que apagar em vez de marcar "sent"]: guardar o histórico de
        entregues transformaria a fila num arquivo paralelo, com política de
        retenção própria e o risco de reenviar algo já gravado. O AnhurDB é a
        memória; esta tabela é só a sala de espera.
        """
        assert self._connection is not None
        self._connection.execute("DELETE FROM pending_memory WHERE id = ?", (item_id,))
        self._connection.commit()

    # -- leitura / limpeza ---------------------------------------------------

    def summarize(self) -> BacklogSummary:
        if not self.available:
            return self.floor.summarize()
        summary = BacklogSummary()
        self._fill_summary(summary)
        return summary

    def _fill_summary(self, summary: BacklogSummary) -> None:
        assert self._connection is not None
        pending_row = self._connection.execute(
            "SELECT COUNT(*), MIN(created_at) FROM pending_memory WHERE state IN (?, ?)",
            (STATE_PENDING, STATE_SENDING),
        ).fetchone()
        summary.pending_count = pending_row[0] or 0
        summary.oldest_pending = _readable_stamp(pending_row[1] or "")

        quarantined_row = self._connection.execute(
            "SELECT COUNT(*), MIN(created_at) FROM pending_memory WHERE state = ?",
            (STATE_QUARANTINED,),
        ).fetchone()
        summary.quarantined_count = quarantined_row[0] or 0
        summary.oldest_quarantined = _readable_stamp(quarantined_row[1] or "")

        if summary.pending_count and not summary.last_error:
            last_error_row = self._connection.execute(
                "SELECT last_error FROM pending_memory WHERE state IN (?, ?)"
                " AND last_error <> '' ORDER BY created_at ASC LIMIT 1",
                (STATE_PENDING, STATE_SENDING),
            ).fetchone()
            if last_error_row:
                summary.last_error = last_error_row[0]

    def purge_if_drained(self, announce: bool = False) -> bool:
        """Apaga o estado local quando não resta nada esperando entrega.

        Junior Tip [a regra do dono: "limpar o local pra não ter risco"]: depois de
        tudo entregue, o arquivo local não guarda nada que o AnhurDB não tenha — ele
        só guarda RISCO. Páginas com conteúdo de conversa continuam legíveis no
        disco muito depois do DELETE (o SQLite marca a página como livre, não a
        sobrescreve). VACUUM reescreve o arquivo sem as páginas mortas: o túnel
        volta a ser um túnel.

        Junior Tip [a quarentena SEGURA a limpeza]: turno em quarentena é o único
        dado que existe SÓ aqui — nunca chegou ao AnhurDB e ninguém sabe a que
        conversa pertence. Limpar com um deles presente seria a perda silenciosa que
        este módulo inteiro existe para impedir.
        """
        if not self.available:
            return False
        assert self._connection is not None
        remaining = self._connection.execute(
            "SELECT COUNT(*) FROM pending_memory"
        ).fetchone()[0]
        if remaining:
            return False
        try:
            self._connection.execute("VACUUM")
            self._connection.commit()
        except sqlite3.Error as vacuum_error:
            logger.warning(
                "anhurdb-memory: local queue is empty but could not be reclaimed: %s",
                vacuum_error,
            )
            return False
        if announce:
            logger.info(
                "anhurdb-memory: local queue fully drained and cleared — nothing "
                "is held outside AnhurDB"
            )
        return True

    def list_pending(self) -> List[tuple]:
        """Turnos aguardando entrega, do mais antigo para o mais novo (só leitura).

        Junior Tip [por que não reusar _claim_batch]: aquele MARCA os itens como
        "sending". Uma inspeção que muda o estado do que inspeciona transformaria
        um diagnóstico em um efeito colateral — e um item deixado em "sending" por
        alguém que só queria olhar ficaria fora do dreno até o resgate.
        """
        if not self.available:
            return [
                (0, turn.session_id, turn.content, 0)
                for turn in (
                    self.floor._read_envelope(path)
                    for path in self.floor._sorted_files(self.queue_dir)
                )
                if turn is not None
            ]
        assert self._connection is not None
        return self._connection.execute(
            "SELECT id, session_id, content, retry_count FROM pending_memory"
            " WHERE state IN (?, ?) ORDER BY created_at ASC, id ASC",
            (STATE_PENDING, STATE_SENDING),
        ).fetchall()

    def list_quarantined(self) -> List[tuple]:
        """Devolve, inteiros, os turnos que aguardam decisão humana.

        Junior Tip [para que serve]: a quarentena nunca se resolve sozinha — por
        definição, ninguém sabe a que conversa o turno pertence. Quem for resolver
        precisa LER o conteúdo para reconhecer a origem. Sem esta leitura, a
        quarentena seria um buraco: preserva o dado e o torna inalcançável, que para
        o usuário é indistinguível de perdê-lo.
        """
        if not self.available:
            return []
        assert self._connection is not None
        return self._connection.execute(
            "SELECT id, content, last_error, created_at FROM pending_memory"
            " WHERE state = ? ORDER BY created_at ASC, id ASC",
            (STATE_QUARANTINED,),
        ).fetchall()


def _parse_queued_at(queued_at: str) -> datetime:
    """Instante do envelope antigo. Irreconhecível vira "agora", que ordena depois
    de tudo o que é datado — o mais seguro para algo cuja idade não pode ser provada.
    """
    try:
        parsed = datetime.fromisoformat(queued_at)
    except (TypeError, ValueError):
        return _utc_now()
    if parsed.tzinfo is None:
        return parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)
