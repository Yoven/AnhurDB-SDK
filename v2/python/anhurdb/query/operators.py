"""
Query operators and modes for the AnhurDB Query DSL.

These operators map directly to SQL operations on the server side. Only
operators the server actually implements are included — ``$neq``, ``$nin`` and
``$like`` were removed because the server rejects them with HTTP 400.

``SemanticMode`` survives only as the argument type of the disabled
``QueryBuilder.semantic_search()``; the AST endpoint has no semantic leg.
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

    DEAD KNOB. The AST query engine reads a ``semantic_search`` block and then
    SKIPS it (record_ast_query.go:169-172), so neither mode has ever changed a
    single returned row. ``QueryBuilder.semantic_search()`` now refuses rather
    than pretending; this enum is kept only as that method's argument type and
    as the name to reuse if the server ever grows the capability. Real semantic
    retrieval is ``Memory.search()`` / ``POST /api/v1/search``.
    """

    TEXT = "$text"
    HYBRID = "$hybrid"
