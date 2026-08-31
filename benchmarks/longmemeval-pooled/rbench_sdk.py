#!/usr/bin/env python3
"""LongMemEval pooled recall@k harness — SDK edition.

This is the RECOMMENDED replication path: it exercises AnhurDB through the
official Python SDK, i.e. exactly the client surface a customer uses. The
original ``rbench.py`` (same directory) is the FROZEN artifact that produced
the numbers in the paper — it speaks raw REST and must never be edited.

Parity contract with rbench.py (do not break):
  * question sampling  — first RB_NPER per question_type, types sorted,
    corpus file order (deterministic; pin the oracle JSON by sha256);
  * session UUIDs      — sha256("longmemeval:<qid>:<sid>"), same layout;
  * chatty split       — hash-stable RB_CHATTY_FRAC of sessions with >=3
    turns get RB_CHATTY_CHUNKS contiguous episodics under one session;
  * ingest payload     — weight=1.0 AND score=0 pinned explicitly, because
    CreateRequest defaults (0.5 / 5) would ingest a different corpus;
  * search payload     — text + limit + sessions=["*"], scope "sessions"
    (the server default), no reranker or expansion flags;
  * scoring            — recall@k by session UUID over the top-10 hits.

The deterministic half of this contract is machine-checked by
``test_parity.py``, which imports both harnesses and compares outputs.

What differs (by design, both closer to the product):
  * search embedding is always SERVER-side (the product path); the lab-only
    client-vector mode of rbench.py does not exist here;
  * the lexical-only mode is expressed as ``skip_query_embed=True`` on the
    SDK call instead of a server-side configuration.

Usage:
    export ANHUR_API_KEY=...            # never commit this
    export ANHUR_BASE=https://...       # default http://localhost:8000
    export RB_ORACLE=/path/oracle.json  # official LongMemEval release
    export RB_NPER=34                   # paper protocol (n=200)
    export RB_INGEST_SLEEP_MS=400       # pace between record creates (0 = burst)
    export RB_INGEST_TAG=paper-200q     # names the ingest manifest
    python3 rbench_sdk.py ingest
    python3 rbench_sdk.py recall  run1  # hybrid (server auto-embed)
    python3 rbench_sdk.py lexical run1  # FTS-only leg
"""
import asyncio
import hashlib
import json
import os
import sys
import time
from typing import Any, Dict, List, NoReturn, Sequence

from anhurdb import AnhurAuthError, AnhurError, CreateRequest, Memory

BASE_URL = (os.environ.get("ANHUR_BASE") or os.environ.get("ANHUR_URL") or "http://localhost:8000").rstrip("/")
API_KEY = os.environ.get("ANHUR_MK") or os.environ.get("ANHUR_API_KEY")
if not API_KEY:
    sys.exit("set ANHUR_MK or ANHUR_API_KEY")
# Junior Tip [master-key path]: single-tenant API keys already encode the
# tenant. Only master keys need an explicit tenant, mirrored from rbench.py.
CLIENT_ID = os.environ.get("RB_CLIENT_ID", "")
TENANT_ID = (CLIENT_ID + "_" if CLIENT_ID else "") + os.environ.get("RB_TENANT", "lme-pool")
SEND_TENANT = bool(CLIENT_ID or os.environ.get("RB_FORCE_TENANT"))
GOLD_PATH = os.environ.get("RB_GOLD", "/tmp/rbench_gold.json")
ORACLE_PATH = os.environ.get("RB_ORACLE", "/tmp/longmemeval_oracle.json")
# Where scored runs are persisted. Set RB_RESULTS_DIR="" to disable.
RESULTS_DIR = os.environ.get("RB_RESULTS_DIR", "results")


def deterministic_session_uuid(question_id: str, source_session_id: str) -> str:
    """Session UUID derived from (question, source session) — identical to rbench.py."""
    digest = hashlib.sha256(("longmemeval:%s:%s" % (question_id, source_session_id)).encode()).hexdigest()
    return "%s-%s-%s-%s-%s" % (digest[:8], digest[8:12], digest[12:16], digest[16:20], digest[20:32])


def pick_questions(oracle: List[Dict[str, Any]], per_type: int) -> List[Dict[str, Any]]:
    """First ``per_type`` questions of each type, types sorted (deterministic).

    Junior Tip [why no random seed]: the sample derives from corpus FILE ORDER,
    so pinning the oracle JSON by sha256 pins the exact question set. A seed
    would add a reproducibility dependency without adding coverage.
    """
    questions_by_type: Dict[str, List[Dict[str, Any]]] = {}
    for question in oracle:
        questions_by_type.setdefault(question["question_type"], []).append(question)
    chosen: List[Dict[str, Any]] = []
    for _, questions in sorted(questions_by_type.items()):
        chosen += questions[:per_type]
    return chosen


def turns_to_content(turns: Sequence[Dict[str, Any]]) -> str:
    """Role-tagged transcript for one episodic record."""
    return "\n\n".join(
        "[%s]: %s" % (turn.get("role", "user").upper(), turn.get("content", ""))
        for turn in turns
    )


def first_user_summary(turns: Sequence[Dict[str, Any]]) -> str:
    """Summary from the first user turn (feeds the lexical leg), as in rbench.py."""
    first_user_text = next((turn.get("content", "") for turn in turns if turn.get("role") == "user"), "")
    return first_user_text[:200] or "conversation"


def chunk_turns_for_chat(turns: Sequence[Dict[str, Any]], chunk_count: int) -> List[List[Dict[str, Any]]]:
    """Split turns into contiguous groups (quick-chat shape), as in rbench.py."""
    if chunk_count < 2 or len(turns) < chunk_count:
        return [list(turns)]
    chunk_size = max(1, (len(turns) + chunk_count - 1) // chunk_count)
    chunks: List[List[Dict[str, Any]]] = []
    for start in range(0, len(turns), chunk_size):
        piece = list(turns[start : start + chunk_size])
        if piece:
            chunks.append(piece)
        if len(chunks) == chunk_count:
            leftover = list(turns[start + chunk_size :])
            if leftover:
                chunks[-1] = chunks[-1] + leftover
            break
    return chunks


def is_chatty_session(session_uuid: str, turns: Sequence[Dict[str, Any]]) -> bool:
    """Hash-stable choice of which sessions get multiple episodics (rbench parity)."""
    chatty_fraction = float(os.environ.get("RB_CHATTY_FRAC", "0.20") or "0.20")
    if chatty_fraction <= 0 or len(turns) < 3:
        return False
    digest = hashlib.sha256(("chatty:" + session_uuid).encode()).hexdigest()
    bucket = int(digest[:8], 16) / 0xFFFFFFFF
    return bucket < chatty_fraction


def build_create_request(session_uuid: str, turns: Sequence[Dict[str, Any]]) -> CreateRequest:
    """Build the episodic write for one (session, turn-group) — PARITY-CRITICAL.

    Junior Tip [why weight and score are pinned by hand]: the frozen REST
    harness posts a hand-built body carrying only weight=1.0 and no score, so
    the server stores score=0 — it does not default that field. ``CreateRequest``
    defaults are weight=0.5 and score=5, and accepting them would ingest a
    materially different corpus than the published run: score feeds the salience
    term V=(score-1)/9 of the cognitive weight, so score=5 yields V=0.44 where
    the paper's corpus had V=0.0, shifting rerank order and therefore recall.

    This lives in its own function so ``test_parity.py`` can assert on the exact
    object the harness builds, rather than on a copy that could drift from it.
    Do not inline it back, and do not "clean up" the explicit defaults.
    """
    return CreateRequest(
        session_id=session_uuid,
        type="episodic",
        summary=first_user_summary(turns),
        content=turns_to_content(turns),
        weight=1.0,
        score=0,
    )


def oracle_sha256() -> str:
    """Checksum of the corpus actually used, so a run is traceable to its input."""
    if not os.path.exists(ORACLE_PATH):
        return ""
    digest = hashlib.sha256()
    with open(ORACLE_PATH, "rb") as corpus_file:
        for block in iter(lambda: corpus_file.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def recorded_tenant() -> str:
    """The tenant to write into an artifact — what was SENT, not what was guessed.

    Junior Tip [do not record a name the client never used]: TENANT_ID is only
    put on the wire for master keys; a single-tenant key carries its tenant
    cryptographically and the harness sends no tenant header at all. Recording
    the unused default ("lme-pool") made every single-tenant run claim a tenant
    that does not exist on the server, which is worse than recording nothing —
    a reader cannot tell the run apart from one that really targeted that name.
    """
    if SEND_TENANT:
        return TENANT_ID
    return "(encoded in the API key; no tenant sent by the client)"


def save_results(result: Dict[str, Any], per_question: List[Dict[str, Any]]) -> str:
    """Persist one scored run as JSON, with the context needed to interpret it.

    Junior Tip [why a harness must write files]: printing to stdout makes every
    comparison a manual transcription, and a number nobody can trace back to a
    corpus, a configuration and a date is not evidence. Neither harness did this
    originally, and the raw output of the published run was lost to /tmp — only
    the hand-copied summary table survived. Results are written next to the
    scripts so they can be committed alongside the claim they support.

    The API key is never recorded. The endpoint is, because a result is
    uninterpretable without knowing what it ran against.
    """
    if not RESULTS_DIR:
        return ""
    os.makedirs(RESULTS_DIR, exist_ok=True)
    payload = {
        "harness": "rbench_sdk",
        "tag": result["tag"],
        "mode": result["mode"],
        "finished_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "endpoint": BASE_URL,
        "tenant": recorded_tenant(),
        "corpus": {
            "oracle_file": os.path.basename(ORACLE_PATH),
            "oracle_sha256": oracle_sha256(),
            "n_per_type": int(os.environ.get("RB_NPER", "5")),
        },
        "config": {
            "chatty_fraction": os.environ.get("RB_CHATTY_FRAC", "0.20"),
            "chatty_chunks": os.environ.get("RB_CHATTY_CHUNKS", "3"),
            "search_retries": os.environ.get("RB_SEARCH_RETRIES", "3"),
            "measure_sleep_ms": os.environ.get("RB_MEASURE_SLEEP_MS", "0"),
        },
        "results": result,
        "per_question": per_question,
    }
    output_path = os.path.join(RESULTS_DIR, "%s-%s.json" % (result["tag"], result["mode"]))
    with open(output_path, "w") as results_file:
        json.dump(payload, results_file, indent=2, sort_keys=True)
        results_file.write("\n")
    return output_path


def save_ingest_manifest(ingest_summary: Dict[str, Any]) -> str:
    """Persist how the corpus was BUILT, next to the runs that measure it.

    Junior Tip [why the ingest needs its own artifact]: a scored run records the
    corpus checksum, which pins the INPUT — not the state the server ended up
    in. Two ingests of the same oracle at different write rates hand the
    maintenance pipeline different amounts of head start, and the paper's own
    threat-to-validity section says record counts being equal does not make two
    corpora equal. Writing the rate, the duration and the error count makes the
    build reproducible instead of remembered, and it is the file to check first
    when a recall number moves for no visible reason.
    """
    if not RESULTS_DIR:
        return ""
    os.makedirs(RESULTS_DIR, exist_ok=True)
    payload = {
        "harness": "rbench_sdk",
        "phase": "ingest",
        "tag": os.environ.get("RB_INGEST_TAG", "ingest"),
        "finished_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "endpoint": BASE_URL,
        "tenant": recorded_tenant(),
        "corpus": {
            "oracle_file": os.path.basename(ORACLE_PATH),
            "oracle_sha256": oracle_sha256(),
            "n_per_type": int(os.environ.get("RB_NPER", "5")),
        },
        "ingest": ingest_summary,
    }
    output_path = os.path.join(RESULTS_DIR, "%s-ingest.json" % payload["tag"])
    with open(output_path, "w") as manifest_file:
        json.dump(payload, manifest_file, indent=2, sort_keys=True)
        manifest_file.write("\n")
    print("  ingest manifest -> %s" % output_path)
    return output_path


def open_memory() -> Memory:
    """One SDK client for the whole run (one HTTP pool, like a real agent).

    Junior Tip [must be used as ``async with``]: the client does NOT connect on
    construction — every call raises "Connection not established" until
    ``connect()`` runs. Because that error carries no HTTP status, an earlier
    version of this harness mistook it for a transient fault and retried it 18
    times per session, reporting "0 records ok" instead of failing. Always
    enter the context manager; never call methods on a bare constructor result.
    """
    return Memory(api_key=API_KEY, url=BASE_URL, tenant_id=TENANT_ID if SEND_TENANT else "")


def fail_loud(context: str, error: BaseException) -> NoReturn:
    """Abort the run on an error that retrying cannot fix.

    Junior Tip [why abort instead of counting a miss]: this harness measures
    recall, so ANY unattributed failure silently becomes "the engine did not
    find it". An earlier run of the REST harness lost a whole evaluation this
    way — a contract error on every query produced a flawless-looking 0.0%
    across all categories, with normal latency and no error surfaced. A bad key
    or a rejected request must stop the run, never quietly deflate the score.
    """
    status = getattr(error, "status_code", None)
    # The SDK already prints the status inside its message for HTTP errors;
    # only append it when the text does not carry it, to avoid "(HTTP 401) (HTTP 401)".
    message = str(error)
    suffix = "" if status is None or ("HTTP %s" % status) in message else " (HTTP %s)" % status
    raise SystemExit(
        "FATAL during %s: %s%s\n"
        "Refusing to continue — a silent failure here would be reported as low recall."
        % (context, message, suffix)
    )


async def ensure_session(memory: Memory, session_uuid: str, attempts: int = 5) -> bool:
    """Register a session id, tolerating already-registered (idempotent re-ingest).

    Junior Tip [session-first race]: kept from rbench.py — consensus can lag a
    beat right after registration, so brief backoff beats a retry storm.
    """
    for attempt_index in range(attempts):
        try:
            await memory.create_session(session_id=session_uuid)
            return True
        except AnhurAuthError as auth_error:
            fail_loud("create_session (check ANHUR_API_KEY / tenant)", auth_error)
        except AnhurError as session_error:
            # 409 = already registered: the expected path on idempotent re-ingest.
            if session_error.status_code == 409:
                return True
            # Junior Tip [no status means CLIENT-side]: an AnhurError without an
            # HTTP status never reached the server — a misuse, a bad URL, a
            # closed connection. Retrying repeats it identically. Treating these
            # as transient is what once turned "you forgot to connect()" into 18
            # quiet per-session retries and a 0-record ingest.
            if session_error.status_code is None or session_error.status_code == 400:
                fail_loud("create_session for %s" % session_uuid[:13], session_error)
            await asyncio.sleep(0.05 * (attempt_index + 1))
        except Exception as unexpected_error:
            await asyncio.sleep(0.05 * (attempt_index + 1))
            if attempt_index + 1 >= attempts:
                fail_loud("create_session for %s" % session_uuid[:13], unexpected_error)
    print("  ERR session", session_uuid[:13])
    return False


async def ingest() -> None:
    """Ingest the pooled corpus flat (episodic records only) via the SDK.

    Junior Tip [NOT idempotent at the record level]: session ids are derived
    from the corpus, so re-running re-uses the same sessions — but the episodic
    records are written again. Measured on a clean instance, two runs of 24
    records left 48 rows stored. Always target an empty tenant; a second ingest
    silently doubles the corpus and invalidates every number that follows.
    """
    oracle = json.load(open(ORACLE_PATH))
    per_type = int(os.environ.get("RB_NPER", "5"))
    questions = pick_questions(oracle, per_type)
    chatty_chunk_count = int(os.environ.get("RB_CHATTY_CHUNKS", "3") or "3")
    gold: Dict[str, Any] = {}
    records_ok = records_err = chatty_sessions = 0
    # Junior Tip [why the corpus is written at a paced, client-like rate]: every
    # create is one real HTTP round trip, but writing them back-to-back is still
    # a burst — the API acknowledges a record once consensus commits it, while
    # embedding, enrichment and the columnar copy are produced ASYNCHRONOUSLY by
    # workers behind a message bus. An unpaced ingest therefore hands the
    # pipeline a queue thousands deep in a few minutes, which is where the
    # regulation loop starts cutting embedding slots and where the measured
    # corpus stops being the corpus the protocol defines. Pacing the writer to
    # roughly the rate the slowest downstream consumer sustains keeps the ingest
    # a steady state instead of a spike. Set RB_INGEST_SLEEP_MS=0 to reproduce
    # the old unpaced behaviour; the value used is recorded with the run.
    ingest_sleep_s = float(os.environ.get("RB_INGEST_SLEEP_MS", "400") or "0") / 1000.0
    progress_every = int(os.environ.get("RB_INGEST_PROGRESS_EVERY", "50") or "0")
    ingest_started_at = time.time()
    memory = open_memory()
    await memory.connect()
    for question in questions:
        question_id = question["question_id"]
        answer_ids = set(question["answer_session_ids"])
        gold[question_id] = {
            "question": question["question"],
            "type": question["question_type"],
            "relevant_uuids": [deterministic_session_uuid(question_id, source_id) for source_id in answer_ids],
        }
        for source_id, turns in zip(question["haystack_session_ids"], question["haystack_sessions"]):
            session_uuid = deterministic_session_uuid(question_id, source_id)
            if not await ensure_session(memory, session_uuid):
                records_err += 1
                continue
            if is_chatty_session(session_uuid, turns):
                pieces = chunk_turns_for_chat(turns, chatty_chunk_count)
                chatty_sessions += 1
            else:
                pieces = [list(turns)]
            for piece in pieces:
                create_request = build_create_request(session_uuid, piece)
                try:
                    await memory.create(create_request)
                    records_ok += 1
                except Exception as create_error:
                    records_err += 1
                    if records_err <= 5:
                        print("  ERR", str(create_error)[:100])
                attempted = records_ok + records_err
                if progress_every > 0 and attempted % progress_every == 0:
                    elapsed_s = time.time() - ingest_started_at
                    print(
                        "  ... %d records (%d ok, %d err) in %.0fs — %.2f rec/s"
                        % (attempted, records_ok, records_err, elapsed_s, attempted / max(elapsed_s, 1e-9)),
                        flush=True,
                    )
                if ingest_sleep_s > 0:
                    await asyncio.sleep(ingest_sleep_s)
    await memory.close()
    json.dump(gold, open(GOLD_PATH, "w"))
    ingest_elapsed_s = time.time() - ingest_started_at
    print(
        "INGEST(sdk): %d questions, %d records ok, %d err, %d chatty_sessions (x%d) "
        "in %.0fs (%.2f rec/s, pacing %dms) | base=%s tenant=%s"
        % (
            len(questions),
            records_ok,
            records_err,
            chatty_sessions,
            chatty_chunk_count,
            ingest_elapsed_s,
            (records_ok + records_err) / max(ingest_elapsed_s, 1e-9),
            round(ingest_sleep_s * 1000),
            BASE_URL,
            TENANT_ID,
        )
    )
    save_ingest_manifest(
        {
            "questions": len(questions),
            "records_ok": records_ok,
            "records_err": records_err,
            "chatty_sessions": chatty_sessions,
            "chatty_chunks": chatty_chunk_count,
            "elapsed_s": round(ingest_elapsed_s, 1),
            "records_per_s": round((records_ok + records_err) / max(ingest_elapsed_s, 1e-9), 3),
            "ingest_sleep_ms": round(ingest_sleep_s * 1000),
        }
    )


async def wait_until_searchable(gold: Dict[str, Any], probe_count: int = 3) -> None:
    """Block until the ingested corpus is actually retrievable, or abort.

    Junior Tip [a guard, not a diagnosis]: fresh records ARE retrievable
    immediately — a single record inserted and queried in the same breath comes
    back at t+0s on both the lexical and hybrid paths, with no embedding
    present. This gate is not compensating for a write-path indexing lag.

    It exists because a scoring run was once observed returning zero results
    for every question right after a bulk ingest, on a corpus that answered
    later. The cause was never isolated, so this is a defensive guard, not a
    finding: it costs a few seconds and prevents a run that is measuring
    nothing from being written out as a result. Do not cite it as evidence
    about the engine.
    """
    timeout_s = float(os.environ.get("RB_READY_TIMEOUT_S", "300") or "300")
    poll_interval_s = float(os.environ.get("RB_READY_POLL_S", "5") or "5")
    probes = list(gold.items())[:probe_count]
    if not probes:
        return

    async def probe_total(memory: Memory) -> int:
        """Total hits across the probe questions — the readiness signal."""
        running_total = 0
        for _, gold_entry in probes:
            try:
                hits = await memory.search(gold_entry["question"], ["*"], limit=10)
            except AnhurAuthError as auth_error:
                fail_loud("readiness probe (check ANHUR_API_KEY / tenant)", auth_error)
            except AnhurError as probe_error:
                if probe_error.status_code is None:
                    fail_loud("readiness probe", probe_error)
                hits = []
            running_total += len(hits)
        return running_total

    # Junior Tip [why sustained stability, and why it is still a heuristic]:
    # indexing advances in steps, so the probe count plateaus transiently. A
    # gate requiring only TWO equal polls released on such a plateau and scored
    # 16.7% on a corpus that reaches 100% once settled — a partial index
    # deflates the number just as dishonestly as an empty one, only less
    # visibly. Requiring several consecutive equal polls survives the plateaus.
    # It remains a heuristic: the server exposes no "indexing complete" signal,
    # and hit-count probing cannot distinguish "still indexing" from "genuinely
    # poor retrieval". Raise RB_READY_STABLE_POLLS on slower deployments.
    required_stable_polls = int(os.environ.get("RB_READY_STABLE_POLLS", "3") or "3")
    memory = open_memory()
    await memory.connect()
    started_at = time.time()
    previous_total = -1
    stable_polls = 0
    announced = False
    try:
        while time.time() - started_at < timeout_s:
            current_total = await probe_total(memory)
            if current_total > 0 and current_total == previous_total:
                stable_polls += 1
                if stable_polls >= required_stable_polls:
                    if announced:
                        print("  corpus settled after ~%ds (%d hits stable over %d polls)"
                              % (int(time.time() - started_at), current_total, stable_polls))
                    return
            else:
                stable_polls = 0
            if not announced:
                print("  waiting for the corpus to settle "
                      "(ingest returns before indexing completes)...", flush=True)
                announced = True
            previous_total = current_total
            await asyncio.sleep(poll_interval_s)
    finally:
        await memory.close()
    fail_loud(
        "readiness wait",
        RuntimeError(
            "the corpus did not settle within %ds (last probe total: %d hits). Either "
            "ingest did not land in this tenant, or indexing is stuck. Scoring now would "
            "report a deflated recall that says nothing about retrieval quality."
            % (int(timeout_s), max(previous_total, 0))
        ),
    )


async def score_run(tag: str, lexical_only: bool, skip_cognitive: bool = False) -> Dict[str, Any]:
    """recall@k by session UUID over the pooled corpus, through the SDK.

    Junior Tip [what latency means here]: timings include the SDK itself
    (parsing, retries, pooling) — the latency a customer actually experiences.
    rbench.py timings are raw REST; report the two separately, never merged.
    """
    gold: Dict[str, Any] = json.load(open(GOLD_PATH))
    await wait_until_searchable(gold)
    sleep_ms = int(os.environ.get("RB_MEASURE_SLEEP_MS", "0") or "0")
    search_attempts = int(os.environ.get("RB_SEARCH_RETRIES", "3") or "3")
    hits_at_1 = hits_at_5 = hits_at_10 = questions_done = transient_failures = 0
    degraded_queries = 0
    hits_by_type: Dict[str, List[int]] = {}
    latencies: List[float] = []
    per_question: List[Dict[str, Any]] = []
    memory = open_memory()
    await memory.connect()
    for question_id, gold_entry in gold.items():
        if sleep_ms > 0 and questions_done > 0:
            await asyncio.sleep(sleep_ms / 1000.0)
        relevant_uuids = set(gold_entry["relevant_uuids"])
        started_at = time.time()
        result_uuids: List[str] = []
        search_succeeded = False
        for attempt_index in range(max(1, search_attempts)):
            try:
                # ["*"] = every session in scope — the pooled protocol
                # (each query searches the whole corpus, with interference).
                # Junior Tip [medir sem olhar a meta e medir outra coisa,
                # 2026-08-13]: o servidor informa se a perna densa caiu
                # (retrieval.degraded / semantic_used). Ignorar isso mistura
                # consultas hibridas com consultas que viraram lexico puro na
                # MESMA execucao — foi assim que duas rodadas identicas deram
                # 81.0% e 77.0% de recall@5 sem explicacao aparente.
                response = await memory.search_with_retrieval(
                    gold_entry["question"], ["*"], limit=10,
                    skip_query_embed=lexical_only,
                    skip_cognitive_rerank=skip_cognitive,
                )
                search_hits = response.results
                retrieval = getattr(response, "retrieval", None)
                if retrieval is not None and getattr(retrieval, "degraded", False):
                    degraded_queries += 1
                result_uuids = [hit.record.uuid for hit in search_hits]
                search_succeeded = True
                break
            except AnhurAuthError as auth_error:
                fail_loud("search (check ANHUR_API_KEY / tenant)", auth_error)
            except AnhurError as search_error:
                # A 4xx other than rate-limit is a contract error, and a missing
                # status means the request never left the client. Both fail
                # identically on every retry, so stop the run instead of letting
                # the question be scored as a miss.
                status = search_error.status_code
                if status is None or (400 <= status < 500 and status != 429):
                    fail_loud("search for question %s" % question_id, search_error)
                if attempt_index + 1 >= search_attempts:
                    break
                await asyncio.sleep(1.5 * (attempt_index + 1))
            except Exception as unexpected_error:
                if attempt_index + 1 >= search_attempts:
                    fail_loud("search for question %s" % question_id, unexpected_error)
                await asyncio.sleep(1.5 * (attempt_index + 1))
        if not search_succeeded:
            # Exhausted retries on a transient (5xx/429) error: count it, and
            # report it at the end so a degraded run is never mistaken for a
            # low-recall engine.
            transient_failures += 1
        latencies.append(time.time() - started_at)
        questions_done += 1
        hits_at_1 += bool(result_uuids[:1] and result_uuids[0] in relevant_uuids)
        hits_at_5 += bool(any(u in relevant_uuids for u in result_uuids[:5]))
        hits_at_10 += bool(any(u in relevant_uuids for u in result_uuids[:10]))
        type_counter = hits_by_type.setdefault(gold_entry["type"], [0, 0])
        type_counter[0] += 1
        type_counter[1] += bool(any(u in relevant_uuids for u in result_uuids[:5]))
        # Per-question detail makes two runs diffable: an aggregate that moved
        # is a question that moved, and without this you cannot tell which.
        per_question.append({
            "question_id": question_id,
            "type": gold_entry["type"],
            "hit_at_1": bool(result_uuids[:1] and result_uuids[0] in relevant_uuids),
            "hit_at_5": bool(any(u in relevant_uuids for u in result_uuids[:5])),
            "hit_at_10": bool(any(u in relevant_uuids for u in result_uuids[:10])),
            "rank_of_first_hit": next(
                (position + 1 for position, uuid in enumerate(result_uuids) if uuid in relevant_uuids),
                None,
            ),
            "returned": len(result_uuids),
            "latency_s": round(latencies[-1], 4),
            "search_failed": not search_succeeded,
        })
        if questions_done == 1 or questions_done % 10 == 0 or questions_done == len(gold):
            print("  progress %d/%d recall@5_so_far=%.1f%% last_lat=%.2fs" % (
                questions_done, len(gold), 100 * hits_at_5 / questions_done, latencies[-1]), flush=True)
    await memory.close()
    latencies.sort()
    p50 = latencies[len(latencies) // 2] if latencies else 0.0
    p95 = latencies[int(len(latencies) * 0.95)] if latencies else 0.0
    if lexical_only:
        mode = "lexical-pure" if skip_cognitive else "lexical-only"
    else:
        mode = "hybrid-pure" if skip_cognitive else "hybrid-server-embed"
    print("\n=== RECALL(sdk) [%s] n=%d mode=%s ===" % (tag, questions_done, mode))
    print("  recall@1=%.1f%%  recall@5=%.1f%%  recall@10=%.1f%%  | lat p50=%.2fs p95=%.2fs" % (
        100 * hits_at_1 / questions_done, 100 * hits_at_5 / questions_done,
        100 * hits_at_10 / questions_done, p50, p95))
    for type_name, (asked, hit) in sorted(hits_by_type.items()):
        print("    %-28s @5=%.0f%% (%d/%d)" % (type_name, 100 * hit / asked, hit, asked))
    if degraded_queries and not lexical_only:
        # Nao e aviso cosmetico: cada consulta degradada foi respondida SEM o
        # vetor, entao a run mede uma mistura de dois sistemas diferentes.
        print("\n  WARNING: %d/%d queries ran DEGRADED (dense leg dropped)."
              % (degraded_queries, questions_done))
        print("  This run mixes hybrid and lexical-only answers — not a clean measurement.")
    if transient_failures:
        # Junior Tip [never publish a degraded run as a result]: these questions
        # scored zero because the request never completed, not because retrieval
        # failed. A run with any transient failure is not comparable.
        print("\n  WARNING: %d/%d questions failed after retries (transient errors)."
              % (transient_failures, questions_done))
        print("  These count as misses and DEFLATE recall — do not report this run.")
    result: Dict[str, Any] = {
        "tag": tag,
        "n": questions_done,
        "mode": mode,
        "recall_at_1": round(100 * hits_at_1 / questions_done, 2),
        "recall_at_5": round(100 * hits_at_5 / questions_done, 2),
        "recall_at_10": round(100 * hits_at_10 / questions_done, 2),
        "latency_p50_s": round(p50, 4),
        "latency_p95_s": round(p95, 4),
        "by_type": {
            type_name: {"asked": asked, "hit_at_5": hit, "recall_at_5": round(100 * hit / asked, 2)}
            for type_name, (asked, hit) in sorted(hits_by_type.items())
        },
        "transient_failures": transient_failures,
        "degraded_queries": degraded_queries,
        "usable": transient_failures == 0 and (lexical_only or degraded_queries == 0),
    }
    saved_to = save_results(result, per_question)
    if saved_to:
        print("\n  results written to %s" % saved_to)
    return result


if __name__ == "__main__":
    cli_mode = sys.argv[1] if len(sys.argv) > 1 else "recall"
    run_tag = sys.argv[2] if len(sys.argv) > 2 else "run"
    if cli_mode == "ingest":
        asyncio.run(ingest())
    elif cli_mode == "lexical":
        asyncio.run(score_run(run_tag, lexical_only=True))
    elif cli_mode == "lexical-pure":
        # Junior Tip [what "lexical" actually measured — 2026-08-13]: the
        # `lexical` mode above only skips the query embedding, so the 4-signal
        # cognitive rerank still reorders the result. It was therefore reporting
        # lexical PLUS cognitive under the name "lexical". Measured on the pinned
        # corpus with query "March 15th" — a term present only in content — the
        # DuckDB leg ranks the right record FIRST and the cognitive rerank pushes
        # it out of the top five entirely (570,590,584,520,591 with the rerank,
        # 397,241,178,273,419 without).
        #
        # This mode isolates the lexical arm. It is deliberately a NEW mode
        # rather than a change to `lexical`, because rbench.py is frozen and does
        # not skip the rerank: changing `lexical` here would silently break the
        # parity contract between the two harnesses. Compare `lexical` against
        # rbench.py; use `lexical-pure` to reason about the arm itself.
        asyncio.run(score_run(run_tag, lexical_only=True, skip_cognitive=True))
    elif cli_mode == "hybrid-pure":
        # Junior Tip [the cell that was missing from the 2x2 — 2026-08-29]: the
        # three modes above vary TWO things at once between the fused arm and the
        # isolated one — whether a query vector is sent, and whether the cognitive
        # rerank runs. `recall` is vector+rank, `lexical` is no-vector+rank,
        # `lexical-pure` is no-vector+no-rank. The fourth cell, vector+no-rank,
        # did not exist, so a fused run could only ever be compared against an
        # arm that also differed in ranking.
        #
        # That gap is not academic: the published series neutralised the rank
        # through TENANT CONFIGURATION, which needs a master key. On a tenant
        # where the rank runs at its defaults, there was no way to reproduce the
        # published configuration from the client side at all, and no way to tell
        # a ranking difference from an engine difference. This mode closes it
        # with the flag the SDK already exposes.
        asyncio.run(score_run(run_tag, lexical_only=False, skip_cognitive=True))
    else:
        asyncio.run(score_run(run_tag, lexical_only=False))
