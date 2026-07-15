"""
Query operators and modes for the AnhurDB Query DSL.

These operators map directly to SQL operations on the server side
. Only operators the server
actually implements are included — ``$neq``, ``$nin``, and ``$like``
were removed because the server silently ignores them.

Semantic search modes are defined but the server currently logs them
without processing. They are included for forward compatibility.
"""

from enum import Enum


class QueryOperator(str, Enum):
    """
    Operators supported by the AnhurDB AST query engine.

    Each operator maps to a SQL clause on the server:
      - ``$eq``  → ``field = ?``
      - ``$gt``  → ``field > ?``
      - ``$gte`` → ``field >= ?``
      - ``$lt``  → ``field < ?``
      - ``$lte`` → ``field <= ?``
      - ``$in``  → ``field IN (?, ?, ...)``
    """

    EQ = "$eq"
    GT = "$gt"
    GTE = "$gte"
    LT = "$lt"
    LTE = "$lte"
    IN = "$in"


class SemanticMode(str, Enum):
    """
    Semantic search modes for hybrid queries.

    Note: The server currently logs semantic_search blocks but does not
    process them. These are included for forward compatibility when the
    server implements standalone semantic SQL mapping.
    """

    TEXT = "$text"
    HYBRID = "$hybrid"
