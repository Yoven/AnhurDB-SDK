"""How one AST case is executed, recorded and compared with the oracle.

Split out of ``ast_live_harness.py`` (which owns the disposable session and the
wire log) because this file answers a different question: given a call, what
happened, and does it agree with the oracle. The house rule is one responsibility
per file and ~300 lines; seeding a live tenant and adjudicating a result are two.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import List, Optional

from ast_live_harness import _LAST_WIRE, AstFixture


@dataclass
class CaseResult:
    """What one AST case did: ids in RESPONSE ORDER, or the error that stopped it."""

    status: Optional[int]
    ids: Optional[List[int]]
    error: Optional[str]
    error_object: Optional[BaseException] = None

    @property
    def id_set(self) -> List[int]:
        assert self.ids is not None, f"case did not reach the server: {self.error}"
        return sorted(self.ids)


def run_case(fixture: AstFixture, case: str, call_text: str, coroutine_factory,
             note: str = "") -> CaseResult:
    """Execute one AST case, log its wire body, and return a structured result.

    Junior Tip [why ids keep the RESPONSE ORDER]: sorting here would erase the
    only observable effect of the ``sort`` clause, and a sort test that cannot
    see order is a test that passes on a broken server. Set comparisons use
    ``CaseResult.id_set``; order comparisons use ``CaseResult.ids``.
    """
    from anhurdb.client.exceptions import AnhurError

    try:
        returned = fixture.run(coroutine_factory())
        ids = [item.id if hasattr(item, "id") else item.get("id") for item in returned]
        fixture.record_case(case, call_text, 200, ids, None, note)
        return CaseResult(200, ids, None)
    except AnhurError as api_error:
        detail = (f"{type(api_error).__name__}|kind={api_error.kind}"
                  f"|retryable={api_error.retryable}|{api_error}")
        fixture.record_case(case, call_text, api_error.status_code, None, detail, note)
        return CaseResult(api_error.status_code, None, detail, api_error)
    except Exception as client_error:
        detail = f"CLIENT|{type(client_error).__name__}|{client_error}"
        fixture.record_case(case, call_text, None, None, detail, note)
        return CaseResult(None, None, detail, client_error)


def run_case_against_oracle(fixture: AstFixture, case: str, call_text: str,
                            coroutine_factory, predicate, note: str = "") -> CaseResult:
    """Run a case whose expected rows depend on a column the SERVER keeps rewriting.

    Junior Tip [why one retry, and why exactly one]: ``weight``, ``status``,
    ``metadata`` and ``updated_at`` are recomputed by the enrichment pipeline
    seconds to minutes after a record is created, so an oracle frozen at seeding
    time can disagree with the database for reasons that have nothing to do with
    the query grammar — a row's weight crosses the pivot and the filter
    legitimately returns it. Refreshing the oracle immediately before the query
    shrinks that window; retrying ONCE after a fresh refresh closes it for a
    background rewrite that landed mid-case. A REAL operator defect is
    deterministic and fails both attempts, so the retry cannot mask it.
    """
    for attempt in (1, 2):
        fixture.refresh_oracle()
        result = run_case(fixture, case, call_text, coroutine_factory, note)
        if result.ids is None:
            return result
        if result.id_set == fixture.expect(predicate):
            return result
        if attempt == 2:
            raise AssertionError(
                f"{case}: the server's rows and the oracle disagree twice in a row.\n"
                f"  server: {result.id_set}\n  oracle: {fixture.expect(predicate)}\n"
                f"  this is a real drift, not a background rewrite"
            )
    raise AssertionError("unreachable")


def run_refused_case(fixture: AstFixture, case: str, call_text: str, build_call,
                     note: str = "") -> BaseException:
    """Execute a call the CLIENT must refuse, and PROVE nothing reached the wire.

    ``build_call`` is a zero-argument callable that builds — and, were it not
    refused, would send — one query. The guards added in 2.1.0 fire while the
    builder is still assembling the AST, so the refusal happens before any
    coroutine is created, let alone awaited.

    Junior Tip [why the wire probe, and not just ``pytest.raises``, 2026-09-14]:
    an exception alone does not say WHERE the refusal happened. A guard that
    somehow ran after the POST would still raise, and the test would still pass,
    while the server had already done the work and the caller had already paid
    the round trip. ``_LAST_WIRE`` is armed to ``None`` immediately before the
    call and re-read immediately after, so a body appearing there is proof the
    request shipped. That is the difference between "the SDK complained" and
    "the SDK refused", and only the second one is the contract.

    Returns:
        The exception the builder raised, for the caller to assert its type,
        ``kind``, ``retryable`` and ``status_code`` on.
    """
    _LAST_WIRE["body"] = None
    try:
        never_sent = build_call()
    except BaseException as rejection:  # noqa: B036 — the refusal IS the result
        shipped_body = _LAST_WIRE.get("body")
        assert shipped_body is None, (
            f"{case}: the call was refused, but {len(shipped_body)} bytes still "
            f"reached POST /api/v1/query: {shipped_body}"
        )
        fixture.record_case(case, call_text, None, None,
                            f"CLIENT|{type(rejection).__name__}|{rejection}", note)
        return rejection

    # Not refused. Close the un-awaited coroutine so the failure message is the
    # assertion below and not a "coroutine was never awaited" warning on top.
    close_method = getattr(never_sent, "close", None)
    if callable(close_method):
        close_method()
    raise AssertionError(
        f"{case}: `{call_text}` was NOT refused client-side. The 2.1.0 guard is "
        f"gone or unreachable, and this query now costs a round trip to learn "
        f"nothing."
    )
