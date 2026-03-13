from .connection import HTTPConnection
from ..query import QueryBuilder, QueryExecutor
from ..models import Record

class MemoryCollection:
    """
    Represents the cognitive memories space in AnhurDB.
    Provides a factory for fluent queries.
    """
    def __init__(self, connection: HTTPConnection):
        self._executor = QueryExecutor(connection)

    # 1. Fluent Query Builder Spawner
    def select(self, *fields: str) -> QueryBuilder:
        return QueryBuilder(self._executor).select(*fields)

    def where(self, **kwargs) -> QueryBuilder:
        return QueryBuilder(self._executor).where(**kwargs)
        
    def semantic_search(self, query: str, mode: str = "$hybrid") -> QueryBuilder:
        # Note: mapping strings to SemanticMode Enum happens internally or user can pass Enum
        return QueryBuilder(self._executor).semantic_search(query, mode=mode)
        
    # 2. Singular Operations (Future)
    def insert(self, record: Record) -> int:
        raise NotImplementedError("Insert operation not yet mapped in V2 API.")
        
    def decay(self, ids: list[int], target_weight: float) -> bool:
        raise NotImplementedError("Decay operation not yet mapped in V2 API.")

class AnhurClient:
    """
    The main client for AnhurDB V2.
    """
    def __init__(self, url: str, api_key: str):
        self._connection = HTTPConnection(base_url=url, api_key=api_key)
        
        # Domain objects (Collections)
        self.memories = MemoryCollection(self._connection)
