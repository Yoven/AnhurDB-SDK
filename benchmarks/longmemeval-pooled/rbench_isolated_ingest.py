#!/usr/bin/env python3
"""Building and WRITING the isolated LongMemEval corpus.

This module owns one responsibility: turn ``longmemeval_s`` haystacks into
sessions and records inside a tenant, and emit the gold file that says which
session identifiers count as evidence for each question. Scoring that corpus is
a different job and lives in ``rbench_isolated.py``, which imports ``ingest``
and ``GOLD_PATH`` from here.

Junior Tip [why the corpus builder owns GOLD_PATH]: the gold file is this
module's OUTPUT contract — the scorer is a consumer of it. Pointing the
dependency this way (scorer imports builder, never the reverse) keeps the two
files free of a circular import and makes it obvious which side is allowed to
change the gold schema.
"""
import asyncio
import hashlib
import json
import os
import time
from typing import Any, Dict, List, Sequence, Tuple

from rbench_sdk import (
    build_create_request,
    chunk_turns_for_chat,
    deterministic_session_uuid,
    fail_loud,
    is_chatty_session,
    open_memory,
    save_ingest_manifest,
)
from anhurdb import AnhurAuthError, AnhurError

S_CORPUS_PATH = os.environ.get("RB_S_CORPUS", "/tmp/longmemeval_s.json")
PINNED_IDS_PATH = os.environ.get(
    "RB_PINNED_IDS", os.path.join(os.path.dirname(os.path.abspath(__file__)), "question_ids.json")
)
GOLD_PATH = os.environ.get("RB_ISO_GOLD", "/tmp/rbench_isolated_gold.json")

# One unit of ingest work: everything that must be written in order, together.
SessionWork = Tuple[str, Sequence[Any]]


class IngestCounters:
    """Mutable tallies shared by the worker pool.

    Junior Tip [why a plain object is safe here]: these workers are asyncio
    coroutines on ONE thread, and ``+=`` on an attribute contains no ``await``,
    so no other worker can interleave inside it. A lock would buy nothing and
    would hide that fact from the next reader. This reasoning stops being true
    the moment anyone moves this pool to threads.
    """

    def __init__(self) -> None:
        self.records_ok = 0
        self.records_err = 0
        self.chatty_sessions = 0
        self.errors_printed = 0
        self.started_at = time.time()


def s_corpus_sha256() -> str:
    """Checksum of the longmemeval_s file this run actually read.

    Junior Tip [why this exists next to the pooled harness's own checksum]:
    ``save_ingest_manifest`` stamps the POOLED corpus (``longmemeval_oracle``)
    because that is the file the pooled harness reads. An isolated run reads a
    different file, so without this the manifest would carry a checksum of a
    corpus that had nothing to do with the number — provenance that looks
    rigorous and is wrong.
    """
    if not os.path.exists(S_CORPUS_PATH):
        return ""
    digest = hashlib.sha256()
    with open(S_CORPUS_PATH, "rb") as corpus_file:
        for block in iter(lambda: corpus_file.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def load_pinned_questions() -> List[Dict[str, Any]]:
    """The paper's exact 200 questions, with their longmemeval_s haystacks."""
    pinned_ids = json.load(open(PINNED_IDS_PATH))["question_ids"]
    corpus_by_id = {question["question_id"]: question for question in json.load(open(S_CORPUS_PATH))}
    missing = [qid for qid in pinned_ids if qid not in corpus_by_id]
    if missing:
        fail_loud("load_pinned_questions",
                  RuntimeError("%d pinned question ids absent from %s (first: %s)"
                               % (len(missing), S_CORPUS_PATH, missing[0])))
    return [corpus_by_id[qid] for qid in pinned_ids]


def build_gold(questions: List[Dict[str, Any]]) -> Dict[str, Any]:
    """Derive the answer key from the corpus alone — never from ingest results.

    Junior Tip [why this runs BEFORE any write, 2026-08-31]: the gold is a pure
    function of the pinned questions, so building it up front makes it
    independent of the order in which sessions happen to be written. When the
    ingest became concurrent, a gold assembled inside the write loop would have
    become order-dependent — the classic way a benchmark silently stops
    measuring the same thing after a performance change.
    """
    gold: Dict[str, Any] = {}
    for question in questions:
        question_id = question["question_id"]
        gold[question_id] = {
            "question": question["question"],
            "type": question["question_type"],
            "relevant_uuids": [deterministic_session_uuid(question_id, sid)
                               for sid in set(question["answer_session_ids"])],
            "scope_uuids": [deterministic_session_uuid(question_id, sid)
                            for sid in question["haystack_session_ids"]],
        }
    return gold


def build_work_units(questions: List[Dict[str, Any]]) -> List[SessionWork]:
    """Flatten every question's haystack into independent per-session units."""
    work_units: List[SessionWork] = []
    for question in questions:
        question_id = question["question_id"]
        for source_id, turns in zip(question["haystack_session_ids"], question["haystack_sessions"]):
            work_units.append((deterministic_session_uuid(question_id, source_id), turns))
    return work_units


async def create_session_with_retry(memory, session_uuid: str, attempts: int = 6) -> None:
    """Register a session, riding out a leader election instead of dying on it.

    Junior Tip [why 503 must NOT be fatal here, measured 2026-08-30]: the first
    isolated ingest aborted after 175 sessions on
    ``503 no leader available``. That status is the replication layer telling
    the client "retry in a moment" — the write never landed, nothing is
    ambiguous, and a leader election resolves in seconds. Treating it as fatal
    threw away a 90-minute ingest for a condition the caller is supposed to
    absorb. A 4xx contract error still aborts (retrying repeats it
    identically), and an auth error still aborts loudly: those are the failures
    that would otherwise be silently scored as low recall.
    """
    last_error: BaseException | None = None
    for attempt_index in range(attempts):
        try:
            await memory.create_session(session_id=session_uuid)
            return
        except AnhurAuthError as auth_error:
            fail_loud("create_session (check key/tenant)", auth_error)
        except AnhurError as session_error:
            last_error = session_error
            status = session_error.status_code
            if status == 409:            # already registered — the idempotent path
                return
            if status not in (429, 500, 502, 503, 504):
                fail_loud("create_session %s" % session_uuid[:13], session_error)
            await asyncio.sleep(min(2.0 * (attempt_index + 1), 10.0))
        except Exception as unexpected_error:
            last_error = unexpected_error
            await asyncio.sleep(min(2.0 * (attempt_index + 1), 10.0))
    # Junior Tip [every exit of this function must be conclusive — bug found
    # 2026-08-30]: an earlier version could fall off the loop without raising.
    # The caller then wrote records into a session that does not exist, the
    # server answered 400 "session_id is required", and those records were
    # counted as errors and silently dropped from the corpus — the exact
    # silent-loss shape this whole project exists to prevent. Exhausting the
    # retries is a failed ingest, not a warning.
    fail_loud("create_session %s after %d attempts" % (session_uuid[:13], attempts),
              last_error or RuntimeError("retries exhausted with no error recorded"))


async def write_one_session(memory, unit: SessionWork, counters: IngestCounters,
                            chatty_chunk_count: int, sleep_s: float, progress_every: int) -> None:
    """Register one session and write its records, strictly in that order."""
    session_uuid, turns = unit
    await create_session_with_retry(memory, session_uuid)
    if is_chatty_session(session_uuid, turns):
        counters.chatty_sessions += 1
        pieces = chunk_turns_for_chat(turns, chatty_chunk_count)
    else:
        pieces = [list(turns)]
    for piece in pieces:
        # Junior Tip [why a record write is retried on 400 "session_id is
        # required" — measured 2026-08-30, fixed server-side the same day in
        # commit 7e77389]: the server gates this write on SessionExists, which
        # USED TO read the tenant's READ pool (a WAL snapshot). A session
        # registered milliseconds earlier could be invisible there, so a client
        # that did everything right got its record REFUSED and the corpus
        # silently lost it. The gate now reads the write connection, but this
        # retry stays: it costs nothing on the fixed path, and a corpus gap is
        # never an acceptable outcome regardless of which layer causes it.
        for create_attempt in range(4):
            try:
                await memory.create(build_create_request(session_uuid, piece))
                counters.records_ok += 1
                break
            except Exception as create_error:
                message = str(create_error)
                transient = ("session_id is required" in message
                             or "no leader available" in message
                             or "consensus timed out" in message
                             or "HTTP 503" in message or "HTTP 500" in message)
                if not transient or create_attempt == 3:
                    counters.records_err += 1
                    if counters.errors_printed < 8:
                        counters.errors_printed += 1
                        print("  ERR", message[:110], flush=True)
                    break
                await asyncio.sleep(1.5 * (create_attempt + 1))
        attempted = counters.records_ok + counters.records_err
        if progress_every > 0 and attempted % progress_every == 0:
            elapsed_s = time.time() - counters.started_at
            print("  ... %d records (%d err) in %.0fs — %.2f rec/s"
                  % (attempted, counters.records_err, elapsed_s, attempted / max(elapsed_s, 1e-9)),
                  flush=True)
        if sleep_s > 0:
            await asyncio.sleep(sleep_s)


async def run_session_pool(memory, work_units: List[SessionWork], counters: IngestCounters,
                           concurrency: int, chatty_chunk_count: int,
                           sleep_s: float, progress_every: int) -> None:
    """Write every session unit with a bounded number of them in flight.

    Junior Tip [why the unit of concurrency is a SESSION, not a record —
    2026-08-31]: a record write is gated on its session already existing, so
    two writes from the SAME session must stay ordered. Records from DIFFERENT
    sessions have no such relation. Taking a whole session as the unit gives
    every bit of the available parallelism while keeping the one ordering the
    server actually enforces. Fanning out per record would race a session
    against its own registration and reintroduce, from the client side, exactly
    the 400 that commit 7e77389 removed from the server side.

    Junior Tip [why this is bounded and not ``gather`` over everything]: the
    corpus is ~10k sessions. Launching them all would open ~10k concurrent
    requests, which is not a measurement of the engine — it is a denial of
    service against it, and it was an unbounded burst of this shape that put
    the embedding service into a restart loop on 2026-08-30.
    """
    pending: asyncio.Queue = asyncio.Queue()
    for unit in work_units:
        pending.put_nowait(unit)

    async def worker() -> None:
        while True:
            try:
                unit = pending.get_nowait()
            except asyncio.QueueEmpty:
                return
            await write_one_session(memory, unit, counters, chatty_chunk_count,
                                    sleep_s, progress_every)

    await asyncio.gather(*[worker() for _ in range(max(1, concurrency))])


async def ingest() -> None:
    """Ingest every pinned question's OWN haystack; per-question UUIDs isolate scopes.

    Junior Tip [isolation comes from the UUID salt, not from tenants]: the
    session UUID is sha256("longmemeval:<qid>:<sid>"), so the same distractor
    session appearing in two questions' haystacks becomes two DIFFERENT
    sessions here. Scoping a query to its question's UUID list therefore
    reproduces the official per-question isolation inside one tenant.
    """
    questions = load_pinned_questions()
    chatty_chunk_count = int(os.environ.get("RB_CHATTY_CHUNKS", "3") or "3")
    # Junior Tip [why the default sleep is now 0 and the throttle is the pool
    # size — 2026-08-31]: the 400 ms pause existed because the ingest was
    # serial and a tighter loop simply produced errors. Concurrency makes the
    # pool size the honest throttle: it caps requests IN FLIGHT, which is what
    # the server and the embedding pipeline actually feel, whereas a per-worker
    # sleep caps a rate that then multiplies by the worker count anyway.
    ingest_sleep_s = float(os.environ.get("RB_INGEST_SLEEP_MS", "0") or "0") / 1000.0
    ingest_concurrency = int(os.environ.get("RB_INGEST_CONCURRENCY", "12") or "12")
    progress_every = int(os.environ.get("RB_INGEST_PROGRESS_EVERY", "500") or "0")

    gold = build_gold(questions)
    work_units = build_work_units(questions)
    counters = IngestCounters()

    memory = open_memory()
    await memory.connect()
    print("INGEST(isolated): %d questions, %d sessions, concurrency=%d"
          % (len(questions), len(work_units), ingest_concurrency), flush=True)
    try:
        await run_session_pool(memory, work_units, counters, ingest_concurrency,
                               chatty_chunk_count, ingest_sleep_s, progress_every)
    finally:
        await memory.close()

    json.dump(gold, open(GOLD_PATH, "w"))
    elapsed_s = time.time() - counters.started_at
    print("INGEST(isolated): %d questions, %d records ok, %d err, %d chatty in %.0fs (%.2f rec/s)"
          % (len(questions), counters.records_ok, counters.records_err,
             counters.chatty_sessions, elapsed_s,
             (counters.records_ok + counters.records_err) / max(elapsed_s, 1e-9)))
    save_ingest_manifest({
        "protocol": "isolated-longmemeval_s",
        "s_corpus_file": os.path.basename(S_CORPUS_PATH),
        "s_corpus_sha256": s_corpus_sha256(),
        "questions": len(questions), "sessions": len(work_units),
        "records_ok": counters.records_ok, "records_err": counters.records_err,
        "chatty_sessions": counters.chatty_sessions, "elapsed_s": round(elapsed_s, 1),
        "ingest_sleep_ms": round(ingest_sleep_s * 1000),
        "ingest_concurrency": ingest_concurrency,
    })
