from enum import Enum

class MemoryType(str, Enum):
    """
    Cognitive memory types defined by the AnhurDB epistemology.
    """
    EPISODIC = "episodic"
    FACT = "fact"
    REASONING = "reasoning"
    DECISION = "decision"
    RISK = "risk"
    PREFERENCE = "preference"
    HUB = "hub"
    IDEA = "idea"
    ENTITY = "entity"

class MemoryStatus(str, Enum):
    """
    Status of a cognitive record in the cluster.
    """
    SAVED = "saved"
    CONSOLIDATED = "consolidated"
    ARCHIVED = "archived"
    DECAYED = "decayed"
    PROCESSING = "processing"
    FAILED = "failed"
