import aiohttp
import json
from typing import Any, Dict, Optional

from .exceptions import AnhurError, AnhurConnectionError, AnhurQueryError, AnhurAuthError

class HTTPConnection:
    """
    An asynchronous HTTP connection wrapper for AnhurDB API.
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
        self._session: Optional[aiohttp.ClientSession] = None

    async def connect(self):
        if self._session is None:
            self._session = aiohttp.ClientSession(headers=self.headers)
            
    async def close(self):
        session = self._session
        if session is not None:
            await session.close()
            self._session = None

    async def post(self, endpoint: str, json_data: Dict[str, Any]) -> Any:
        url = f"{self.base_url}/api/v1/mcp/direct"
        session = self._session
        
        if session is None:
            raise AnhurConnectionError("Connection not established. Use 'async with Client()'")
            
        # Roteamento Translúcido: Converter endpoints REST em Chamadas de Tools MCP
        tool_name = ""
        args = {"api_key": self.api_key}
        
        if endpoint == "/v2/records":
            tool_name = "create_memory"
            args.update(json_data)
        elif endpoint == "/v2/search/ast":
            tool_name = "execute_ast"
            args.update(json_data)
        else:
            raise AnhurQueryError(f"Unsupported SDK Endpoint translated for MCP: {endpoint}")
            
        payload = {
            "tool": tool_name,
            "args": args
        }
            
        try:
            async with session.post(url, json=payload) as response:
                body_text = await response.text()
                
                if response.status in (401, 403):
                    raise AnhurAuthError(f"Authentication failed: {body_text}")
                elif response.status == 400:
                    raise AnhurQueryError(f"Invalid request ({response.status}): {body_text}")
                elif response.status >= 500:
                    raise AnhurError(f"Server error ({response.status}): {body_text}")
                    
                if not body_text:
                    return {}
                
                # Unwrap the MCP Tool Result payload (Standard format: {"content": [{"text": "{...JSON...}"}]})
                mcp_res = json.loads(body_text)
                if mcp_res.get("isError"):
                    raise AnhurQueryError(f"MCP Tool Execution Error: {mcp_res.get('error')}")
                    
                if "content" in mcp_res and len(mcp_res["content"]) > 0:
                    text_content = mcp_res["content"][0].get("text", "{}")
                    try:
                        return json.loads(text_content)
                    except json.JSONDecodeError:
                         # Return raw text if not JSON (e.g for confirmations)
                         return {"message": text_content}
                         
                return {}
                
        except aiohttp.ClientError as e:
            raise AnhurConnectionError(f"Failed to connect to MCP Gateway at {url}: {str(e)}")
