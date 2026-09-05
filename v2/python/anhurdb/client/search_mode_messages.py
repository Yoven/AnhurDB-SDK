"""The operator-facing text of the ADR-0031 search knobs.

This module answers ONE question: *what exact words does the SDK say when a
search knob was mistyped, ignored, or could not be confirmed?*

Junior Tip [why these strings live alone, in a module of their own, 2026-09-05]:
they are a CROSS-SDK CONTRACT, not prose. The same sentence has to come out of
the Go SDK (``client/search_mode_messages.go``), the TypeScript SDK
(``src/searchModeMessages.ts``) and this one, byte for byte, because an operator
reading a log line or a support ticket must never have to work out which SDK
wrote it before they can search for the message.

That contract has now broken TWICE in the same shape. The first parity round
pinned the REQUEST-validation strings (``INVALID_PARAM``) and left the
RESPONSE-verification strings one function further down, so ``SERVER_TOO_OLD``
and the warnings drifted apart again — different wording AND different quoting
style (this SDK used ``mode='semantic'`` where the other two used
``mode="semantic"``). Naming each string, in one module per SDK, is what makes
the next drift visible in a diff instead of invisible in a log.

Junior Tip [how to change one of these safely]: change it in all THREE files in
the same commit, and update the three tests that compare with EXACT equality
(Go ``TestServerTooOldMessageIsThePinnedCrossSDKText``, TypeScript
``search_knobs.test.ts``, this SDK's ``tests/test_search_mode.py``). A substring
assertion is what let these drift the first time — never weaken one to make a
change pass.
"""

# Tags every warning this SDK emits, so an operator grepping a mixed application
# log can isolate the SDK's own voice. Go prints the same prefix through
# log.Printf and TypeScript through console.warn, so the three emitted lines
# match character for character rather than merely in substance.
WARNING_PREFIX = "anhurdb-sdk: warning: "

# The one scope where absence proves nothing:
# ``handler/record_search_shared_all.go`` builds its RetrievalMeta by hand and
# deliberately leaves Mode empty, because two merged legs have no single honest
# mode. A CURRENT server therefore looks exactly like an ancient one here, so
# raising would reject healthy servers on every shared-plane query.
WARN_MODE_SHARED_ALL_UNCONFIRMABLE = (
    'mode="semantic" cannot be CONFIRMED on scope="shared_all" — the '
    "server never echoes retrieval.mode for a two-leg merge, so a server too old for ADR-0031 "
    "looks identical to a current one here. Use a single scope to get the strict-semantic "
    "guarantee verified."
)

# Explains an ABSENCE, which is why it exists at all: without it, empty
# ``signals`` and an empty ``leg_scores`` read as "nothing matched" when the
# truth is "the server never understood the request".
WARN_DEBUG_SIGNALS_IGNORED = (
    "this AnhurDB server ignored debug_signals (it predates ADR-0031); "
    "per-hit signals and leg_scores are absent, not empty."
)


def server_too_old_for_semantic_mode_message(served_mode: str) -> str:
    """The ONE hard failure of the set.

    ``mode="semantic"`` is a promise ("strict semantics or an error"), and a
    server that ignored it answered HTTP 200 with results that may be purely
    lexical.

    Args:
        served_mode: What the server echoed in ``retrieval.mode``; ``""`` when
            it echoed nothing, which is itself the tell that it predates
            ADR-0031.
    """
    return (
        'SERVER_TOO_OLD: requested mode="semantic" but the server answered '
        f'retrieval.mode="{served_mode}" — this server predates ADR-0031 and IGNORED the mode field, so these '
        "results came from the balanced budget and may be purely lexical. Upgrade the server, or "
        'drop to mode="balanced" and read retrieval.degraded yourself.'
    )


def warn_mode_ignored_message(requested_mode: str) -> str:
    """Fires for ``fast``/``balanced`` against a server that echoed no mode.

    Junior Tip [why this branch existed in Go and TypeScript and NOT here until
    2026-09-05]: this SDK gated the soft-warning branch on
    ``served_mode == "" and (semantic_timeout_ms > 0 or debug_signals)`` —
    ``requested_mode`` was simply not in the condition. Same call, same server: a
    Go or TypeScript operator was told the mode had been ignored and a Python
    operator was told nothing at all. A knob that warns in two SDKs and is silent
    in the third is the same defect class as a knob the server drops.

    It is a warning and not an error because ``fast``/``balanced`` promise no
    semantics — the caller got balanced results, which is a real answer, just not
    the budget they chose.

    Args:
        requested_mode: The mode that was asked for, already normalised.
    """
    return (
        f'this AnhurDB server ignored mode="{requested_mode}" (it predates ADR-0031) and ran its own '
        "balanced pipeline; the results are balanced results."
    )


def warn_semantic_timeout_ignored_message(semantic_timeout_ms: int) -> str:
    """Args:
    semantic_timeout_ms: The budget that was actually put on the wire.
    """
    return (
        f"this AnhurDB server ignored semantic_timeout_ms={semantic_timeout_ms} (it predates "
        "ADR-0031); the server's own semantic budget (700ms) was used instead."
    )


__all__ = [
    "WARNING_PREFIX",
    "WARN_MODE_SHARED_ALL_UNCONFIRMABLE",
    "WARN_DEBUG_SIGNALS_IGNORED",
    "server_too_old_for_semantic_mode_message",
    "warn_mode_ignored_message",
    "warn_semantic_timeout_ignored_message",
]
