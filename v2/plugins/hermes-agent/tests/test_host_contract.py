"""Contract test against a REAL installed Hermes Agent, in ITS OWN venv.

Everything else in this suite reproduces the host's loader; this one uses the
host itself — its discovery heuristic, its ``_load_provider_from_dir``, its
``MemoryProvider`` ABC, and its ``MemoryManager`` routing table.

Junior Tip [por que subprocess com o python do Hermes, e não ``sys.path``]: o
runtime do Hermes tem dependências próprias que não existem no venv do SDK, e
importá-lo dentro do pytest contaminaria ``sys.modules`` para os outros testes
(``_running_inside_hermes()`` passaria a responder True e mudaria o
comportamento do provider). Um subprocesso isolado prova o contrato sem
alterar o processo de teste.

The test SKIPS when no Hermes install is present, so the suite stays portable.
It never writes to the operator's real Hermes home or AnhurDB state dir: both
are redirected to a temp directory.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

PLUGIN_DIR = Path(__file__).resolve().parent.parent

_HOST_PROBE_SOURCE = '''
import json, sys
from pathlib import Path

sys.path.insert(0, HERMES_AGENT_DIR)

from agent.memory_provider import MemoryProvider
from agent.memory_manager import MemoryManager
import plugins.memory as memory_discovery

plugin_dir = Path(PLUGIN_DIR)
report = {}
report["is_memory_provider_dir"] = memory_discovery._is_memory_provider_dir(plugin_dir)

provider = memory_discovery._load_provider_from_dir(plugin_dir)
report["loaded"] = provider is not None
report["is_memory_provider_subclass"] = isinstance(provider, MemoryProvider)
report["name"] = provider.name
report["tool_schemas"] = [schema["name"] for schema in provider.get_tool_schemas()]
report["is_available"] = provider.is_available()
report["availability_reason"] = provider.availability_reason
report["tool_call_is_json"] = provider.handle_tool_call("anhurdb_recall", {})

manager = MemoryManager()
manager.add_provider(provider)
report["manager_tools"] = sorted(manager.get_all_tool_names())
report["manager_schemas"] = [s["name"] for s in manager.get_all_tool_schemas()]

print("PROBE_JSON:" + json.dumps(report))
'''


_LIVE_PROBE_SOURCE = '''
import json, sys
from pathlib import Path

sys.path.insert(0, HERMES_AGENT_DIR)

from agent.memory_manager import MemoryManager
import plugins.memory as memory_discovery

provider = memory_discovery._load_provider_from_dir(Path(PLUGIN_DIR))
manager = MemoryManager()
manager.add_provider(provider)

report = {"available": provider.is_available()}
manager.initialize_all(session_id=SESSION_ID, platform="cli")
report["session_id"] = provider.session_id
report["system_prompt"] = manager.build_system_prompt()

manager.sync_all("what did we decide about naming?", "explicit names, always")
manager.flush_pending(timeout=30)

report["prefetch"] = manager.prefetch_all("naming style")
report["tool"] = manager.handle_tool_call("anhurdb_recall", {"query": "naming"})
provider.shutdown()

print("PROBE_JSON:" + json.dumps(report))
'''


def _hermes_agent_dir() -> Path | None:
    candidates = [
        os.environ.get("HERMES_AGENT_DIR", ""),
        str(Path.home() / ".hermes" / "hermes-agent"),
    ]
    for candidate in candidates:
        if not candidate:
            continue
        candidate_path = Path(candidate)
        if (candidate_path / "agent" / "memory_provider.py").exists():
            return candidate_path
    return None


def _hermes_python(hermes_agent_dir: Path) -> str:
    venv_python = hermes_agent_dir / "venv" / "bin" / "python"
    return str(venv_python) if venv_python.exists() else sys.executable


def test_real_hermes_runtime_accepts_this_plugin(tmp_path):
    hermes_agent_dir = _hermes_agent_dir()
    if hermes_agent_dir is None:
        pytest.skip("no Hermes Agent installation found (set HERMES_AGENT_DIR)")

    probe_script = tmp_path / "host_probe.py"
    probe_script.write_text(
        f"HERMES_AGENT_DIR = {str(hermes_agent_dir)!r}\n"
        f"PLUGIN_DIR = {str(PLUGIN_DIR)!r}\n" + _HOST_PROBE_SOURCE,
        encoding="utf-8",
    )

    # Redirect BOTH homes into tmp so the probe cannot touch the operator's
    # real Hermes profile or AnhurDB state directory.
    probe_environment = dict(os.environ)
    probe_environment.update(
        {
            "HERMES_HOME": str(tmp_path / "hermes-home"),
            "ANHUR_STATE_DIR": str(tmp_path / "anhur-state"),
            "ANHUR_ENV_FILE": str(tmp_path / "anhur-state" / "env"),
            "ANHUR_API_KEY": "",
        }
    )
    (tmp_path / "hermes-home").mkdir()

    completed = subprocess.run(
        [_hermes_python(hermes_agent_dir), str(probe_script)],
        capture_output=True,
        text=True,
        timeout=120,
        env=probe_environment,
    )
    probe_lines = [
        line for line in completed.stdout.splitlines() if line.startswith("PROBE_JSON:")
    ]
    assert probe_lines, (
        f"probe produced no report\nstdout:\n{completed.stdout}\n"
        f"stderr:\n{completed.stderr}"
    )
    report = json.loads(probe_lines[0][len("PROBE_JSON:") :])

    # The host's own text heuristic must classify this directory correctly,
    # or the plugin never even appears in `hermes memory setup`.
    assert report["is_memory_provider_dir"] is True
    # The host's loader must find the provider through register(ctx).
    assert report["loaded"] is True
    assert report["is_memory_provider_subclass"] is True
    assert report["name"] == "anhurdb"
    assert report["tool_schemas"] == ["anhurdb_recall", "anhurdb_search"]
    # The MemoryManager must accept and route both tools (no core-name clash).
    assert report["manager_tools"] == ["anhurdb_recall", "anhurdb_search"]
    assert report["manager_schemas"] == ["anhurdb_recall", "anhurdb_search"]
    # A tool call answers with JSON even when the backend is unconfigured.
    json.loads(report["tool_call_is_json"])
    # With no key configured the provider must be honestly unavailable AND say why.
    assert report["is_available"] is False
    assert report["availability_reason"]


def test_real_hermes_runtime_persists_a_turn_end_to_end(tmp_path, fake_anhurdb):
    """Full stack: real host + real SDK + real HTTP → fake AnhurDB.

    Nothing is installed into the operator's Hermes venv: the SDK is made
    importable with ``PYTHONPATH`` for the duration of one subprocess.
    """
    hermes_agent_dir = _hermes_agent_dir()
    if hermes_agent_dir is None:
        pytest.skip("no Hermes Agent installation found (set HERMES_AGENT_DIR)")
    sdk_dir = PLUGIN_DIR.parent.parent / "python"
    if not (sdk_dir / "anhurdb" / "__init__.py").exists():
        pytest.skip(f"AnhurDB Python SDK not found at {sdk_dir}")

    state, base_url = fake_anhurdb
    state.search_results = [
        {
            "record": {"id": 3, "type": "preference", "summary": "explicit names"},
            "similarity": 0.77,
        }
    ]
    session_id = "20260730_205500_hostprobe"

    probe_script = tmp_path / "live_probe.py"
    probe_script.write_text(
        f"HERMES_AGENT_DIR = {str(hermes_agent_dir)!r}\n"
        f"PLUGIN_DIR = {str(PLUGIN_DIR)!r}\n"
        f"SESSION_ID = {session_id!r}\n" + _LIVE_PROBE_SOURCE,
        encoding="utf-8",
    )

    probe_environment = dict(os.environ)
    probe_environment.update(
        {
            "PYTHONPATH": str(sdk_dir),
            "HERMES_HOME": str(tmp_path / "hermes-home"),
            "ANHUR_STATE_DIR": str(tmp_path / "anhur-state"),
            "ANHUR_ENV_FILE": str(tmp_path / "anhur-state" / "env"),
            "ANHUR_API_KEY": "anhur_probe_key",
            "ANHUR_URL": base_url,
            "ANHUR_CONTAINER": "hermes-probe",
            "ANHUR_HTTP_TIMEOUT": "10",
            "ANHUR_PREFETCH_TIMEOUT": "10",
        }
    )
    (tmp_path / "hermes-home").mkdir()

    completed = subprocess.run(
        [_hermes_python(hermes_agent_dir), str(probe_script)],
        capture_output=True,
        text=True,
        timeout=180,
        env=probe_environment,
    )
    probe_lines = [
        line for line in completed.stdout.splitlines() if line.startswith("PROBE_JSON:")
    ]
    assert probe_lines, (
        f"probe produced no report\nstdout:\n{completed.stdout}\n"
        f"stderr:\n{completed.stderr}"
    )
    report = json.loads(probe_lines[0][len("PROBE_JSON:") :])

    assert report["available"] is True
    # 1 Hermes session = 1 AnhurDB session, registered verbatim.
    assert report["session_id"] == session_id
    assert state.created_sessions == [session_id]
    # The turn reached AnhurDB through the host's own sync fan-out.
    persisted = state.ingested_for(session_id)
    assert len(persisted) == 1
    assert "USER: what did we decide about naming?" in persisted[0]
    assert "ASSISTANT: explicit names, always" in persisted[0]
    # Reads work through the host too.
    assert "<anhur-memory" in report["system_prompt"]
    assert "explicit names" in report["prefetch"]
    assert json.loads(report["tool"])["count"] == 1
