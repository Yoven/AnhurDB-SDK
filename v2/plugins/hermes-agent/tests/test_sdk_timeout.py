"""ANHUR_HTTP_TIMEOUT must reach the SDK socket, not only the caller deadline.

Junior Tip [por que um servidor que aceita e nunca responde]: o runtime tem
DOIS prazos — o do socket (``Memory(timeout=)``, igual a ANHUR_HTTP_TIMEOUT) e
o do chamador (``_run``, ANHUR_HTTP_TIMEOUT + 5s). Se o kwarg deixar de chegar
ao SDK, o socket cai no default de 30s e quem corta é o ``_run``, com a
mensagem "timed out after 6.0s". Um servidor mudo separa os dois: com o
socket honrando 1s, o erro chega em ~1s e vem do SDK, não do ``_run``.
"""

from __future__ import annotations

import socket
import threading
import time
from typing import List

import pytest

SOCKET_TIMEOUT_SECONDS = 1.0


@pytest.fixture
def silent_server():
    """TCP listener that accepts connections and never writes a byte."""
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.bind(("127.0.0.1", 0))
    listener.listen(8)
    accepted_connections: List[socket.socket] = []
    stop_event = threading.Event()

    def accept_forever() -> None:
        listener.settimeout(0.2)
        while not stop_event.is_set():
            try:
                accepted_connection, _address = listener.accept()
            except OSError:
                continue
            accepted_connections.append(accepted_connection)

    accept_thread = threading.Thread(target=accept_forever, daemon=True)
    accept_thread.start()
    host, port = listener.getsockname()
    try:
        yield f"http://{host}:{port}"
    finally:
        stop_event.set()
        accept_thread.join(timeout=5)
        for accepted_connection in accepted_connections:
            accepted_connection.close()
        listener.close()


def test_http_timeout_is_passed_to_the_sdk_constructor(plugin_modules, silent_server, tmp_path):
    config_module = plugin_modules["config"]
    runtime_module = plugin_modules["sdk_runtime"]
    config = config_module.load_plugin_config(
        environment={
            "ANHUR_API_KEY": "anhur_test_key",
            "ANHUR_URL": silent_server,
            "ANHUR_CONTAINER": "hermes-test",
            "ANHUR_STATE_DIR": str(tmp_path / "state"),
            "ANHUR_HTTP_TIMEOUT": str(SOCKET_TIMEOUT_SECONDS),
        }
    )
    assert config.http_timeout_seconds == SOCKET_TIMEOUT_SECONDS
    runtime = runtime_module.AnhurRuntime(config)
    try:
        started_at = time.monotonic()
        with pytest.raises(runtime_module.AnhurRuntimeError) as raised:
            runtime.register_session("timeout-probe-session")
        elapsed_seconds = time.monotonic() - started_at
    finally:
        runtime.close()

    caller_deadline_seconds = SOCKET_TIMEOUT_SECONDS + 5
    assert "timed out after" not in str(raised.value), (
        "the caller-side deadline fired, so the SDK socket did not honour "
        f"ANHUR_HTTP_TIMEOUT: {raised.value}"
    )
    assert elapsed_seconds < caller_deadline_seconds - 2, elapsed_seconds
    assert elapsed_seconds >= SOCKET_TIMEOUT_SECONDS * 0.8, elapsed_seconds
