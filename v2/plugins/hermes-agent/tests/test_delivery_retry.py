"""O disco é último recurso: 3 tentativas antes de criar estado local.

Par exato de ``plugins/core/delivery_retry_test.go``.
"""

from __future__ import annotations

import pytest


@pytest.fixture
def retry_module(plugin_modules):
    return plugin_modules["delivery_retry"]


def _no_sleep(_seconds: float) -> None:
    """Tira a espera dos testes sem tirar a lógica."""


def test_blip_does_not_create_local_state(retry_module):
    """A razão de a regra existir: uma eleição de líder ou uma reconexão NÃO pode
    deixar uma segunda cópia da memória do usuário no disco."""
    attempts = {"count": 0}

    def flaky():
        attempts["count"] += 1
        if attempts["count"] < 2:
            raise ConnectionError("connection refused")

    outcome = retry_module.deliver_with_retry(flaky, sleep=_no_sleep)

    assert outcome.delivered, f"um blip virou falha de entrega: {outcome.error}"
    assert attempts["count"] == 2, "deveria parar assim que entregou"


def test_unresponsive_backend_uses_all_three_attempts(retry_module):
    attempts = {"count": 0}

    def always_down():
        attempts["count"] += 1
        raise ConnectionError("connection refused")

    outcome = retry_module.deliver_with_retry(always_down, sleep=_no_sleep)

    assert attempts["count"] == retry_module.MAX_DELIVERY_ATTEMPTS
    assert not outcome.delivered, "banco fora do ar reportado como entregue"
    assert not outcome.terminal, (
        "banco fora do ar marcado como terminal — ele volta sozinho e o log diria "
        "a coisa errada"
    )


@pytest.mark.parametrize(
    "refusal_message",
    [
        "AnhurError: Unauthorized (HTTP 401)",
        "AnhurError: Server error (HTTP 409): session has reached the maximum",
        "AnhurError: status 422: validation error",
        "AnhurError: not found",
    ],
)
def test_server_refusal_is_not_retried(retry_module, refusal_message):
    """Um 401 ou um 409 é o servidor RESPONDENDO. Repetir gasta o turno do usuário
    para chegar ao mesmo lugar. O turno é enfileirado do mesmo jeito."""
    attempts = {"count": 0}

    def refused():
        attempts["count"] += 1
        raise RuntimeError(refusal_message)

    outcome = retry_module.deliver_with_retry(refused, sleep=_no_sleep)

    assert attempts["count"] == 1, "a resposta do servidor não muda na segunda"
    assert outcome.terminal, "recusa não marcada como terminal"
    assert not outcome.delivered


@pytest.mark.parametrize(
    "transient_message",
    [
        "AnhurError: Server error (HTTP 500)",
        "AnhurError: Server error (HTTP 503): upstream unavailable",
        "AnhurError: HTTP 429: too many requests",
        "AnhurError: HTTP 408: request timeout",
        "TimeoutError: read timed out",
    ],
)
def test_transient_failures_are_retried(retry_module, transient_message):
    """408 e 429 são 4xx que melhoram sozinhos — classificá-los como terminais
    desistiria cedo e criaria estado local à toa."""
    attempts = {"count": 0}

    def transient():
        attempts["count"] += 1
        raise RuntimeError(transient_message)

    retry_module.deliver_with_retry(transient, sleep=_no_sleep)
    assert attempts["count"] == retry_module.MAX_DELIVERY_ATTEMPTS, (
        f"{transient_message!r} deveria ser tentado de novo"
    )


def test_a_number_inside_an_id_is_not_read_as_a_status_code(retry_module):
    """Casar "409" solto daria falso positivo em qualquer uuid que o contivesse —
    e um transiente classificado como terminal desiste cedo demais."""
    attempts = {"count": 0}

    def looks_like_a_status_but_is_not():
        attempts["count"] += 1
        raise RuntimeError("connection reset while writing session 3f409ab2-7712")

    retry_module.deliver_with_retry(looks_like_a_status_but_is_not, sleep=_no_sleep)
    assert attempts["count"] == retry_module.MAX_DELIVERY_ATTEMPTS


def test_terminality_label_separates_the_two_failures(retry_module):
    """A distinção que custou 10 dias: no log antigo, "still failing" cobria tanto
    o banco fora do ar quanto uma rejeição permanente."""
    down = retry_module.deliver_with_retry(
        lambda: (_ for _ in ()).throw(ConnectionError("refused")), sleep=_no_sleep
    )
    refused = retry_module.deliver_with_retry(
        lambda: (_ for _ in ()).throw(RuntimeError("HTTP 401 unauthorized")),
        sleep=_no_sleep,
    )
    assert down.terminality_label != refused.terminality_label
