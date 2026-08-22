#!/usr/bin/env python3
"""Constrói um corpus DENSO a partir do LongMemEval — uma sessão por sessão do
oracle, um episodic por turno, ingerido pelo caminho que roda a extração.

POR QUE ESTE ARQUIVO EXISTE (2026-08-15)
----------------------------------------
O harness `longmemeval-pooled` escreve cada sessão como UM episodic via
`CreateRequest` → `POST /records`, que NÃO passa pela extração. O corpus
resultante mede 69% episodic e 31% estruturado — ou seja, ele testa o
ranqueador sobre um dump achatado, não sobre uma memória que o pipeline
trabalhou. O tenant de memória real do dono é o oposto: 12% episodic, 88%
estruturado.

Isso importa porque o prior de tipo — e qualquer sinal que dependa de tipo —
só tem informação quando existem tipos. Medir aquele botão no corpus achatado
responde a pergunta errada, e foi o que fizemos por semanas.

Aqui cada TURNO vira um episodic próprio via `add(mode="ingest")` →
`POST /api/v1/ingest`, que dispara extração (fact/decision/risk/task/...),
depois consolidação, depois hub. Com 12 turnos por sessão do oracle, a
densidade que o dono pediu (10 a 20 episodics por sessão) sai do dado como ele
é, sem inventar conteúdo.

DIMENSÃO (medida no oracle, não estimada)
-----------------------------------------
    100 perguntas ->  265 sessões,  3.094 episodics
    200 perguntas ->  487 sessões,  5.724 episodics
    500 perguntas ->  940 sessões, 10.960 episodics

O custo dominante é UMA chamada de LLM por episodic na extração. Escolha o
tamanho por aí, não pelo número de perguntas.

IDEMPOTÊNCIA
------------
Os UUIDs de sessão são derivados por SHA-256 do (question_id, session_id) do
oracle — o mesmo esquema do harness antigo. Rodar de novo com o mesmo tenant
NÃO duplica sessões; os episodics são pulados se a sessão já tiver a contagem
esperada, então uma execução interrompida continua de onde parou.

USO
---
    export ANHUR_API_KEY=...        # nunca aparece em log
    export ANHUR_BASE=https://anhurdb.yoven.ai
    export DENSE_TENANT=<client_id>_<tenant>      # o tenant novo de testes
    export DENSE_ORACLE=/caminho/longmemeval_oracle.json
    export DENSE_QUESTIONS=200      # padrão 200
    python3 build_corpus.py plan     # só mostra o tamanho, não escreve
    python3 build_corpus.py ingest   # escreve
"""
import asyncio
import hashlib
import json
import os
import sys
import time
from typing import Any, Dict, List, Sequence

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "v2", "python"))

from anhurdb import AnhurAuthError, AnhurError, Memory  # noqa: E402

BASE = (os.environ.get("ANHUR_BASE") or "https://anhurdb.yoven.ai").rstrip("/")
API_KEY = os.environ.get("ANHUR_API_KEY", "")
TENANT = os.environ.get("DENSE_TENANT", "")
ORACLE_PATH = os.environ.get("DENSE_ORACLE", "/tmp/longmemeval_oracle.json")
QUESTION_COUNT = int(os.environ.get("DENSE_QUESTIONS", "200"))
GOLD_PATH = os.environ.get("DENSE_GOLD", "dense_gold.json")
CONCURRENCY = int(os.environ.get("DENSE_CONCURRENCY", "4"))


def session_uuid_for(question_id: str, oracle_session_id: str) -> str:
    """UUID determinístico — mesmo esquema do harness pooled, para que uma
    reingestão seja idempotente em vez de duplicar o corpus."""
    digest = hashlib.sha256(
        ("longmemeval-dense:%s:%s" % (question_id, oracle_session_id)).encode()
    ).hexdigest()
    return "%s-%s-%s-%s-%s" % (
        digest[:8], digest[8:12], digest[12:16], digest[16:20], digest[20:32])


def turn_to_text(turn: Dict[str, Any]) -> str:
    """Um turno vira o texto de UM episodic.

    Junior Tip [o papel entra no texto de propósito]: a extração lê isto como
    conteúdo de conversa, e "user:" / "assistant:" é o que diz a ela de quem é
    a afirmação. Sem o prefixo, uma preferência do usuário e uma sugestão do
    assistente viram fatos indistinguíveis — e metade das perguntas do
    LongMemEval depende exatamente dessa distinção.
    """
    role = (turn.get("role") or "user").strip()
    content = (turn.get("content") or "").strip()
    return "%s: %s" % (role, content)


def build_plan(oracle: Sequence[Dict[str, Any]], question_count: int) -> Dict[str, Any]:
    """Decide o que ingerir: as sessões que CONTÊM a resposta de cada pergunta.

    Junior Tip [só as sessões de resposta, e por quê]: o oracle traz também
    sessões distratoras. Ingerir todas multiplicaria o custo de extração por
    três sem mudar o que a métrica mede — o gold é a sessão de resposta, e a
    interferência já existe porque TODAS as sessões de resposta de TODAS as
    perguntas convivem no mesmo tenant. Com 200 perguntas isso são 487 sessões
    competindo em cada consulta.
    """
    chosen = list(oracle)[:question_count]
    sessions: Dict[str, List[Dict[str, Any]]] = {}
    gold: Dict[str, Dict[str, Any]] = {}

    for question in chosen:
        question_id = question["question_id"]
        answer_ids = set(question.get("answer_session_ids") or [])
        haystack_ids = question.get("haystack_session_ids") or []
        haystack_sessions = question.get("haystack_sessions") or []

        gold_uuids: List[str] = []
        for oracle_session_id, turns in zip(haystack_ids, haystack_sessions):
            if oracle_session_id not in answer_ids:
                continue
            uuid = session_uuid_for(question_id, oracle_session_id)
            sessions[uuid] = turns
            gold_uuids.append(uuid)

        if gold_uuids:
            gold[question_id] = {
                "question": question["question"],
                "type": question["question_type"],
                "relevant_uuids": gold_uuids,
            }

    episodic_total = sum(len(turns) for turns in sessions.values())
    return {"sessions": sessions, "gold": gold, "episodic_total": episodic_total}


async def existing_episodic_counts(memory: Memory) -> Dict[str, int]:
    """Quantos episodics cada sessão do tenant já tem.

    Junior Tip [a idempotência não é de graça — 2026-08-15]: a primeira versão
    deste arquivo AFIRMAVA na docstring que reingerir pulava o que já estava
    escrito, e não implementava nada disso. Rodar de novo teria duplicado 5.652
    episodics em silêncio, e o corpus duplicado ainda passaria em qualquer
    verificação de contagem que eu tivesse escrito depois. Uma promessa de
    idempotência sem o código que a cumpre é pior que não prometer.

    Junior Tip [por que não usa memory.list_sessions()]: o método do SDK chama
    /sessions/stats SEM limit nem offset, então devolve só a primeira página —
    50 de 491 aqui. Ele não sinaliza que truncou. Usar essa leitura para decidir
    o que já foi escrito marcaria 441 sessões como "faltando" e as duplicaria.
    Enquanto o SDK não pagina, este harness fala com a conexão dele
    diretamente, e a lacuna fica anotada.
    """
    counts: Dict[str, int] = {}
    offset = 0
    while True:
        page = await memory._connection.get(  # noqa: SLF001 — ver Junior Tip
            "/api/v1/sessions/stats", params={"limit": 50, "offset": offset})
        for session in page.get("sessions", []):
            counts[session["uuid"]] = (session.get("types") or {}).get("episodic", 0)
        if not page.get("has_more"):
            break
        offset = page.get("next_offset", offset + 50)
        if offset > 20000:
            break
    return counts


async def wipe_session(memory: Memory, uuid: str) -> int:
    """Apaga TODOS os registros de uma sessão. Devolve quantos apagou.

    Junior Tip [sessão parcial se limpa, nunca se completa — 2026-08-15]: a
    tentação é escrever só os turnos que faltam. Não dá: o que existe no banco
    são episodics, e não há como saber QUAIS turnos viraram quais registros sem
    comparar conteúdo. As duas primeiras versões deste arquivo reescreviam a
    sessão inteira por cima do parcial, e cada passada de "complemento" criava
    duplicata nova — 62 na primeira, 36 na segunda. Limpar e reescrever é
    determinístico e converge; completar não.

    Apaga também os satélites que a extração já produziu a partir dos episodics
    duplicados, porque eles estão na mesma sessão. Deixá-los criaria fatos
    repetidos que nenhuma contagem de episodics revelaria.
    """
    deleted = 0
    while True:
        page = await memory._connection.get(  # noqa: SLF001
            "/api/v1/sessions/%s/history" % uuid, params={"limit": 200, "offset": 0})
        records = page if isinstance(page, list) else (page.get("records") or [])
        if not records:
            return deleted
        for record in records:
            try:
                await memory.delete(record["id"])
                deleted += 1
            except Exception:  # noqa: BLE001
                return deleted


async def ensure_session(memory: Memory, uuid: str, attempts: int = 5) -> None:
    """Registra a sessão e ESPERA ela ficar visível antes de escrever nela.

    Junior Tip [a corrida do Raft, que já nos mordeu antes]: o POST devolve
    sucesso quando o comando foi aceito, e a sessão pode levar um beat para
    ficar visível no nó que atende a escrita seguinte. Sem espera, os primeiros
    `add` da sessão voltam 400 e a sessão inteira se perde. Foi exatamente isso
    que produziu 72 falhas em 5 sessões na primeira execução — e o harness
    `longmemeval-pooled` já tinha esse retry, com este mesmo comentário; eu
    escrevi o meu sem olhar o dele.

    Junior Tip [não engolir o erro]: a versão anterior tratava QUALQUER
    AnhurError como "já existe" e seguia em frente. A causa real ficava
    escondida e só aparecia como um 400 sem contexto doze vezes seguidas.
    """
    last_error: Exception | None = None
    for attempt in range(attempts):
        try:
            await memory.create_session(session_id=uuid)
            return
        except AnhurAuthError:
            raise
        except AnhurError as session_error:
            message = str(session_error).lower()
            if "exist" in message or "conflict" in message or "409" in message:
                return  # já registrada — reingestão idempotente
            last_error = session_error
            await asyncio.sleep(0.5 * (attempt + 1))
    raise RuntimeError("create_session não convergiu para %s: %s" % (uuid[:8], last_error))


async def ingest_one_session(
    memory: Memory, uuid: str, turns: Sequence[Dict[str, Any]], failures: List[str]
) -> int:
    """Cria a sessão e ingere cada turno como um episodic próprio.

    Junior Tip [falhar ALTO por sessão, não abortar tudo]: uma sessão que falha
    é registrada e a execução segue. Abortar perderia horas de extração já
    paga; engolir a falha em silêncio produziria um corpus incompleto que
    ninguém saberia estar incompleto — e aí toda medição sobre ele estaria
    errada sem aviso. O relatório final conta quantas falharam.
    """
    try:
        await ensure_session(memory, uuid)
    except AnhurAuthError:
        raise
    except Exception as session_error:  # noqa: BLE001
        failures.append("%s: sessão não registrada: %s" % (uuid[:8], repr(session_error)[:80]))
        return 0

    written = 0
    for turn in turns:
        text = turn_to_text(turn)
        if not text.strip():
            continue
        for attempt in range(3):
            try:
                await memory.add(text, session_id=uuid)
                written += 1
                break
            except AnhurAuthError:
                raise
            except Exception as write_error:  # noqa: BLE001
                if attempt == 2:
                    failures.append("%s: %s" % (uuid[:8], repr(write_error)[:80]))
                else:
                    await asyncio.sleep(1.5 * (attempt + 1))
    return written


async def run_ingest(plan: Dict[str, Any]) -> None:
    if not API_KEY:
        sys.exit("defina ANHUR_API_KEY")
    if not TENANT:
        sys.exit("defina DENSE_TENANT (o tenant novo de testes)")

    sessions = plan["sessions"]
    failures: List[str] = []
    started = time.time()
    done = 0
    written_total = 0

    semaphore = asyncio.Semaphore(CONCURRENCY)

    skipped = 0
    async with Memory(api_key=API_KEY, url=BASE, tenant_id=TENANT) as memory:
        existing = await existing_episodic_counts(memory)
        already_written = sum(existing.values())
        if already_written:
            print("  já no tenant: %d episodics em %d sessões — serão PULADAS"
                  % (already_written, sum(1 for v in existing.values() if v)), flush=True)

        async def worker(uuid: str, turns: Sequence[Dict[str, Any]]) -> None:
            nonlocal done, written_total, skipped
            async with semaphore:
                already = existing.get(uuid, 0)
                # Só pula quando bate EXATO. Sessão sobrando é tão inválida
                # quanto sessão faltando: a de excesso carrega duplicata, e
                # `>=` a tratava como completa — foi assim que 36 duplicatas
                # sobreviveram a duas passadas de correção.
                if already == len(turns):
                    done += 1
                    skipped += 1
                    return
                if already > 0:
                    # Parcial: limpa e reescreve. Completar duplicaria — ver
                    # a Junior Tip de wipe_session.
                    removed = await wipe_session(memory, uuid)
                    print("  %s parcial (%d/%d): %d registros apagados, reescrevendo"
                          % (uuid[:8], already, len(turns), removed), flush=True)
                written = await ingest_one_session(memory, uuid, turns, failures)
                done += 1
                written_total += written
                if done % 25 == 0:
                    elapsed = time.time() - started
                    rate = done / elapsed if elapsed else 0
                    remaining = (len(sessions) - done) / rate if rate else 0
                    print("  %d/%d sessões  %d episodics  ~%.0f min restantes"
                          % (done, len(sessions), written_total, remaining / 60), flush=True)

        await asyncio.gather(*(worker(u, t) for u, t in sessions.items()))

    with open(GOLD_PATH, "w") as gold_file:
        json.dump(plan["gold"], gold_file)

    print("\nsessões: %d   puladas (já completas): %d   episodics escritos agora: %d   falhas: %d"
          % (len(sessions), skipped, written_total, len(failures)))
    print("gold: %s (%d perguntas)" % (GOLD_PATH, len(plan["gold"])))
    if failures:
        print("\nFALHAS (o corpus está INCOMPLETO — não medir sem resolver):")
        for failure in failures[:20]:
            print("  " + failure)
        sys.exit(1)


def main() -> None:
    command = sys.argv[1] if len(sys.argv) > 1 else "plan"
    with open(ORACLE_PATH) as oracle_file:
        oracle = json.load(oracle_file)

    plan = build_plan(oracle, QUESTION_COUNT)
    counts = [len(turns) for turns in plan["sessions"].values()]
    print("perguntas com gold: %d" % len(plan["gold"]))
    print("sessões a ingerir:  %d" % len(plan["sessions"]))
    print("episodics totais:   %d" % plan["episodic_total"])
    if counts:
        print("episodics/sessão:   min=%d max=%d média=%.1f"
              % (min(counts), max(counts), sum(counts) / len(counts)))
    print("custo dominante:    %d chamadas de LLM na extração" % plan["episodic_total"])

    if command == "plan":
        print("\n(plan: nada foi escrito — rode 'ingest' para escrever)")
        return
    if command != "ingest":
        sys.exit("comando desconhecido: %s (use plan | ingest)" % command)

    print("\ningerindo em tenant=%s ..." % TENANT)
    asyncio.run(run_ingest(plan))


if __name__ == "__main__":
    main()
