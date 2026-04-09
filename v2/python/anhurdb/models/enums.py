from enum import Enum

class MemoryType(str, Enum):
    """
    Cognitive memory types defined by the AnhurDB epistemology.
    """
    # Must match AnhurCore/core.yaml taxonomy (12 types)
    EPISODIC = "episodic"
    FACT = "fact"
    PREFERENCE = "preference"
    DECISION = "decision"
    TASK = "task"
    RISK = "risk"
    REASONING = "reasoning"
    IDEA = "idea"
    EMOTION = "emotion"
    CONSOLIDATED = "consolidated"
    HUB = "hub"
    FILE = "file"

class MemoryStatus(str, Enum):
    """
    Status of a cognitive record in the cluster.
    Must match all statuses used across agents and AnhurDB handlers.

    DEFINITIVE LIST — sourced from:
      - Go server: create.go, update.go, record_batch.go, upload.go
      - Python agents: consolidator.py, judge.py, regression/worker.py
    """
    SAVED = "saved"                                # Default on record creation
    PENDING = "pending"                            # Legacy status (pre-v2 records)
    CONSOLIDATED = "consolidated"                  # After consolidation merges records
    ARCHIVED = "archived"                          # Soft-deleted (consolidated children)
    DECAYED = "decayed"                            # Memory decay applied (low fidelity)
    PROCESSING = "processing"                      # Being ingested/transformed
    COMPLETED = "completed"                        # File ingestion or planner step done
    LINKED = "linked"                              # Similarity edges established by linker
    HUBBED = "hubbed"                              # Assigned to a hub node by hub_growth
    FAILED = "failed"                              # Generic failure
    # Agent pipeline statuses (set by Judge, Consolidation, Planner)
    PENDING_JUDGE = "pending_judge"                # Awaiting Judge validation
    FAILED_JUDGE = "failed_judge"                  # Judge rejected after max retries
    FAILED_CONSOLIDATION = "failed_consolidation"  # Consolidation failed
