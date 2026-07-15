"""
Fluent query builder for the AnhurDB AST query engine.

Generates a JSON Abstract Syntax Tree (AST) that the server processes
via ``POST /api/v1/query``. The AST is validated server-side against
a column whitelist and operator set.

Server contract: whitelisted filter/sort columns on the AST query endpoint.

Usage::

    qb = QueryBuilder()
    qb.where(type="risk", weight__gt=0.8)
    qb.order_by("weight", "desc")
    qb.limit(10)
    ast = qb.build_ast()

    # Or with Filter shorthand:
    f = Filter({"type": {"$eq": "risk"}})
    ast = f.ast()

Security:
    - Column names are validated against the server's whitelist.
    - Operator suffixes are validated against the server's supported set.
    - Values are passed through as-is (the server uses parameterised queries).
"""

from typing import Any, Dict, List, Optional
import copy

from .operators import QueryOperator, SemanticMode


# Columns the server allows in filters and sort.
# Must match the server AST query whitelist.
ALLOWED_WHERE_COLUMNS = {
    "id", "uuid", "type", "dimension", "weight", "score",
    "status", "consolidated", "archived", "created_at", "updated_at",
    "prefix", "metadata", "summary",
    "superseded_by", "valid_from", "valid_until",
}

# Operator suffix → QueryOperator mapping.
# Only operators the server actually implements are included.
# $neq, $nin, $like were removed — server silently ignores them.
_OP_MAP = {
    "eq": QueryOperator.EQ,
    "gt": QueryOperator.GT,
    "gte": QueryOperator.GTE,
    "lt": QueryOperator.LT,
    "lte": QueryOperator.LTE,
    "in": QueryOperator.IN,
}


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

        Args:
            **kwargs: Field=value or field__op=value pairs.

        Returns:
            Self for chaining.

        Raises:
            ValueError: If field is not in the server's whitelist.
            ValueError: If operator suffix is not supported.
        """
        for key, value in kwargs.items():
            if "__" in key:
                field, op_suffix = key.split("__", 1)
                if field not in ALLOWED_WHERE_COLUMNS:
                    raise ValueError(
                        f"Field '{field}' is not allowed in filters. "
                        f"Allowed: {sorted(ALLOWED_WHERE_COLUMNS)}"
                    )

                if op_suffix not in _OP_MAP:
                    raise ValueError(
                        f"Operator suffix '{op_suffix}' is not supported. "
                        f"Allowed: {sorted(_OP_MAP.keys())}"
                    )

                if field not in self._filters:
                    self._filters[field] = {}
                elif not isinstance(self._filters[field], dict):
                    raise ValueError(
                        f"Field '{field}' has conflicting exact match."
                    )

                self._filters[field][_OP_MAP[op_suffix].value] = value

            else:
                if key not in ALLOWED_WHERE_COLUMNS:
                    raise ValueError(
                        f"Field '{key}' is not allowed in filters. "
                        f"Allowed: {sorted(ALLOWED_WHERE_COLUMNS)}"
                    )
                self._filters[key] = {QueryOperator.EQ.value: value}

        return self

    def semantic_search(
        self, query: str, mode: SemanticMode = SemanticMode.HYBRID
    ) -> "QueryBuilder":
        """
        Append a semantic search block to the query.

        Note: The server currently logs this block but does not process it.
        Included for forward compatibility.

        Args:
            query: Natural language search query.
            mode:  Search mode (``$text`` or ``$hybrid``).

        Returns:
            Self for chaining.
        """
        self._filters["semantic_search"] = {
            "query": query,
            "mode": mode.value,
        }
        return self

    def order_by(self, field: str, direction: str = "desc") -> "QueryBuilder":
        """
        Add a sort clause.

        Args:
            field:     Column to sort by (must be in whitelist).
            direction: ``"asc"`` or ``"desc"`` (default: ``"desc"``).

        Returns:
            Self for chaining.

        Raises:
            ValueError: If field is not in the whitelist.
            ValueError: If direction is not ``asc`` or ``desc``.
        """
        if field not in ALLOWED_WHERE_COLUMNS:
            raise ValueError(
                f"Field '{field}' is not allowed in order_by. "
                f"Allowed: {sorted(ALLOWED_WHERE_COLUMNS)}"
            )
        direction_lower = direction.lower()
        if direction_lower not in ("asc", "desc"):
            raise ValueError("order_by direction must be 'asc' or 'desc'.")

        self._sort.append({"field": field, "order": direction_lower})
        return self

    def limit(self, max_results: int) -> "QueryBuilder":
        """
        Set maximum results to return.

        The server caps this at 1000 regardless of what is set here.

        Args:
            max_results: Maximum results (1-1000).

        Returns:
            Self for chaining.

        Raises:
            ValueError: If out of range.
        """
        if max_results < 1 or max_results > 1000:
            raise ValueError("Limit must be between 1 and 1000.")
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
            ValueError: If negative.
        """
        if skip < 0:
            raise ValueError("Offset cannot be negative.")
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
            RuntimeError: If no executor was provided.
        """
        if not self._executor:
            raise RuntimeError(
                "Cannot execute: No executor was provided to QueryBuilder."
            )

        ast = self.build_ast()
        return await self._executor.execute_query(ast)


def Eq(field: str, value: Any) -> Dict[str, Any]:
    """Shorthand for an exact-match filter dict."""
    return {field: {"$eq": value}}


class Filter:
    """
    Syntactic sugar for creating a pre-built AST filter.

    Usage::

        f = Filter({"type": {"$eq": "risk"}, "weight": {"$gt": 0.8}})
        results = await client.search_with_ast(f, session_uuid="session-uuid")
    """

    def __init__(self, condition: Optional[Dict[str, Any]] = None, **kwargs: Any):
        self._builder = QueryBuilder()
        if condition:
            for k, v in condition.items():
                self._builder._filters[k] = v

    def ast(self) -> Dict[str, Any]:
        """Return the compiled AST dict."""
        return self._builder.build_ast()
