"""Synchronous bridge to the asynchronous AnhurDB Python SDK.

The Hermes ``MemoryProvider`` interface is synchronous; the official AnhurDB
Python SDK (``anhurdb``) is ``async``/aiohttp. This module owns ONE background
thread running ONE event loop, and exposes plain blocking methods on top of it.

Junior Tip [por que uma thread com loop próprio, e não ``asyncio.run``]:
``asyncio.run`` levanta ``RuntimeError`` se já existe um loop rodando NA
THREAD que chama. ``prefetch`` roda inline no caminho do turno e ``sync_turn``
roda no executor do MemoryManager — duas threads diferentes, e o Hermes é
livre para mudar isso amanhã. Um loop dedicado funciona igual nos dois casos e
não encosta no loop de quem chamou. Bônus: a ``aiohttp.ClientSession`` do SDK
precisa viver e ser usada no MESMO loop; centralizar aqui garante isso.

This plugin dogfoods the SDK on purpose: the Claude Code plugin exercises the
Go SDK, this one exercises the Python SDK. Bugs users would hit are bugs we
hit first — e.g. the 2026-07-30 ``_read_capped_body`` truncation fix that made
searches intermittently return ``[]`` with no error.
"""

from __future__ import annotations

import asyncio
import concurrent.futures
import logging
import threading
from typing import Any, Callable, Dict, List, Optional

from .config import AnhurPluginConfig

logger = logging.getLogger(__name__)

# Every write is pinned to an explicit session id, so the SDK client's own
# "registered session" state is never consulted. See _ensure_session.
_SESSION_WILDCARD = "*"


class AnhurRuntimeError(RuntimeError):
    """Any failure talking to AnhurDB, with the API key stripped out."""


class SdkUnavailableError(AnhurRuntimeError):
    """The ``anhurdb`` package could not be imported."""


class AnhurRuntime:
    """Blocking facade over the async AnhurDB SDK client."""

    def __init__(self, config: AnhurPluginConfig) -> None:
        self._config = config
        self._loop: Optional[asyncio.AbstractEventLoop] = None
        self._loop_thread: Optional[threading.Thread] = None
        self._client: Any = None
        self._lifecycle_lock = threading.Lock()
        self._registered_session_ids: set = set()
        self._closed = False

    # -- availability --------------------------------------------------------

    @staticmethod
    def probe_sdk() -> str:
        """Return "" when the SDK imports, else a human-readable reason.

        Import-only, no network, no client construction: safe to call from
        ``is_available()``, which Hermes invokes during discovery.
        """
        try:
            import anhurdb  # noqa: F401,PLC0415 (lazy on purpose — see config.py)
        except Exception as import_error:  # noqa: BLE001
            return f"{type(import_error).__name__}: {import_error}"
        return ""

    # -- lifecycle -----------------------------------------------------------

    def _ensure_loop(self) -> asyncio.AbstractEventLoop:
        with self._lifecycle_lock:
            if self._closed:
                raise AnhurRuntimeError("runtime already shut down")
            if self._loop is not None:
                return self._loop
            loop_ready = threading.Event()
            created_loop = asyncio.new_event_loop()

            def run_event_loop() -> None:
                asyncio.set_event_loop(created_loop)
                loop_ready.set()
                created_loop.run_forever()

            loop_thread = threading.Thread(
                target=run_event_loop,
                name="anhurdb-memory-loop",
                daemon=True,  # a wedged HTTP call must never block interpreter exit
            )
            loop_thread.start()
            loop_ready.wait(timeout=5)
            self._loop = created_loop
            self._loop_thread = loop_thread
            return created_loop

    def _run(self, coroutine_factory: Callable[[], Any], timeout_seconds: float) -> Any:
        """Run a coroutine on the plugin loop and block up to ``timeout_seconds``.

        On timeout the in-flight coroutine is cancelled and the caller gets an
        :class:`AnhurRuntimeError`. The caller-side deadline is what protects
        the user's turn; the SDK's own HTTP timeout protects the socket.
        """
        loop = self._ensure_loop()
        try:
            pending_future = asyncio.run_coroutine_threadsafe(
                coroutine_factory(), loop
            )
        except RuntimeError as submit_error:
            raise AnhurRuntimeError(f"cannot schedule call: {submit_error}") from None
        try:
            return pending_future.result(timeout=timeout_seconds)
        except concurrent.futures.TimeoutError:
            pending_future.cancel()
            raise AnhurRuntimeError(
                f"AnhurDB call timed out after {timeout_seconds:.1f}s"
            ) from None
        except concurrent.futures.CancelledError:
            # Junior Tip [CancelledError NÃO é Exception]: desde o 3.8 ela
            # herda de BaseException, então um `except Exception` a deixaria
            # subir por cima do host — e o worker de memória do Hermes também
            # só captura Exception. Uma cancelada escapando mataria a thread
            # de escrita em silêncio, que é o pior desfecho possível aqui.
            raise AnhurRuntimeError("AnhurDB call was cancelled") from None
        except AnhurRuntimeError:
            # Already one of ours (e.g. SdkUnavailableError) — keep the type so
            # the caller can tell "SDK missing" from "server said no".
            raise
        except Exception as call_error:  # noqa: BLE001 — normalised, never swallowed
            raise AnhurRuntimeError(
                f"{type(call_error).__name__}: {_strip_secret(str(call_error), self._config.api_key)}"
            ) from None

    async def _ensure_client(self) -> Any:
        """Build and connect the SDK client once, inside the plugin loop."""
        if self._client is not None:
            return self._client
        try:
            from anhurdb import Memory  # noqa: PLC0415 (lazy on purpose)
        except Exception as import_error:  # noqa: BLE001
            raise SdkUnavailableError(
                f"anhurdb SDK is not importable: {type(import_error).__name__}: {import_error}"
            ) from None
        # user_id IS the container tag: it scopes profile aggregation and is
        # stamped into each record's metadata. It is NOT a session and is never
        # used as one — every write below passes an explicit session_id.
        client = Memory(
            api_key=self._config.api_key,
            url=self._config.url,
            user_id=self._config.container,
        )
        # Junior Tip [o SDK ainda não expõe timeout no construtor de Memory]:
        # ``HTTPConnection`` aceita ``timeout``, mas ``Memory.__init__`` não o
        # repassa (SDK v2.0.0), então o único jeito de honrar
        # ANHUR_HTTP_TIMEOUT no socket é ajustar a conexão ANTES do
        # ``connect()`` — é ali que a ``ClientSession`` nasce com esse valor.
        # É acesso a atributo privado, logo é BEST-EFFORT e falha para o
        # default do SDK sem derrubar nada: o prazo do lado do chamador
        # (``_run``) continua protegendo o turno de qualquer forma. No dia em
        # que o SDK aceitar ``timeout=``, troque isto por um kwarg.
        try:
            client._connection._timeout = _client_timeout(
                self._config.http_timeout_seconds
            )
        except Exception as timeout_error:  # noqa: BLE001
            logger.warning(
                "anhurdb-memory: could not apply ANHUR_HTTP_TIMEOUT to the SDK "
                "connection (%s) — falling back to the SDK default",
                timeout_error,
            )
        await client.connect()
        self._client = client
        return client

    def close(self) -> None:
        """Close the SDK client and stop the loop thread. Idempotent."""
        with self._lifecycle_lock:
            loop = self._loop
            self._closed = True
            self._loop = None
        if loop is None:
            return
        client = self._client
        self._client = None
        if client is not None:
            try:
                asyncio.run_coroutine_threadsafe(client.close(), loop).result(
                    timeout=5
                )
            except Exception as close_error:  # noqa: BLE001
                logger.debug("anhurdb-memory: client close failed: %s", close_error)
        loop.call_soon_threadsafe(loop.stop)

    # -- operations ----------------------------------------------------------

    def register_session(self, session_id: str) -> None:
        """Register the Hermes session id as an AnhurDB session (idempotent).

        Junior Tip [session-first: ADR-0014 tornou ``session_id`` OBRIGATÓRIO
        na escrita]: sem registrar a sessão, TODA gravação volta 400. Foi
        exatamente assim que o agente ``hub_growth`` passou três dias sem
        formar um hub. Registrar é barato e idempotente; não registrar é
        perder tudo em silêncio.
        """
        cleaned_session_id = (session_id or "").strip()
        if not cleaned_session_id:
            raise AnhurRuntimeError("register_session called without a session id")
        if cleaned_session_id in self._registered_session_ids:
            return
        self._run(
            lambda: self._create_session(cleaned_session_id),
            self._config.http_timeout_seconds + 5,
        )
        self._registered_session_ids.add(cleaned_session_id)

    async def _create_session(self, session_id: str) -> None:
        client = await self._ensure_client()
        await client.create_session(session_id=session_id)

    def ingest_turn(self, session_id: str, content: str) -> None:
        """Persist one turn under ``session_id``. Raises on any failure."""
        cleaned_session_id = (session_id or "").strip()
        if not cleaned_session_id:
            raise AnhurRuntimeError("ingest_turn called without a session id")
        self.register_session(cleaned_session_id)
        try:
            self._run(
                lambda: self._ingest(cleaned_session_id, content),
                self._config.http_timeout_seconds + 5,
            )
        except AnhurRuntimeError:
            # Force a re-registration next time: a write rejected because the
            # server no longer knows this session must not stay wedged behind
            # our local "already registered" cache.
            self._registered_session_ids.discard(cleaned_session_id)
            raise

    async def _ingest(self, session_id: str, content: str) -> None:
        client = await self._ensure_client()
        # Junior Tip [session_id SEMPRE explícito]: ``create_session`` muda o
        # ``_session_uuid`` interno do cliente como efeito colateral, e este
        # runtime é compartilhado por turnos de sessões diferentes (gateway,
        # grupos). Passar o id explicitamente faz a escrita não depender desse
        # estado compartilhado — um turno nunca herda a sessão do vizinho.
        await client.add(content, mode="ingest", session_id=session_id)

    def search_memories(
        self,
        query: str,
        limit: int,
        timeout_seconds: float,
    ) -> List[Dict[str, Any]]:
        """Hybrid search across every session of the tenant.

        ``sessions=["*"]`` is the ADR-0014 wildcard: recall deliberately spans
        past conversations — that is the point of long-term memory — while
        WRITES stay pinned to the current session.
        """
        raw_results = self._run(
            lambda: self._search(query, limit),
            timeout_seconds,
        )
        return raw_results

    async def _search(self, query: str, limit: int) -> List[Dict[str, Any]]:
        client = await self._ensure_client()
        hits = await client.search(query, [_SESSION_WILDCARD], limit=limit)
        normalised: List[Dict[str, Any]] = []
        for hit in hits:
            record = getattr(hit, "record", None)
            if record is None:
                continue
            normalised.append(
                {
                    "id": getattr(record, "id", 0),
                    "type": getattr(record, "type", ""),
                    "summary": getattr(record, "summary", ""),
                    "similarity": getattr(hit, "similarity", 0.0),
                    "session": getattr(record, "uuid", ""),
                }
            )
        return normalised

    def search_by_type(
        self,
        query: str,
        limit: int,
        memory_type: str,
        timeout_seconds: float,
    ) -> List[Dict[str, Any]]:
        """Same search plane, narrowed to one cognitive type."""
        return self._run(
            lambda: self._search_by_type(query, limit, memory_type),
            timeout_seconds,
        )

    async def _search_by_type(
        self, query: str, limit: int, memory_type: str
    ) -> List[Dict[str, Any]]:
        client = await self._ensure_client()
        hits = await client.search(
            query, [_SESSION_WILDCARD], limit=limit, type_filter=memory_type
        )
        return [
            {
                "id": getattr(hit.record, "id", 0),
                "type": getattr(hit.record, "type", ""),
                "summary": getattr(hit.record, "summary", ""),
                "similarity": getattr(hit, "similarity", 0.0),
                "session": getattr(hit.record, "uuid", ""),
            }
            for hit in hits
            if getattr(hit, "record", None) is not None
        ]

    def fetch_profile(self, timeout_seconds: float) -> Dict[str, Any]:
        """Container-scoped profile (decisions, facts, preferences, topics)."""
        return self._run(lambda: self._profile(), timeout_seconds)

    async def _profile(self) -> Dict[str, Any]:
        client = await self._ensure_client()
        return await client.profile()


def _client_timeout(total_seconds: float) -> Any:
    """Build the aiohttp timeout object the SDK connection expects."""
    import aiohttp  # noqa: PLC0415 (lazy on purpose)

    return aiohttp.ClientTimeout(total=total_seconds)


def _strip_secret(message: str, secret: str) -> str:
    """Belt-and-braces: never let an API key reach a log line.

    The SDK already keeps the key out of its exception messages; this exists
    because a log line is forever and a future SDK version is not our code.
    """
    if secret and secret in message:
        return message.replace(secret, "***")
    return message
