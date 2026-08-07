#!/usr/bin/env python3
"""End-to-end verifier for the AnhurDB memory provider inside the Hermes Agent.

Run it and read ONE line at the bottom. Everything above it is evidence.

    python3 verify.py              # full check, writes one probe turn
    python3 verify.py --no-network # local checks only (can never say WORKING)
    python3 verify.py --help

Junior Tip [por que este arquivo existe — incidente de 2026-07-30]: o plugin
irmão (Claude Code) passou 12,8 dias sem gravar NADA: 743 invocações de hook,
743 skips, exit 0, stdout e stderr vazios. Ninguém percebeu porque todo mundo
olhava STATUS ("rodou? deu 0? então está bom") em vez de EFEITO ("gravou?
voltou na leitura?"). Este verificador olha EFEITO. O código de saída existe só
para script; o veredito é a linha final, em texto.

Junior Tip [o que este verificador NÃO faz]: ele não conserta nada e não
instala nada. Cada falha imprime o comando exato do conserto e para de fingir
que está tudo bem. Um verificador que erra para o lado do "OK" é pior que
nenhum — ele transforma um problema visível num problema invisível.

Junior Tip [este arquivo é IMPORTADO pelo Hermes a cada boot — não só
executado]: ``plugins/memory/__init__.py:_load_provider_from_dir`` faz
``provider_dir.glob("*.py")`` e executa TODO irmão do ``__init__.py`` como
submódulo, inclusive este. Por isso nada aqui pode rodar em tempo de import:
todo trabalho vive dentro de funções, atrás do ``if __name__ == "__main__"``.
Um ``print`` solto ou uma chamada de rede no topo deste arquivo viraria efeito
colateral em toda inicialização do agente — e um erro no topo viraria um
submódulo meio-carregado em ``sys.modules``, que é como um plugin some sem
mensagem.
"""

from __future__ import annotations

import argparse
import hashlib
import logging
import os
import sys
import time
import uuid
from pathlib import Path
from typing import Any, List, Optional, Tuple

SOURCE_DIR = Path(__file__).resolve().parent

# Environment marker that stops the re-exec from looping forever.
REEXEC_MARKER = "ANHUR_VERIFY_REEXECED"

PASS = "PASS"
WARN = "WARN"
FAIL = "FAIL"
SKIP = "SKIP"

_STATUS_WIDTH = 5

# The verdict is one line and must stay readable in a terminal. The full text
# of every failure is already printed in the step above it.
_VERDICT_FRAGMENT_LIMIT = 150


def _shorten(text: str) -> str:
    """Trim a fragment destined for the single-line verdict."""
    collapsed = " ".join(text.split())
    if len(collapsed) <= _VERDICT_FRAGMENT_LIMIT:
        return collapsed
    return collapsed[: _VERDICT_FRAGMENT_LIMIT - 3] + "..."


class Report:
    """Collects check results and prints them as they happen.

    Junior Tip [imprimir na hora, não no fim]: se o verificador travar numa
    chamada de rede, o operador ainda precisa ver até onde chegou. Um relatório
    que só existe no final desaparece junto com o processo.
    """

    def __init__(self) -> None:
        self.failures: List[Tuple[str, str]] = []
        self.warnings: List[Tuple[str, str]] = []
        self.current_step = ""
        self.proved_write = False
        self.skipped_network = False

    def step(self, number: int, total: int, title: str) -> None:
        self.current_step = f"{number}/{total} {title}"
        print(f"\n[{number}/{total}] {title}")

    def record(self, status: str, message: str, fix: str = "") -> None:
        print(f"  {status:<{_STATUS_WIDTH}} {message}")
        for line_index, fix_line in enumerate(fix.splitlines()):
            prefix = "fix:" if line_index == 0 else "    "
            print(f"        {prefix} {fix_line}")
        if status == FAIL:
            # The verdict must stay ONE line, so it carries only the first line
            # of the fix — the full recipe is already printed right above.
            headline_fix = _shorten(fix.splitlines()[0]) if fix else ""
            one_line_message = _shorten(" ".join(message.split()))
            self.failures.append(
                (
                    self.current_step,
                    f"{one_line_message}"
                    + (f" | fix: {headline_fix}" if headline_fix else ""),
                )
            )
        elif status == WARN:
            self.warnings.append((self.current_step, message))

    def note(self, message: str) -> None:
        print(f"        {message}")


# ---------------------------------------------------------------------------
# Bootstrap: run under the interpreter Hermes itself uses
# ---------------------------------------------------------------------------

def locate_hermes_agent_dir() -> Optional[Path]:
    """Find the hermes-agent source tree (where ``plugins/memory`` lives)."""
    explicit = os.environ.get("HERMES_AGENT_DIR", "").strip()
    candidates = [Path(explicit)] if explicit else []
    candidates.append(Path.home() / ".hermes" / "hermes-agent")
    for candidate in candidates:
        if (candidate / "plugins" / "memory" / "__init__.py").exists():
            return candidate.resolve()
    return None


def ensure_hermes_interpreter(hermes_agent_dir: Path) -> None:
    """Re-exec under the Hermes venv when the current python cannot host it.

    Junior Tip [por que o INTERPRETADOR importa]: "o SDK está instalado?" só
    tem resposta dentro do python que o Hermes realmente executa. Rodar este
    script com o python do sistema responderia sobre outro ambiente — e daria
    um veredito verdadeiro sobre a máquina errada, que é uma mentira útil.

    Junior Tip [trocar de interpretador ANTES de qualquer import, sem testar se
    "já dá para importar"]: ``hermes_constants`` importa em qualquer python,
    porque só usa a stdlib. Usá-lo como prova de ambiente deixaria o script
    seguir no python do sistema e responder sobre o venv errado — foi o que
    aconteceu na primeira execução deste arquivo. A regra é dura: existe venv
    do Hermes, é nele que rodamos.
    """
    if not os.environ.get(REEXEC_MARKER):
        for relative_python in ("venv/bin/python", ".venv/bin/python",
                                "venv/Scripts/python.exe"):
            candidate_python = hermes_agent_dir / relative_python
            if not candidate_python.exists():
                continue
            if Path(sys.executable).resolve() == candidate_python.resolve():
                break
            print(f"  (re-running under the Hermes interpreter {candidate_python})")
            os.environ[REEXEC_MARKER] = "1"
            os.execv(str(candidate_python), [str(candidate_python), *sys.argv])

    sys.path.insert(0, str(hermes_agent_dir))


def import_hermes_runtime(hermes_agent_dir: Path) -> None:
    """Import the host modules the verifier depends on.

    Junior Tip [só DEPOIS de HERMES_HOME estar definido]: vários módulos do
    Hermes chamam ``get_hermes_home()`` em tempo de import. Com a variável
    ainda vazia e um perfil sticky ativo, o host imprime um aviso de "fallback
    para o perfil DEFAULT" que contradiz o que este verificador acabou de
    resolver — dois números na tela, um deles errado. Importar depois deixa o
    host concordar com a gente.
    """
    try:
        import agent.memory_provider  # noqa: F401
        import hermes_cli.config  # noqa: F401
        import plugins.memory  # noqa: F401
    except Exception as import_error:
        print(
            f"FATAL: {sys.executable} cannot import the Hermes runtime from "
            f"{hermes_agent_dir} ({type(import_error).__name__}: {import_error}). "
            "Run this script with the python that runs `hermes`, or set "
            "HERMES_AGENT_DIR.",
            file=sys.stderr,
        )
        sys.exit(2)


def resolve_effective_hermes_home(report: Report) -> Path:
    """Resolve HERMES_HOME exactly the way the ``hermes`` CLI does.

    Junior Tip [perfis são HOMEs independentes — a armadilha nº 1 aqui]: um
    ``active_profile`` diferente de ``default`` faz o CLI apontar HERMES_HOME
    para ``<root>/profiles/<nome>``. Plugin instalado em ``~/.hermes/plugins``
    com o perfil ``architect`` ativo NUNCA é descoberto — e o Hermes não
    reclama, porque para ele o diretório simplesmente não existe. Réplica fiel
    de ``hermes_cli/main.py:_apply_profile_override``.
    """
    from hermes_constants import get_default_hermes_root

    default_root = get_default_hermes_root()
    environment_home = os.environ.get("HERMES_HOME", "").strip()

    if environment_home and Path(environment_home).parent.name == "profiles":
        return Path(environment_home)

    active_profile = ""
    active_profile_path = default_root / "active_profile"
    try:
        if active_profile_path.exists():
            active_profile = active_profile_path.read_text().strip()
    except OSError:
        active_profile = ""

    if active_profile and active_profile != "default":
        profile_home = default_root / "profiles" / active_profile
        if profile_home.is_dir():
            report.note(
                f"sticky profile {active_profile!r} ({active_profile_path}) "
                "redirects HERMES_HOME"
            )
            return profile_home
        report.record(
            WARN,
            f"active_profile says {active_profile!r} but {profile_home} does not "
            "exist — Hermes would fail to start under that profile",
            fix=f"hermes profile use default   (or recreate {active_profile})",
        )

    if environment_home:
        return Path(environment_home)
    return default_root


# ---------------------------------------------------------------------------
# Step 1 — discovery
# ---------------------------------------------------------------------------

def read_plugin_id(plugin_dir: Path) -> str:
    """Provider id declared in plugin.yaml (``name:``); "" when unreadable."""
    manifest_path = plugin_dir / "plugin.yaml"
    if not manifest_path.exists():
        return ""
    try:
        import yaml

        manifest = yaml.safe_load(manifest_path.read_text(encoding="utf-8-sig")) or {}
        return str(manifest.get("name", "") or "")
    except Exception:
        for line in manifest_path.read_text(encoding="utf-8", errors="replace").splitlines():
            if line.startswith("name:"):
                return line.split(":", 1)[1].strip().strip("\"'")
    return ""


def fingerprint(plugin_dir: Path) -> str:
    """sha256 over the plugin's code + manifest, so a stale copy is visible."""
    digest = hashlib.sha256()
    for file_path in sorted(plugin_dir.glob("*.py")) + sorted(plugin_dir.glob("plugin.y*ml")):
        digest.update(file_path.name.encode())
        digest.update(file_path.read_bytes())
    return digest.hexdigest()[:16]


def step_discovery(report: Report, hermes_home: Path, hermes_agent_dir: Path) -> Optional[str]:
    """Is the plugin somewhere Hermes scans, and does config.yaml select it?

    Returns the installed provider id, or None when it can never be loaded.
    """
    expected_id = read_plugin_id(SOURCE_DIR) or SOURCE_DIR.name
    plugins_dir = hermes_home / "plugins"
    install_path = plugins_dir / expected_id

    report.note(f"source        : {SOURCE_DIR}  (fingerprint {fingerprint(SOURCE_DIR)})")
    report.note(f"scanned dir   : {plugins_dir}")

    installed_id = ""
    if install_path.is_dir():
        installed_id = expected_id
    else:
        # Installed under some other directory name? The directory name IS the
        # provider id for Hermes, so a rename silently changes the id.
        for child in sorted(plugins_dir.iterdir()) if plugins_dir.is_dir() else []:
            if child.is_dir() and child.resolve() == SOURCE_DIR:
                installed_id = child.name
                install_path = child
                break

    if not installed_id:
        report.record(
            FAIL,
            f"not installed: {install_path} does not exist — Hermes never scans "
            "this plugin, so nothing is ever persisted",
            fix=f'mkdir -p "{plugins_dir}" && ln -s "{SOURCE_DIR}" "{install_path}"',
        )
        return None

    report.record(PASS, f"installed at {install_path}")

    if install_path.is_symlink() and install_path.resolve() == SOURCE_DIR:
        report.record(PASS, "it is a symlink to this source tree (always current)")
    else:
        installed_fingerprint = fingerprint(install_path)
        if installed_fingerprint == fingerprint(SOURCE_DIR):
            report.record(PASS, f"installed copy matches this source ({installed_fingerprint})")
        else:
            report.record(
                WARN,
                f"installed copy ({installed_fingerprint}) DIFFERS from this source "
                f"({fingerprint(SOURCE_DIR)}) — you are verifying an older snapshot",
                fix=f'rm -rf "{install_path}" && ln -s "{SOURCE_DIR}" "{install_path}"',
            )

    if installed_id != expected_id:
        report.record(
            WARN,
            f"installed as {installed_id!r} but plugin.yaml declares {expected_id!r} "
            "— the DIRECTORY name is the provider id Hermes uses",
            fix=f'mv "{install_path}" "{plugins_dir / expected_id}"',
        )

    # The 8 KB text scan is the whole gate: fail it and the directory is simply
    # not a memory provider as far as Hermes is concerned.
    sys.path.insert(0, str(hermes_agent_dir))
    import plugins.memory as memory_discovery

    if memory_discovery._is_memory_provider_dir(install_path):
        report.record(PASS, "passes the host's memory-provider scan (8 KB of __init__.py)")
    else:
        report.record(
            FAIL,
            "__init__.py does not contain 'MemoryProvider' or "
            "'register_memory_provider' in its first 8192 bytes — Hermes skips "
            "the directory without logging anything",
            fix="keep those names near the TOP of __init__.py (docstrings push them down)",
        )
        return None

    if memory_discovery.find_provider_dir(installed_id) is None:
        report.record(
            FAIL,
            f"the host cannot resolve provider {installed_id!r} from {plugins_dir}",
            fix=f'ln -s "{SOURCE_DIR}" "{plugins_dir / expected_id}"',
        )
        return None

    # config.yaml is the activation switch. Without it the plugin is inert.
    from hermes_cli.config import cfg_get, get_config_path, load_config

    selected = str(cfg_get(load_config(), "memory", "provider") or "").strip()
    if selected == installed_id:
        report.record(PASS, f"config.yaml selects memory.provider: {selected}")
    elif not selected:
        report.record(
            FAIL,
            f"memory.provider is not set in {get_config_path()} — the plugin is "
            "installed but Hermes will never load it (built-in memory only)",
            fix=f"hermes memory setup      (pick {installed_id!r})\n"
                f"or add to {get_config_path()}:\n"
                f"  memory:\n    provider: {installed_id}",
        )
        return None
    else:
        report.record(
            FAIL,
            f"memory.provider is {selected!r}, not {installed_id!r} — only ONE "
            "external provider runs, and it is not this one",
            fix=f"set memory.provider: {installed_id} in {get_config_path()}",
        )
        return None

    return installed_id


# ---------------------------------------------------------------------------
# Step 2 — loading (fail loudly exactly where the host stays quiet)
# ---------------------------------------------------------------------------

class _LogCapture(logging.Handler):
    """Captures what the host loader would have whispered into logger.debug."""

    def __init__(self) -> None:
        super().__init__(level=logging.DEBUG)
        self.records: List[str] = []

    def emit(self, record: logging.LogRecord) -> None:
        try:
            self.records.append(f"{record.levelname} {record.name}: {record.getMessage()}")
        except Exception:
            pass


class _FakeContext:
    """Stand-in for the host's ``_ProviderCollector``."""

    def __init__(self) -> None:
        self.provider: Any = None
        self.registered_tools: List[str] = []

    def register_memory_provider(self, provider: Any) -> None:
        self.provider = provider

    def register_tool(self, **kwargs: Any) -> None:
        self.registered_tools.append(str(kwargs.get("name", "")))

    def register_hook(self, *args: Any, **kwargs: Any) -> None:
        pass

    def register_cli_command(self, *args: Any, **kwargs: Any) -> None:
        pass


def step_loading(report: Report, provider_id: str) -> Optional[Any]:
    """Load through the host's own loader, then re-prove ``register(ctx)``.

    Junior Tip [o host ENGOLE o erro de import — nós não]:
    ``plugins/memory/__init__.py`` manda toda falha de ``exec_module`` e de
    ``register()`` para ``logger.debug`` e devolve ``None``. Do lado de fora
    isso é indistinguível de "não instalado", e foi assim que um plugin quebrado
    virou um plugin invisível. Aqui a mesma linha de log vira uma FALHA com o
    texto do erro na tela.
    """
    import plugins.memory as memory_discovery
    from agent.memory_provider import MemoryProvider

    capture = _LogCapture()
    loader_logger = logging.getLogger("plugins.memory")
    previous_level = loader_logger.level
    loader_logger.setLevel(logging.DEBUG)
    loader_logger.addHandler(capture)
    try:
        provider = memory_discovery.load_memory_provider(provider_id)
    finally:
        loader_logger.removeHandler(capture)
        loader_logger.setLevel(previous_level)

    swallowed = [line for line in capture.records if "Failed" in line or "failed" in line]

    if provider is None:
        report.record(
            FAIL,
            f"load_memory_provider({provider_id!r}) returned None — the host "
            "would show NOTHING and simply run without memory",
            fix="fix the import error printed below, then re-run this verifier",
        )
        for line in capture.records or ["(the host logged nothing at all)"]:
            report.note(f"host log: {line}")
        return None

    report.record(PASS, f"loaded {type(provider).__name__} via the host loader")

    if swallowed:
        report.record(
            FAIL,
            "the host loader swallowed errors while importing this plugin "
            "(it logs them at DEBUG and keeps going)",
            fix="fix each import error below — today they are invisible in production",
        )
        for line in swallowed:
            report.note(f"host log: {line}")

    if isinstance(provider, MemoryProvider):
        report.record(PASS, "it is a real agent.memory_provider.MemoryProvider subclass")
    else:
        report.record(
            FAIL,
            f"{type(provider).__name__} is NOT a MemoryProvider subclass — the "
            "class-scan fallback would refuse it",
            fix="check that the Hermes runtime is importable from the plugin "
                "(provider.py falls back to a stub base class when it is not)",
        )

    # The register(ctx) path, exercised explicitly with a fake context.
    # The package name differs between a user install
    # (``_hermes_user_memory.<id>``) and a bundled one
    # (``plugins.memory.<id>``), so it is derived from the provider class
    # instead of assumed — an assumption here would silently skip the check.
    module_name = type(provider).__module__.rsplit(".", 1)[0]
    plugin_module = sys.modules.get(module_name)
    if plugin_module is None or not hasattr(plugin_module, "register"):
        report.record(
            WARN,
            f"module {module_name} exposes no register() — the host falls back to "
            "instantiating the first MemoryProvider subclass it finds",
        )
    else:
        context = _FakeContext()
        try:
            plugin_module.register(context)
        except Exception as register_error:
            report.record(
                FAIL,
                f"register(ctx) raised {type(register_error).__name__}: {register_error}",
                fix="register() must never raise — the host logs it at DEBUG and drops the provider",
            )
            return provider
        if context.provider is None:
            report.record(
                FAIL,
                "register(ctx) did NOT call register_memory_provider — the "
                "collector goes home empty and the provider is dropped silently",
                fix="call ctx.register_memory_provider(provider) inside register()",
            )
        else:
            report.record(
                PASS,
                "register(ctx) called register_memory_provider "
                f"(+{len(context.registered_tools)} tool(s) registered)",
            )

    # Routing: the MemoryManager is what actually exposes the tools to the model.
    from agent.memory_manager import MemoryManager

    manager = MemoryManager()
    manager.add_provider(provider)
    routed_tools = sorted(manager.get_all_tool_names())
    declared_tools = sorted(schema["name"] for schema in provider.get_tool_schemas())
    if routed_tools == declared_tools and routed_tools:
        report.record(PASS, f"MemoryManager routes its tools: {', '.join(routed_tools)}")
    else:
        report.record(
            WARN,
            f"declared {declared_tools} but the MemoryManager routes {routed_tools} "
            "— names colliding with core tools are dropped with only a log line",
            fix="rename the colliding tool (keep the anhurdb_ prefix)",
        )

    return provider


# ---------------------------------------------------------------------------
# Step 3 — configuration
# ---------------------------------------------------------------------------

def step_config(report: Report, provider: Any, hermes_home: Path) -> bool:
    """Where the key came from, and whether the provider calls itself ready.

    The API key VALUE never appears here — only its source. That is the whole
    point: ``key source=file`` vs ``key source=missing`` is the diagnostic the
    2026-07-30 blackout lacked.
    """
    config = getattr(provider, "config", None)
    if config is None:
        report.record(
            FAIL,
            "the provider has no configuration object — it failed to load its own config",
            fix="run with ANHUR_ENV_FILE pointing at a readable env file",
        )
        return False

    report.note(f"key source    : {config.key_source}    (the value is never printed)")
    report.note(f"endpoint      : {config.url}")
    report.note(f"container     : {config.container}")
    report.note(f"state dir     : {config.state_dir}")
    report.note(
        f"env file      : {config.env_file_path} "
        f"({config.env_file_error or f'{config.env_file_variable_count} var(s)'})"
    )

    key_resolved = config.has_api_key
    if key_resolved:
        report.record(PASS, f"API key resolved from the {config.key_source}")
    else:
        report.record(
            FAIL,
            "no API key — nothing will ever be persisted",
            fix=f"hermes memory setup    (writes ANHUR_API_KEY into {hermes_home}/.env)",
        )
        report.note(config.missing_key_diagnostic())

    sdk_path = SOURCE_DIR.parents[1] / "python"
    sdk_fix = (
        f'"{sys.executable}" -m pip install -e "{sdk_path}"'
        if (sdk_path / "anhurdb").is_dir()
        else f'"{sys.executable}" -m pip install anhurdb'
    )

    if provider.is_available():
        report.record(PASS, "is_available() == True — the host would activate it")
        return True

    reason = provider.availability_reason or "no reason recorded"
    if not key_resolved and "ANHUR_API_KEY" in reason:
        # Same root cause as the failure above. Reporting it twice would bury
        # the real defect under its own echo.
        report.note("is_available() == False for that same reason (no key)")
        return False

    fix = sdk_fix if "SDK is not importable" in reason else "see the reason above"
    report.record(
        FAIL,
        f"is_available() == False: {reason}",
        fix=f"{fix}\nthe host drops an unavailable provider with a DEBUG line only",
    )
    return False


# ---------------------------------------------------------------------------
# Step 4 — the real cycle
# ---------------------------------------------------------------------------

def read_back_from_anhurdb(config: Any, session_id: str, token: str) -> Tuple[bool, str]:
    """Independent oracle: read the probe turn straight from AnhurDB.

    Junior Tip [por que a leitura é SESSION-SCOPED e procura o token literal —
    medido em 2026-07-30]: ``search(token, ["*"])`` devolveu 10 acertos e
    NENHUM continha o token; a busca tenant-wide traz vizinhos semânticos. Um
    verificador que aceitasse "voltaram resultados" diria OK com o banco vazio
    — exatamente o falso OK que este arquivo existe para não emitir. Escopar na
    sessão da sonda e exigir o token literal torna a prova binária.

    Junior Tip [oráculo INDEPENDENTE do plugin]: quem lê aqui é um cliente SDK
    próprio, não o ``prefetch`` do plugin. Se o leitor do plugin estivesse
    quebrado, um teste que usasse o próprio plugin dos dois lados passaria.
    """
    import asyncio

    from anhurdb import Memory

    async def run() -> Tuple[bool, str]:
        client = Memory(api_key=config.api_key, url=config.url, user_id=config.container)
        await client.connect()
        try:
            hits = await client.search(token, [session_id], limit=10)
            for hit in hits:
                summary = getattr(getattr(hit, "record", None), "summary", "") or ""
                if token in summary:
                    return True, summary[:160]
            return False, f"{len(hits)} record(s) in the session, none containing the token"
        finally:
            await client.close()

    return asyncio.run(run())


def last_plugin_error(config: Any) -> str:
    """Most recent ERROR line the plugin wrote to its own log.

    Junior Tip [dizer o motivo, não onde procurá-lo]: "veja o plugin.log" é o
    parente pobre de "exit 0" — tecnicamente honesto e inútil na prática. O
    plugin JÁ escreveu a causa; o verificador só precisa buscá-la e mostrar.
    """
    try:
        log_lines = config.log_path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return ""
    for log_line in reversed(log_lines[-200:]):
        if " ERROR " in log_line and "shutting down" not in log_line:
            return log_line.strip()
    return ""


def step_cycle(report: Report, provider: Any, hermes_home: Path) -> None:
    """initialize -> sync_turn -> prove it landed -> prefetch. The real thing."""
    config = provider.config
    queue_module = sys.modules[f"{type(provider).__module__.rsplit('.', 1)[0]}.memory_queue"]
    queue = queue_module.DurableTurnQueue(queue_dir=config.queue_dir)

    token = "anhurverify" + uuid.uuid4().hex[:12]
    # A dedicated, never-reused session id. 1 Hermes session = 1 AnhurDB
    # session is inviolable, so a verification run gets its OWN session and
    # never borrows a real conversation's id.
    session_id = "hermes-verify-" + uuid.uuid4().hex[:12]
    report.note(f"probe session : {session_id}")
    report.note(f"probe token   : {token}")

    started = time.time()
    try:
        provider.initialize(
            session_id=session_id,
            platform="cli",
            hermes_home=str(hermes_home),
            agent_context="primary",
        )
    except Exception as initialize_error:
        report.record(
            FAIL,
            f"initialize() raised {type(initialize_error).__name__}: {initialize_error}",
            fix="initialize() must not raise — the host catches it and disables memory",
        )
        return
    report.record(PASS, f"initialize() bound the session in {time.time() - started:.2f}s")

    if provider.session_id != session_id:
        report.record(
            FAIL,
            f"provider adopted session {provider.session_id!r}, not {session_id!r} "
            "— turns would be attributed to the wrong conversation",
            fix="check _adopt_session(); 1 Hermes session = 1 AnhurDB session",
        )

    backlog_before = queue.summarize()
    started = time.time()
    try:
        provider.sync_turn(
            f"Remember the verification token {token}.",
            f"Noted. The verification token is {token}.",
            session_id=session_id,
        )
    except Exception as sync_error:
        report.record(
            FAIL,
            f"sync_turn() raised {type(sync_error).__name__}: {sync_error}",
            fix="sync_turn() must queue on failure, never raise",
        )
        return
    write_seconds = time.time() - started
    backlog_after = queue.summarize()

    queued_delta = backlog_after.pending_count - backlog_before.pending_count
    quarantined_delta = backlog_after.quarantined_count - backlog_before.quarantined_count
    if queued_delta > 0 or quarantined_delta > 0:
        report.record(
            FAIL,
            f"the turn did NOT reach AnhurDB: {queued_delta} queued and "
            f"{quarantined_delta} quarantined in {config.queue_dir} — memory is "
            "NOT live (the turn is safe on disk and will be retried)",
            fix=last_plugin_error(config) or f"read {config.log_path}",
        )
        return
    report.record(PASS, f"sync_turn() accepted the turn in {write_seconds:.2f}s (nothing queued)")

    started = time.time()
    try:
        found, evidence = read_back_from_anhurdb(config, session_id, token)
    except Exception as read_error:
        report.record(
            FAIL,
            f"read-back failed: {type(read_error).__name__}: {read_error}",
            fix="the write reported success but cannot be read — check the server",
        )
        return

    if found:
        report.record(
            PASS,
            f"read back from AnhurDB in {time.time() - started:.2f}s, in the right "
            "session — memory is LIVE",
        )
        report.note(f"stored as: {evidence!r}")
        report.proved_write = True
    else:
        report.record(
            FAIL,
            f"the turn was accepted but is NOT readable in session {session_id} "
            f"({evidence}) — this is silent data loss",
            fix="check the ingestion pipeline for this tenant/container",
        )
        return

    # Recall path. It is deliberately judged apart from the read-back above:
    # prefetch searches tenant-wide, so it is NOT required to return the probe.
    try:
        hits = provider.runtime.search_memories(
            token, config.recall_limit, config.prefetch_timeout_seconds
        )
    except Exception as recall_error:
        report.record(
            FAIL,
            f"recall failed: {type(recall_error).__name__}: {recall_error}",
            fix="writes work but the model will get no memory injected",
        )
        return

    block = provider.prefetch(token, session_id=session_id)
    if hits and not block:
        report.record(
            FAIL,
            f"recall returned {len(hits)} memories but prefetch() rendered an empty "
            "block — the model would receive nothing",
            fix="check transcript.format_recall_block()",
        )
    elif hits:
        report.record(
            PASS,
            f"recall plane answered with {len(hits)} memories and prefetch() rendered "
            f"{len(block.splitlines())} lines into the turn",
        )
    else:
        report.record(
            WARN,
            "recall returned 0 memories — expected on a brand-new container, "
            "suspicious on an old one (the read-back above already proved the write)",
        )

    profile_block = provider.system_prompt_block()
    if profile_block.strip():
        report.record(
            PASS,
            f"system prompt block built ({len(profile_block.splitlines())} lines of profile)",
        )
    else:
        report.record(
            WARN,
            "system_prompt_block() is empty — the session starts without the "
            "static memory block (profile fetch failed or the container is new)",
        )


# ---------------------------------------------------------------------------
# Step 5 — state on disk
# ---------------------------------------------------------------------------

def step_state(report: Report, provider: Any) -> None:
    """Queue and quarantine: turns that exist only on this machine."""
    config = getattr(provider, "config", None)
    if config is None:
        report.record(SKIP, "no configuration — cannot locate the queue")
        return

    queue_module = sys.modules[f"{type(provider).__module__.rsplit('.', 1)[0]}.memory_queue"]
    backlog = queue_module.DurableTurnQueue(queue_dir=config.queue_dir).summarize()

    report.note(f"queue dir     : {config.queue_dir}")
    report.note(f"plugin log    : {config.log_path}")

    if backlog.pending_count == 0:
        report.record(PASS, "queue is empty — no turn is waiting to be persisted")
    else:
        report.record(
            FAIL,
            f"{backlog.pending_count} turn(s) stuck in the queue (oldest "
            f"{backlog.oldest_pending or 'unknown'}) — they never reached AnhurDB",
            fix=(last_plugin_error(config) or "")
            + f"\nthey retry on every turn; if they never drain, read {config.log_path}",
        )

    if backlog.quarantined_count == 0:
        report.record(PASS, "quarantine is empty")
    else:
        report.record(
            FAIL,
            f"{backlog.quarantined_count} quarantined turn(s) in "
            f"{config.queue_dir / 'quarantine'} (oldest "
            f"{backlog.oldest_quarantined or 'unknown'}) — they will NEVER be "
            "persisted automatically",
            fix="a human must decide which session they belong to; writing them "
                "anywhere else would merge two conversations",
        )


# ---------------------------------------------------------------------------
# Verdict
# ---------------------------------------------------------------------------

def print_verdict(report: Report) -> int:
    """One line. Junior Tip: the EXIT CODE is not the verdict — this line is."""
    print("\n" + "=" * 78)
    if report.failures:
        first_step, first_failure = report.failures[0]
        print(
            f"VERDICT: BROKEN — {len(report.failures)} failure(s). "
            f"First: step {first_step} — {first_failure}"
        )
        if len(report.failures) > 1:
            for step_name, failure in report.failures[1:]:
                print(f"         also: step {step_name} — {failure}")
        return 1

    if report.skipped_network:
        print(
            "VERDICT: UNPROVEN — every local check passed, but --no-network skipped "
            "the only step that proves memory works (write + read back). "
            "Re-run without --no-network."
        )
        return 1

    if not report.proved_write:
        print(
            "VERDICT: UNPROVEN — no failure, but the write/read-back was never "
            "completed. Treat this as broken until a full run proves it."
        )
        return 1

    warning_note = f" ({len(report.warnings)} warning(s) above)" if report.warnings else ""
    print(f"VERDICT: WORKING — a turn was written to AnhurDB and read back{warning_note}.")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(
        prog="verify.py",
        description=(
            "Verify the AnhurDB memory provider end to end inside the Hermes "
            "Agent: installed where Hermes scans, selected in config.yaml, "
            "loadable, configured, and actually writing and reading memory."
        ),
        epilog=(
            "The full run writes ONE probe turn into a dedicated "
            "'hermes-verify-<id>' session of the configured container, then "
            "reads it back. It never touches a real conversation. Read the "
            "VERDICT line at the bottom; the exit code (0 working / 1 not) is "
            "for scripts."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--no-network",
        action="store_true",
        help="skip everything that talks to AnhurDB (can never report WORKING)",
    )
    arguments = parser.parse_args()

    hermes_agent_dir = locate_hermes_agent_dir()
    if hermes_agent_dir is None:
        print(
            "VERDICT: BROKEN — no Hermes Agent install found. Looked for "
            f"plugins/memory/__init__.py under {Path.home() / '.hermes' / 'hermes-agent'}. "
            "Set HERMES_AGENT_DIR to your install and re-run."
        )
        return 1

    ensure_hermes_interpreter(hermes_agent_dir)

    report = Report()
    print("AnhurDB memory provider — Hermes Agent verifier")
    print("=" * 78)
    print(f"  hermes agent  : {hermes_agent_dir}")
    print(f"  interpreter   : {sys.executable}")

    hermes_home = resolve_effective_hermes_home(report)
    os.environ["HERMES_HOME"] = str(hermes_home)
    print(f"  HERMES_HOME   : {hermes_home}")
    import_hermes_runtime(hermes_agent_dir)

    # Replicate the CLI's own .env injection, or the key would look missing
    # here and present in production (or the reverse).
    try:
        from hermes_cli.env_loader import load_hermes_dotenv

        loaded_env_files = load_hermes_dotenv(hermes_home=hermes_home)
        print(f"  env loaded    : {', '.join(str(p) for p in loaded_env_files) or '(none)'}")
    except Exception as env_error:
        print(f"  env loaded    : FAILED ({type(env_error).__name__}: {env_error})")

    total_steps = 5
    report.step(1, total_steps, "DISCOVERY — can Hermes even see this plugin?")
    provider_id = step_discovery(report, hermes_home, hermes_agent_dir)
    if provider_id is None:
        return print_verdict(report)

    report.step(2, total_steps, "LOADING — does the host loader produce a provider?")
    provider = step_loading(report, provider_id)
    if provider is None:
        return print_verdict(report)

    report.step(3, total_steps, "CONFIG — key, endpoint, readiness")
    is_ready = step_config(report, provider, hermes_home)

    report.step(4, total_steps, "CYCLE — write a turn and read it back")
    if arguments.no_network:
        report.skipped_network = True
        report.record(SKIP, "--no-network: the only proof of working memory was skipped")
    elif not is_ready:
        report.record(SKIP, "provider is not available (see step 3) — nothing to write with")
    else:
        step_cycle(report, provider, hermes_home)

    report.step(5, total_steps, "STATE — queue and quarantine on disk")
    step_state(report, provider)

    try:
        provider.shutdown()
    except Exception as shutdown_error:
        report.record(WARN, f"shutdown() raised {type(shutdown_error).__name__}: {shutdown_error}")

    return print_verdict(report)


if __name__ == "__main__":
    sys.exit(main())
