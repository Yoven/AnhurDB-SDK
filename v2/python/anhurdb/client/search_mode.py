"""Retrieval-mode vocabulary and the cross-version honesty check (ADR-0031).

This module answers ONE question and nothing else: *which retrieval budget
did the caller ask for, and did the server actually honour it?*

Junior Tip [why a whole module for three strings]: ``mode`` is the only
search knob whose silent loss changes the ANSWER SET rather than the amount
of debug detail. ADR-0031 (2026-09-05) added ``mode``/``semantic_timeout_ms``/
``debug_signals`` as ADDITIVE fields. Additive is safe for the parser, not for
the semantics: a server that predates the ADR does not know field ``mode``,
drops it into unknown fields, runs ``balanced``, degrades quietly when the
embedder is down and answers HTTP 200 with lexical hits — while the caller
believes it asked for strict semantic retrieval and that a 5xx would have come
back otherwise. That is a degradation nobody can observe, which is the exact
defect class ADR-0031 exists to kill.

The honest detector is the RESPONSE, never the request: an ADR-0031 server
always fills ``retrieval.mode`` (``handler.normalizeSearchMode`` always
resolves to ``fast``/``balanced``/``semantic``, never to the empty string).
So "asked for semantic, got something else back" means the field was ignored.
No env var, no configuration knob — the detection is derived from data the
server already sends.
"""

import warnings
from typing import Any, Optional

from .exceptions import AnhurError
from .search_mode_messages import (
    WARN_DEBUG_SIGNALS_IGNORED,
    WARN_MODE_SHARED_ALL_UNCONFIRMABLE,
    WARNING_PREFIX,
    server_too_old_for_semantic_mode_message,
    warn_mode_ignored_message,
    warn_semantic_timeout_ignored_message,
)

# The three retrieval budgets (ADR-0012), byte-identical to the server's
# handler.SearchModeFast / SearchModeBalanced / SearchModeSemantic.
SEARCH_MODE_FAST = "fast"
SEARCH_MODE_BALANCED = "balanced"
SEARCH_MODE_SEMANTIC = "semantic"

# Ordered exactly as it appears in the error message, so the three SDKs and
# the server documentation all read the same way.
SEARCH_MODES = (SEARCH_MODE_FAST, SEARCH_MODE_BALANCED, SEARCH_MODE_SEMANTIC)

# The scope whose response cannot carry the detector. ``shared_all`` fans out
# to two planes, and BOTH ports deliberately return a minimal retrieval block
# for it (``handler/record_search_shared_all.go`` builds a RetrievalMeta with
# no Mode at all), because two legs over two stores have no single honest
# mode/leg distribution to report.
_SCOPE_SHARED_ALL = "shared_all"


def _unsupported_mode_error(offending_value: object) -> AnhurError:
    """Build the ONE cross-SDK message for a mode the SDK refuses.

    Junior Tip [why this string is byte-identical in Go, TypeScript and Python]:
    an operator reading a support ticket, a log line or a test assertion should
    not have to know which SDK produced it. ``session_filter`` already pins its
    INVALID_PARAM text across the three SDKs and the Go test compares with exact
    equality; ``mode`` now follows the same precedent. The offending value is
    ECHOED because "must be one of: ..." tells the caller the rule they already
    read in the docs, while ``'mode' "semantik" is not supported`` tells them
    the typo they actually made — which is the only new information available.

    Args:
        offending_value: exactly what the caller passed, un-normalised. A caller
            who typed ``"SEMANTIK"`` must see ``"SEMANTIK"`` back, not a lowered
            form they never wrote.

    Returns:
        The ``AnhurError`` to raise. Building instead of raising keeps the
        message in ONE place while both call sites stay a plain ``raise``.
    """
    return AnhurError(
        f'INVALID_PARAM: \'mode\' "{offending_value}" is not supported; '
        f'use "{SEARCH_MODE_FAST}", "{SEARCH_MODE_BALANCED}" '
        f'or "{SEARCH_MODE_SEMANTIC}"',
        kind=AnhurError.KIND_INVALID_REQUEST,
    )


def validate_search_mode(mode: Optional[str]) -> str:
    """Validate a caller-supplied ``mode`` and return the value to put on the wire.

    ``None`` and ``""`` both mean "caller never touched this knob" and return
    ``""``, which the caller then OMITS from the payload so the server's own
    default (balanced) decides — the same omit-when-unset discipline every
    other search knob follows.

    Junior Tip [why the SDK validates something the server tolerates]: the
    server normalises an unknown mode to ``balanced`` on purpose, so that gRPC
    and REST can never disagree about a typo. That is right for the SERVER and
    wrong for the SDK: ``mode="sematic"`` would then run balanced and look
    perfectly successful. The client is the only layer that still knows the
    caller's intent, so it is the only layer that can refuse the typo.

    Raises:
        AnhurError: ``INVALID_PARAM: ...`` when ``mode`` is not one of the three.
    """
    if mode is None:
        return ""
    if not isinstance(mode, str):
        raise _unsupported_mode_error(mode)
    normalized_mode = mode.strip().lower()
    if normalized_mode == "":
        return ""
    if normalized_mode not in SEARCH_MODES:
        raise _unsupported_mode_error(mode)
    return normalized_mode


def validate_semantic_timeout_ms(semantic_timeout_ms: Optional[int]) -> int:
    """Validate the per-request semantic budget in milliseconds.

    ``None`` and ``0`` both mean "use the server default" (700 ms —
    ``handler.defaultSemanticTimeoutMs``) and return ``0``, which the caller
    omits from the wire. A negative budget is a caller bug: the server would
    silently clamp it, so the SDK refuses it instead.

    Raises:
        AnhurError: ``INVALID_PARAM: ...`` when the value is negative or not an int.
    """
    if semantic_timeout_ms is None:
        return 0
    if isinstance(semantic_timeout_ms, bool) or not isinstance(semantic_timeout_ms, int):
        raise AnhurError(
            "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0",
            kind=AnhurError.KIND_INVALID_REQUEST,
        )
    if semantic_timeout_ms < 0:
        raise AnhurError(
            "INVALID_PARAM: 'semantic_timeout_ms' must be an integer >= 0",
            kind=AnhurError.KIND_INVALID_REQUEST,
        )
    return semantic_timeout_ms


def enforce_search_mode_contract(
    requested_mode: str,
    scope: str,
    response_payload: Any,
    *,
    semantic_timeout_ms: int = 0,
    debug_signals: bool = False,
) -> None:
    """Compare what the caller asked for against what the server reported back.

    ``mode="semantic"`` is a PROMISE ("strict semantic retrieval or an error"),
    so a server that ignored the field is a hard failure — the results in hand
    are lexical, and returning them would be the silent lie ADR-0031 forbids.
    ``semantic_timeout_ms`` and ``debug_signals`` only cost the caller a budget
    or some debug detail; they degrade without misrepresenting the result set,
    so those get a warning.

    Args:
        requested_mode:      What the caller passed (already validated; ``""`` = unset).
        scope:               The plane the request targeted.
        response_payload:    The RAW decoded ``/api/v1/search`` body.
        semantic_timeout_ms: The budget that was put on the wire (0 = unset).
        debug_signals:       Whether the request asked for per-hit signals.

    Raises:
        AnhurError: when ``mode="semantic"`` was asked for and the server's
            ``retrieval.mode`` says otherwise.
    """
    retrieval_block = response_payload.get("retrieval") if isinstance(response_payload, dict) else None
    served_mode = ""
    if isinstance(retrieval_block, dict):
        served_mode = str(retrieval_block.get("mode") or "")

    # shared_all is the one scope where a CURRENT server also answers with an
    # empty mode, so absence proves nothing there. Warning instead of raising
    # is the only honest option: raising would reject a healthy server.
    if scope == _SCOPE_SHARED_ALL:
        if requested_mode == SEARCH_MODE_SEMANTIC:
            _warn(WARN_MODE_SHARED_ALL_UNCONFIRMABLE)
        # The soft knobs cannot be judged here either, for the same reason: the
        # detector they depend on is absent by design on this scope. Go and
        # TypeScript both stop here too — warning anyway would train operators
        # to ignore the line.
        return

    if requested_mode == SEARCH_MODE_SEMANTIC and served_mode != SEARCH_MODE_SEMANTIC:
        raise AnhurError(
            server_too_old_for_semantic_mode_message(served_mode),
            kind=AnhurError.KIND_INVALID_REQUEST,
        )

    # A server that answers with a mode is a server that READ the field, so
    # there is nothing left to warn about.
    if served_mode != "":
        return

    # One warning per ignored knob, in the same order and with the same words as
    # Go and TypeScript. ``requested_mode`` is in this list because it was
    # MISSING from it until 2026-09-05 — see warn_mode_ignored_message for the
    # defect that omission produced.
    if requested_mode:
        _warn(warn_mode_ignored_message(requested_mode))
    if semantic_timeout_ms > 0:
        _warn(warn_semantic_timeout_ignored_message(semantic_timeout_ms))
    if debug_signals:
        _warn(WARN_DEBUG_SIGNALS_IGNORED)


def _warn(message_body: str) -> None:
    """Emit one SDK warning, prefixed exactly the way Go and TypeScript prefix it.

    Junior Tip [why the prefix is inside the message and not left to the
    warnings module]: ``warnings`` decorates the line with the file and lineno of
    the CALLER, which differs per SDK and per call site. The part an operator
    greps for has to be identical in all three, so the SDK owns the prefix
    itself. ``stacklevel=4`` points the warning at the caller's own ``search()``
    call rather than at this helper.
    """
    warnings.warn(WARNING_PREFIX + message_body, RuntimeWarning, stacklevel=4)


__all__ = [
    "SEARCH_MODE_FAST",
    "SEARCH_MODE_BALANCED",
    "SEARCH_MODE_SEMANTIC",
    "SEARCH_MODES",
    "validate_search_mode",
    "validate_semantic_timeout_ms",
    "enforce_search_mode_contract",
]
