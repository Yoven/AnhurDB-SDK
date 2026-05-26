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
    Processing status of a memory record.
    """
    SAVED = "saved"                                # Written, awaiting processing
    PENDING = "pending"                            # Queued for processing
    PROCESSING = "processing"                      # Being processed
    COMPLETED = "completed"                        # Processing complete
    FAILED = "failed"                              # Processing failed
    ARCHIVED = "archived"                          # Soft-deleted
    DECAYED = "decayed"                            # Low-fidelity (memory decay applied)
    CONSOLIDATED = "consolidated"                  # Included in a summary record
    LINKED = "linked"                              # Cross-session links established
    HUBBED = "hubbed"                              # Grouped into a topic cluster
    PENDING_JUDGE = "pending_judge"                # Pending review
    FAILED_JUDGE = "failed_judge"                  # Review rejected
    FAILED_CONSOLIDATION = "failed_consolidation"  # Summarization failed
