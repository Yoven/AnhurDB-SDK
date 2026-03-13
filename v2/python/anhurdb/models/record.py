from typing import List, Optional, Any, Dict
from pydantic import BaseModel, ConfigDict, Field
from datetime import datetime
from .enums import MemoryType, MemoryStatus

class CreateRequest(BaseModel):
    """
    High-Level input from a Client to AnhurDB.
    The SDK hides cognitive params (weight, dimension, vectors) so the Server handles it.
    """
    model_config = ConfigDict(populate_by_name=True, extra="ignore")
    
    uuid: str
    type: MemoryType = Field(default=MemoryType.EPISODIC)
    content: str
    metadata: Dict[str, str] = Field(default_factory=dict)

class Record(BaseModel):
    """
    Represents a unified Cognitive Memory Record structure returned by AnhurDB.
    """
    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    id: Optional[int] = Field(default=0)
    uuid: str
    type: MemoryType = Field(default=MemoryType.EPISODIC)
    dimension: int = Field(default=384)
    prefix: str = Field(default="")
    weight: float = Field(default=0.0)
    score: int = Field(default=5)
    
    related_ids: List[int] = Field(default_factory=list, alias="related_json")
    main_ids: List[int] = Field(default_factory=list, alias="main_json")
    
    consolidate_id: int = Field(default=0)
    consolidated: bool = Field(default=False)
    archived: bool = Field(default=False)
    status: MemoryStatus = Field(default=MemoryStatus.SAVED)
    
    metadata: str = Field(default="")
    summary: str = Field(default="")
    
    file_path: str = Field(default="")
    checksum: str = Field(default="")
    
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None
    
    # Internal vectors (not usually returned in plain queries)
    vector: Optional[str] = None
    
    # Payload content from FileStorage (not from DB directly)
    content: Optional[Any] = None

class SearchResult(BaseModel):
    record: Record
    similarity: float
