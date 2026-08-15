#!/usr/bin/env python3
"""Mede recall no corpus denso — e reporta INTERVALO, nunca só o ponto.

POR QUE ESTE ARQUIVO EXISTE (2026-08-15)
----------------------------------------
O harness anterior imprimia recall@k e nada mais. Sobre 30 consultas isso levou
a três decisões erradas no mesmo dia: diferenças de 0,02–0,04 de MRR foram
lidas como achado quando o intervalo de confiança atravessava zero de ponta a
ponta (7 consultas melhores contra 6 piores). Duas execuções da MESMA
configuração diferiam 0,002 — a medição era estável, o tamanho é que não
enxergava.

Então este harness NÃO imprime uma diferença sem o intervalo dela, e compara
configurações de forma PAREADA (mesma consulta nas duas), que é o que remove a
variância entre perguntas — a maior fonte de ruído aqui.

USO
---
    export ANHUR_API_KEY=... ANHUR_BASE=... DENSE_TENANT=<client>_<tenant>
    export DENSE_GOLD=dense_gold.json
    python3 rbench_dense.py compare recall lexical-pure
    python3 rbench_dense.py compare recall lexical      # com/sem rank cognitivo
"""
import asyncio
import json
import os
import random
import statistics
import sys
from typing import Any, Dict, List, Sequence, Tuple

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "v2", "python"))

from anhurdb import AnhurAuthError, Memory  # noqa: E402

BASE = (os.environ.get("ANHUR_BASE") or "https://anhurdb.yoven.ai").rstrip("/")
API_KEY = os.environ.get("ANHUR_API_KEY", "")
TENANT = os.environ.get("DENSE_TENANT", "")
GOLD_PATH = os.environ.get("DENSE_GOLD", "dense_gold.json")
LIMIT = int(os.environ.get("DENSE_LIMIT", "10"))
CONCURRENCY = int(os.environ.get("DENSE_CONCURRENCY", "4"))
BOOTSTRAP_SAMPLES = int(os.environ.get("DENSE_BOOTSTRAP", "20000"))

# (skip_query_embed, skip_cognitive_rerank)
MODES: Dict[str, Tuple[bool, bool]] = {
    "recall": (False, False),        # híbrido completo
    "recall-pure": (False, True),    # híbrido sem rank cognitivo
    "lexical": (True, False),        # só léxico, COM rank cognitivo
    "lexical-pure": (True, True),    # só o braço léxico
}


async def rank_of_gold(
    memory: Memory, question: str, gold_uuids: Sequence[str], mode: str
) -> int:
    """Posição 1-based da primeira sessão gold; 0 = não apareceu no top-LIMIT."""
    skip_embed, skip_cognitive = MODES[mode]
    wanted = set(gold_uuids)
    for attempt in range(3):
        try:
            response = await memory.search_with_retrieval(
                question, ["*"], limit=LIMIT,
                skip_query_embed=skip_embed,
                skip_cognitive_rerank=skip_cognitive,
            )
            for position, hit in enumerate(response.results, start=1):
                if hit.record.uuid in wanted:
                    return position
            return 0
        except AnhurAuthError:
            raise
        except Exception:  # noqa: BLE001
            if attempt == 2:
                return -1  # falha de transporte — NÃO é miss, é ausência de medida
            await asyncio.sleep(1.5 * (attempt + 1))
    return -1


async def run_mode(gold: Dict[str, Any], mode: str) -> Dict[str, int]:
    """Roda todas as perguntas num modo. Devolve question_id -> rank."""
    ranks: Dict[str, int] = {}
    semaphore = asyncio.Semaphore(CONCURRENCY)
    async with Memory(api_key=API_KEY, url=BASE, tenant_id=TENANT) as memory:
        async def worker(question_id: str, entry: Dict[str, Any]) -> None:
            async with semaphore:
                ranks[question_id] = await rank_of_gold(
                    memory, entry["question"], entry["relevant_uuids"], mode)
        await asyncio.gather(*(worker(qid, e) for qid, e in gold.items()))
    return ranks


def reciprocal(rank: int) -> float:
    return 1.0 / rank if rank and rank > 0 else 0.0


def bootstrap_difference(
    paired: List[Tuple[float, float]], samples: int
) -> Tuple[float, float, float, int, int]:
    """Diferença média pareada e IC 95% por bootstrap.

    Junior Tip [pareado, não duas médias independentes]: a variância ENTRE
    perguntas é enorme (umas são triviais, outras impossíveis) e é idêntica nas
    duas configurações. Comparar médias independentes carrega essa variância
    para dentro do intervalo e esconde qualquer efeito real. A diferença por
    consulta a cancela.
    """
    differences = [second - first for first, second in paired]
    observed = statistics.mean(differences)
    resampled = sorted(
        statistics.mean(random.choices(differences, k=len(differences)))
        for _ in range(samples)
    )
    lower = resampled[int(0.025 * samples)]
    upper = resampled[int(0.975 * samples)]
    better = sum(1 for value in differences if value > 1e-9)
    worse = sum(1 for value in differences if value < -1e-9)
    return observed, lower, upper, better, worse


def recall_at(ranks: Dict[str, int], k: int) -> float:
    measured = [r for r in ranks.values() if r >= 0]
    if not measured:
        return 0.0
    return 100.0 * sum(1 for r in measured if 1 <= r <= k) / len(measured)


def report(mode_a: str, ranks_a: Dict[str, int], mode_b: str, ranks_b: Dict[str, int]) -> None:
    common = sorted(set(ranks_a) & set(ranks_b))
    usable = [q for q in common if ranks_a[q] >= 0 and ranks_b[q] >= 0]
    unmeasured = len(common) - len(usable)

    print("\nconsultas pareadas: %d" % len(usable))
    if unmeasured:
        print("NÃO MEDIDAS (falha de transporte, excluídas): %d — não são miss" % unmeasured)

    print("\n%-14s %8s %8s %8s" % ("modo", "r@1", "r@5", "r@10"))
    for mode, ranks in ((mode_a, ranks_a), (mode_b, ranks_b)):
        print("%-14s %7.1f%% %7.1f%% %7.1f%%"
              % (mode, recall_at(ranks, 1), recall_at(ranks, 5), recall_at(ranks, 10)))

    print("\ndiferença PAREADA (%s menos %s), bootstrap %d amostras" % (mode_b, mode_a, BOOTSTRAP_SAMPLES))

    mrr_pairs = [(reciprocal(ranks_a[q]), reciprocal(ranks_b[q])) for q in usable]
    observed, lower, upper, better, worse = bootstrap_difference(mrr_pairs, BOOTSTRAP_SAMPLES)
    verdict = "DETECTÁVEL" if not (lower <= 0 <= upper) else "dentro do ruído"
    print("  MRR    %+7.3f   IC 95%% [%+.3f, %+.3f]   %d melhores / %d piores   %s"
          % (observed, lower, upper, better, worse, verdict))

    for k in (1, 5):
        hit_pairs = [
            (1.0 if 1 <= ranks_a[q] <= k else 0.0, 1.0 if 1 <= ranks_b[q] <= k else 0.0)
            for q in usable
        ]
        observed, lower, upper, better, worse = bootstrap_difference(hit_pairs, BOOTSTRAP_SAMPLES)
        verdict = "DETECTÁVEL" if not (lower <= 0 <= upper) else "dentro do ruído"
        print("  hit@%-2d %+6.1f pt   IC 95%% [%+.1f, %+.1f]   %d melhores / %d piores   %s"
              % (k, 100 * observed, 100 * lower, 100 * upper, better, worse, verdict))

    print("\n  (IC atravessando zero = este n não resolve esta diferença;")
    print("   NÃO é o mesmo que 'as configurações são iguais')")


def main() -> None:
    if len(sys.argv) < 4 or sys.argv[1] != "compare":
        sys.exit("uso: rbench_dense.py compare <modo_a> <modo_b>   modos: %s"
                 % ", ".join(MODES))
    mode_a, mode_b = sys.argv[2], sys.argv[3]
    for mode in (mode_a, mode_b):
        if mode not in MODES:
            sys.exit("modo desconhecido: %s" % mode)
    if not API_KEY or not TENANT:
        sys.exit("defina ANHUR_API_KEY e DENSE_TENANT")

    random.seed(int(os.environ.get("DENSE_SEED", "20260815")))
    with open(GOLD_PATH) as gold_file:
        gold = json.load(gold_file)
    print("gold: %d perguntas   tenant: %s" % (len(gold), TENANT))

    ranks_a = asyncio.run(run_mode(gold, mode_a))
    print("  %s medido" % mode_a, flush=True)
    ranks_b = asyncio.run(run_mode(gold, mode_b))
    print("  %s medido" % mode_b, flush=True)

    report(mode_a, ranks_a, mode_b, ranks_b)


if __name__ == "__main__":
    main()
