from enum import Enum

class QueryOperator(str, Enum):
    """
    Allowed abstract operators for the AnhurDB Query DSL.
    """
    EQ = "$eq"
    NEQ = "$neq"
    GT = "$gt"
    GTE = "$gte"
    LT = "$lt"
    LTE = "$lte"
    IN = "$in"
    NIN = "$nin"
    LIKE = "$like"
    
class SemanticMode(str, Enum):
    TEXT = "$text"
    HYBRID = "$hybrid"
