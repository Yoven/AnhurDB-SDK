#!/usr/bin/env python3
"""Machine-check that rbench_sdk.py measures the same thing as rbench.py.

Runs offline — no server, no API key, no corpus. Import both harnesses and
compare every deterministic decision that shapes what gets ingested and how it
is scored. If this fails, the two harnesses no longer produce comparable
numbers and the SDK results must not be published next to the paper's.

    python3 test_parity.py

Junior Tip [why this file exists]: the frozen REST harness is the artifact
behind the published table, and the SDK harness is the recommended path. That
pairing is only honest while the two agree. A silent drift here would look
like a real quality regression in the engine, which is exactly the class of
confusion this benchmark is supposed to eliminate.
"""
import importlib.util
import os
import random
import sys
from typing import Any, Dict, List

os.environ.setdefault("ANHUR_API_KEY", "offline-parity-check")


def load_module(module_name: str, file_path: str) -> Any:
    """Import a harness by path so both can coexist in one process."""
    spec = importlib.util.spec_from_file_location(module_name, file_path)
    if spec is None or spec.loader is None:
        sys.exit("cannot load %s" % file_path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


HERE = os.path.dirname(os.path.abspath(__file__))
rest_harness = load_module("rest_harness", os.path.join(HERE, "rbench.py"))
sdk_harness = load_module("sdk_harness", os.path.join(HERE, "rbench_sdk.py"))

failures: List[str] = []


def check(condition: bool, description: str) -> None:
    """Record a failure instead of aborting, so one run reports everything."""
    if not condition:
        failures.append(description)


# ── 1. session UUID derivation ────────────────────────────────────────────────
for question_id in ["q1", "abc-123", "x" * 40, "unicode-ção"]:
    for source_id in ["s1", "sess_9", "", "0"]:
        check(
            rest_harness.det_uuid(question_id, source_id)
            == sdk_harness.deterministic_session_uuid(question_id, source_id),
            "session uuid diverged for (%s, %s)" % (question_id, source_id),
        )

# ── 2. chatty-session selection (hash-stable) ─────────────────────────────────
for index in range(500):
    session_uuid = rest_harness.det_uuid("q%d" % index, "s%d" % index)
    turn_count = random.Random(index).randint(0, 8)
    turns = [{"role": "user", "content": "c%d" % turn_index} for turn_index in range(turn_count)]
    check(
        rest_harness.is_chatty_session(session_uuid, turns)
        == sdk_harness.is_chatty_session(session_uuid, turns),
        "chatty selection diverged at index %d" % index,
    )

# ── 3. turn chunking ──────────────────────────────────────────────────────────
for turn_count in range(0, 15):
    turns = [{"role": "user", "content": str(i)} for i in range(turn_count)]
    for chunk_count in (1, 2, 3, 4):
        check(
            rest_harness.chunk_turns_for_chat(turns, chunk_count)
            == sdk_harness.chunk_turns_for_chat(turns, chunk_count),
            "chunking diverged for %d turns into %d chunks" % (turn_count, chunk_count),
        )

# ── 4. record body: content and summary ───────────────────────────────────────
# Junior Tip [long turns are mandatory here]: the summary is a TRUNCATION of the
# first user turn. With short fixtures every truncation length agrees, so a
# changed cut-off would slip through green. Every case below must exceed the
# cut-off comfortably.
for turn_count in range(0, 6):
    turns = [
        {
            "role": "assistant" if i % 2 else "user",
            "content": "texto %d " % i + "palavra " * 60,  # ~500 chars, well past any cut-off
        }
        for i in range(turn_count)
    ]
    check(
        rest_harness.turns_to_content(turns) == sdk_harness.turns_to_content(turns),
        "content formatting diverged at %d turns" % turn_count,
    )
    check(
        rest_harness.first_user_summary(turns) == sdk_harness.first_user_summary(turns),
        "summary derivation diverged at %d turns" % turn_count,
    )

# ── 5. question sampling ──────────────────────────────────────────────────────
# Junior Tip [insertion order must NOT equal sorted order]: both harnesses walk
# the type buckets with sorted(). If the fixture's types happened to appear in
# alphabetical order, dropping that sorted() would still produce an identical
# list and the mutation would pass unnoticed. These names are deliberately
# inserted in reverse.
synthetic_oracle: List[Dict[str, Any]] = [
    {
        "question_id": "q%d" % i,
        "question_type": ["zeta", "mu", "alpha"][i % 3],
        "answer_session_ids": [],
        "haystack_session_ids": [],
        "haystack_sessions": [],
    }
    for i in range(60)
]
for per_type in (1, 5, 34):
    rest_ids = [q["question_id"] for q in rest_harness.pick(synthetic_oracle, per_type)]
    sdk_ids = [q["question_id"] for q in sdk_harness.pick_questions(synthetic_oracle, per_type)]
    check(rest_ids == sdk_ids, "sampling diverged for per_type=%d" % per_type)

# ── 6. ingest payload ─────────────────────────────────────────────────────────
# The REST harness hand-builds its body; the SDK serializes a model with its own
# defaults. Fields the REST body omits must reach the server as the Go zero value
# (or a value the server maps to the same thing, e.g. status "" -> "saved").
try:
    # Long enough that the summary truncation is actually exercised.
    sample_turns = [
        {"role": "user", "content": "primeira pergunta " + "detalhe " * 60},
        {"role": "assistant", "content": "resposta " + "contexto " * 60},
    ]
    # The REST body, reproduced from rbench.post_episodic (which needs a live
    # server, so its payload shape is mirrored here rather than called).
    rest_body = {
        "session_id": "S",
        "type": "episodic",
        "summary": rest_harness.first_user_summary(sample_turns),
        "content": rest_harness.turns_to_content(sample_turns),
        "weight": 1.0,
    }
    # Ask the SDK harness for the REAL object it would send. Building a copy
    # here instead would let the harness drift while this test stayed green —
    # which is exactly how a score=5 regression once slipped past it.
    sdk_body = sdk_harness.build_create_request("S", sample_turns).model_dump(exclude_none=True)

    # Values the server treats as "not supplied".
    inert_defaults = {
        "uuid": "", "score": 0, "status": "saved", "related_ids": [], "metadata": "",
        "dimension": 0, "vector": "", "prefix": "", "main_ids": [],
        "consolidated": False, "consolidate_id": 0,
    }
    for field_name, sdk_value in sdk_body.items():
        if field_name in rest_body:
            check(
                sdk_value == rest_body[field_name],
                "ingest field %r diverged: sdk=%r rest=%r" % (field_name, sdk_value, rest_body[field_name]),
            )
        else:
            check(
                field_name in inert_defaults and sdk_body[field_name] == inert_defaults[field_name],
                "ingest sends non-inert extra field %r=%r" % (field_name, sdk_value),
            )
except ImportError:
    print("NOTE: anhurdb SDK not installed — skipped ingest payload check")


if failures:
    print("PARITY FAILURES: %d" % len(failures))
    for failure in failures:
        print("  - %s" % failure)
    sys.exit(1)
print("parity OK — sampling, uuids, chunking, record body and ingest payload all match")
