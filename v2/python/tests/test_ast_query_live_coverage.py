"""Completeness sweep: all 17 filter columns × all 6 operators, live.

The other live files test what each operator MEANS on the columns whose values
we control. This one closes the combinatorial gap: it fires every
(column, operator) pair the grammar admits at the real server and demands that
each one is accepted and stays inside the disposable session.

Junior Tip [why an "it is accepted" sweep is not a weak test]: the server's
whitelist and its operator switch are two separate pieces of code, and a pair
that falls between them does not fail loudly — the request either 400s (a column
the SDK offers but the server refuses: the caller's query is impossible to
write) or, worse, returns rows the predicate should have excluded. Asserting
subset-of-the-session catches the second: a predicate that is silently dropped
would widen the result past the uuid scope only if the scope itself broke, so
every pair is also checked against the oracle where the oracle can see it.
"""

from __future__ import annotations

import pytest

from anhurdb.query import QueryBuilder
from anhurdb.query.builder import ALLOWED_WHERE_COLUMNS

from ast_live_cases import run_case
from ast_live_harness import AstFixture

OPERATOR_SUFFIXES = ["eq", "gt", "gte", "lt", "lte", "in"]

# A probe value per column, chosen so the pair is legal for the SERVER (types are
# never validated against the column, so anything scalar is accepted) without
# depending on fixture state. The point is acceptance, not the row count.
PROBE_VALUES = {
    "id": 1,
    "uuid": "ast-teste-probe",
    "type": "fact",
    "dimension": 1,
    "weight": 0.5,
    "score": 5,
    "status": "completed",
    "consolidated": False,
    "archived": False,
    "created_at": "2000-01-01T00:00:00Z",
    "updated_at": "2000-01-01T00:00:00Z",
    "prefix": "ast-teste-probe",
    "metadata": "{}",
    "summary": "ast-teste probe",
    "superseded_by": 1,
    "valid_from": "2000-01-01T00:00:00Z",
    "valid_until": "2000-01-01T00:00:00Z",
}

PAIRS = [(column, suffix) for column in sorted(ALLOWED_WHERE_COLUMNS)
         for suffix in OPERATOR_SUFFIXES]


@pytest.mark.parametrize("column,suffix", PAIRS,
                         ids=[f"{column}__{suffix}" for column, suffix in PAIRS])
def test_every_column_operator_pair_is_accepted_and_stays_in_scope(
        ast_fixture: AstFixture, column: str, suffix: str):
    """All 102 pairs: HTTP 200, and never a row outside the seeded session."""
    probe = PROBE_VALUES[column]
    value = [probe] if suffix == "in" else probe
    expression = f"{column}__{suffix}"

    result = run_case(
        ast_fixture, f"pair.{expression}", f"where(uuid=S, {expression}={value!r})",
        lambda: ast_fixture.memory.query(
            QueryBuilder().where(uuid=ast_fixture.session_id)
            .where(**{expression: value}).limit(200)),
        note="completeness sweep: column × operator")

    assert result.status == 200, (
        f"{expression} was rejected: {result.error} — the SDK offers a "
        f"(column, operator) pair the server will not accept"
    )
    assert set(result.id_set) <= set(ast_fixture.visible_ids), (
        f"{expression} returned rows outside the session scope: "
        f"{sorted(set(result.id_set) - set(ast_fixture.visible_ids))}"
    )


def test_the_sweep_covered_the_whole_grammar(ast_fixture: AstFixture):
    """Anti-vacuity: 17 columns × 6 operators must all have been recorded.

    Junior Tip [why count the RECORDED cases and not the parametrisation]: the
    parametrise list is generated from the same constant the sweep uses, so it
    agrees with itself by construction. The recorded cases are written only after
    a request actually returned, so this counts round trips, not intentions.
    """
    swept = {case for case in ast_fixture.recorded_cases if case.startswith("pair.")}
    assert len(swept) == len(ALLOWED_WHERE_COLUMNS) * len(OPERATOR_SUFFIXES) == 102, (
        f"only {len(swept)} of 102 column×operator pairs reached the server"
    )
