"""Bounded retry before anything is written locally.

Junior Tip [a regra do dono, 2026-07-31]: "só pode gravar local caso o banco não
responda depois de 3 tentativas; assim que voltar, descarregar tudo que tem local
e limpar o local pra não ter risco". Antes dela, UMA falha qualquer — um blip de
rede, uma eleição de líder de 200 ms — já criava estado local. Estado local é uma
segunda cópia da verdade: enquanto existe, a memória do usuário está em dois
lugares e só um deles é o AnhurDB. A fila tem de ser um túnel, não um armário.

Junior Tip [3 tentativas, mas NÃO 3 tentativas em tudo]: o dono disse "o banco não
responde". Não responder é uma coisa; responder "não" é outra. Um 401 (chave
errada) ou um 409 (sessão cheia) são o servidor RESPONDENDO, e a resposta não muda
na segunda tentativa — repetir só gastaria o tempo do turno do usuário para chegar
ao mesmo lugar. Então 3 é o TETO para o que pode melhorar sozinho, e o que já foi
decidido vai direto para a fila. O turno é enfileirado nos dois casos: a regra muda
QUANDO se grava local, nunca SE a memória é preservada.

Este módulo é o par exato de ``plugins/core/delivery_retry.go``. Os dois plugins
Go compartilham aquele arquivo byte a byte; este mantém o Hermes Python na mesma
semântica. Mudou um, muda o outro — ver o invariante de paridade dos SDKs.
"""

from __future__ import annotations

import re
import time
from dataclasses import dataclass
from typing import Callable, Optional, Sequence

# O número do dono: três tentativas antes de aceitar o disco.
MAX_DELIVERY_ATTEMPTS = 3

# Espera ANTES de cada tentativa (a primeira é imediata).
#
# Junior Tip [por que tão curto]: isto roda dentro do turno do usuário. O alvo é o
# blip — eleição de líder, reconexão, um 503 de deploy —, não uma queda longa.
# Queda longa é justamente o que a fila resolve, e a fila não tem pressa.
DELIVERY_RETRY_BACKOFF_SECONDS: Sequence[float] = (0.0, 0.3, 1.2)

# Junior Tip [por que casar por texto AQUI e por tipo no Go]: o SDK Go devolve
# erros tipados (ErrUnauthorized, *APIError com StatusCode) e lá a classificação é
# por tipo. O runtime Python embrulha tudo em AnhurRuntimeError com a mensagem
# original dentro, então o status só existe como texto. Casar por número isolado
# ("409") daria falso positivo em qualquer id que contivesse 409; o padrão abaixo
# exige a palavra "status"/"HTTP" perto do código, ou a frase da API.
_TERMINAL_STATUS_PATTERN = re.compile(
    r"(?:\bhttp\b|\bstatus\b|\bcode\b)\D{0,4}(400|401|403|404|405|409|413|415|422)\b",
    re.IGNORECASE,
)
_TERMINAL_PHRASES = (
    "unauthorized",
    "forbidden",
    "invalid api key",
    "api key is invalid",
    "not found",
    "session has reached the maximum",
    "quota exceeded",
    "validation error",
)


@dataclass
class DeliveryOutcome:
    """Como uma entrega terminou.

    Junior Tip [a distinção que custou 10 dias]: no log antigo, "still failing"
    cobria tanto o banco fora do ar quanto uma rejeição permanente. São situações
    opostas — uma se resolve sozinha, a outra nunca — e quem lia o log não tinha
    como separá-las. Um HTTP 409 passou 10 dias parecendo queda temporária.
    """

    error: Optional[BaseException] = None
    attempts_made: int = 0
    terminal: bool = False

    @property
    def delivered(self) -> bool:
        return self.error is None

    @property
    def terminality_label(self) -> str:
        if self.terminal:
            return "server refused; retrying would not help"
        return "no usable response; will retry from the queue"


def deliver_with_retry(
    attempt_delivery: Callable[[], None],
    *,
    sleep: Callable[[float], None] = time.sleep,
) -> DeliveryOutcome:
    """Executa ``attempt_delivery`` até MAX_DELIVERY_ATTEMPTS vezes.

    Para cedo quando o erro é terminal. ``sleep`` é injetável para os testes
    exercitarem a lógica sem pagar a espera real.
    """
    last_error: Optional[BaseException] = None
    for attempt_index in range(MAX_DELIVERY_ATTEMPTS):
        wait_before = DELIVERY_RETRY_BACKOFF_SECONDS[attempt_index]
        if wait_before > 0:
            sleep(wait_before)
        try:
            attempt_delivery()
            return DeliveryOutcome(attempts_made=attempt_index + 1)
        except Exception as delivery_error:  # noqa: BLE001 — classificado, não engolido
            last_error = delivery_error
            if is_terminal_delivery_error(delivery_error):
                return DeliveryOutcome(
                    error=delivery_error,
                    attempts_made=attempt_index + 1,
                    terminal=True,
                )
    return DeliveryOutcome(error=last_error, attempts_made=MAX_DELIVERY_ATTEMPTS)


def is_terminal_delivery_error(delivery_error: BaseException) -> bool:
    """O servidor já decidiu, ou ainda pode mudar?

    Junior Tip [na dúvida, TENTAR DE NOVO]: o custo do engano é assimétrico.
    Classificar um transiente como terminal desiste cedo e cria estado local sem
    necessidade — exatamente o que a regra do dono existe para evitar. Classificar
    um terminal como transiente custa duas chamadas rápidas a mais. Por isso o
    padrão é falso: só é terminal o que a gente RECONHECE como recusa.
    """
    message = str(delivery_error).lower()
    # 408 (timeout) e 429 (excesso) são 4xx que melhoram sozinhos: não entram na
    # lista acima, de propósito.
    if _TERMINAL_STATUS_PATTERN.search(message):
        return True
    return any(phrase in message for phrase in _TERMINAL_PHRASES)
