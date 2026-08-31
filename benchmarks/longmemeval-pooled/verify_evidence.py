#!/usr/bin/env python3
"""Prova que o corpus contem a EVIDENCIA que o gold promete — antes de pontuar.

Por que este arquivo existe (2026-08-31)
---------------------------------------------------------------------------
Uma janela degradada de ingest perdeu 497 de 12.500 registros. Distrator
perdido so deixa o palheiro menor; EVIDENCIA perdida torna a pergunta
impossivel, e o resultado aparece como "o motor nao encontrou" — deflacionando
o numero na direcao errada, sem nenhum sinal de que algo deu errado. Esta
verificacao e dirigida: so as sessoes listadas em ``relevant_uuids``.

A GUARDA QUE DA NOME AO ARQUIVO
---------------------------------------------------------------------------
Junior Tip [instrumento quebrado se parece com sistema quebrado — custou uma
hora em 2026-08-31]: a primeira versao desta checagem usou ``urllib`` cru e
reportou **356 de 356 evidencias ausentes**. O corpus estava intacto: o
endpoint fica atras de Cloudflare, que responde **HTTP 403 a
``Python-urllib``**. Um veredito de 100% de falha e quase sempre o
INSTRUMENTO, nunca o sistema — e um relatorio de "corpus destruido" teria
mandado alguem re-ingerir 13 mil registros por nada.

Por isso ``provar_instrumento`` roda ANTES de qualquer medicao: ele pega uma
sessao que o proprio servidor acabou de listar (portanto existe por
construcao) e exige que a sonda a encontre. Se o controle falha, o programa
morre com o motivo — nunca reporta numero. As duas portas precisam ser
atravessadas: a que prova que a sonda funciona, e a que mede o que interessa.

O ``User-Agent`` vem do SDK (``AnhurSDK-Python/2.1``,
``v2/python/anhurdb/client/__init__.py:74``) e nao de um literal novo: e o
mesmo cabecalho que o resto do harness ja manda, e a regra da casa e procurar
o irmao antes de inventar contrato.

Uso:
    export ANHUR_API_KEY=...   ANHUR_BASE=https://...
    export RB_ISO_GOLD=/caminho/gold.json
    python3 verify_evidence.py          # sai 1 se faltar evidencia
    python3 verify_evidence.py --listar # imprime os uuids ausentes, um por linha
"""
import concurrent.futures
import json
import os
import sys
import urllib.error
import urllib.request
from typing import Dict, List, Optional, Tuple

# Mesmo User-Agent do SDK. NAO troque por um literal novo sem trocar la tambem:
# o servidor esta atras de um WAF que recusa agentes desconhecidos com 403.
AGENTE_DO_HARNESS = "AnhurSDK-Python/2.1"

BASE_URL = (os.environ.get("ANHUR_BASE") or os.environ.get("ANHUR_URL") or "").rstrip("/")
API_KEY = os.environ.get("ANHUR_MK") or os.environ.get("ANHUR_API_KEY") or ""
GOLD_PATH = os.environ.get("RB_ISO_GOLD", "/tmp/rbench_isolated_gold.json")
TENTATIVAS = int(os.environ.get("RB_VERIFY_RETRIES", "3") or "3")
CONCORRENCIA = int(os.environ.get("RB_VERIFY_CONCURRENCY", "10") or "10")


def morrer(motivo: str) -> None:
    """Aborta sem reportar numero. Um resultado parcial aqui vira decisao errada."""
    raise SystemExit("FATAL: %s\nRecusando reportar — um numero medido com o "
                     "instrumento quebrado e pior que numero nenhum." % motivo)


def _pedir(caminho: str) -> Dict:
    pedido = urllib.request.Request(
        BASE_URL + caminho,
        headers={"X-API-Key": API_KEY, "User-Agent": AGENTE_DO_HARNESS},
    )
    with urllib.request.urlopen(pedido, timeout=45) as resposta:
        return json.loads(resposta.read())


def sessao_tem_registro(session_uuid: str) -> Tuple[str, Optional[bool]]:
    """True/False se a sonda respondeu; None quando ela mesma falhou."""
    ultimo_erro: Optional[BaseException] = None
    for _ in range(TENTATIVAS):
        try:
            corpo = _pedir("/api/v1/sessions/%s/history?limit=1" % session_uuid)
            return session_uuid, int(corpo.get("count", 0)) > 0
        except Exception as erro_de_consulta:
            ultimo_erro = erro_de_consulta
    return session_uuid, None


def provar_instrumento() -> None:
    """Controle: uma sessao que o servidor ACABOU de listar tem de ser achada.

    Junior Tip [por que uma sessao viva e nao um uuid fixo]: um uuid escrito no
    codigo envelhece — o tenant muda, a sessao e apagada, e o controle passa a
    falhar por motivo legitimo, treinando quem le a ignora-lo. Pedir ao proprio
    servidor uma sessao que existe agora torna o controle auto-atualizavel: se
    ele falhar, o defeito e da sonda, nao do dado.
    """
    try:
        listagem = _pedir("/api/v1/sessions?limit=1")
    except urllib.error.HTTPError as erro_http:
        dica = ""
        if erro_http.code == 403:
            dica = ("  (403 costuma ser o WAF recusando o User-Agent — "
                    "confira AGENTE_DO_HARNESS contra o do SDK)")
        morrer("a listagem de sessoes respondeu HTTP %d%s" % (erro_http.code, dica))
    except Exception as erro_de_rede:
        morrer("nao consegui listar sessoes: %r" % erro_de_rede)

    sessoes = listagem.get("sessions") or []
    if not sessoes:
        morrer("o tenant nao tem sessao nenhuma — nao ha o que verificar")

    uuid_de_controle = sessoes[0]
    _, achou = sessao_tem_registro(uuid_de_controle)
    if achou is None:
        morrer("a sonda falhou contra uma sessao que o servidor acabou de listar "
               "(%s) — o instrumento esta quebrado" % uuid_de_controle[:13])
    if achou is False:
        morrer("a sonda diz que a sessao %s nao tem registro, mas o servidor "
               "acabou de lista-la — o instrumento esta lendo errado"
               % uuid_de_controle[:13])


def main() -> int:
    if not BASE_URL or not API_KEY:
        morrer("defina ANHUR_BASE e ANHUR_API_KEY")
    if not os.path.exists(GOLD_PATH):
        morrer("gold ausente em %s (defina RB_ISO_GOLD)" % GOLD_PATH)

    provar_instrumento()

    gold = json.load(open(GOLD_PATH))
    evidencias = sorted({uuid for entrada in gold.values() for uuid in entrada["relevant_uuids"]})
    with concurrent.futures.ThreadPoolExecutor(max_workers=CONCORRENCIA) as executor:
        resultado = dict(executor.map(sessao_tem_registro, evidencias))

    falhas_de_sonda = [uuid for uuid, valor in resultado.items() if valor is None]
    if falhas_de_sonda:
        morrer("%d consultas falharam depois de %d tentativas — resultado "
               "incompleto nao vira veredito" % (len(falhas_de_sonda), TENTATIVAS))

    ausentes = {uuid for uuid, valor in resultado.items() if valor is False}
    afetadas: List[str] = [question_id for question_id, entrada in gold.items()
                           if any(uuid in ausentes for uuid in entrada["relevant_uuids"])]

    if "--listar" in sys.argv:
        for uuid in sorted(ausentes):
            print(uuid)
        return 1 if ausentes else 0

    print("evidencias conferidas : %d" % len(evidencias))
    print("com registro          : %d" % (len(evidencias) - len(ausentes)))
    print("SEM registro          : %d" % len(ausentes))
    print("perguntas afetadas    : %d de %d" % (len(afetadas), len(gold)))
    if ausentes:
        print("\nO corpus NAO corresponde ao gold. Reponha as sessoes antes de "
              "pontuar — cada evidencia ausente e uma pergunta impossivel, e o "
              "recall sai deflacionado sem aviso.")
        for question_id in afetadas[:10]:
            print("   %s" % question_id)
        return 1
    print("\ncorpus INTEGRO: toda sessao de evidencia tem registro")
    return 0


if __name__ == "__main__":
    sys.exit(main())
