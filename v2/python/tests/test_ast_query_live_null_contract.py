"""LIVE: the null trap, pinned from BOTH ends — the client refuses it, the server allows it.

WHY THIS FILE EXISTS
--------------------
Two facts about ``null`` in an AST filter are true at the same time, and losing
either one causes a different accident:

1. THE SERVER accepts ``null`` as a scalar and answers **HTTP 200 with zero
   rows**, on every column, because it compiles the predicate to ``col = ?``
   bound to NULL — never true in SQL. No error. No warning. Confirmed live
   against production on 2026-09-14, including on ``superseded_by``, a column
   that is NULL on every row the endpoint can return: the honest answer there
   is "all of them" and the caller gets none.

2. THE SDK (2.1.0+) refuses to build that filter at all. Commit 63e3dae added
   the guard precisely because fact 1 is a silent wrong answer, and the AST
   grammar has no ``$exists``/``$ne``/``IS NULL``, so "this column is null"
   is not expressible through ``POST /api/v1/query`` under any spelling.

Those two facts landed in the same commit and CONTRADICTED each other in the
suite: the live tests still asserted ``result.status == 200`` for a call the
new guard now stops inside the process. They were gated behind
``ANHUR_LIVE_AST``, never ran in CI, and the contradiction shipped in 2.1.0.

Junior Tip [why fact 1 is not simply deleted]: a suite that only records "the
SDK refuses null" leaves nothing explaining WHY the refusal is worth its
breaking change. The next person to meet the guard, with no record of the
server's behaviour, removes it as over-zealous validation and restores the
silent empty page. So fact 1 is still proven here — through a RAW AST dict,
which both Python and TypeScript still forward unvalidated by design.

Run with::

    ANHUR_LIVE_AST=1 ANHUR_API_KEY=... ANHUR_URL=https://anhurdb.yoven.ai \\
        pytest tests/test_ast_query_live_null_contract.py -q
"""

from __future__ import annotations

from typing import Any, Dict

from anhurdb.client.exceptions import AnhurError, AnhurQueryError
from anhurdb.query import QueryBuilder

from ast_live_cases import run_case, run_refused_case
from ast_live_harness import AstFixture

# The columns the old test walked. `superseded_by` is the sharp one: the server
# pins `superseded_by IS NULL` on every returnable row, so `= null` asking for
# "the rows where it is null" is the caller's most natural mistake.
NULL_TRAP_COLUMNS = ("superseded_by", "prefix", "valid_from")


def scoped(fixture: AstFixture) -> QueryBuilder:
    """A builder already pinned to the disposable session, with room for all rows."""
    return QueryBuilder().where(uuid=fixture.session_id).limit(200)


def raw_null_ast(fixture: AstFixture, column: str) -> Dict[str, Any]:
    """The same query as a RAW dict, which bypasses the builder's guards.

    Junior Tip [why the raw path is a feature and not a hole]: ``Memory.query()``
    promises to forward a hand-written AST untouched, because the server's
    grammar moves faster than the SDK's and a caller must be able to reach a new
    operator without waiting for a release. That promise is exactly what makes
    the raw dict the right instrument here: it is the only way left to ask the
    server what IT does with ``$eq: null``.
    """
    return {
        "filters": {"uuid": {"$eq": fixture.session_id}, column: {"$eq": None}},
        "pagination": {"limit": 200},
    }


def assert_client_side_rejection(rejection: BaseException, column: str) -> None:
    """The full typed-error contract for a refusal that never left the process."""
    assert isinstance(rejection, AnhurQueryError), (
        f"{column}: expected AnhurQueryError, got {type(rejection).__name__}: {rejection}"
    )
    assert rejection.kind == AnhurError.KIND_INVALID_REQUEST, (
        f"{column}: kind must be invalid_request, got {rejection.kind!r}"
    )
    assert rejection.retryable is False, (
        f"{column}: a malformed query must never be retried"
    )
    assert rejection.status_code is None, (
        f"{column}: status_code must stay None — nothing was ever sent — "
        f"got {rejection.status_code!r}"
    )
    assert "null" in str(rejection).lower() or "none" in str(rejection).lower(), (
        f"{column}: the message must name the null operand, got: {rejection}"
    )


def test_eq_null_is_refused_by_the_builder(ast_fixture):
    """The SDK contract: ``where(col=None)`` dies in the process, not on the wire."""
    memory = ast_fixture.memory
    assert memory is not None

    for column in NULL_TRAP_COLUMNS:
        rejection = run_refused_case(
            ast_fixture, f"eq.{column}_null_refused_client_side",
            f"where(uuid=S, {column}=None)",
            lambda selected=column: scoped(ast_fixture).where(**{selected: None}),
            note="2.1.0 guard (anhurdb/query/grammar.py): $eq null is a guaranteed "
                 "empty result the grammar cannot express any other way, so the "
                 "builder refuses instead of buying a round trip to learn nothing")
        assert_client_side_rejection(rejection, column)


def test_eq_null_still_answers_200_and_zero_rows_on_the_server(ast_fixture):
    """The SERVER contract the guard exists to protect callers from.

    If this ever stops being true — if the server starts REJECTING ``$eq: null``
    with a 400 — the client-side guard becomes redundant and this file is where
    that shows up. Until then, the 200 is the reason the guard is not optional.
    """
    memory = ast_fixture.memory

    for column in NULL_TRAP_COLUMNS:
        result = run_case(
            ast_fixture, f"eq.{column}_null_raw_ast",
            f'query({{"filters": {{"{column}": {{"$eq": null}}}}}})',
            lambda selected=column: memory.query(raw_null_ast(ast_fixture, selected)),
            note="raw AST bypasses the builder guard: the server compiles "
                 "`col = ?` bound to NULL, never true, and answers 200 empty")
        assert result.status == 200, (
            f"{column}: the server stopped accepting $eq null (got {result.status}: "
            f"{result.error}). That is a CONTRACT CHANGE — the client-side guard "
            f"in grammar.py may now be redundant; re-read both before touching it."
        )
        assert result.id_set == [], (
            f"{column}: $eq null unexpectedly matched rows {result.id_set}. SQL "
            f"equality against NULL is unknown, never true — if this matched, the "
            f"server is no longer compiling it the way it did on 2026-09-14."
        )

    # superseded_by deserves the explicit contrast: every visible row satisfies
    # `superseded_by IS NULL`, so the honest answer is the whole fixture. The
    # server returns none. That gap IS the trap, stated as an assertion.
    assert ast_fixture.visible_ids, "fixture is empty — the contrast below is vacuous"


def test_in_with_a_null_element_is_refused_but_the_server_would_accept_it(ast_fixture):
    """The same two-sided contract for a null ELEMENT inside ``$in``.

    ``score IN (7, '9', NULL)`` is not an error in SQL — the NULL element simply
    never matches — so the server answers 200 and returns the rows that matched
    the other elements. A caller who put the NULL there on purpose ("rows whose
    score is 7, 9, or unset") gets a silently incomplete answer, which is why
    the builder refuses the element rather than quietly dropping it.
    """
    memory = ast_fixture.memory

    rejection = run_refused_case(
        ast_fixture, "in.null_element_refused_client_side",
        "where(uuid=S, score__in=[7,'9',None])",
        lambda: scoped(ast_fixture).where(score__in=[7, "9", None]),
        note="grammar.py refuses a null ELEMENT for the same reason as a null "
             "operand: `col IN (..., NULL)` silently ignores it")
    assert_client_side_rejection(rejection, "score")

    raw_in_ast = {
        "filters": {"uuid": {"$eq": ast_fixture.session_id},
                    "score": {"$in": [7, "9", None]}},
        "pagination": {"limit": 200},
    }
    server = run_case(
        ast_fixture, "in.null_element_raw_ast",
        'query({"filters": {"score": {"$in": [7, "9", null]}}})',
        lambda: memory.query(raw_in_ast),
        note="SQLite column affinity converts the TEXT '9' and matches score=9; "
             "the NULL element contributes nothing and raises no error")
    assert server.status == 200
    assert server.id_set == ast_fixture.expect(lambda row: row["score"] in (7, 9)), (
        "the null element must be inert, not exclusionary: the other elements "
        "still match exactly the rows they would match alone"
    )
