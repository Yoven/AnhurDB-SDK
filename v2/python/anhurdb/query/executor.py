from typing import Dict, Any, List
# We'll need a protocol or ABC for connection if we want strict typing but for now we duck-type.

class QueryExecutor:
    """
    Bridges the gap between the AST built by QueryBuilder and the actual network connection.
    Protects the builder from knowing about HTTP status codes or API keys.
    """
    def __init__(self, connection):
        self.connection = connection

    def execute_query(self, ast: Dict[str, Any]) -> List[Dict[str, Any]]:
        """
        Sends the compiled AST to the AnhurDB backend via the connection.
        """
        # We expect the connection to expose a POST method
        response_data = self.connection.post("/api/v1/query", json=ast)
        
        if "records" not in response_data:
            raise RuntimeError(f"Unexpected response structure from AnhurDB: {response_data}")
            
        return response_data["records"]
