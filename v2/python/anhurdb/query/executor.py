"""
Query execution bridge for the AnhurDB QueryBuilder.

Sends the compiled AST directly to ``POST /api/v1/query``.

Important: The server expects the AST fields (``filters``, ``pagination``,
``sort``, ``select``) **flat at the top level** of the request body.
Do NOT wrap them in ``{"query": ...}``.
"""

from typing import Any, Dict, List


class QueryExecutor:
    """
    Bridges the AST built by QueryBuilder and the HTTP connection.

    Protects the builder from knowing about HTTP status codes or API keys.

    Args:
        connection: An HTTPConnection instance with a ``post()`` method.
    """

    def __init__(self, connection: Any):
        self.connection = connection

    async def execute_query(self, ast: Dict[str, Any]) -> List[Dict[str, Any]]:
        """
        Send the compiled AST to ``POST /api/v1/query``.

        The AST dict is sent flat as the request body — the server
        deserialises it directly into its ``AstQuery`` struct.

        Args:
            ast: The compiled AST from ``QueryBuilder.build_ast()``.

        Returns:
            List of record dicts from the server response.
        """
        # Send AST flat (not wrapped in {"query": ast}).
        response_data = await self.connection.post("/api/v1/query", json_data=ast)

        if isinstance(response_data, dict) and "records" in response_data:
            return response_data["records"]

        return response_data if isinstance(response_data, list) else []
