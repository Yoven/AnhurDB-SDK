#!/usr/bin/env python3
"""Pin the evaluation corpus: hash the oracle file and freeze the question set.

Why this exists: the harness samples questions by FILE ORDER (first N of each
type, types sorted). That is deterministic for a given file, but a reordered
or updated release of LongMemEval would silently yield a different sample and
therefore different numbers. Two artifacts remove that ambiguity:

  * corpus.sha256    — identifies the exact oracle file used;
  * question_ids.json — the exact question ids selected, so the sample can be
    reconstructed even if a future release reorders the corpus.

Run once, commit both files, and reference them from the README.

    RB_ORACLE=/path/longmemeval_oracle.json RB_NPER=34 python3 pin_corpus.py
"""
import hashlib
import json
import os
import sys

ORACLE_PATH = os.environ.get("RB_ORACLE", "/tmp/longmemeval_oracle.json")
PER_TYPE = int(os.environ.get("RB_NPER", "34"))


def sha256_of_file(path: str) -> str:
    """Streaming sha256 so a large corpus never has to sit in memory at once."""
    digest = hashlib.sha256()
    with open(path, "rb") as corpus_file:
        for block in iter(lambda: corpus_file.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def main() -> None:
    if not os.path.exists(ORACLE_PATH):
        sys.exit("oracle not found at %s — set RB_ORACLE" % ORACLE_PATH)

    corpus_digest = sha256_of_file(ORACLE_PATH)
    oracle = json.load(open(ORACLE_PATH))

    # Same selection rule as both harnesses — keep these three lines in sync.
    questions_by_type = {}
    for question in oracle:
        questions_by_type.setdefault(question["question_type"], []).append(question)
    selected = [q for _, qs in sorted(questions_by_type.items()) for q in qs[:PER_TYPE]]

    counts_by_type = {}
    for question in selected:
        counts_by_type[question["question_type"]] = counts_by_type.get(question["question_type"], 0) + 1

    with open("corpus.sha256", "w") as digest_file:
        digest_file.write("%s  %s\n" % (corpus_digest, os.path.basename(ORACLE_PATH)))

    pin = {
        "oracle_file": os.path.basename(ORACLE_PATH),
        "oracle_sha256": corpus_digest,
        "n_per_type": PER_TYPE,
        "n_questions": len(selected),
        "counts_by_type": counts_by_type,
        "question_ids": [question["question_id"] for question in selected],
    }
    with open("question_ids.json", "w") as pin_file:
        json.dump(pin, pin_file, indent=2)
        pin_file.write("\n")

    print("oracle sha256 : %s" % corpus_digest)
    print("questions     : %d (%d per type)" % (len(selected), PER_TYPE))
    for type_name, count in sorted(counts_by_type.items()):
        print("  %-30s %d" % (type_name, count))
    print("\nwrote corpus.sha256 and question_ids.json — commit both.")


if __name__ == "__main__":
    main()
