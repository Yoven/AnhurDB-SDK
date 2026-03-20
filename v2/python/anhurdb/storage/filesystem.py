import requests
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

    def read_json_with_fallback(self, tenant_id: str, uuid: str, record_id: int, api_url: str = None, api_key: str = None) -> dict:
        """
        Reads the payload json. Tries direct disk first.
        If it fails (e.g., due to strict V7 Hex auth isolation on the volume),
        it falls back transparently to the REST API if api_url is provided.
        """
        path = self.build_path(tenant_id, uuid, record_id)
        if os.path.exists(path):
            with gzip.open(path, 'rt', encoding='utf-8') as f:
                return json.load(f)
                
        # Fallback to REST API
        if not api_url:
            raise FileNotFoundError(f"Record payload not found locally at {path} and no API fallback URL provided.")
            
        headers = {}
        if api_key:
            headers["X-API-Key"] = api_key
        if tenant_id:
            headers["X-Tenant-ID"] = tenant_id
            
        url = f"{api_url.rstrip('/')}/api/v1/records/{record_id}/content"
        resp = requests.get(url, headers=headers)
        if not resp.ok:
            raise FileNotFoundError(f"Record payload not found locally and API fallback failed: {resp.status_code} {resp.text}")
            
        return resp.json()
