import os
from typing import Optional, List
from .connection import HTTPConnection
from ..query import QueryBuilder, SemanticMode
from ..models import Record, SearchResult, CreateRequest

class Client:
    """
    The main asynchronous client for AnhurDB V2.
    """
    def __init__(self, url: str = "http://localhost:8080", api_key: Optional[str] = None):
        key = api_key or os.environ.get("ANHUR_API_KEY", "")
        self._connection = HTTPConnection(base_url=url, api_key=key)

    async def __aenter__(self):
        await self._connection.connect()
        return self

    async def __aexit__(self, exc_type, exc_val, exc_tb):
        await self._connection.close()

    async def create(self, req: CreateRequest) -> None:
        """
        Stores a new cognitive or episodic record.
        """
        await self._connection.post("/v2/records", req.model_dump(by_alias=True))
        
    class _SearchResponse:
        def __init__(self, records: List[Record]):
            self.records = records

    async def search_with_ast(self, session_uuid: str, filter_builder) -> _SearchResponse:
        """
        Uses the AST filter to search inside a specific dataset/session.
        """
        ast = filter_builder.ast()
        
        payload = {
            "session_uuid": session_uuid,
            "query": ast
        }
        
        response_data = await self._connection.post("/v2/search/ast", payload)
        
        records_data = response_data.get("records", [])
        records = [Record(**r) for r in records_data]
        return self._SearchResponse(records=records)
