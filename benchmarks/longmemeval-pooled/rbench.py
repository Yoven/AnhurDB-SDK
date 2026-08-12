#!/usr/bin/env python3
# Self-contained LongMemEval recall@k harness.
# Recall by UUID (session id), which survives the enrichment pipeline (metadata does NOT).
#
# Junior Tip [HEL1 BGE-M3, 2026-07-19]: public search auto-embeds query text server-side
# (same TEI model as enrichment). Prefer that product path over client-side Ollama embeds —
# client qwen3 vectors against BGE-M3 indexes would silently corrupt Stage-1.
#
# Junior Tip [chatty sessions, 2026-07-20]: most haystack sessions stay 1 episodic /
# session_id (flat LME). A hash-stable fraction (RB_CHATTY_FRAC, default 0.20) with
# ≥3 turns is split into RB_CHATTY_CHUNKS (default 3) episodics under the SAME
# session_id — quick-chat shape without changing gold (hit@k is still by session UUID).
import os, sys, json, hashlib, time, requests

BASE = os.environ.get("ANHUR_BASE") or os.environ.get("ANHUR_URL") or "http://localhost:8000"
BASE = BASE.rstrip("/")
MK = os.environ.get("ANHUR_MK") or os.environ.get("ANHUR_API_KEY")
if not MK:
    sys.exit("set ANHUR_MK or ANHUR_API_KEY")
CID = os.environ.get("RB_CLIENT_ID", "")  # master-key path: full X-Tenant-ID needs client_id_
TENANT = (CID + "_" if CID else "") + os.environ.get("RB_TENANT", "lme-pool")
H = {
    "X-API-Key": MK,
    "Content-Type": "application/json",
    # Junior Tip: Cloudflare blocks bare python-requests UA with 1010.
    "User-Agent": os.environ.get("RB_USER_AGENT", "AnhurLongMemEval/1.0"),
}
# Single-tenant API keys already encode tenant — only master keys need X-Tenant-ID.
if CID or os.environ.get("RB_FORCE_TENANT"):
    H["X-Tenant-ID"] = TENANT
GOLD = os.environ.get("RB_GOLD", "/tmp/rbench_gold.json")
ORACLE = os.environ.get("RB_ORACLE", "/tmp/longmemeval_oracle.json")
# Optional: client-side embed URL (lab only). Empty = server auto-embed (HEL1 product path).
EMBED_URL = os.environ.get("RB_EMBED_URL", "").rstrip("/")
EMBED_MODEL = os.environ.get("RB_EMBED_MODEL", "BAAI/bge-m3")


def det_uuid(qid, sid):
    h = hashlib.sha256(("longmemeval:%s:%s" % (qid, sid)).encode()).hexdigest()
    return "%s-%s-%s-%s-%s" % (h[:8], h[8:12], h[12:16], h[16:20], h[20:32])


def pick(oracle, n_per_type=int(os.environ.get("RB_NPER", "5"))):
    by = {}
    for q in oracle:
        by.setdefault(q["question_type"], []).append(q)
    chosen = []
    for t, qs in sorted(by.items()):
        chosen += qs[:n_per_type]
    return chosen


def ensure_session(session_uuid, attempts=5):
    """Register session_id before Create (session-first gate, 2026-07-18). Idempotent on conflict.

    Junior Tip [session-first race]: HEL1 Raft can lag a beat after POST /sessions —
    a single retry storm of 94 errors poisoned an earlier LME run. Brief backoff
    before giving up keeps flat re-ingest deterministic without a sleep tax on
    every session.
    """
    last_status = None
    for attempt in range(attempts):
        try:
            response = requests.post(
                BASE + "/api/v1/sessions",
                headers=H,
                json={"session_id": session_uuid},
                timeout=30,
            )
            last_status = response.status_code
            # 201 created, 200/409 already registered — all fine for flat re-ingest
            if response.status_code < 300 or response.status_code in (409,):
                return True
        except Exception:
            last_status = None
        time.sleep(0.05 * (attempt + 1))
    if last_status is not None:
        print("  ERR session", session_uuid[:13], "status", last_status)
    return False


def turns_to_content(turns):
    """Format chat turns as role-tagged content for one episodic."""
    return "\n\n".join(
        "[%s]: %s" % (turn.get("role", "user").upper(), turn.get("content", ""))
        for turn in turns
    )


def first_user_summary(turns):
    """Short summary from the first user turn (ILIKE/BM25 summary field)."""
    first_user = next((turn.get("content", "") for turn in turns if turn.get("role") == "user"), "")
    return (first_user[:200] or "conversation")


def chunk_turns_for_chat(turns, chunk_count=3):
    """Split a session's turns into chunk_count contiguous groups (quick-chat shape).

    Junior Tip: Prefer contiguous slices over round-robin so each episodic reads
    like a short burst of dialogue, not interleaved fragments.
    """
    if chunk_count < 2 or len(turns) < chunk_count:
        return [turns]
    chunk_size = max(1, (len(turns) + chunk_count - 1) // chunk_count)
    chunks = []
    for start in range(0, len(turns), chunk_size):
        piece = turns[start : start + chunk_size]
        if piece:
            chunks.append(piece)
        if len(chunks) == chunk_count:
            leftover = turns[start + chunk_size :]
            if leftover:
                chunks[-1] = chunks[-1] + leftover
            break
    return chunks


def is_chatty_session(session_uuid, turns):
    """Decide whether this session gets multiple episodics (deterministic).

    RB_CHATTY_FRAC (default 0.20) — fraction of eligible sessions (≥3 turns).
    Selection is hash-stable so re-ingest keeps the same mix.
    """
    frac = float(os.environ.get("RB_CHATTY_FRAC", "0.20") or "0.20")
    if frac <= 0 or len(turns) < 3:
        return False
    digest = hashlib.sha256(("chatty:" + session_uuid).encode()).hexdigest()
    bucket = int(digest[:8], 16) / 0xFFFFFFFF
    return bucket < frac


def post_episodic(session_uuid, summary, content):
    """POST one episodic under an already-registered session_id."""
    payload = {
        "session_id": session_uuid,
        "type": "episodic",
        "summary": summary,
        "content": content,
        "weight": 1.0,
    }
    return requests.post(BASE + "/api/v1/records", headers=H, json=payload, timeout=30)


def ingest():
    oracle = json.load(open(ORACLE))
    qs = pick(oracle)
    gold = {}
    rec, err = 0, 0
    chatty_sessions = 0
    chatty_chunks = int(os.environ.get("RB_CHATTY_CHUNKS", "3") or "3")
    for q in qs:
        qid = q["question_id"]
        sess_ids = q["haystack_session_ids"]
        sessions = q["haystack_sessions"]
        ans = set(q["answer_session_ids"])
        gold[qid] = {"question": q["question"], "type": q["question_type"],
                     "relevant_uuids": [det_uuid(qid, s) for s in ans]}
        for sid, turns in zip(sess_ids, sessions):
            session_uuid = det_uuid(qid, sid)
            if not ensure_session(session_uuid):
                err += 1
                continue
            if is_chatty_session(session_uuid, turns):
                pieces = chunk_turns_for_chat(turns, chatty_chunks)
                chatty_sessions += 1
            else:
                pieces = [turns]
            for piece in pieces:
                summary = first_user_summary(piece)
                content = turns_to_content(piece)
                try:
                    response = post_episodic(session_uuid, summary, content)
                    if response.status_code < 300:
                        rec += 1
                    else:
                        err += 1
                        if err <= 5:
                            print("  ERR", response.status_code, response.text[:80])
                except Exception as exc:
                    err += 1
                    if err <= 5:
                        print("  EXC", str(exc)[:80])
    json.dump(gold, open(GOLD, "w"))
    print(
        "INGEST: %d questions, %d records ok, %d err, %d chatty_sessions (×%d) | base=%s tenant=%s"
        % (len(qs), rec, err, chatty_sessions, chatty_chunks, BASE, TENANT)
    )


def _client_query_vector_b64(question):
    """Lab-only: embed on RB_EMBED_URL (OpenAI-compat or Ollama). Empty string on skip/fail."""
    if not EMBED_URL:
        return ""
    import base64, struct
    try:
        if EMBED_URL.endswith("/api/embeddings") or "11434" in EMBED_URL:
            emb = requests.post(
                EMBED_URL if EMBED_URL.endswith("/embeddings") else EMBED_URL + "/api/embeddings",
                json={"model": EMBED_MODEL, "prompt": question}, timeout=60,
            ).json().get("embedding", [])
        else:
            # OpenAI-compat: POST {base}/embeddings
            url = EMBED_URL if EMBED_URL.endswith("/embeddings") else EMBED_URL + "/embeddings"
            emb = requests.post(
                url,
                json={"model": EMBED_MODEL, "input": question}, timeout=60,
                headers={"Authorization": "Bearer " + os.environ.get("RB_EMBED_KEY", "unused")},
            ).json().get("data", [{}])[0].get("embedding", [])
        if emb:
            return base64.b64encode(struct.pack("<%df" % len(emb), *emb)).decode()
    except Exception:
        return ""
    return ""


def _score_run(tag, use_client_vector):
    gold = json.load(open(GOLD))
    at1 = at5 = at10 = tot = 0
    bytype = {}
    lat = []
    embed_fail = 0
    # Junior Tip [gentle LME]: TEI Candle fp16 wedges under burst search on CX33.
    # Pace queries (default 0); hel1-lme-isolated-bench sets ~800ms between searches.
    sleep_ms = int(os.environ.get("RB_MEASURE_SLEEP_MS", "0") or "0")
    # Junior Tip [HEL1]: CF + TEI warmup can exceed 45s; keep configurable for gentle LME.
    search_timeout = float(os.environ.get("RB_SEARCH_TIMEOUT", "90") or "90")
    gold_n = len(gold)
    for qid, g in gold.items():
        if sleep_ms > 0 and tot > 0:
            time.sleep(sleep_ms / 1000.0)
        rel = set(g["relevant_uuids"])
        # Junior Tip [ADR-0014: sessions é OBRIGATÓRIO — 2026-07-30]: sem este
        # campo o servidor responde 400 INVALID_PARAM e o harness contava a
        # pergunta como MISS. O efeito era devastador e silencioso: recall 0,0%
        # em TODAS as categorias, nos dois modos, com latência normal e
        # embed_fail=0 — parecia que a busca havia parado de funcionar quando o
        # que quebrou foi o contrato do harness. ["*"] = todas as sessões do
        # escopo, que é exatamente a semântica pooled deste benchmark (cada
        # pergunta busca no corpus inteiro, com interferência entre perguntas).
        payload = {"text": g["question"], "limit": 10, "sessions": ["*"]}
        if use_client_vector:
            qvec_b64 = _client_query_vector_b64(g["question"])
            if qvec_b64:
                # Junior Tip [BUG-13 / BR.7, 2026-07-21]: POST /api/v1/search reads
                # `vector` (SDK search() already does). `query_vector` was only
                # promoted on /search/global — sending it here silently skipped Stage 1.
                payload["vector"] = qvec_b64
            else:
                embed_fail += 1
        t0 = time.time()
        res = []
        # Junior Tip [HEL1 measure]: CF 504 + brief Raft elections show up as
        # 503/504. Retry a few times before counting a miss — ingest is already
        # paid for; we want ranking quality, not gateway flakiness.
        search_attempts = int(os.environ.get("RB_SEARCH_RETRIES", "3") or "3")
        last_err = ""
        for attempt in range(max(1, search_attempts)):
            try:
                r = requests.post(BASE + "/api/v1/search", headers=H, json=payload, timeout=search_timeout)
                if r.status_code < 300:
                    res = r.json().get("results", [])
                    last_err = ""
                    break
                last_err = "SEARCH ERR %s %s" % (r.status_code, r.text[:120])
                if r.status_code not in (503, 504, 502) or attempt + 1 >= search_attempts:
                    if tot < 3:
                        print("  " + last_err, flush=True)
                    break
                time.sleep(1.5 * (attempt + 1))
            except Exception as e:
                last_err = "SEARCH EXC " + str(e)[:120]
                if attempt + 1 >= search_attempts:
                    if tot < 3:
                        print("  " + last_err, flush=True)
                    break
                time.sleep(1.5 * (attempt + 1))
        lat.append(time.time() - t0)
        uuids = [(x.get("record") or {}).get("uuid") for x in res]
        tot += 1
        at1 += bool(uuids[:1] and uuids[0] in rel)
        at5 += bool(any(u in rel for u in uuids[:5]))
        at10 += bool(any(u in rel for u in uuids[:10]))
        bt = bytype.setdefault(g["type"], [0, 0]); bt[0] += 1; bt[1] += bool(any(u in rel for u in uuids[:5]))
        if tot == 1 or tot % 10 == 0 or tot == gold_n:
            print("  progress %d/%d recall@5_so_far=%.1f%% last_lat=%.2fs" % (
                tot, gold_n, (100 * at5 / tot), lat[-1] if lat else 0), flush=True)
    lat.sort()
    p50 = lat[len(lat)//2] if lat else 0
    p95 = lat[int(len(lat)*0.95)] if lat else 0
    mode = "client-vector" if use_client_vector else "server-autoembed"
    print("\n=== RECALL [%s] n=%d mode=%s embed_fail=%d ===" % (tag, tot, mode, embed_fail))
    print("  recall@1=%.1f%%  recall@5=%.1f%%  recall@10=%.1f%%  | search lat p50=%.2fs p95=%.2fs" %
          (100*at1/tot, 100*at5/tot, 100*at10/tot, p50, p95))
    for t, (n, h) in sorted(bytype.items()):
        print("    %-28s @5=%.0f%% (%d/%d)" % (t, 100*h/n, h, n))
    return {"tag": tag, "n": tot, "r1": 100*at1/tot, "r5": 100*at5/tot, "r10": 100*at10/tot,
            "p50": p50, "p95": p95, "mode": mode}


def recall(tag):
    # Product path on HEL1: text-only search → server auto-embed (BGE-M3) → hybrid.
    return _score_run(tag, use_client_vector=False)


def recall_hybrid(tag):
    # Same as product path unless RB_EMBED_URL is set (lab client-vector A/B).
    return _score_run(tag, use_client_vector=bool(EMBED_URL))


if __name__ == "__main__":
    mode = sys.argv[1] if len(sys.argv) > 1 else "recall"
    if mode == "ingest":
        ingest()
    elif mode == "hybrid":
        recall_hybrid(sys.argv[2] if len(sys.argv) > 2 else "run")
    else:
        recall(sys.argv[2] if len(sys.argv) > 2 else "run")
