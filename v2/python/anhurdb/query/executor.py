from typing import Dict, Any, List
# We'll need a protocol or ABC for connection if we want strict typing but for now we duck-type.

class QueryExecutor:
    """
    Bridges the gap between the AST built by QueryBuilder and the actual network connection.
    Protects the builder from knowing about HTTP status codes or API keys.
    """
    def __init__(self, connection):
        self.connection = connection

    async def execute_query(self, ast: Dict[str, Any]) -> List[Dict[str, Any]]:
        """
        Sends the compiled AST to the AnhurDB backend via the connection.
        """
        # We expect the connection to expose an async post method
        response_data = await self.connection.post("/v2/search/ast", json_data={"query": ast})
        
        if "records" not in response_data:
             # Depending on endpoint, it might return empty or not wrap records
             return response_data if isinstance(response_data, list) else []
            
        return response_data["records"]
