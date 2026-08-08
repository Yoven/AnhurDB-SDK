from enum import Enum

class MemoryType(str, Enum):
    """
    Cognitive memory types defined by the AnhurDB epistemology.
    """
    # Canonical AnhurDB taxonomy (13 types).
    #
    # Junior Tip [taxonomy SSOT]: the authority is AnhurCore's core.yaml, mirrored
    # by the server's schema.MemoryTypes and the MCP list_types tool — not this
    # file. list_types() below derives straight from this enum, so a missing
    # member silently shortens the public taxonomy. Keep Go, Python, TypeScript
    # and core.yaml in step, in the same change.
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
    ROUTER = "router"  # Macro-theme backbone: a hub of hubs (ADR-0005)

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
