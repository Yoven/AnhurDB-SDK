"""The install hint a customer sees must be a command that actually works.

Junior Tip [por que este teste existe, 2026-10-17]: o ``verify.py`` sugeria
``pip install anhurdb`` quando o SDK nao estava ao lado do plugin, que e
exatamente o caso de quem instala pelo ``hermes plugins install``. O pacote
nao esta no PyPI, entao a "correcao" sugerida falhava. A URL certa vive em
dois arquivos (``sdk_runtime.py`` e ``verify.py``, que roda como script solto
e nao importa o plugin); este teste impede que as duas divirjam e que a
sugestao volte a ser o nome nu do pacote.
"""

from __future__ import annotations

import importlib.util
from pathlib import Path

PLUGIN_DIR = Path(__file__).resolve().parent.parent


def _load_verify_script():
    """Load verify.py by path, the same way an operator runs it (standalone)."""
    spec = importlib.util.spec_from_file_location(
        "_anhurdb_verify_under_test", PLUGIN_DIR / "verify.py"
    )
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_verify_and_runtime_suggest_the_same_wheel(plugin_modules):
    runtime_url = plugin_modules["sdk_runtime"].SDK_WHEEL_URL
    verify_url = _load_verify_script().SDK_WHEEL_URL

    assert verify_url == runtime_url


def test_install_hint_is_a_published_wheel_not_a_bare_package_name(plugin_modules):
    runtime_url = plugin_modules["sdk_runtime"].SDK_WHEEL_URL

    assert runtime_url.startswith("https://github.com/Yoven/AnhurDB-SDK/releases/download/")
    assert runtime_url.endswith(".whl")
    verify_source = (PLUGIN_DIR / "verify.py").read_text(encoding="utf-8")
    assert "-m pip install anhurdb'" not in verify_source
