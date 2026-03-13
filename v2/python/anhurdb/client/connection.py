import urllib.parse
import urllib.request
import json
from typing import Any, Dict

from .exceptions import AnhurError, AnhurConnectionError, AnhurQueryError, AnhurAuthError

class HTTPConnection:
    """
    A simple, synchronous HTTP connection wrapper for AnhurDB API.
    Handles authentication headers and JSON serialization.
    """
    def __init__(self, base_url: str, api_key: str):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        
        self.headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {self.api_key}",
            "User-Agent": "AnhurSDK-Python-V2/1.0"
        }

    def post(self, endpoint: str, json_data: Dict[str, Any]) -> Any:
        url = f"{self.base_url}{endpoint}"
        
        data = json.dumps(json_data).encode("utf-8")
        req = urllib.request.Request(url, data=data, headers=self.headers, method="POST")
        
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                body = response.read()
                return json.loads(body.decode("utf-8"))
        except urllib.error.HTTPError as e:
            if e.code in (401, 403):
                raise AnhurAuthError(f"Authentication failed: {e.reason}")
            elif e.code == 400:
                body = e.read().decode("utf-8")
                raise AnhurQueryError(f"Invalid request ({e.code}): {body}")
            else:
                body = e.read().decode("utf-8")
                raise AnhurError(f"Server error ({e.code}): {body}")
        except urllib.error.URLError as e:
            raise AnhurConnectionError(f"Failed to connect to {url}: {e.reason}")
