# LongMemEval — pooled evidence-recall harness

Reproduction code for the evaluation section of *The Pyramid: A Hierarchical
Temporal Memory Architecture for Autonomous AI Agents*.

This directory contains everything needed to re-run the study **except** the
corpus, which belongs to the LongMemEval authors and is not redistributed here.

---

## What is measured, and what is not

We report **evidence recall@k by session identifier**: for each question, did
the retrieved top-k include the session that actually contains the evidence?

This is an *objective* metric — a set-membership check against ground truth —
deliberately chosen instead of LLM-judged answer accuracy. Judged accuracy
depends on the judge model, the prompt, and the answer-generation budget, none
of which are standardised across published numbers in this category. Two
consequences follow, and we state both plainly:

* These numbers are **not comparable** to vendor-reported "accuracy" figures,
  in either direction. Do not build a ranking table out of them.
* Recall measures the **retrieval substrate**, not end-to-end answer quality.
  A system could score well here and still answer badly.

## The pooled protocol (harder than the official one)

All evidence sessions are ingested **flat into a single tenant**, so every query
searches the entire corpus and competes with every other question's evidence.
The official LongMemEval protocol isolates each question's haystack. Pooling is
strictly harder and biases our numbers **downward**.

Ingestion is episodic-only: consolidation, supersession, and decay are **not**
exercised. The numbers are therefore a conservative floor for the full system,
not its ceiling.

---

## Two harnesses, two purposes

| File | Path | Use it when |
|---|---|---|
| `rbench.py` | raw REST | You want the **exact artifact** that produced the published table. **Frozen — do not edit.** |
| `rbench_sdk.py` | official Python SDK | You want the **recommended** path: same protocol, exercised through the client surface a customer actually uses. |

Both implement the same measurement contract — identical question sampling,
identical session UUIDs (`sha256("longmemeval:<qid>:<sid>")`), identical chatty
split, identical ingest payload, identical scoring. A divergence between them
is a finding, not noise.

That contract is **machine-checked**, offline, with no server or corpus:

```bash
python3 test_parity.py
```

It compares the two implementations across sampling, UUID derivation, turn
chunking, record body, and the exact wire payload the SDK harness builds. Run
it before trusting any SDK-produced number. The check was itself
mutation-tested: deliberately breaking the sample size, the UUID salt, the
summary cut-off, the turn separator, the type ordering, or the ingest weight
and score each makes it fail.

They differ in two deliberate ways. `rbench_sdk.py` always lets the **server**
embed the query (the product path) and expresses the lexical-only leg as
`skip_query_embed=True`; `rbench.py` additionally carries a lab-only
client-side embedding mode. And `rbench_sdk.py` **aborts on any non-retryable
error** rather than letting a failed request be scored as a miss — see below.

### Latency is not comparable between the two

`rbench.py` times raw HTTP. `rbench_sdk.py` times the SDK round trip, including
parsing, retries and connection pooling — the latency a client experiences.
Report them separately. Never average them.

---

## Setup

```bash
pip install -r requirements.txt        # Python >= 3.11
```

Obtain the LongMemEval **oracle** variant from the official release
(<https://github.com/xiaowu0162/LongMemEval>) and pin it:

```bash
export RB_ORACLE=/path/to/longmemeval_oracle.json
export RB_NPER=34                       # the published protocol → n = 200
python3 pin_corpus.py                   # writes corpus.sha256 + question_ids.json
```

**Check `corpus.sha256` against the committed value before comparing numbers.**
The sample is drawn by file order (first `RB_NPER` of each question type, types
sorted), so a reordered corpus release would silently change which 200
questions run. `question_ids.json` records the exact selection as a backstop.

You also need an AnhurDB instance to point at — self-hosted, or a hosted
instance if you have access.

```bash
export ANHUR_API_KEY=...                # never commit this
export ANHUR_BASE=https://your-instance
export RB_TENANT=lme-pool               # a dedicated, empty tenant
```

## Run

```bash
python3 rbench_sdk.py ingest            # ~356 sessions, idempotent (deterministic UUIDs)
python3 rbench_sdk.py recall  run1      # hybrid: dense + lexical, fused and reranked
python3 rbench_sdk.py lexical run1      # lexical leg alone, for the ablation
```

### Always ingest into an empty tenant

**`ingest` is not idempotent at the record level.** Session ids are derived from
the corpus, so re-running re-uses the same sessions — but the episodic records
are written again. Measured on a clean instance: two `ingest` runs of 24 records
produced **48 stored records**, a duplicated corpus.

A duplicated corpus is not the corpus this benchmark defines, and the resulting
numbers are not comparable to anything. Use a fresh, empty tenant for every
measured run, and treat "I re-ran ingest to be safe" as a reason to discard the
result.

### If a run reports transient failures, throw it away

`rbench_sdk.py` prints a warning when any question failed after exhausting
retries. Those questions score zero because the request never completed — not
because retrieval missed. **A run with a non-zero transient-failure count is
not a result.** Fix the cause and re-run.

This is deliberate, and it is the same failure mode the paper is about: an
earlier evaluation was silently invalidated when a request-contract error made
every query fail, producing a clean-looking 0.0% across all categories with
normal latency and no error surfaced. Both harnesses now fail loudly instead.

---

## Published results

The table in the paper was produced by `rbench.py` on **2026-07-08**, with the
reranker OFF (a measured A/B showed the cognitive rerank alone outperforming
the cross-encoder stage on this corpus), no GPU on the search path, and a
self-hosted three-node cluster.

| Mode | recall@1 | recall@5 | recall@10 | p50 | p95 |
|---|---|---|---|---|---|
| Hybrid (dense + lexical, fused + reranked) | 43.5% | 59.5% | 64.0% | 0.21 s | 0.61 s |
| Lexical only | 37.5% | 51.5% | 63.5% | 0.20 s | 0.58 s |

Per category (hybrid, recall@5): temporal-reasoning **88%**,
single-session-assistant 76%, multi-session 68%, knowledge-update 44%,
single-session-user 41%, single-session-preference 37%.

The dense leg's largest contribution is knowledge-update, which goes from 3% to
44% — the category most sensitive to paraphrase.

These figures are a **dated measurement, not a current claim.** The engine has
continued to change. Any later number must come from re-running this harness,
never from extrapolating these.

<!-- TODO(dono): preencher antes de publicar o repo
     - versão/tag do engine correspondente ao run (o commit interno 0fbeaae
       não significa nada num repo público)
     - versão do modelo de embedding
     - sha256 do oracle usado no run de 07-08
-->

---

## License

Apache-2.0 (see `LICENSE`). The LongMemEval corpus is **not** covered by this
license and remains subject to its authors' terms.
