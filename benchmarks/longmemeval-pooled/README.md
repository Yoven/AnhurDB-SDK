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

## One harness

`rbench_sdk.py` is the only harness. It exercises AnhurDB through the official
Python SDK — the same client surface a customer uses — always letting the
**server** embed the query (the product path), and expressing the lexical-only
leg as `skip_query_embed=True`.

There used to be a second, REST-speaking harness (`rbench.py`), and the two were
held to each other by a parity test. It has been retired. What replaced it is
stricter, not weaker: the measurement contract is now pinned against **committed
golden values** generated from the exact build that produced the published
numbers.

That contract is **machine-checked**, offline, with no server or corpus:

```bash
python3 test_parity.py
```

It checks question sampling, session UUIDs (`sha256("longmemeval:<qid>:<sid>")`),
the chatty split, turn chunking, record body, scoring, and the exact wire
payload the harness builds, against `contract_gold.json`. Run it before trusting
any number this harness produces. The check was itself mutation-tested:
deliberately breaking the sample size, the UUID salt, the summary cut-off, the
turn separator, the type ordering, or the ingest weight and score each makes it
fail.

**Regenerating the goldens is a deliberate act.** If this test fails, the
harness no longer builds the corpus the published numbers came from, and new
results must not be reported beside them.

### What the latency numbers mean

`rbench_sdk.py` times the SDK round trip — parsing, retries and connection
pooling included — which is the latency a client actually experiences, not raw
HTTP. Figures measured through a different transport are not comparable to
these and must never be averaged with them.

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

### Results are written to disk

Each scored run writes `results/<tag>-<mode>.json` containing the aggregate
recall and latency, a per-question breakdown (including the rank of the first
correct hit), the endpoint and tenant, the corpus checksum, and the run
configuration. Per-question detail is what makes two runs diffable: an
aggregate that moved is a question that moved, and without the detail you
cannot tell which. Set `RB_RESULTS_DIR=""` to disable.

The API key is never recorded. The endpoint is, because a result is
uninterpretable without knowing what produced it.

### The readiness gate — what it is, and what it is not for

Before scoring, `rbench_sdk.py` probes a few known questions and waits until
the hit count stops changing across `RB_READY_STABLE_POLLS` consecutive polls
(default 3, five seconds apart). Set it to 1 to effectively skip the wait.

**Be precise about why this exists.** Fresh records are retrievable
immediately. On a clean instance, a single record inserted and queried in the
same breath is returned at t+0s by both the lexical and the hybrid path, with
no embedding present — the model-free legs (full-text and content fingerprints)
carry retrieval while embeddings are still being computed, which is what they
are designed to do. This gate is **not** compensating for a write-path
indexing lag.

What we did observe, once, is a scoring run returning zero results for every
question right after a bulk ingest, where the same corpus answered later. We
could not isolate the cause, and we will not dress a guess up as a finding. The
gate is therefore a **defensive guard**: it costs a few seconds and stops a run
that is measuring nothing from being written out as a result. It is not
evidence about the engine and should not be cited as such.

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

An earlier table was produced by a since-retired REST harness on **2026-07-08**, with the
reranker OFF (a measured A/B showed the cognitive rerank alone outperforming
the cross-encoder stage on this corpus), no GPU on the search path, and a
self-hosted three-node cluster.

| Mode | recall@1 | recall@5 | recall@10 | p50 | p95 |
|---|---|---|---|---|---|
| Hybrid (dense + lexical, fused + cognitive rank) | 43.5% | 59.5% | 64.0% | 0.21 s | 0.61 s |
| Lexical arm **plus the cognitive rank** | 37.5% | 51.5% | 63.5% | 0.20 s | 0.58 s |

**The second row's old label was wrong.** It read "Lexical only", but the
`lexical` mode suppresses the dense arm and leaves the cognitive rank running,
so the row measured two things at once. Isolating the arm needs `lexical-pure`,
a mode added 2026-08-14 that also passes `skip_cognitive_rerank`. On a later
corpus the distinction was worth 39 points of recall@1, so the mislabel is not
cosmetic. Re-run all three (`recall`, `lexical`, `lexical-pure`) before quoting
any of them.

Per category (hybrid, recall@5): temporal-reasoning **88%**,
single-session-assistant 76%, multi-session 68%, knowledge-update 44%,
single-session-user 41%, single-session-preference 37%.

The dense leg's largest contribution is knowledge-update, which goes from 3% to
44% — the category most sensitive to paraphrase.

These figures are a **dated measurement, not a current claim.** The engine has
continued to change. Any later number must come from re-running this harness,
never from extrapolating these.

Two changes since this run move these numbers directly, which is why they are
not refreshed in place here — a table that mixes engine versions is worse than
a dated one. The fusion normaliser divided by a constant two legs, so any
query answered by a single arm (every lexical row above) had its relevance term
halved against the fixed priors it competes with; and the connectivity term of
the cognitive weight counted one edge set, which under-weighted every
summary-type record. Both were corrected 2026-08-15.

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
