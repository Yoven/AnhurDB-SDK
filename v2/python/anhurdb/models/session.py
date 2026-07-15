from pydantic import BaseModel, ConfigDict
from typing import Dict, Optional

class SessionStats(BaseModel):
    """
    Represents aggregated metadata regarding a specific cognitive session inside AnhurDB.
    """
    model_config = ConfigDict(populate_by_name=True, extra="ignore")

    uuid: str
    record_count: int
    types: Dict[str, int]
    last_activity: str
    summary: Optional[str] = None
