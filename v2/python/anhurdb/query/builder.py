"""
Fluent query builder for the AnhurDB AST query engine.

Generates a JSON Abstract Syntax Tree (AST) that the server processes
via ``POST /api/v1/query``. The AST is validated server-side against
a column whitelist and operator set.

The grammar itself — the whitelist, the operator set, and every client-side
rejection — lives in ``grammar.py``. This file is only the fluent surface.

Usage::

    qb = QueryBuilder()
    qb.where(type="risk", weight__gt=0.8)
    qb.order_by("weight", "desc")
    qb.limit(10)
    ast = qb.build_ast()

    # Or with Filter shorthand:
    f = Filter({"type": {"$eq": "risk"}})
    ast = f.ast()

Errors:
    Every rejection this builder issues is an ``AnhurQueryError`` with
    ``kind == "invalid_request"`` and ``status_code is None`` — the same type
    the server's own HTTP 400 arrives as, so one ``except AnhurQueryError``
    covers a bad query whether it was caught here or on the wire.

Security:
    - Column names are validated against the server's whitelist.
    - Operator suffixes are validated against the server's supported set.
    - Values are passed through as-is (the server uses parameterised queries).
"""

from typing import Any, Dict, List, Optional
import copy

from .grammar import (
    ALLOWED_WHERE_COLUMNS,
    OPERATOR_SUFFIXES,
    assert_filter_column,
    assert_filter_value,
    assert_operator_suffix,
    query_rejection,
    semantic_search_rejection,
)
from .operators import QueryOperator, SemanticMode

# Re-exported: importers have used ``anhurdb.query.builder.ALLOWED_WHERE_COLUMNS``
# since 2.0 (the live coverage sweep enumerates it to build its 102 pairs).
__all__ = ["ALLOWED_WHERE_COLUMNS", "QueryBuilder"]


class QueryBuilder:
    """
    Fluent interface for building AnhurDB AST queries.

    The builder generates a JSON dict with this structure::

        {
            "select": ["id", "summary"],        # ignored by server currently
            "filters": {
                "type": {"$eq": "risk"},
                "weight": {"$gt": 0.8}
            },
            "sort": [{"field": "weight", "order": "desc"}],
            "pagination": {"limit": 50, "offset": 0}
        }

    Args:
        executor: Optional ``QueryExecutor`` for ``.execute()`` support.
    """

    def __init__(self, executor: Any = None):
        self._executor = executor
        self._select: List[str] = []
        self._filters: Dict[str, Any] = {}
        self._sort: List[Dict[str, str]] = []
        self._limit: int = 50
        self._offset: int = 0

    def select(self, *fields: str) -> "QueryBuilder":
        """
        Specify which fields to return.

        Note: The server currently ignores this and returns all columns.
        Included for forward compatibility.

        Args:
            *fields: Column names to include in results.

        Returns:
            Self for chaining.
        """
        self._select.extend(fields)
        return self

    def where(self, **kwargs: Any) -> "QueryBuilder":
        """
        Add filter conditions using Django-style kwargs.

        Supports two forms:
          - Exact match: ``where(type="risk")`` → ``{"type": {"$eq": "risk"}}``
          - Operator suffix: ``where(weight__gt=0.8)`` → ``{"weight": {"$gt": 0.8}}``

        Supported operators: ``eq``, ``gt``, ``gte``, ``lt``, ``lte``, ``in``.

        Every predicate is ANDed by the server; there is no ``or``. An SDK that
        offered one would be inventing a capability the grammar does not have.

        Args:
            **kwargs: Field=value or field__op=value pairs.

        Returns:
            Self for chaining.

        Raises:
            AnhurQueryError: field not in the server's whitelist, unsupported
                operator suffix, or a value that would silently match nothing
                (``None``, or an empty ``$in`` list).
        """
        for key, value in kwargs.items():
            if "__" in key:
                field, op_suffix = key.split("__", 1)
                assert_filter_column(field)
                assert_operator_suffix(op_suffix)
                assert_filter_value(field, op_suffix, value)

                if field not in self._filters:
                    self._filters[field] = {}
                elif not isinstance(self._filters[field], dict):
                    raise query_rejection(
                        f"Field '{field}' has conflicting exact match."
                    )

                self._filters[field][OPERATOR_SUFFIXES[op_suffix].value] = value

            else:
                assert_filter_column(key)
                assert_filter_value(key, "eq", value)
                self._filters[key] = {QueryOperator.EQ.value: value}

        return self

    def semantic_search(
        self, query: str, mode: SemanticMode = SemanticMode.HYBRID
    ) -> "QueryBuilder":
        """
        DISABLED — always raises. The AST endpoint has no semantic leg.

        The server accepts a ``semantic_search`` block and then skips it, so
        this method never influenced a single returned row. It now refuses
        instead of pretending, and the error names the endpoint that does the
        real work (``Memory.search`` / ``POST /api/v1/search``).

        Raises:
            AnhurQueryError: always.
        """
        raise semantic_search_rejection(getattr(mode, "value", mode))

    def order_by(self, field: str, direction: str = "desc") -> "QueryBuilder":
        """
        Add a sort clause.

        Args:
            field:     Column to sort by (must be in whitelist).
            direction: ``"asc"`` or ``"desc"`` (default: ``"desc"``).

        Returns:
            Self for chaining.

        Raises:
            AnhurQueryError: field not in the whitelist, or a direction outside
                ``asc``/``desc``.

        Junior Tip [why an unknown direction is refused here]: the server does
        NOT reject one. Its whitelist is ``ASC/DESC/asc/desc`` and anything else
        falls through to DESC silently (record_ast_query.go:240-242), so a typo
        like ``"ascending"`` returns rows in the exact opposite order with a
        200. That is a wrong answer with no error attached, which is precisely
        the case a client-side check is for.
        """
        assert_filter_column(field, clause="order_by")
        direction_lower = direction.lower()
        if direction_lower not in ("asc", "desc"):
            raise query_rejection("order_by direction must be 'asc' or 'desc'.")

        self._sort.append({"field": field, "order": direction_lower})
        return self

    def limit(self, max_results: int) -> "QueryBuilder":
        """
        Set maximum results to return.

        Args:
            max_results: Maximum results (1-1000).

        Returns:
            Self for chaining.

        Raises:
            AnhurQueryError: If out of range.

        Junior Tip [why 1001 is refused instead of forwarded]: the server caps
        at 1000 by SILENT CLAMP — ask for 5000 and it answers 200 with 1000
        rows and no field anywhere in the response saying it truncated you. A
        caller paging on "did I get everything" would loop forever. Refusing
        client-side is the only place that number can be questioned.
        """
        if max_results < 1 or max_results > 1000:
            raise query_rejection("Limit must be between 1 and 1000.")
        self._limit = max_results
        return self

    def offset(self, skip: int) -> "QueryBuilder":
        """
        Set pagination offset.

        Args:
            skip: Number of results to skip (>= 0).

        Returns:
            Self for chaining.

        Raises:
            AnhurQueryError: If negative.
        """
        if skip < 0:
            raise query_rejection("Offset cannot be negative.")
        self._offset = skip
        return self

    def build_ast(self) -> Dict[str, Any]:
        """
        Compile the builder state into the JSON AST the server expects.

        Returns:
            Dict matching the server's ``AstQuery`` struct::

                {
                    "filters": {...},
                    "pagination": {"limit": N, "offset": N},
                    "select": [...],   # optional
                    "sort": [...]      # optional
                }
        """
        ast: Dict[str, Any] = {
            "filters": copy.deepcopy(self._filters),
            "pagination": {
                "limit": self._limit,
                "offset": self._offset,
            },
        }
        if self._select:
            ast["select"] = list(set(self._select))
        if self._sort:
            ast["sort"] = copy.deepcopy(self._sort)

        return ast

    async def execute(self) -> Any:
        """
        Validate the AST and dispatch execution to the provided executor.

        Raises:
            AnhurQueryError: If no executor was provided.
        """
        if not self._executor:
            raise query_rejection(
                "Cannot execute: No executor was provided to QueryBuilder."
            )

        ast = self.build_ast()
        return await self._executor.execute_query(ast)
