#!/usr/bin/env python3
"""LongMemEval ISOLATED-protocol harness — the market-comparable companion.

The pooled harness (``rbench_sdk.py``) ingests every question's evidence into
one flat tenant and lets every query fight every other question's sessions.
That is the paper's protocol: strictly harder than the official one, biased
downward on purpose. THIS harness measures the other end, for numbers that can
sit next to vendor marketing without a footnote war:

  * corpus: ``longmemeval_s`` — the official variant vendors quote, where each
    question carries its OWN haystack of ~50 sessions (evidence + distractors);
  * isolation: each query is scoped to its own question's session set via the
    search API's ``sessions`` filter — the official per-question protocol;
  * metric: UNCHANGED — evidence recall@k by session identifier, objective,
    no LLM judge. We do not adopt judged accuracy just to inflate the number.

Junior Tip [why this file imports the pooled harness instead of copying it]:
``deterministic_session_uuid``, the chatty split, and ``build_create_request``
are PARITY-CRITICAL — pinned by ``test_parity.py`` against committed goldens.
Importing them means the isolated corpus is built by the exact code the
contract checks; copying them would create a second implementation that can
drift silently, which is this repo's most-burned failure class.

Junior Tip [why the question sample comes from question_ids.json, not from
file order]: the pooled paper runs pinned WHICH 200 questions are measured.
``longmemeval_s`` is a different file with its own ordering; re-deriving the
sample from ITS file order could silently select different questions. Reading
the committed pin makes "same questions, different protocol" true by
construction.

The corpus BUILDER lives in ``rbench_isolated_ingest.py``; this file keeps the
protocol and the scoring. Both are driven from the CLI below.

Usage:
    export ANHUR_API_KEY=...              # fresh, EMPTY tenant — never reuse
    export ANHUR_BASE=https://...
    export RB_S_CORPUS=/path/longmemeval_s
    python3 rbench_isolated.py ingest     # ~10k sessions for the 200-q pin
    python3 rbench_isolated.py recall  tag1
    python3 rbench_isolated.py lexical tag1
"""
import asyncio
import json
import os
import sys
import time
from typing import Any, Dict, List

from rbench_sdk import RESULTS_DIR, fail_loud, open_memory
from rbench_isolated_ingest import GOLD_PATH, ingest
from anhurdb import AnhurAuthError, AnhurError


async def score(tag: str, lexical_only: bool) -> None:
    """Score with each query scoped to its question's own sessions."""
    gold: Dict[str, Any] = json.load(open(GOLD_PATH))
    search_attempts = int(os.environ.get("RB_SEARCH_RETRIES", "3") or "3")
    hits_at = {1: 0, 5: 0, 10: 0}
    latencies: List[float] = []
    per_question: List[Dict[str, Any]] = []
    transient_failures = 0
    memory = open_memory()
    await memory.connect()
    for question_id, entry in gold.items():
        relevant = set(entry["relevant_uuids"])
        result_uuids: List[str] = []
        search_succeeded = False
        for attempt_index in range(search_attempts):
            try:
                query_started = time.time()
                response = await memory.search_with_retrieval(
                    entry["question"], entry["scope_uuids"], limit=10,
                    skip_query_embed=lexical_only, skip_cognitive_rerank=True,
                )
                latencies.append(time.time() - query_started)
                result_uuids = [hit.record.uuid for hit in response.results]
                search_succeeded = True
                break
            except AnhurAuthError as auth_error:
                fail_loud("search (check key/tenant)", auth_error)
            except AnhurError as search_error:
                status = search_error.status_code
                if status is None or (400 <= status < 500 and status != 429):
                    fail_loud("search %s" % question_id, search_error)
                await asyncio.sleep(1.5 * (attempt_index + 1))
        if not search_succeeded:
            transient_failures += 1
        rank_of_first_hit = 0
        for position, uuid in enumerate(result_uuids, 1):
            if uuid in relevant:
                rank_of_first_hit = position
                break
        for cutoff in hits_at:
            if 0 < rank_of_first_hit <= cutoff:
                hits_at[cutoff] += 1
        per_question.append({
            "question_id": question_id, "type": entry["type"],
            "rank_of_first_hit": rank_of_first_hit,
            "hit_at_1": 0 < rank_of_first_hit <= 1,
            "hit_at_5": 0 < rank_of_first_hit <= 5,
            "hit_at_10": 0 < rank_of_first_hit <= 10,
            "scope_size": len(entry["scope_uuids"]),
            "latency_s": round(latencies[-1], 4) if search_succeeded else None,
            "search_failed": not search_succeeded,
        })
    await memory.close()
    asked = len(per_question)
    latencies.sort()
    mode = "isolated-lexical-pure" if lexical_only else "isolated-hybrid"
    print("\n=== ISOLATED [%s] n=%d mode=%s ===" % (tag, asked, mode))
    print("  recall@1=%.1f%%  recall@5=%.1f%%  recall@10=%.1f%%  | p50=%.2fs p95=%.2fs"
          % (100 * hits_at[1] / asked, 100 * hits_at[5] / asked, 100 * hits_at[10] / asked,
             latencies[len(latencies) // 2], latencies[int(len(latencies) * 0.95)]))
    if transient_failures:
        print("  WARNING: %d transient failures — DISCARD this run." % transient_failures)
    if RESULTS_DIR:
        os.makedirs(RESULTS_DIR, exist_ok=True)
        payload = {
            "harness": "rbench_isolated", "tag": tag, "mode": mode,
            "protocol": "official per-question isolation over longmemeval_s haystacks",
            "finished_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "results": {
                "n": asked,
                "recall_at_1": round(100 * hits_at[1] / asked, 2),
                "recall_at_5": round(100 * hits_at[5] / asked, 2),
                "recall_at_10": round(100 * hits_at[10] / asked, 2),
                "latency_p50_s": round(latencies[len(latencies) // 2], 4),
                "latency_p95_s": round(latencies[int(len(latencies) * 0.95)], 4),
                "transient_failures": transient_failures,
            },
            "per_question": per_question,
        }
        output_path = os.path.join(RESULTS_DIR, "%s-%s.json" % (tag, mode))
        with open(output_path, "w") as results_file:
            json.dump(payload, results_file, indent=2, sort_keys=True)
            results_file.write("\n")
        print("  results -> %s" % output_path)


if __name__ == "__main__":
    cli_mode = sys.argv[1] if len(sys.argv) > 1 else "recall"
    run_tag = sys.argv[2] if len(sys.argv) > 2 else "run"
    if cli_mode == "ingest":
        asyncio.run(ingest())
    elif cli_mode == "lexical":
        asyncio.run(score(run_tag, lexical_only=True))
    else:
        asyncio.run(score(run_tag, lexical_only=False))
