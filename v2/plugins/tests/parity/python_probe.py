#!/usr/bin/env python3
"""Python arm of the cross-language parity harness.

Runs every shared scenario against the REAL Hermes memory provider modules
(``hermes-agent/config.py``, ``memory_queue.py``, ``transcript.py``) and writes a
language-neutral observation file. It never asserts: the comparator
(``compare_parity.py``) owns the verdict, so a divergence is reported once, by
one component.

Usage (normally called by ``run_parity.sh``)::

    python3 python_probe.py <scenarios_dir> <output.json>

Standard library only — see the Junior Tip at the top of ``hermes-agent/config.py``
for why these plugin modules must stay import-light, and why the harness must not
add a dependency the plugin itself refuses to have.
"""

from __future__ import annotations

import hashlib
import importlib.machinery
import importlib.util
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

# The item id is embedded in every queued body so an observation can name WHICH
# item was attempted, persisted or quarantined without depending on either
# implementation's on-disk format.
PARITY_BODY_MARKER = "BODY-"

HARNESS_DIR = Path(__file__).resolve().parent
PLUGIN_DIR = HARNESS_DIR.parent.parent / "hermes-agent"


def load_plugin_modules() -> Dict[str, Any]:
    """Load the plugin the way the HOST loads it, and return its submodules.

    Junior Tip [why not just import — 2026-07-30]: the plugin directory name
    contains a hyphen and lives outside ``sys.path``, and Hermes loads it with
    ``importlib.util.spec_from_file_location`` under a synthetic parent package
    (``plugins/memory/__init__.py:_load_provider_from_dir``). A harness that
    imported it some other way would prove something the host never does — and
    "tested one way, executed another" is precisely how the previous plugin sat
    in the wrong format for weeks. This mirrors
    ``hermes-agent/tests/conftest.py:_load_plugin_package``; it is duplicated on
    purpose so the parity harness needs nothing but the standard library (that
    conftest imports pytest). Both copies fail LOUD, unlike the host, which
    swallows a submodule error into ``logger.debug``.
    """
    if not PLUGIN_DIR.is_dir():
        raise SystemExit(f"plugin directory not found: {PLUGIN_DIR}")

    namespace = "_parity_hermes_memory"
    package_name = f"{namespace}.anhurdb"

    if namespace not in sys.modules:
        namespace_spec = importlib.machinery.ModuleSpec(namespace, None, is_package=True)
        namespace_spec.submodule_search_locations = []
        sys.modules[namespace] = importlib.util.module_from_spec(namespace_spec)

    package_spec = importlib.util.spec_from_file_location(
        package_name,
        str(PLUGIN_DIR / "__init__.py"),
        submodule_search_locations=[str(PLUGIN_DIR)],
    )
    if package_spec is None or package_spec.loader is None:
        raise SystemExit(f"cannot build a module spec for {PLUGIN_DIR}")
    package_module = importlib.util.module_from_spec(package_spec)
    sys.modules[package_name] = package_module

    for submodule_file in sorted(PLUGIN_DIR.glob("*.py")):
        if submodule_file.name == "__init__.py":
            continue
        submodule_name = f"{package_name}.{submodule_file.stem}"
        submodule_spec = importlib.util.spec_from_file_location(
            submodule_name, str(submodule_file)
        )
        if submodule_spec is None or submodule_spec.loader is None:
            raise SystemExit(f"cannot build a module spec for {submodule_file}")
        submodule = importlib.util.module_from_spec(submodule_spec)
        sys.modules[submodule_name] = submodule
        submodule_spec.loader.exec_module(submodule)

    package_spec.loader.exec_module(package_module)
    return {
        short_name: sys.modules[f"{package_name}.{short_name}"]
        for short_name in ("config", "memory_queue", "queue_store", "transcript")
    }


# ── shared helpers ───────────────────────────────────────────────────────────


def parity_fingerprint(secret_value: str) -> str:
    """sha256(value)[:12] — proves WHICH secret resolved, never reveals it.

    Junior Tip [a harness that CAN print a key WILL print one]: observation
    files get printed, diffed and pasted into reports, and somebody will
    eventually point this harness at a real env file. Comparing fingerprints
    keeps that mistake harmless.
    """
    if not secret_value:
        return ""
    return hashlib.sha256(secret_value.encode("utf-8")).hexdigest()[:12]


def parity_digest(payload: str) -> str:
    """Full sha256 of a payload — the proof that chunking lost nothing."""
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def parity_portable_path(absolute_path: Path, scenario_root: Path) -> str:
    """Rewrite an absolute path into something comparable across machines."""
    absolute_text = str(absolute_path)
    root_text = str(scenario_root)
    if absolute_text == root_text:
        return "~"
    if absolute_text.startswith(root_text + os.sep):
        return "~/" + absolute_text[len(root_text) + 1 :]
    return absolute_text


def parity_item_id(content: str) -> str:
    """Recover the item id embedded in a queued body (``BODY-<id> ...``)."""
    marker_index = content.find(PARITY_BODY_MARKER)
    if marker_index < 0:
        return "unknown"
    rest = content[marker_index + len(PARITY_BODY_MARKER) :]
    separator_positions = [
        position
        for position in (rest.find(separator) for separator in (" ", "\n", "\t"))
        if position >= 0
    ]
    if separator_positions:
        return rest[: min(separator_positions)]
    return rest


def parity_queue_content(item: Dict[str, str]) -> str:
    """Render a queued unit — byte-identical to what the Go arm writes.

    Junior Tip [same bytes, different session channel]: the Go plugin can only
    prove a queued chunk's owner from the ``Claude Code session <id>`` header
    inside the text, while the Python queue carries the session in the envelope
    field and ignores the header. Writing the SAME bytes on both sides is what
    makes ``remaining_bodies_intact`` a like-for-like comparison instead of a
    formatting opinion.
    """
    body = PARITY_BODY_MARKER + item["id"] + " " + item.get("body", "")
    if not item.get("session"):
        return body
    return (
        "Claude Code session "
        + item["session"]
        + " — conversation excerpt (2026-07-30T00:00:00Z):\n"
        + body
    )


# ── rule 1: configuration loading ────────────────────────────────────────────


def observe_config(modules: Dict[str, Any], scenario_root: Path, given: Dict[str, Any]) -> Dict[str, Any]:
    """Materialise the env file, resolve the config, report what came out."""
    config_module = modules["config"]
    default_state_directory = scenario_root / ".anhur-hermes-memory"

    process_environment: Dict[str, str] = dict(given.get("process_env") or {})

    location = given.get("env_file_location")
    if location == "state_dir":
        env_file_path: Optional[Path] = default_state_directory / config_module.ENV_FILE_NAME
    elif location == "explicit":
        env_file_path = scenario_root / "elsewhere" / "anhur.env"
        process_environment["ANHUR_ENV_FILE"] = str(env_file_path)
    elif location == "none":
        env_file_path = None
    else:
        raise SystemExit(f"unknown env_file_location {location!r} (want state_dir|explicit|none)")

    if env_file_path is not None:
        env_file_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        env_file_path.write_text(
            "\n".join(given.get("env_file_lines") or []) + "\n", encoding="utf-8"
        )
        os.chmod(env_file_path, 0o600)

    # ``Path.home()`` reads the REAL process environment, not the mapping passed
    # to load_plugin_config, so HOME has to be redirected here as well — without
    # it the probe would resolve the operator's live ~/.anhur-hermes-memory.
    environment = {"HOME": str(scenario_root), **process_environment}
    with redirected_home(scenario_root):
        loaded = config_module.load_plugin_config(environment=environment)

    return {
        "api_key_source": loaded.key_source,
        "api_key_fingerprint": parity_fingerprint(loaded.api_key),
        "url": loaded.url,
        "container": loaded.container,
        "state_dir": parity_portable_path(loaded.state_dir, scenario_root),
        "vars_loaded": loaded.env_file_variable_count,
        "env_file_error": bool(loaded.env_file_error),
    }


class redirected_home:  # noqa: N801 — a context manager reads better lowercase here
    """Point ``$HOME`` at the scenario root for the duration of a block."""

    def __init__(self, home_directory: Path) -> None:
        self._home_directory = str(home_directory)
        self._saved_home: Optional[str] = None

    def __enter__(self) -> "redirected_home":
        self._saved_home = os.environ.get("HOME")
        os.environ["HOME"] = self._home_directory
        return self

    def __exit__(self, *exception_details: Any) -> None:
        if self._saved_home is None:
            os.environ.pop("HOME", None)
        else:
            os.environ["HOME"] = self._saved_home


# ── rule 2: the on-disk queue ────────────────────────────────────────────────


def observe_queue(modules: Dict[str, Any], scenario_root: Path, given: Dict[str, Any]) -> Dict[str, Any]:
    """Seed the queue, drain it against a scripted backend, report the effect."""
    queue_module = modules["memory_queue"]
    queue_directory = scenario_root / "queue"
    queue_directory.mkdir(parents=True, exist_ok=True, mode=0o700)

    items: List[Dict[str, str]] = list(given.get("items") or [])
    expected_bodies: Dict[str, str] = {}
    for item_index, item in enumerate(items):
        content = parity_queue_content(item)
        expected_bodies[item["id"]] = content
        envelope = {
            "version": queue_module.QUEUE_ENVELOPE_VERSION,
            "session_id": item.get("session", ""),
            "content": content,
            "queued_at": "2026-07-30T00:00:00Z",
        }
        # The stamp prefix is what both implementations sort by, so the file
        # names decide the drain order — exactly as in production.
        file_name = f"20260730T0000{item_index:02d}.000000-4711-{item_index:06d}.json"
        (queue_directory / file_name).write_text(
            json.dumps(envelope, ensure_ascii=False), encoding="utf-8"
        )

    # Junior Tip [a sonda tem de olhar onde a REGRA vive, 2026-07-31]: até a fila
    # com estado, "ainda pendente" era um arquivo em queue/*.json. Depois dela é uma
    # linha com state='pending'. Uma sonda deixada no DurableTurnQueue mediria o PISO
    # — o caminho que só roda quando o SQLite não abre — e daria "ok" sobre um
    # mecanismo que o produto não usa mais. O StateQueue carrega o piso dentro de si,
    # então esta linha exercita exatamente o que o Hermes executa.
    state_queue_module = modules["queue_store"]
    queue = state_queue_module.StateQueue(queue_dir=queue_directory)
    transport = str(given.get("transport") or "ok")

    attempts: List[str] = []
    persisted: List[str] = []

    def send_turn(session_id: str, content: str) -> None:
        item_id = parity_item_id(content)
        attempts.append(f"{item_id}@{session_id}")
        if transport == "down":
            raise RuntimeError("parity scenario: write rejected")
        if transport.startswith("reject:") and item_id == transport.split(":", 1)[1]:
            raise RuntimeError("parity scenario: write rejected")
        persisted.append(f"{item_id}@{session_id}")

    queue.drain(send_turn)
    first_attempts = list(attempts)
    first_persisted = list(persisted)

    second_drain_attempted: List[str] = []
    if given.get("drain_twice"):
        queue.drain(send_turn)
        second_drain_attempted = attempts[len(first_attempts) :]

    # Quando o banco não abre, a fila cai para os arquivos: a sonda soma os dois
    # lugares em vez de escolher um, senão acusaria perda onde houve fallback.
    pending_ids, pending_intact = scan_state_queue(queue.list_pending(), expected_bodies)
    file_pending_ids, file_pending_intact = scan_queue_directory(
        queue_directory, expected_bodies
    )
    pending_ids += file_pending_ids
    pending_intact = pending_intact and file_pending_intact

    quarantined_ids, quarantine_intact = scan_state_queue(
        queue.list_quarantined(), expected_bodies, content_index=1
    )
    file_quarantined_ids, file_quarantine_intact = scan_queue_directory(
        queue.quarantine_dir, expected_bodies
    )
    quarantined_ids += file_quarantined_ids
    quarantine_intact = quarantine_intact and file_quarantine_intact

    accounted_for = {label.split("@", 1)[0] for label in persisted}
    accounted_for.update(pending_ids)
    accounted_for.update(quarantined_ids)

    return {
        "attempted": first_attempts,
        # Only the FIRST drain, so the field means the same thing on both arms:
        # what one drain delivered. The second drain is reported separately, and
        # accounting above uses everything that ever landed.
        "persisted": first_persisted,
        "pending_after": pending_ids,
        "quarantined_after": quarantined_ids,
        "second_drain_attempted": second_drain_attempted,
        "every_item_accounted_for": len(accounted_for) == len(items),
        "remaining_bodies_intact": pending_intact and quarantine_intact,
    }



def scan_state_queue(rows, expected_bodies, content_index: int = 2):
    """Ids na ordem da fila + se todo corpo restante ainda tem os bytes originais.

    ``content_index`` difere porque ``list_pending`` devolve
    ``(id, session_id, content, retry_count)`` e ``list_quarantined`` devolve
    ``(id, content, last_error, created_at)`` — as duas formas são as que o
    operador precisa em cada caso, e a sonda se adapta a elas em vez de forçar
    uma forma única que serviria mal aos dois.
    """
    item_ids = []
    bodies_intact = True
    for row in rows:
        content = row[content_index]
        item_id = parity_item_id(content)
        item_ids.append(item_id)
        if expected_bodies.get(item_id) != content:
            bodies_intact = False
    return item_ids, bodies_intact

def scan_queue_directory(
    directory: Path, expected_bodies: Dict[str, str]
) -> Tuple[List[str], bool]:
    """List item ids still in a directory (queue order) and whether they are intact."""
    item_ids: List[str] = []
    bodies_intact = True
    if not directory.is_dir():
        return item_ids, bodies_intact
    for entry in sorted(directory.iterdir()):
        if not entry.is_file() or entry.suffix != ".json":
            continue
        raw_text = entry.read_text(encoding="utf-8")
        try:
            content = str(json.loads(raw_text).get("content", ""))
        except ValueError:
            content = raw_text
        item_id = parity_item_id(content)
        item_ids.append(item_id)
        if expected_bodies.get(item_id) != content:
            bodies_intact = False
    return item_ids, bodies_intact


# ── rule 3: chunking ─────────────────────────────────────────────────────────


def observe_chunk(modules: Dict[str, Any], fixtures_dir: Path, given: Dict[str, Any]) -> Dict[str, Any]:
    """Split a shared fixture and report the no-loss properties."""
    transcript_module = modules["transcript"]
    fixture_path = fixtures_dir / str(given["fixture"])
    input_text = fixture_path.read_text(encoding="utf-8")
    max_characters = int(given["max_chars"])

    chunks = transcript_module.split_into_chunks(input_text, max_characters)
    # len() on a Python str counts CODE POINTS; the Go arm measures bytes. The
    # scenario files say which of these fields may be compared across languages.
    largest_chunk = max((len(chunk) for chunk in chunks), default=0)
    rejoined = "".join(chunks)

    return {
        "chunk_count": len(chunks),
        "max_chunk_size": largest_chunk,
        "all_within_limit": all(len(chunk) <= max_characters for chunk in chunks),
        "rejoin_equals_input": rejoined == input_text,
        "rejoined_sha256": parity_digest(rejoined),
        "input_sha256": parity_digest(input_text),
    }


# ── driver ───────────────────────────────────────────────────────────────────


def main(argv: List[str]) -> int:
    if len(argv) != 3:
        print(f"usage: {argv[0]} <scenarios_dir> <output.json>", file=sys.stderr)
        return 2
    scenarios_dir = Path(argv[1]).resolve()
    output_path = Path(argv[2]).resolve()
    fixtures_dir = scenarios_dir.parent / "fixtures"

    modules = load_plugin_modules()
    known_rules = ("config", "queue", "chunk")

    observations: Dict[str, Any] = {}
    scenario_files = sorted(scenarios_dir.glob("*.json"))
    if not scenario_files:
        print(f"no scenarios found in {scenarios_dir}", file=sys.stderr)
        return 2

    for scenario_file in scenario_files:
        scenario = json.loads(scenario_file.read_text(encoding="utf-8"))
        name = scenario.get("name") or ""
        rule = scenario.get("rule") or ""
        if not name:
            raise SystemExit(f"scenario {scenario_file} has no name")
        if rule not in known_rules:
            raise SystemExit(f"scenario {name} has unknown rule {rule!r} (want config|queue|chunk)")
        given = scenario.get("given")
        if not isinstance(given, dict):
            raise SystemExit(f"scenario {name} has no usable `given` block")

        # One temp root per scenario: state dir, env file and queue all live
        # under it, and it doubles as HOME so a scenario can never touch the
        # operator's real ~/.anhur-*-memory.
        with tempfile.TemporaryDirectory(prefix="anhur-parity-py-") as temporary_root:
            scenario_root = Path(temporary_root)
            if rule == "chunk":
                observations[name] = observe_chunk(modules, fixtures_dir, given)
            elif rule == "config":
                observations[name] = observe_config(modules, scenario_root, given)
            else:
                observations[name] = observe_queue(modules, scenario_root, given)

    document = {
        "implementation": "python",
        "source": "AnhurDB-SDK/v2/plugins/hermes-agent (Hermes Agent memory provider)",
        "scenarios": observations,
    }
    output_path.write_text(json.dumps(document, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"parity probe: wrote {len(observations)} scenario observations to {output_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
