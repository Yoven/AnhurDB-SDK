from typing import Any, Dict, List, Optional
import copy

from .operators import QueryOperator, SemanticMode
from ..models import Record

# Whitelisted columns that are secure to filter by
ALLOWED_WHERE_COLUMNS = {
    "id", "uuid", "type", "dimension", "weight", "score",
    "consolidate_id", "consolidated", "archived", "status",
    "created_at", "updated_at"
}

class QueryBuilder:
    """
    A unified fluent interface for building AnhurDB Queries (DSL).
    
    [V2 ARCHITECTURE NOTE]:
    This builder explicitly generates a JSON Abstract Syntax Tree (AST).
    When `.execute()` is called on the Public SDK, this AST is wrapped securely 
    into an MCP Tool payload (`execute_ast`) and routed through the MCP Gateway
    on port 9092, never hitting the AnhurDB cluster directly. 
    """
    def __init__(self, executor=None):
        self._executor = executor
        self._select: List[str] = []
        self._filters: Dict[str, Any] = {}
        self._sort: List[Dict[str, str]] = []
        self._limit: int = 50
        self._offset: int = 0

    def select(self, *fields: str) -> "QueryBuilder":
        """
        Specify which fields should be returned to reduce payload size.
        """
        self._select.extend(fields)
        return self

    def where(self, **kwargs) -> "QueryBuilder":
        """
        Construct filters fluently using Django-style kwargs.
        Example: .where(type="risk", weight__gt=0.8)
        """
        for key, value in kwargs.items():
            if "__" in key:
                field, op_suffix = key.split("__", 1)
                if field not in ALLOWED_WHERE_COLUMNS:
                    raise ValueError(f"Field '{field}' is not allowed in filters.")
                
                op_map = {
                    "eq": QueryOperator.EQ,
                    "neq": QueryOperator.NEQ,
                    "gt": QueryOperator.GT,
                    "gte": QueryOperator.GTE,
                    "lt": QueryOperator.LT,
                    "lte": QueryOperator.LTE,
                    "in": QueryOperator.IN,
                    "nin": QueryOperator.NIN,
                    "like": QueryOperator.LIKE,
                }
                
                if op_suffix not in op_map:
                    raise ValueError(f"Operator suffix '{op_suffix}' is not supported.")
                
                # Append operator to existing field dict or create new
                if field not in self._filters:
                    self._filters[field] = {}
                elif not isinstance(self._filters[field], dict):
                    # Edge case: previously set an exact match, e.g. .where(weight=1).where(weight__gt=0)
                    raise ValueError(f"Field '{field}' has conflicting exact match.")
                
                self._filters[field][op_map[op_suffix].value] = value
                
            else:
                if key not in ALLOWED_WHERE_COLUMNS:
                    raise ValueError(f"Field '{key}' is not allowed in filters.")
                self._filters[key] = {QueryOperator.EQ.value: value}

        return self

    def semantic_search(self, query: str, mode: SemanticMode = SemanticMode.HYBRID) -> "QueryBuilder":
        """
        Appends a semantic search block to the query.
        This allows combining metadata filters with vector/FTS search.
        """
        self._filters["semantic_search"] = {
            "query": query,
            "mode": mode.value
        }
        return self

    def order_by(self, field: str, direction: str = "desc") -> "QueryBuilder":
        if field not in ALLOWED_WHERE_COLUMNS:
            raise ValueError(f"Field '{field}' is not allowed in order_by.")
        if direction.lower() not in ["asc", "desc"]:
            raise ValueError("order_by direction must be 'asc' or 'desc'.")
            
        self._sort.append({"field": field, "order": direction.lower()})
        return self

    def limit(self, max_results: int) -> "QueryBuilder":
        if max_results < 1 or max_results > 500:
            raise ValueError("Limit must be between 1 and 500.")
        self._limit = max_results
        return self

    def offset(self, skip: int) -> "QueryBuilder":
        if skip < 0:
            raise ValueError("Offset cannot be negative.")
        self._offset = skip
        return self

    def build_ast(self) -> Dict[str, Any]:
        """
        Compiles the fluent operations into the expected JSON AST format.
        """
        ast: Dict[str, Any] = {
            "filters": copy.deepcopy(self._filters),
            "pagination": {
                "limit": self._limit,
                "offset": self._offset
            }
        }
        if self._select:
            ast["select"] = list(set(self._select))
        if self._sort:
            ast["sort"] = copy.deepcopy(self._sort)
            
        return ast

    async def execute(self) -> Any:
        """
        Validates the AST and dispatches execution to the provided executor.
        """
        if not self._executor:
            raise RuntimeError("Cannot execute: No executor was provided to QueryBuilder.")
        
        ast = self.build_ast()
        return await self._executor.execute_query(ast)

def Eq(field: str, value: Any) -> Dict[str, Any]:
    return {field: {"$eq": value}}

class Filter:
    """
    Syntactic sugar for instantiating a QueryBuilder with a specific condition.
    """
    def __init__(self, condition: Optional[Dict[str, Any]] = None, **kwargs):
        self._builder = QueryBuilder()
        if condition:
            # Merging condition directly into the builder's filter dictionary
            for k, v in condition.items():
                self._builder._filters[k] = v
                
    def ast(self) -> Dict[str, Any]:
        return self._builder.build_ast()
