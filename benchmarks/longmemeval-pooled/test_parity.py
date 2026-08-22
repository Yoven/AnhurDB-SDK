#!/usr/bin/env python3
"""Contract test for the harness: every deterministic decision that shapes the
corpus and the sample is pinned against committed golden values.

Runs offline — no server, no API key, no corpus:

    python3 test_parity.py

Why goldens and not a second implementation: the golden values were generated
from the exact harness build that produced every number in the paper. If this
test fails, the harness no longer builds the corpus those numbers came from,
and new results must not be published next to them. The goldens live in
contract_gold.json, committed beside this file; regenerating them is a
deliberate act that must be stated in the paper's reproducibility appendix.
"""
import importlib.util
import json
import os
import sys
from typing import Any, List

os.environ.setdefault("ANHUR_API_KEY", "offline-contract-check")

HERE = os.path.dirname(os.path.abspath(__file__))


def load_module(module_name: str, file_path: str) -> Any:
    spec = importlib.util.spec_from_file_location(module_name, file_path)
    if spec is None or spec.loader is None:
        sys.exit("cannot load %s" % file_path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


harness = load_module("harness", os.path.join(HERE, "rbench_sdk.py"))
gold = json.load(open(os.path.join(HERE, "contract_gold.json")))

failures: List[str] = []


def check(condition: bool, description: str) -> None:
    if not condition:
        failures.append(description)


def turns_fixture(count: int) -> list:
    # Long content on purpose: the summary is a truncation of the first user
    # turn, and short fixtures would let a changed cut-off slip through green.
    return [
        {"role": "assistant" if i % 2 else "user",
         "content": "texto %d " % i + "palavra " * 60}
        for i in range(count)
    ]


# 1. session UUID derivation — identity of every session in the corpus
for key, expected in gold["uuids"].items():
    question_id, source_id = key.split("|", 1)
    check(harness.deterministic_session_uuid(question_id, source_id) == expected,
          "uuid diverged for %s" % key)

# 2. chatty-session selection — which sessions get chunked
for index_str, expected in gold["chatty"].items():
    index = int(index_str)
    session_uuid = harness.deterministic_session_uuid("q%d" % index, "s%d" % index)
    check(harness.is_chatty_session(session_uuid, turns_fixture(4)) == expected,
          "chatty selection diverged at %s" % index_str)

# 3. turn chunking — how a chatty session becomes records
for key, expected in gold["chunks"].items():
    turn_count, chunk_count = (int(x) for x in key.split("|"))
    got = harness.chunk_turns_for_chat(
        [{"role": "user", "content": str(i)} for i in range(turn_count)], chunk_count)
    check(got == expected, "chunking diverged for %s" % key)

# 4. record body — the exact text ingested
check(harness.turns_to_content(turns_fixture(2)) == gold["content_2"],
      "content formatting diverged")
check(harness.first_user_summary(turns_fixture(2)) == gold["summary_2"],
      "summary derivation diverged")

# 5. question sampling — which questions the run asks, in which order
oracle = [
    {"question_id": "q%d" % i,
     "question_type": ["zeta", "mu", "alpha"][i % 3],
     "answer_session_ids": [], "haystack_session_ids": [], "haystack_sessions": []}
    for i in range(60)
]
check([q["question_id"] for q in harness.pick_questions(oracle, 5)] == gold["pick_5"],
      "question sampling diverged")

# 6. the write itself — weight and score are pinned BY HAND in the harness
#    (weight=1.0, score=0); SDK defaults (0.5, 5) would ingest a different
#    corpus whose salience term V shifts every rank.
req = harness.build_create_request("S", turns_fixture(2))
for field, expected in gold["create"].items():
    check(getattr(req, field) == expected, "create.%s diverged" % field)

if failures:
    print("CONTRACT BROKEN (%d):" % len(failures))
    for failure in failures:
        print("  - %s" % failure)
    sys.exit(1)
print("contract intact: %d pinned decisions verified" % (
    len(gold["uuids"]) + len(gold["chatty"]) + len(gold["chunks"])
    + 2 + 1 + len(gold["create"])))
