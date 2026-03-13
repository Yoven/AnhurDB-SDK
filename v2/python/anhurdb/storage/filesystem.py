import os
import gzip
import json

class FileStorage:
    """
    Handles reading and writing compressed cognitive payload files.
    """
    def __init__(self, base_path: str):
        self.base_path = base_path

    def build_path(self, tenant_id: str, uuid: str, record_id: int) -> str:
        return os.path.join(self.base_path, tenant_id, uuid, f"{record_id}.gz")
        
    def read_json(self, tenant_id: str, uuid: str, record_id: int) -> dict:
        path = self.build_path(tenant_id, uuid, record_id)
        if not os.path.exists(path):
            raise FileNotFoundError(f"Record payload not found: {path}")
            
        with gzip.open(path, 'rt', encoding='utf-8') as f:
            return json.load(f)
