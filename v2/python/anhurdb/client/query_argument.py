"""
Turning whatever ``Memory.query()`` was handed into a compiled AST dict.

One responsibility: accept the three argument shapes the public API promises
(a raw AST dict, a ``QueryBuilder``, a ``Filter``) and reject everything else
in the SDK's standard query-error shape.
"""

from typing import Any, Dict, Optional

from .exceptions import AnhurError, AnhurQueryError


def compile_query_argument(
    ast: Any, session_uuid: Optional[str] = None
) -> Dict[str, Any]:
    """Return a fresh AST dict, session-scoped when ``session_uuid`` is given.

    The returned dict is always a COPY: ``query()`` mutates it to inject the
    session filter, and a caller who reuses their own dict across several
    queries must not find a ``uuid`` predicate glued onto it afterwards.

    Junior Tip [why this raises AnhurQueryError and not TypeError, 2026-09-14]:
    passing the wrong argument type and writing a filter the server rejects are
    the same failure from the caller's seat — "this query is not valid" — and
    they used to arrive as two unrelated exception classes, one of which
    (``TypeError``) also fires for unrelated programming mistakes. Both are now
    ``AnhurQueryError`` with ``kind == "invalid_request"``; ``status_code``
    stays ``None`` here because nothing was ever sent.
    """
    if isinstance(ast, dict):
        compiled_ast = dict(ast)
    elif hasattr(ast, "build_ast"):
        compiled_ast = ast.build_ast()
    elif hasattr(ast, "ast"):
        compiled_ast = ast.ast()
    else:
        raise AnhurQueryError(
            "query() needs an AST dict or a QueryBuilder/Filter "
            "(exposing build_ast()/ast()), got %s." % type(ast).__name__,
            kind=AnhurError.KIND_INVALID_REQUEST,
        )

    # The server does NOT accept session_uuid as a separate field — it must be
    # a regular filter in the AST's filters dict.
    if session_uuid:
        compiled_ast.setdefault("filters", {})["uuid"] = {"$eq": session_uuid}

    return compiled_ast
