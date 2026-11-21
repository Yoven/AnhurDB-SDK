"""An in-process fake of the AnhurDB client-key surface (aiohttp).

Response shapes are copied from the AnhurDB handlers (branch
feat/phase1-client-children), not from the SDK's dataclasses, so a passing
test proves the SDK reads what the SERVER writes:

    POST/GET /children, suspend/resume          server/handler/client_children.go
    GET /tenants, GET /children/{id}/tenants     server/handler/client_keys.go (writeTenantList)
    POST /tenants, POST /children/{id}/tenants   server/handler/client_tenants.go (tenantCreatedBody)
    POST /impersonate, /children/{id}/impersonate server/handler/client_impersonate.go
    POST /impersonate/revoke                     server/handler/client_keys.go
    error envelope {"error","detail"}            server/handler/response.go (writeErrorCode)
"""

import json
from typing import Any, Callable, Dict, List, Optional

from aiohttp import web


class FakeClientServer:
    """Stateful stand-in; ``requests`` records every request on the wire."""

    def __init__(self) -> None:
        self.requests: List[Dict[str, Any]] = []
        self.child_by_ref: Dict[str, Dict[str, Any]] = {}
        self.child_status: Dict[str, str] = {}
        self.impersonate_override: Optional[Callable[[Dict[str, Any]], Dict[str, Any]]] = None
        self.static_answer: Optional[web.Response] = None
        self._runner: Optional[web.AppRunner] = None
        self.base_url = ""

    async def start(self) -> "FakeClientServer":
        app = web.Application(middlewares=[self._record])
        base = "/api/v1/client"
        app.router.add_post(f"{base}/children", self._create_child)
        app.router.add_get(f"{base}/children", self._list_children)
        app.router.add_post(f"{base}/children/{{child_id}}/suspend", self._set_status("suspended"))
        app.router.add_post(f"{base}/children/{{child_id}}/resume", self._set_status("active"))
        app.router.add_get(f"{base}/children/{{child_id}}/tenants", self._list_tenants)
        app.router.add_get(f"{base}/tenants", self._list_tenants)
        app.router.add_post(f"{base}/children/{{child_id}}/tenants", self._create_tenant)
        app.router.add_post(f"{base}/tenants", self._create_tenant)
        app.router.add_post(f"{base}/children/{{child_id}}/impersonate", self._impersonate)
        app.router.add_post(f"{base}/impersonate", self._impersonate)
        app.router.add_post(f"{base}/impersonate/revoke", self._revoke)
        self._runner = web.AppRunner(app)
        await self._runner.setup()
        site = web.TCPSite(self._runner, "127.0.0.1", 0)
        await site.start()
        port = site._server.sockets[0].getsockname()[1]  # type: ignore[union-attr]
        self.base_url = f"http://127.0.0.1:{port}"
        return self

    async def stop(self) -> None:
        if self._runner is not None:
            await self._runner.cleanup()

    @web.middleware
    async def _record(self, request: web.Request, handler: Any) -> web.StreamResponse:
        raw_body = await request.text()
        try:
            decoded = json.loads(raw_body) if raw_body else {}
        except ValueError:
            decoded = {}
        self.requests.append({
            "method": request.method, "path": request.path, "raw_body": raw_body, "body": decoded,
            "api_key": request.headers.get("X-API-Key"), "has_tenant_header": "X-Tenant-ID" in request.headers,
        })
        if self.static_answer is not None:
            return self.static_answer
        return await handler(request)

    async def _create_child(self, request: web.Request) -> web.Response:
        ref = (await request.json())["external_ref"]
        if ref in self.child_by_ref:
            summary = dict(self.child_by_ref[ref], status=self.child_status[self.child_by_ref[ref]["child_client_id"]])
            return web.json_response(dict(summary, created=False), status=200)
        child_id = f"9b1d2c3e-4f5a-4b6c-8d7e-{len(self.child_by_ref) + 1:012d}"
        summary = {"child_client_id": child_id, "external_ref": ref, "status": "active",
                   "created_at": "2026-11-21T12:00:00Z"}
        self.child_by_ref[ref] = summary
        self.child_status[child_id] = "active"
        return web.json_response(dict(summary, created=True), status=201)

    async def _list_children(self, request: web.Request) -> web.Response:
        return web.json_response({"children": list(self.child_by_ref.values())})

    def _set_status(self, status: str) -> Callable[[web.Request], Any]:
        async def handle(request: web.Request) -> web.Response:
            child_id = request.match_info["child_id"]
            if child_id not in self.child_status:
                return web.json_response({"error": "child_not_found",
                                          "detail": "no child client with this id for the caller"}, status=404)
            self.child_status[child_id] = status
            return web.json_response({"child_client_id": child_id, "status": status})
        return handle

    async def _list_tenants(self, request: web.Request) -> web.Response:
        return web.json_response({"tenants": [{"tenant_id": "contabilidade"}, {"tenant_id": "geral"}]})

    async def _create_tenant(self, request: web.Request) -> web.Response:
        body = await request.json()
        owner = request.match_info.get("child_id", "parent-client")
        answer: Dict[str, Any] = {"client_id": owner, "tenant_id": body["tenant_id"],
                                  "composite_tenant": f"{owner}_{body['tenant_id']}", "created": True}
        max_rpm = (body.get("profile") or {}).get("max_rpm")
        if max_rpm is not None:
            answer["max_rpm"] = min(max_rpm, 1200)
        return web.json_response(answer, status=201)

    async def _impersonate(self, request: web.Request) -> web.Response:
        body = await request.json()
        if self.impersonate_override is not None:
            return web.json_response(self.impersonate_override(body), status=201)
        owner = request.match_info.get("child_id", "parent-client")
        return web.json_response({
            "api_key": "anhur_secret", "key_id": "ik_42", "client_id": owner, "tenant_id": body["tenant_id"],
            "composite_tenant": f"{owner}_{body['tenant_id']}", "scope": body.get("scope") or "admin",
            "l1_only": body.get("l1_only") is True, "kind": "impersonation", "expires_in": 900,
            "expires_at": "2026-11-21T13:00:00Z",
        }, status=201)

    async def _revoke(self, request: web.Request) -> web.Response:
        key_id = (await request.json())["key_id"]
        if key_id != "ik_42":
            return web.json_response({"error": "key_not_found",
                                      "detail": "no impersonation key with this key_id"}, status=404)
        return web.json_response({"revoked": True, "key_id": key_id})
