"""Test harness: loads the plugin EXACTLY the way the Hermes Agent loads it.

Junior Tip [por que o teste replica o loader do host em vez de dar
``import``]: o diretório do plugin não é um pacote importável (o nome contém
hífen e ele vive fora do ``sys.path``), e o Hermes o carrega por
``importlib.util.spec_from_file_location`` com um pacote-pai sintético
(``plugins/memory/__init__.py:_load_provider_from_dir``). Se os testes
importassem de outro jeito, eles provariam algo que o host nunca faz — foi
exatamente esse tipo de "testado de um jeito, executado de outro" que deixou o
plugin anterior no formato errado por semanas. Aqui há uma diferença
DELIBERADA: o host engole falha de submódulo em ``logger.debug``; este harness
FALHA. Um ``ImportError`` no plugin tem de quebrar o teste, não sumir.
"""

from __future__ import annotations

import importlib.machinery
import importlib.util
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Dict, List, Optional

import pytest

PLUGIN_DIR = Path(__file__).resolve().parent.parent
USER_NAMESPACE = "_hermes_user_memory"
PLUGIN_PACKAGE_NAME = f"{USER_NAMESPACE}.anhurdb"


def _load_plugin_package():
    """Reproduce ``_load_provider_from_dir`` (minus the error swallowing)."""
    if PLUGIN_PACKAGE_NAME in sys.modules:
        return sys.modules[PLUGIN_PACKAGE_NAME]

    if USER_NAMESPACE not in sys.modules:
        namespace_spec = importlib.machinery.ModuleSpec(
            USER_NAMESPACE, None, is_package=True
        )
        namespace_spec.submodule_search_locations = []
        sys.modules[USER_NAMESPACE] = importlib.util.module_from_spec(namespace_spec)

    package_spec = importlib.util.spec_from_file_location(
        PLUGIN_PACKAGE_NAME,
        str(PLUGIN_DIR / "__init__.py"),
        submodule_search_locations=[str(PLUGIN_DIR)],
    )
    assert package_spec is not None and package_spec.loader is not None
    package_module = importlib.util.module_from_spec(package_spec)
    sys.modules[PLUGIN_PACKAGE_NAME] = package_module

    # The host pre-execs every sibling module before __init__.py; do the same,
    # but let failures surface.
    for submodule_file in sorted(PLUGIN_DIR.glob("*.py")):
        if submodule_file.name == "__init__.py":
            continue
        submodule_name = f"{PLUGIN_PACKAGE_NAME}.{submodule_file.stem}"
        if submodule_name in sys.modules:
            continue
        submodule_spec = importlib.util.spec_from_file_location(
            submodule_name, str(submodule_file)
        )
        assert submodule_spec is not None and submodule_spec.loader is not None
        submodule = importlib.util.module_from_spec(submodule_spec)
        sys.modules[submodule_name] = submodule
        submodule_spec.loader.exec_module(submodule)

    package_spec.loader.exec_module(package_module)
    return package_module


@pytest.fixture(scope="session")
def plugin():
    """The loaded plugin package (``__init__``)."""
    return _load_plugin_package()


@pytest.fixture(scope="session")
def plugin_modules(plugin):
    """Submodules, by short name."""
    return {
        short_name: sys.modules[f"{PLUGIN_PACKAGE_NAME}.{short_name}"]
        for short_name in (
            "config",
            "memory_queue",
            "provider",
            "schemas",
            "sdk_runtime",
            "tools",
            "transcript",
        )
    }


# ---------------------------------------------------------------------------
# Fake AnhurDB server
# ---------------------------------------------------------------------------


class FakeAnhurDBState:
    """Recorded requests + injectable failures for one fake server."""

    def __init__(self) -> None:
        self.created_sessions: List[str] = []
        self.ingested: List[Dict[str, str]] = []
        self.failing_paths: set = set()
        self.search_results: List[Dict[str, Any]] = []
        self.profile_payload: Dict[str, Any] = {
            "static": {"facts": ["the user ships sovereign memory"]},
            "dynamic": {"recent_topics": ["hermes plugin"]},
            "stats": {"total_records": 42, "sessions": 3},
        }
        self.request_log: List[str] = []

    def ingested_for(self, session_id: str) -> List[str]:
        return [
            entry["content"]
            for entry in self.ingested
            if entry["session_id"] == session_id
        ]


def _make_handler(state: FakeAnhurDBState):
    class FakeAnhurDBHandler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *args: Any) -> None:  # silence stderr noise
            return

        def _read_json_body(self) -> Dict[str, Any]:
            content_length = int(self.headers.get("Content-Length") or 0)
            if content_length <= 0:
                return {}
            raw_body = self.rfile.read(content_length)
            try:
                return json.loads(raw_body.decode("utf-8"))
            except ValueError:
                return {}

        def _respond(self, status_code: int, payload: Any) -> None:
            body = json.dumps(payload).encode("utf-8")
            self.send_response(status_code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self) -> None:  # noqa: N802 (stdlib naming)
            path = self.path.split("?")[0]
            state.request_log.append(f"POST {path}")
            body = self._read_json_body()

            if path in state.failing_paths:
                self._respond(500, {"error": "injected failure"})
                return

            if path == "/api/v1/sessions":
                session_id = str(body.get("session_id") or "server-generated")
                state.created_sessions.append(session_id)
                self._respond(200, {"session_id": session_id})
                return

            if path == "/api/v1/ingest":
                session_id = str(body.get("session_id") or "")
                content = str(body.get("content") or "")
                state.ingested.append(
                    {
                        "session_id": session_id,
                        "content": content,
                        "container_tag": str(body.get("container_tag") or ""),
                    }
                )
                self._respond(
                    200,
                    {
                        "session_id": session_id,
                        "records": [
                            {"id": len(state.ingested), "type": "episodic",
                             "summary": content[:200]}
                        ],
                    },
                )
                return

            if path == "/api/v1/search":
                self._respond(
                    200,
                    {
                        "count": len(state.search_results),
                        "results": state.search_results,
                    },
                )
                return

            self._respond(404, {"error": f"no fake route for {path}"})

        def do_GET(self) -> None:  # noqa: N802 (stdlib naming)
            path = self.path.split("?")[0]
            state.request_log.append(f"GET {path}")
            if path in state.failing_paths:
                self._respond(500, {"error": "injected failure"})
                return
            if path == "/api/v1/profile":
                self._respond(200, state.profile_payload)
                return
            self._respond(404, {"error": f"no fake route for {path}"})

    return FakeAnhurDBHandler


@pytest.fixture
def fake_anhurdb():
    """Start a fake AnhurDB on a random port; yield ``(state, base_url)``."""
    state = FakeAnhurDBState()
    server = ThreadingHTTPServer(("127.0.0.1", 0), _make_handler(state))
    server_thread = threading.Thread(target=server.serve_forever, daemon=True)
    server_thread.start()
    host, port = server.server_address[0], server.server_address[1]
    try:
        yield state, f"http://{host}:{port}"
    finally:
        server.shutdown()
        server.server_close()
        server_thread.join(timeout=5)


@pytest.fixture
def make_provider(plugin, plugin_modules, tmp_path):
    """Factory for isolated providers; every instance is shut down at teardown."""
    config_module = plugin_modules["config"]
    provider_module = plugin_modules["provider"]
    created_providers = []

    def factory(
        *,
        url: str,
        api_key: str = "anhur_test_key",
        state_dir: Optional[Path] = None,
        extra_environment: Optional[Dict[str, str]] = None,
    ):
        environment = {
            "ANHUR_STATE_DIR": str(state_dir or (tmp_path / "state")),
            "ANHUR_URL": url,
            "ANHUR_CONTAINER": "hermes-test",
            "ANHUR_HTTP_TIMEOUT": "5",
            "ANHUR_PREFETCH_TIMEOUT": "5",
        }
        if api_key:
            environment["ANHUR_API_KEY"] = api_key
        if extra_environment:
            environment.update(extra_environment)
        config = config_module.load_plugin_config(environment=environment)
        provider = provider_module.AnhurDBMemoryProvider(config=config)
        created_providers.append(provider)
        return provider

    yield factory

    for provider in created_providers:
        try:
            provider.shutdown()
        except Exception:  # pragma: no cover - teardown must never mask a failure
            pass
