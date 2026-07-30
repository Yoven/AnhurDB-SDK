"""Configuration loading for the AnhurDB memory provider.

This module is deliberately dependency-free: only the standard library is
imported at module level. Everything AnhurDB-specific (the SDK, the network)
is loaded lazily by :mod:`sdk_runtime`.

Junior Tip [por que NADA de terceiros no topo destes módulos — 2026-07-30]:
o loader de memory providers do Hermes
(``plugins/memory/__init__.py:_load_provider_from_dir``) executa cada ``*.py``
do plugin e, se um deles levantar, registra ``logger.debug`` e segue em frente
deixando um módulo meio-inicializado em ``sys.modules``. O ``__init__.py``
falha logo depois, o provider nunca é registrado e o usuário não vê NADA — a
mesma assinatura do blackout de 12,8 dias do plugin do Claude Code (743
invocações, 743 skips, exit 0, stderr vazio). Um ``import anhurdb`` no topo
transformaria "SDK não instalado" em "plugin sumiu em silêncio". Importando o
SDK dentro de função, a ausência dele vira uma mensagem explícita em
``is_available()`` — visível, acionável, testável.
"""

from __future__ import annotations

import logging
import os
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Dict, Mapping, Optional, Tuple

logger = logging.getLogger(__name__)


# Name of the configuration file inside the state directory. Same name the
# Claude Code plugin family has always used, so an operator who already has
# ~/.anhur-hermes-memory/env does not have to move anything.
ENV_FILE_NAME = "env"

# Default state directory (queue, quarantine, diagnostic log) under $HOME.
DEFAULT_STATE_DIR_NAME = ".anhur-hermes-memory"

# Default memory profile (container tag) within the tenant the API key selects.
DEFAULT_CONTAINER = "hermes-ltm"

# Used only when the installed SDK cannot be imported to supply its own
# constant. Junior Tip [prefira a constante do SDK]: o plugin do Claude Code
# tomou ``client.DefaultCloudURL`` justamente para não apontar para um endpoint
# velho no dia em que o SDK mudar de casa. Aqui a importação é tolerante: se o
# SDK não estiver instalado, o plugin ainda precisa carregar para poder DIZER
# que não está — então cai neste literal.
FALLBACK_CLOUD_URL = "https://anhurdb.yoven.ai"

# Where the API key came from. These labels go to the log; the VALUE never does.
KEY_SOURCE_ENVIRONMENT = "environment"
KEY_SOURCE_FILE = "file"
KEY_SOURCE_MISSING = "missing"

# Defaults for the numeric knobs.
DEFAULT_HTTP_TIMEOUT_SECONDS = 15.0
# Junior Tip [por que o prefetch tem prazo PRÓPRIO e mais curto]: ``prefetch``
# roda INLINE no caminho do turno (agent/turn_context.py chama
# ``prefetch_all`` de forma síncrona antes do loop de ferramentas), enquanto
# ``sync_turn`` roda no executor de background do MemoryManager. Reaproveitar
# os 15 s de escrita aqui daria ao usuário 15 s de tela parada sempre que o
# AnhurDB estivesse lento. Recall é opcional; o turno não é.
DEFAULT_PREFETCH_TIMEOUT_SECONDS = 5.0
DEFAULT_RECALL_LIMIT = 8
DEFAULT_MAX_CHUNK_CHARS = 24000


@dataclass(frozen=True)
class AnhurPluginConfig:
    """Everything the provider needs to talk to AnhurDB, plus HOW it was found.

    The API key lives here and nowhere else: it is handed straight to the SDK
    as the ``X-API-Key`` header and is never logged, printed, or written to
    disk by this plugin.
    """

    api_key: str
    url: str
    container: str
    state_dir: Path
    env_file_path: Path
    key_source: str
    env_file_variable_count: int
    env_file_error: str
    http_timeout_seconds: float
    prefetch_timeout_seconds: float
    recall_limit: int
    max_chunk_chars: int

    @property
    def queue_dir(self) -> Path:
        """Directory holding turns that could not be persisted yet."""
        return self.state_dir / "queue"

    @property
    def log_path(self) -> Path:
        """Plugin-owned diagnostic log (never contains the API key)."""
        return self.state_dir / "plugin.log"

    @property
    def has_api_key(self) -> bool:
        """True when a non-empty key was resolved from environment or file."""
        return bool(self.api_key)

    def describe(self) -> str:
        """One-line, secret-free description of how configuration resolved.

        Junior Tip [registrar a FONTE da chave não é enfeite — 2026-07-30]: no
        blackout do plugin do Claude Code, a MESMA invocação funcionava ou
        pulava dependendo do shell que a lançasse (187 sucessos intercalados
        mascararam 743 falhas). Sem esta linha, o próximo diagnóstico volta a
        ser adivinhação.
        """
        env_file_note = (
            f"unreadable: {self.env_file_error}"
            if self.env_file_error
            else f"{self.env_file_variable_count} var(s) loaded"
        )
        return (
            f"key source={self.key_source} url={self.url} "
            f"container={self.container} state_dir={self.state_dir} "
            f"env_file={self.env_file_path} ({env_file_note})"
        )

    def missing_key_diagnostic(self) -> str:
        """Actionable message for the "no API key" case — the #1 failure mode."""
        env_file_note = (
            f"could not be read ({self.env_file_error})"
            if self.env_file_error
            else (
                f"was read and provided {self.env_file_variable_count} variable(s), "
                "but not ANHUR_API_KEY"
            )
        )
        return (
            "ANHUR_API_KEY is not set — AnhurDB memory is DISABLED and nothing "
            f"will be persisted. The env file {self.env_file_path} {env_file_note}. "
            "Fix it with `hermes memory setup` (writes ~/.hermes/.env), or put "
            f"`export ANHUR_API_KEY=anhur_...` in {self.env_file_path} (mode 0600)."
        )


def read_env_file(env_file_path: Path) -> Tuple[Dict[str, str], str]:
    """Parse ``KEY=VALUE`` pairs out of the plugin's own configuration file.

    Returns ``(values, error_message)``; ``error_message`` is empty on success
    and non-empty when the file could not be read at all. A MISSING file is
    NOT an error here — the operator may legitimately configure everything
    through ``~/.hermes/.env`` or the shell — so it returns ``({}, "")``.

    The accepted format is deliberately tolerant, because this file is written
    by hand, by ``hermes memory setup``, and by key-rotation scripts:

    * ``KEY=value`` and ``export KEY=value`` (the ``export`` prefix is ignored)
    * single or double quotes around the value are stripped
    * blank lines and ``#`` comments are ignored
    * whitespace around the key and the ``=`` is tolerated

    Junior Tip [por que uma linha malformada NÃO aborta a leitura]: o arquivo
    pode conter um resto de merge ou um comentário sem ``#``. Abortar
    descartaria a chave que está na linha SEGUINTE — trocaria um problema por
    um pior. Linha sem ``=`` é pulada; quem chama decide o que fazer se a chave
    essencial não apareceu.
    """
    if not env_file_path.exists():
        return {}, ""
    try:
        raw_text = env_file_path.read_text(encoding="utf-8", errors="replace")
    except OSError as read_error:
        return {}, f"{type(read_error).__name__}: {read_error}"

    parsed_values: Dict[str, str] = {}
    for raw_line in raw_text.splitlines():
        trimmed_line = raw_line.strip()
        if not trimmed_line or trimmed_line.startswith("#"):
            continue
        if trimmed_line.startswith("export "):
            trimmed_line = trimmed_line[len("export ") :].strip()
        separator_index = trimmed_line.find("=")
        if separator_index <= 0:
            continue
        variable_name = trimmed_line[:separator_index].strip()
        variable_value = _strip_surrounding_quotes(
            trimmed_line[separator_index + 1 :].strip()
        )
        if not variable_name:
            continue
        parsed_values[variable_name] = variable_value
    return parsed_values, ""


def _strip_surrounding_quotes(raw_value: str) -> str:
    """Remove one symmetric pair of quotes around a value."""
    if len(raw_value) < 2:
        return raw_value
    first_character, last_character = raw_value[0], raw_value[-1]
    if first_character == last_character and first_character in ("'", '"'):
        return raw_value[1:-1]
    return raw_value


def load_plugin_config(
    environment: Optional[Mapping[str, str]] = None,
) -> AnhurPluginConfig:
    """Resolve the plugin configuration. The environment always wins.

    Junior Tip [precedência: ambiente VENCE arquivo]: uma variável já presente
    no ambiente do processo nunca é sobrescrita pelo arquivo. Isso preserva o
    override por invocação (``ANHUR_URL=... hermes chat``) e mantém compatível
    quem já exporta no shell — e é exatamente a precedência que o Hermes
    produz ao carregar ``~/.hermes/.env`` para dentro de ``os.environ`` no
    boot da CLI (``hermes_cli/env_loader.load_hermes_dotenv``).

    Junior Tip [este loader NÃO mexe em ``os.environ``]: o equivalente em Go
    injeta no ambiente do processo porque o binário morre em seguida. Aqui o
    processo é o agente inteiro, de vida longa e multi-thread: injetar a chave
    no ambiente global a exporia para todo subprocesso que o agente lançar
    (shell_exec incluído). A configuração é passada explicitamente.
    """
    resolved_environment: Mapping[str, str] = (
        os.environ if environment is None else environment
    )

    default_state_dir = str(Path.home() / DEFAULT_STATE_DIR_NAME)
    # Junior Tip [o ovo e a galinha do state dir]: o arquivo de configuração
    # mora, por padrão, DENTRO do state dir — então o state dir precisa ser
    # resolvido antes de o arquivo poder ser lido. Por isso a LOCALIZAÇÃO do
    # arquivo usa só o ambiente (ou o default), e o state dir efetivo é
    # recalculado depois da leitura, para que ``ANHUR_STATE_DIR`` escrito no
    # próprio arquivo ainda mova fila/quarentena/log. Sem esse segundo passo, a
    # variável seria aceita, ignorada em silêncio, e a fila apareceria num
    # lugar que ninguém procuraria.
    lookup_state_dir = _resolve_path(
        _first_non_empty(
            resolved_environment.get("ANHUR_STATE_DIR"), default_state_dir
        )
    )

    env_file_path = _resolve_path(
        _first_non_empty(
            resolved_environment.get("ANHUR_ENV_FILE"),
            str(lookup_state_dir / ENV_FILE_NAME),
        )
    )

    file_values, env_file_error = read_env_file(env_file_path)

    def resolve(variable_name: str, fallback: str = "") -> str:
        return _first_non_empty(
            resolved_environment.get(variable_name),
            file_values.get(variable_name),
            fallback,
        )

    state_dir = _resolve_path(resolve("ANHUR_STATE_DIR", default_state_dir))

    api_key = resolve("ANHUR_API_KEY")
    if not api_key:
        key_source = KEY_SOURCE_MISSING
    elif _is_non_empty(resolved_environment.get("ANHUR_API_KEY")):
        key_source = KEY_SOURCE_ENVIRONMENT
    else:
        key_source = KEY_SOURCE_FILE

    return AnhurPluginConfig(
        api_key=api_key,
        url=resolve("ANHUR_URL", _resolve_default_cloud_url()),
        container=resolve("ANHUR_CONTAINER", DEFAULT_CONTAINER),
        state_dir=state_dir,
        env_file_path=env_file_path,
        key_source=key_source,
        env_file_variable_count=len(file_values),
        env_file_error=env_file_error,
        http_timeout_seconds=_to_float(
            resolve("ANHUR_HTTP_TIMEOUT"), DEFAULT_HTTP_TIMEOUT_SECONDS
        ),
        prefetch_timeout_seconds=_to_float(
            resolve("ANHUR_PREFETCH_TIMEOUT"), DEFAULT_PREFETCH_TIMEOUT_SECONDS
        ),
        recall_limit=_to_int(resolve("ANHUR_RECALL_LIMIT"), DEFAULT_RECALL_LIMIT),
        max_chunk_chars=_to_int(
            resolve("ANHUR_MAX_CHUNK_CHARS"), DEFAULT_MAX_CHUNK_CHARS
        ),
    )


def _resolve_default_cloud_url() -> str:
    """Prefer the SDK's own default endpoint; fall back to the literal."""
    try:
        from anhurdb.client import DEFAULT_CLOUD_URL  # noqa: PLC0415 (lazy on purpose)

        return str(DEFAULT_CLOUD_URL)
    except Exception:
        return FALLBACK_CLOUD_URL


def _resolve_path(raw_path: str) -> Path:
    """Expand ``~`` and ``$VAR`` in a path coming from config.

    Junior Tip [por que expandir só CAMINHOS]: o arquivo de configuração é
    lido pelo plugin, não pelo shell, então ``ANHUR_STATE_DIR=$HOME/.anhur-…``
    chegaria aqui com o ``$HOME`` literal e criaria um diretório chamado
    ``$HOME`` dentro do cwd do agente — a fila existiria, ninguém acharia, e o
    backup não a levaria. Expandir é a correção. Expandir TUDO seria um erro
    oposto: uma API key com ``$`` viraria outra string em silêncio, que é
    exatamente a classe de bug que este plugin existe para não ter.
    """
    return Path(os.path.expandvars(raw_path)).expanduser()


def _first_non_empty(*candidate_values: Optional[str]) -> str:
    """Return the first candidate that is a non-blank string, else ``""``.

    An env var set to the empty string counts as ABSENT: an empty
    ``ANHUR_API_KEY`` exported by a wrapper script must not mask a valid key
    sitting in the env file.
    """
    for candidate_value in candidate_values:
        if _is_non_empty(candidate_value):
            return str(candidate_value).strip()
    return ""


def _is_non_empty(candidate_value: Optional[str]) -> bool:
    return candidate_value is not None and str(candidate_value).strip() != ""


def _to_int(raw_value: str, fallback: int) -> int:
    try:
        parsed_value = int(str(raw_value).strip())
    except (TypeError, ValueError):
        return fallback
    return parsed_value if parsed_value > 0 else fallback


def _to_float(raw_value: str, fallback: float) -> float:
    try:
        parsed_value = float(str(raw_value).strip())
    except (TypeError, ValueError):
        return fallback
    return parsed_value if parsed_value > 0 else fallback


def ensure_state_dir(config: AnhurPluginConfig) -> bool:
    """Create the state directory (0700). Returns False and logs on failure."""
    try:
        config.state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        return True
    except OSError as mkdir_error:
        logger.error(
            "anhurdb-memory: cannot create state dir %s (%s) — queued turns "
            "cannot be written to disk",
            config.state_dir,
            mkdir_error,
        )
        return False


def log_diagnostic(config: AnhurPluginConfig, level: int, message: str) -> None:
    """Log to the host logger AND to the plugin's own file.

    Junior Tip [dois canais, de propósito — 2026-07-30]: o log do host depende
    da configuração de logging do Hermes, que o operador pode ter silenciado;
    o arquivo do plugin depende apenas do disco. O incidente que originou este
    plugin foi invisível justamente porque o único canal existente era um
    ``exit 0`` mudo. Nenhum dos dois canais recebe a API key — só a FONTE dela.
    """
    logger.log(level, "anhurdb-memory: %s", message)
    try:
        config.state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        timestamp = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        level_name = logging.getLevelName(level)
        with open(config.log_path, "a", encoding="utf-8") as log_file:
            log_file.write(f"{timestamp} {level_name} {message}\n")
        os.chmod(config.log_path, 0o600)
    except OSError:
        # The file log is a convenience; the host logger already carried the
        # message. Never let diagnostics become a new failure path.
        pass
