"""``AnhurDBMemoryProvider`` — the Hermes memory provider backed by AnhurDB.

Lifecycle mapping (host contract in ``agent/memory_provider.py``):

  ``is_available()``   → honest yes/no, plus a LOUD reason when it is no.
  ``initialize(sid)``  → register the Hermes session in AnhurDB, drain the
                         disk queue, and build the static memory block.
  ``sync_turn(...)``   → persist the turn; on failure it goes to the disk
                         queue and is retried on the next turn. Never dropped.
  ``prefetch(query)``  → bounded recall, returned as a context block.
  ``get_tool_schemas()``→ the explicit ``anhurdb_recall`` / ``anhurdb_search``.

THE inviolable rule: **1 Hermes session = 1 AnhurDB session**. A turn is only
ever written under the session it belongs to; when that session cannot be
proven, the turn is quarantined rather than attributed to a neighbour.
"""

from __future__ import annotations

import logging
import sys
from typing import Any, Dict, List, Optional

from .config import (
    AnhurPluginConfig,
    ensure_state_dir,
    load_plugin_config,
    log_diagnostic,
)
from .delivery_retry import deliver_with_retry
from .memory_queue import BacklogSummary, DurableTurnQueue, QueueWriteError
from .queue_store import StateQueue
from .schemas import MEMORY_TOOL_SCHEMAS
from .sdk_runtime import AnhurRuntime, AnhurRuntimeError
from .tools import dispatch as dispatch_memory_tool
from .transcript import (
    build_chunk_header,
    format_profile_block,
    format_recall_block,
    format_turn,
    split_into_chunks,
)

logger = logging.getLogger(__name__)

PROVIDER_NAME = "anhurdb"

try:
    from agent.memory_provider import MemoryProvider as _MemoryProviderBase

    USING_FALLBACK_BASE = False
except Exception:  # noqa: BLE001 — running outside the Hermes runtime

    class _MemoryProviderBase:  # type: ignore[no-redef]
        """Stand-in base used when the Hermes runtime is not importable.

        Junior Tip [por que existe um fallback, e por que ele GRITA se for
        usado dentro do Hermes]: este plugin mora no repositório do SDK do
        AnhurDB, onde ``agent.memory_provider`` não existe — sem o fallback,
        não haveria como testar o provider fora do host, e um plugin sem teste
        é o plugin que passou 12,8 dias quebrado. Mas um fallback silencioso é
        pior que o problema: se o host renomear o módulo, herdaríamos de um
        stub, ``issubclass(attr, MemoryProvider)`` falharia no loader e o
        provider sumiria de novo em silêncio. Por isso ``is_available()``
        detecta "estou rodando dentro do Hermes com a base errada" e reporta
        ALTO.
        """

    USING_FALLBACK_BASE = True


class AnhurDBMemoryProvider(_MemoryProviderBase):  # type: ignore[misc,valid-type]
    """AnhurDB-backed long-term memory for the Hermes Agent."""

    def __init__(self, config: Optional[AnhurPluginConfig] = None) -> None:
        # Junior Tip [o construtor NÃO pode levantar]: o loader do Hermes tem
        # um caminho de fallback que instancia a classe sem argumentos
        # (``plugins/memory/__init__.py``) e engole a exceção em ``debug``. Um
        # ``__init__`` que estoura vira "o provider não apareceu" sem uma
        # linha de explicação — a assinatura exata do incidente que este
        # plugin foi escrito para não repetir.
        self._configuration_error = ""
        try:
            self._config = config if config is not None else load_plugin_config()
        except Exception as config_error:  # noqa: BLE001
            self._config = None  # type: ignore[assignment]
            self._configuration_error = (
                f"{type(config_error).__name__}: {config_error}"
            )
            logger.error(
                "anhurdb-memory: configuration could not be loaded (%s) — memory "
                "is DISABLED",
                self._configuration_error,
            )

        self._runtime: Optional[AnhurRuntime] = None
        self._queue: Optional[DurableTurnQueue] = None
        self._session_id = ""
        self._writes_enabled = True
        self._agent_context = "primary"
        self._system_prompt_block = ""
        self._availability_reason = ""
        self._reported_problems: set = set()
        self._last_backlog = BacklogSummary()

        if self._config is not None:
            # Junior Tip [StateQueue apresenta a MESMA interface do DurableTurnQueue,
            # 2026-07-31]: enqueue/quarantine/drain/summarize são idênticos, e o
            # StateQueue carrega o DurableTurnQueue dentro de si como PISO. O provider
            # não precisa saber qual das duas está ativa — só que a memória não se
            # perde. Se o SQLite não abrir, tudo continua caindo no arquivo achatado.
            self._queue = StateQueue(queue_dir=self._config.queue_dir)

    # -- identity ------------------------------------------------------------

    @property
    def name(self) -> str:
        """Provider id. Must be stable — ``memory.provider`` points at it."""
        return PROVIDER_NAME

    @property
    def config(self) -> Optional[AnhurPluginConfig]:
        """Resolved configuration (``None`` when loading failed)."""
        return self._config

    @property
    def runtime(self) -> Optional[AnhurRuntime]:
        """The SDK bridge, or ``None`` while the provider is unusable."""
        return self._runtime

    @property
    def availability_reason(self) -> str:
        """Why the provider is unavailable — empty when it is fine."""
        return self._availability_reason

    @property
    def session_id(self) -> str:
        """The AnhurDB session this provider writes to (== Hermes session)."""
        return self._session_id

    # -- availability --------------------------------------------------------

    def is_available(self) -> bool:
        """Honest readiness check. Config + deps only — never the network.

        Junior Tip [a única coisa pior que indisponível é "disponível e
        inerte"]: o plugin anterior respondia "tudo bem" e não gravava nada,
        743 vezes seguidas. Aqui todo ``False`` vem acompanhado do MOTIVO em
        nível ERROR (log do host + arquivo do plugin) e da instrução de
        conserto. Nada de resposta muda.
        """
        if self._config is None:
            self._availability_reason = (
                f"configuration failed to load: {self._configuration_error}"
            )
            self._report_problem("config", self._availability_reason)
            return False

        if USING_FALLBACK_BASE and _running_inside_hermes():
            self._availability_reason = (
                "agent.memory_provider.MemoryProvider could not be imported "
                "inside the Hermes runtime — the plugin fell back to a stub "
                "base class and the host may refuse to route it. Check the "
                "Hermes version."
            )
            self._report_problem("base-class", self._availability_reason)
            return False

        sdk_problem = AnhurRuntime.probe_sdk()
        if sdk_problem:
            self._availability_reason = (
                f"the AnhurDB Python SDK is not importable ({sdk_problem}). "
                "Install it into the Hermes environment, e.g. "
                "`~/.hermes/hermes-agent/venv/bin/pip install -e "
                "<path>/AnhurDB-SDK/v2/python`."
            )
            self._report_problem("sdk", self._availability_reason)
            return False

        if not self._config.has_api_key:
            self._availability_reason = self._config.missing_key_diagnostic()
            self._report_problem("api-key", self._availability_reason)
            return False

        self._availability_reason = ""
        return True

    # -- lifecycle -----------------------------------------------------------

    def initialize(self, session_id: str, **kwargs: Any) -> None:
        """Bind this Hermes session to an AnhurDB session and warm up."""
        self._agent_context = str(kwargs.get("agent_context", "primary") or "primary")
        # The host asks providers to skip writes for non-primary contexts
        # (cron/subagent/flush): those turns are not the user's conversation
        # and writing them would pollute the user's representation. We honour
        # it, but we SAY it — a silent skip is what we are here to prevent.
        self._writes_enabled = self._agent_context == "primary"
        if not self._writes_enabled:
            self._log(
                logging.WARNING,
                f"agent_context={self._agent_context!r}: turns from this context "
                "are NOT persisted (host policy for non-primary contexts)",
            )

        # Junior Tip [a sessão é adotada ANTES do portão de disponibilidade —
        # bug pego pelo próprio teste, 2026-07-30]: numa primeira versão o
        # ``return`` de indisponibilidade vinha antes desta linha, então uma
        # chave ausente deixava ``_session_id`` vazio e TODO turno seguinte ia
        # para a quarentena — um estado do qual nada se recupera sozinho, por
        # falta de um dado que o host JÁ tinha entregado. É literalmente o
        # erro do incidente: a rede de segurança atrás do mesmo portão que ela
        # deveria proteger. Adotar o id é local e não custa nada; só o
        # REGISTRO no AnhurDB depende de estar disponível.
        self._adopt_session(session_id, origin="initialize")
        if self._config is not None and not ensure_state_dir(self._config):
            self._report_problem(
                "state-dir",
                f"state dir {self._config.state_dir} is not writable — a failed "
                "turn cannot be queued and WOULD be lost",
            )

        if not self.is_available():
            return

        self._log(logging.INFO, f"config: {self._config.describe()}")
        self._runtime = AnhurRuntime(self._config)
        self._register_session(origin="initialize")
        self._last_backlog = self._drain_queue()
        self._system_prompt_block = self._build_profile_block(self._last_backlog)

    def on_session_switch(
        self,
        new_session_id: str,
        *,
        parent_session_id: str = "",
        reset: bool = False,
        rewound: bool = False,
        **kwargs: Any,
    ) -> None:
        """Follow the host when it rotates ``session_id`` mid-process.

        Junior Tip [é AQUI que sessões se fundem quando ninguém olha]: o
        Hermes troca ``session_id`` em ``/resume``, ``/branch``, ``/reset``,
        ``/new`` e na compressão de contexto, SEM recriar o provider. Um
        provider que guarda a sessão em ``initialize`` e nunca mais a atualiza
        continua gravando a conversa NOVA dentro da sessão VELHA — duas
        conversas em um registro só, exatamente o que a regra inviolável
        proíbe. Este método é a defesa; remover é reintroduzir a fusão.
        """
        self._adopt_session(new_session_id, origin="session-switch")
        if self.is_available():
            self._register_session(origin="session-switch")

    def shutdown(self) -> None:
        """Close the SDK client and stop the loop thread."""
        backlog = self._queue.summarize() if self._queue is not None else None
        if backlog is not None and backlog.needs_human_attention:
            self._log(
                logging.ERROR,
                f"shutting down with {backlog.pending_count} queued and "
                f"{backlog.quarantined_count} quarantined turn(s) still on disk "
                f"in {self._config.queue_dir if self._config else '?'}",
            )
        if self._runtime is not None:
            self._runtime.close()
            self._runtime = None

    # -- writes --------------------------------------------------------------

    def sync_turn(
        self,
        user_content: str,
        assistant_content: str,
        *,
        session_id: str = "",
        messages: Optional[List[Dict[str, Any]]] = None,
    ) -> None:
        """Persist one completed turn. A failure queues; it never drops.

        ``messages`` (the raw OpenAI-style list) is intentionally NOT
        persisted: the AnhurDB extraction pipeline is calibrated on clean
        dialogue, and tool scaffolding both dilutes it and multiplies the
        stored volume. The rendered ``USER:``/``ASSISTANT:`` turn is the unit
        of memory here.
        """
        if not self._writes_enabled:
            return
        turn_text = format_turn(user_content, assistant_content)
        if not turn_text:
            return
        if self._config is None:
            # Without configuration we do not even know where the queue lives,
            # and inventing a path would be guessing. This is the floor: say it
            # as loudly as we can.
            self._report_problem(
                "no-config",
                "configuration is unavailable — this turn can be neither "
                "persisted nor queued and is LOST",
            )
            return

        # Resolve the owning session WITHOUT ever guessing. An explicit
        # session_id from the host wins (gateway serves concurrent sessions on
        # one provider); it does NOT become this provider's session — the
        # lifecycle owns that, so a neighbour's turn can never move our cursor.
        target_session_id = (session_id or self._session_id or "").strip()
        if not target_session_id:
            self._report_problem(
                "no-session",
                "a turn arrived with no session id (neither the host nor "
                "initialize provided one) — quarantining it instead of "
                "attributing it to another conversation",
            )
            if self._queue is not None:
                self._queue.quarantine(turn_text, reason="no session id available")
            return

        chunks = self._build_chunks(target_session_id, turn_text)

        # ``_runtime is None`` covers initialize() having run while the plugin
        # was unusable (no key yet): the backend is not reachable, so the turn
        # takes the disk path instead of raising an AttributeError into the
        # host's worker.
        if not self.is_available() or self._runtime is None:
            # Unusable backend: keep the turn on disk so a fixed key or a
            # recovered server replays it later. This is the safety net the
            # 2026-07-30 post-mortem said must NEVER sit behind the same
            # early-return as the thing it protects — and it queues the SAME
            # bytes the successful path would have written, so a recovered
            # turn is indistinguishable from one that never failed.
            for chunk in chunks:
                self._queue_turn(target_session_id, chunk)
            return

        # Drain the OLD backlog before adding this turn, so recovered memories
        # keep their original ordering.
        self._last_backlog = self._drain_queue()

        for chunk in chunks:
            # Junior Tip [3 tentativas ANTES do disco, 2026-07-31]: gravar local é
            # último recurso. Um blip — eleição de líder, reconexão, 503 de deploy —
            # não pode criar uma segunda cópia da memória do usuário. Só depois de o
            # banco não responder MAX_DELIVERY_ATTEMPTS vezes (ou responder um "não"
            # definitivo) o turno desce para a fila.
            #
            # Junior Tip [o `except Exception` mora dentro do deliver_with_retry]: o
            # objetivo nunca foi classificar o erro, foi garantir que NENHUM caminho
            # de falha — inclusive um bug nosso — termine com o turno só na memória
            # do processo. Isso continua valendo: o outcome só é sucesso quando o
            # AnhurDB aceitou; qualquer outra coisa vira fila e log alto.
            outcome = deliver_with_retry(
                lambda pinned_chunk=chunk: self._runtime.ingest_turn(
                    target_session_id, pinned_chunk
                )
            )
            if not outcome.delivered:
                self._report_problem(
                    "ingest",
                    f"AnhurDB write failed after {outcome.attempts_made} attempt(s) "
                    f"({outcome.terminality_label}: {outcome.error}) — the turn is "
                    "queued on disk and retried on the next turn",
                )
                self._queue_turn(target_session_id, chunk)

    def _build_chunks(self, session_id: str, turn_text: str) -> List[str]:
        """Header + body for every piece of the turn. Nothing is truncated."""
        bodies = split_into_chunks(turn_text, self._config.max_chunk_chars)
        return [
            build_chunk_header(session_id, chunk_index, len(bodies)) + "\n" + body
            for chunk_index, body in enumerate(bodies, start=1)
        ]

    def _queue_turn(self, session_id: str, content: str) -> None:
        if self._queue is None:
            logger.error(
                "anhurdb-memory: no queue available — a turn for session %s is "
                "being LOST right now",
                session_id,
            )
            return
        try:
            self._queue.enqueue(session_id, content)
        except QueueWriteError as queue_error:
            self._log(
                logging.ERROR,
                f"QUEUE WRITE FAILED for session {session_id} ({queue_error}) — "
                "this turn is lost",
            )

    def _drain_queue(self) -> BacklogSummary:
        """Retry queued turns. Returns what is still stuck."""
        if self._queue is None or self._runtime is None:
            return BacklogSummary()
        summary = self._queue.drain(self._runtime.ingest_turn)
        if summary.drained_count:
            self._log(
                logging.INFO,
                f"drained {summary.drained_count} queued turn(s) into AnhurDB",
            )
        if summary.pending_count:
            self._log(
                logging.ERROR,
                f"{summary.pending_count} turn(s) still queued on disk "
                f"(oldest {summary.oldest_pending}); last error: "
                f"{summary.last_error or 'unknown'}",
            )
        return summary

    # -- reads ---------------------------------------------------------------

    def system_prompt_block(self) -> str:
        """Static block: the AnhurDB profile, built once at initialize."""
        return self._system_prompt_block

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        """Bounded recall for the upcoming turn, as an injectable block.

        Runs INLINE on the turn path, so it uses the short
        ``ANHUR_PREFETCH_TIMEOUT`` deadline and degrades to "" (plus any
        backlog warning) rather than delaying the user.
        """
        backlog = self._queue.summarize() if self._queue is not None else BacklogSummary()
        cleaned_query = (query or "").strip()
        if not cleaned_query or not self.is_available() or self._runtime is None:
            return format_recall_block(cleaned_query, [], backlog)
        try:
            hits = self._runtime.search_memories(
                cleaned_query,
                self._config.recall_limit,
                self._config.prefetch_timeout_seconds,
            )
        except AnhurRuntimeError as recall_error:
            self._report_problem(
                "prefetch", f"recall failed ({recall_error}) — no memory injected"
            )
            return format_recall_block(cleaned_query, [], backlog)
        return format_recall_block(cleaned_query, hits, backlog)

    def get_tool_schemas(self) -> List[Dict[str, Any]]:
        """Explicit memory tools exposed to the model."""
        return list(MEMORY_TOOL_SCHEMAS)

    def handle_tool_call(
        self, tool_name: str, args: Dict[str, Any], **kwargs: Any
    ) -> str:
        """Route a tool call. Always a JSON string; never raises."""
        return dispatch_memory_tool(self, tool_name, args)

    # -- setup / operations --------------------------------------------------

    def get_config_schema(self) -> List[Dict[str, Any]]:
        """Fields collected by ``hermes memory setup``.

        Every field carries ``env_var``, so the wizard writes them to
        ``~/.hermes/.env`` and ``save_config`` stays a no-op — the contract the
        ABC documents for env-only providers.
        """
        return [
            {
                "key": "api_key",
                "description": "AnhurDB tenant API key (never the master key)",
                "secret": True,
                "required": True,
                "env_var": "ANHUR_API_KEY",
                "url": "https://anhurdb.yoven.ai",
            },
            {
                "key": "url",
                "description": "AnhurDB endpoint",
                "env_var": "ANHUR_URL",
                "default": self._config.url if self._config else "",
            },
            {
                "key": "container",
                "description": (
                    "Memory profile (container tag) within the tenant — keep it "
                    "stable across sessions"
                ),
                "env_var": "ANHUR_CONTAINER",
                "default": self._config.container if self._config else "",
            },
        ]

    def backup_paths(self) -> List[str]:
        """State kept OUTSIDE HERMES_HOME, so ``hermes backup`` includes it.

        Junior Tip [a fila é dado do usuário, não cache]: um turno enfileirado
        ainda não chegou ao AnhurDB. Se o backup não levar o state dir, um
        backup/restore descarta exatamente os turnos que o sistema prometeu
        não perder.
        """
        if self._config is None:
            return []
        return [str(self._config.state_dir)]

    # -- internals -----------------------------------------------------------

    def _adopt_session(self, session_id: str, origin: str) -> None:
        """Adopt ``session_id`` as THE session. Local only — never network."""
        cleaned_session_id = (session_id or "").strip()
        if not cleaned_session_id:
            self._session_id = ""
            self._report_problem(
                f"bind-{origin}",
                f"{origin} provided no session id — turns will be quarantined "
                "instead of guessing an owner",
            )
            return
        self._session_id = cleaned_session_id

    def _register_session(self, origin: str) -> None:
        """Register the adopted session in AnhurDB (idempotent, best-effort).

        A failure here is NOT fatal: turns are queued and every later write
        retries the registration, so a server that comes back finds its
        session waiting.
        """
        if self._runtime is None or not self._session_id:
            return
        # Junior Tip [o registro retenta pelo mesmo motivo que a escrita, 2026-07-31]:
        # o servidor exige sessão registrada ANTES do write (session-first). Se o
        # registro cai num blip e não retenta, TODOS os turnos daquele intervalo
        # descem para o disco — a regra "gravar local é último recurso" seria burlada
        # pela porta dos fundos. O par Go (core/core.go, cmdPersist) envolve
        # CreateSession no mesmo deliver_with_retry; deixar só um lado retentando
        # criaria uma divergência que um cliente descobriria antes de nós.
        outcome = deliver_with_retry(
            lambda: self._runtime.register_session(self._session_id)
        )
        if outcome.delivered:
            self._log(
                logging.INFO,
                f"{origin}: session {self._session_id} registered in AnhurDB",
            )
            return
        self._report_problem(
            f"register-{origin}",
            f"could not register session {self._session_id} after "
            f"{outcome.attempts_made} attempt(s) ({outcome.terminality_label}: "
            f"{outcome.error}) — turns will be queued until it succeeds",
        )

    def _build_profile_block(self, backlog: BacklogSummary) -> str:
        if self._runtime is None or self._config is None:
            return ""
        try:
            profile = self._runtime.fetch_profile(
                self._config.http_timeout_seconds + 5
            )
        except AnhurRuntimeError as profile_error:
            self._report_problem(
                "profile",
                f"profile fetch failed ({profile_error}) — starting the session "
                "without the static memory block",
            )
            profile = {}
        return format_profile_block(
            self._config.container, profile, self._config.recall_limit, backlog
        )

    def _report_problem(self, problem_key: str, message: str) -> None:
        """Log a problem LOUD the first time, then keep a per-turn paper trail.

        Junior Tip [alto sem virar ruído]: repetir o mesmo ERROR a cada turno
        treina o operador a ignorar o log — e um log ignorado é tão útil
        quanto silêncio. A primeira ocorrência de cada problema vai a ERROR
        nos dois canais; as repetições continuam indo para o arquivo do
        plugin, que é justamente o histórico que faltava no blackout.
        """
        if problem_key not in self._reported_problems:
            self._reported_problems.add(problem_key)
            self._log(logging.ERROR, message)
            return
        self._log(logging.DEBUG, message)

    def _log(self, level: int, message: str) -> None:
        if self._config is None:
            logger.log(level, "anhurdb-memory: %s", message)
            return
        log_diagnostic(self._config, level, message)


def _running_inside_hermes() -> bool:
    """True when this process is the Hermes agent (not a test / lint run)."""
    return any(
        module_name in sys.modules
        for module_name in ("hermes_cli", "run_agent", "agent.memory_manager")
    )
