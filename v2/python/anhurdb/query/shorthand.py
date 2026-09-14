"""
Raw-AST shorthands: the documented escape hatch around ``QueryBuilder``.

``QueryBuilder`` is the guarded path — it validates every column, operator and
value against the grammar the server implements. The two helpers here are the
UNGUARDED path, kept on purpose: sometimes the server is the only authority you
want, either because the SDK's copy of the whitelist has drifted or because you
are deliberately probing what the server does with a payload the builder would
refuse to construct.

Junior Tip [why keeping an unvalidated path is not a hole, 2026-09-14]: nothing
here can produce a query the server would not have accepted from any HTTP
client, so this bypasses no security boundary — the server parameterises every
value regardless. What it bypasses is the SDK's early-warning layer, and the
cost of that is one round trip and an HTTP 400. That trade is worth offering
explicitly, because the alternative is people hand-rolling ``aiohttp`` calls
and losing the auth, retry and error typing this SDK provides.
"""

from typing import Any, Dict, Optional

from .builder import QueryBuilder


def Eq(field: str, value: Any) -> Dict[str, Any]:
    """Shorthand for an exact-match filter dict."""
    return {field: {"$eq": value}}


class Filter:
    """
    Syntactic sugar for creating a pre-built AST filter.

    Unlike ``QueryBuilder.where()``, this deliberately performs NO validation:
    it is the documented escape hatch for sending a raw AST and letting the
    server be the authority. A mistake here comes back as an HTTP 400 —
    ``AnhurQueryError`` with ``status_code == 400``, the same type the builder
    raises locally.

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
