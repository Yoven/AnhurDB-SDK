"""Máquina de estados da fila + a regra "disco é último recurso".

Cada teste aqui é o par de um teste Go em ``plugins/core``. Os dois plugins
prometem a mesma coisa ao usuário; uma divergência silenciosa entre eles seria
descoberta por um cliente, não por nós.
"""

from __future__ import annotations

import sqlite3

import pytest


@pytest.fixture
def state_queue(plugin_modules, tmp_path):
    queue_module = plugin_modules["queue_store"]
    queue = queue_module.StateQueue(queue_dir=tmp_path / "queue")
    assert queue.available, "o SQLite deveria abrir num tmp_path limpo"
    yield queue
    queue.close()


# ---------------------------------------------------------------------------
# Máquina de estados
# ---------------------------------------------------------------------------


def test_enqueue_refuses_missing_session(state_queue, plugin_modules):
    """Adivinhar a sessão fundiria duas conversas — a regra inviolável."""
    queue_error = plugin_modules["memory_queue"].QueueWriteError
    with pytest.raises(queue_error):
        state_queue.enqueue("", "conteudo orfao")


def test_drain_preserves_conversation_order(state_queue):
    """Entregar o turno 7 antes do 5 escreve a conversa errada, e nenhuma
    consolidação posterior conserta isso."""
    for body in ("primeiro", "segundo", "terceiro"):
        state_queue.enqueue("sess-A", body)

    delivered = []
    summary = state_queue.drain(lambda session_id, content: delivered.append(content))

    assert delivered == ["primeiro", "segundo", "terceiro"]
    assert summary.drained_count == 3
    assert summary.pending_count == 0


def test_failure_keeps_the_turn_with_a_reason(state_queue):
    """A resposta para as 52 linhas de log que diziam "still failing" sem dizer
    há quanto tempo, quantas vezes, nem por quê."""

    def always_fails(session_id, content):
        raise RuntimeError("authentication failed: invalid API key")

    state_queue.enqueue("sess-A", "conteudo")
    summary = state_queue.drain(always_fails)

    assert summary.pending_count == 1, "turno que falhou NUNCA é descartado"
    assert "invalid API key" in summary.last_error, (
        "a pergunta 'por que está preso?' precisa ter resposta"
    )
    pending = state_queue.list_pending()
    assert pending[0][3] == 1, "a tentativa tem de ficar contada"


def test_backoff_keeps_a_dead_endpoint_from_being_hammered(state_queue):
    def always_fails(session_id, content):
        raise RuntimeError("connection refused")

    state_queue.enqueue("sess-A", "conteudo")
    state_queue.drain(always_fails)

    attempts = []
    state_queue.drain(lambda session_id, content: attempts.append(content))
    assert attempts == [], "item reapareceu antes do backoff — viraria martelo na rede"


def test_failure_blocks_its_own_session_only(state_queue):
    """Dentro de uma sessão a ordem É a conversa. Entre sessões não há ordem a
    preservar, e bloquear tudo deixaria uma rejeição permanente (HTTP 409)
    congelar a fila inteira para sempre."""
    state_queue.enqueue("sess-blocked", "turno-1")
    state_queue.enqueue("sess-blocked", "turno-2")
    state_queue.enqueue("sess-open", "outra-conversa")

    delivered = []

    def selective(session_id, content):
        if session_id == "sess-blocked":
            raise RuntimeError("session has reached the maximum of 500 records")
        delivered.append(content)

    summary = state_queue.drain(selective)

    assert delivered == ["outra-conversa"], (
        "uma sessão travada não pode congelar as outras"
    )
    assert summary.pending_count == 2
    # turno-2 nem pode ter sido TENTADO, nem punido por um erro que não foi dele.
    untried = [row for row in state_queue.list_pending() if row[2] == "turno-2"]
    assert untried[0][3] == 0, "o turno não tentado foi punido pelo vizinho"


def test_recovery_after_the_backend_returns(state_queue):
    """A promessa central: uma queda ATRASA a memória, nunca a destrói."""
    state_queue.enqueue("sess-A", "turno-1")
    state_queue.enqueue("sess-A", "turno-2")

    def always_fails(session_id, content):
        raise RuntimeError("connection refused")

    assert state_queue.drain(always_fails).pending_count == 2

    # O backoff do item que falhou ainda vale; o teste o zera porque o que se
    # prova aqui é a recuperação, não a espera (o backoff tem teste próprio).
    state_queue._connection.execute(
        "UPDATE pending_memory SET next_attempt_at = '2000-01-01T00:00:00+00:00'"
    )
    state_queue._connection.commit()

    delivered = []
    summary = state_queue.drain(lambda session_id, content: delivered.append(content))
    assert delivered == ["turno-1", "turno-2"]
    assert summary.pending_count == 0


def test_quarantine_is_never_retried(state_queue):
    state_queue.quarantine("orfao sem sessao", "no session marker")

    attempts = []
    summary = state_queue.drain(lambda session_id, content: attempts.append(content))

    assert attempts == [], "quarentena tem de ser terminal para o laço automático"
    assert summary.quarantined_count == 1, "quarentena tem de ser VISÍVEL"
    assert state_queue.list_quarantined()[0][1] == "orfao sem sessao"


def test_stale_sending_is_reclaimed(state_queue, plugin_modules):
    """O host pode morrer no meio do envio. Sem o resgate, o turno ficaria preso
    para sempre — o modo de falha que esta tabela existe para acabar."""
    queue_module = plugin_modules["queue_store"]
    state_queue.enqueue("sess-A", "conteudo")
    state_queue._connection.execute(
        "UPDATE pending_memory SET state = ?, state_changed_at = ?",
        (queue_module.STATE_SENDING, "2000-01-01T00:00:00+00:00"),
    )
    state_queue._connection.commit()

    delivered = []
    state_queue.drain(lambda session_id, content: delivered.append(content))
    assert delivered == ["conteudo"], "turno preso por processo morto não foi resgatado"


def test_backoff_grows_and_is_capped(plugin_modules):
    queue_module = plugin_modules["queue_store"]
    first = queue_module.backoff_for_attempt(1)
    later = queue_module.backoff_for_attempt(5)
    assert later > first, "sem crescimento, um endpoint morto é martelado"
    assert queue_module.backoff_for_attempt(999) == queue_module.MAX_BACKOFF, (
        "sem teto, a recuperação demoraria horas depois de uma queda longa"
    )


# ---------------------------------------------------------------------------
# Migração do formato antigo
# ---------------------------------------------------------------------------


def test_migration_preserves_order_and_removes_the_files(plugin_modules, tmp_path):
    """O envelope antigo carregava o instante; a fila nova ordena por created_at.
    Carimbar tudo com "agora" embaralharia turnos que estavam corretos — a
    migração destruiria justamente o que ela existe para salvar."""
    queue_module = plugin_modules["queue_store"]
    floor_module = plugin_modules["memory_queue"]

    queue_dir = tmp_path / "queue"
    floor = floor_module.DurableTurnQueue(queue_dir=queue_dir)
    for body in ("primeiro", "segundo", "terceiro"):
        floor.enqueue("sess-A", body)

    migrated = queue_module.StateQueue(queue_dir=queue_dir)
    try:
        assert migrated.available
        delivered = []
        migrated.drain(lambda session_id, content: delivered.append(content))
        assert delivered == ["primeiro", "segundo", "terceiro"]
        assert list(queue_dir.glob("*.json")) == [], (
            "arquivo antigo sobrou — migraria de novo e viraria duplicata"
        )
    finally:
        migrated.close()


def test_flat_file_is_the_floor_when_sqlite_cannot_open(
    plugin_modules, tmp_path, monkeypatch
):
    """A lição de 2026-07-30: uma rede de segurança que compartilha modo de falha
    com aquilo que protege não é rede. Se o banco não abre, a memória AINDA
    precisa chegar ao disco."""
    queue_module = plugin_modules["queue_store"]

    def refuse_to_connect(*args, **kwargs):
        raise sqlite3.OperationalError("unable to open database file")

    monkeypatch.setattr(queue_module.sqlite3, "connect", refuse_to_connect)
    queue = queue_module.StateQueue(queue_dir=tmp_path / "queue")

    assert not queue.available
    queue.enqueue("sess-A", "memoria que nao pode sumir")

    survivors = list((tmp_path / "queue").glob("*.json"))
    assert len(survivors) == 1, "banco indisponível custou uma memória"
    assert "memoria que nao pode sumir" in survivors[0].read_text(encoding="utf-8")


# ---------------------------------------------------------------------------
# "Limpar o local pra não ter risco"
# ---------------------------------------------------------------------------


def test_local_state_is_cleared_after_a_full_drain(state_queue):
    for body in ("turno-1", "turno-2", "turno-3"):
        state_queue.enqueue("sess-A", body)
    state_queue.drain(lambda session_id, content: None)

    database_bytes = state_queue.database_path.read_bytes()
    for delivered_body in (b"turno-1", b"turno-2", b"turno-3"):
        assert delivered_body not in database_bytes, (
            "conteúdo entregue ainda legível no arquivo local — o DELETE marca a "
            "página como livre, só o VACUUM a reescreve"
        )


def test_quarantine_holds_the_cleanup(state_queue):
    """Turno em quarentena é o ÚNICO dado que existe só aqui. Limpar com um deles
    presente seria a perda silenciosa que todo este módulo existe para impedir."""
    state_queue.quarantine("orfao sem sessao", "no session marker")
    assert state_queue.purge_if_drained() is False
    assert state_queue.list_quarantined()[0][1] == "orfao sem sessao"
