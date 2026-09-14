"""LIVE semantics of the AST query that are neither operator nor error.

Three things live here, all of them things a caller can only learn by running
the query against a real SQLite behind a real router:

  * TYPE COERCION — the JSON type you send is not the column type. SQLite's
    column affinity converts some mismatches and quietly refuses others.
  * IMPLICIT PREDICATES — two WHERE clauses the caller never wrote.
  * UNFILTERED QUERIES — an empty ``filters`` object is legal and returns the
    whole tenant, capped by pagination.
"""

from __future__ import annotations

import pytest

from anhurdb.query import QueryBuilder

from ast_live_cases import run_case
from ast_live_harness import AstFixture


def scoped(fixture: AstFixture) -> QueryBuilder:
    return QueryBuilder().where(uuid=fixture.session_id).limit(200)


def test_sqlite_affinity_converts_some_type_mismatches_and_not_others(ast_fixture):
    """The same "wrong type" is a match on one column and a miss on another.

    Junior Tip [why you cannot reason about this from the JSON alone]: ``score``
    is an INTEGER column, so the TEXT ``"7"`` is converted and MATCHES; ``type``
    is a TEXT column, so the number ``5`` is NOT converted to ``"5"`` and misses.
    Neither case is an error — both answer 200. A client that validates value
    types loosely will produce silently empty pages, which is the single failure
    mode this project treats as worse than a crash.
    """
    memory = ast_fixture.memory
    delta = ast_fixture.row("delta")

    numeric_column_text_value = run_case(
        ast_fixture, "coerce.score_as_string", 'where(uuid=S, score="7")',
        lambda: memory.query(scoped(ast_fixture).where(score="7")),
        note="INTEGER column + TEXT value: affinity converts, rows match")
    assert numeric_column_text_value.id_set == ast_fixture.expect(lambda row: row["score"] == 7)
    assert delta["id"] in numeric_column_text_value.id_set

    text_column_numeric_value = run_case(
        ast_fixture, "coerce.type_as_number", "where(uuid=S, type=5)",
        lambda: memory.query(scoped(ast_fixture).where(type=5)),
        note="TEXT column + number: no conversion, silently empty")
    assert text_column_numeric_value.status == 200
    assert text_column_numeric_value.id_set == []

    integer_as_float = run_case(
        ast_fixture, "coerce.id_as_float", "where(uuid=S, id=<delta>.0)",
        lambda: memory.query(scoped(ast_fixture).where(id=float(delta["id"]))))
    assert integer_as_float.id_set == [delta["id"]]

    boolean_as_int = run_case(
        ast_fixture, "coerce.archived_as_int_zero", "where(uuid=S, archived=0)",
        lambda: memory.query(scoped(ast_fixture).where(archived=0)),
        note="0/False and 1/True are interchangeable on the BOOLEAN columns")
    assert boolean_as_int.id_set == ast_fixture.visible_ids

    boolean_as_bool = run_case(
        ast_fixture, "coerce.consolidated_as_int_one", "where(uuid=S, consolidated=1)",
        lambda: memory.query(scoped(ast_fixture).where(consolidated=1)))
    assert boolean_as_bool.id_set == []


def test_archived_rows_are_hidden_unless_the_caller_filters_archived(ast_fixture):
    """The implicit ``archived = 0`` is lifted the moment you mention the column.

    Junior Tip [why this carve-out exists and why it is easy to miss]: the SQL
    appends ``AND archived = 0`` UNLESS ``archived`` appears in the filters, so
    the ONLY way to see an archived row is to ask for it by name. Filtering on
    ``archived`` therefore does two things at once — it adds your predicate and
    it removes a predicate you never wrote.

    Read-only: this test never writes. It borrows an already-archived row from
    the tenant, and SKIPS loudly if there is none, because a silent pass here
    would be a test that proves nothing.
    """
    memory = ast_fixture.memory
    archived_page = run_case(
        ast_fixture, "implicit.archived_filter_lifts_the_hidden_clause",
        'raw {"filters":{"archived":{"$eq":true}}}',
        lambda: memory.query({"filters": {"archived": {"$eq": True}},
                              "pagination": {"limit": 5}}),
        note="explicit archived filter removes the implicit archived = 0")
    assert archived_page.status == 200
    if not archived_page.ids:
        pytest.skip("no archived row exists in this tenant: the carve-out cannot be observed")

    rows = ast_fixture.run(memory.query({"filters": {"archived": {"$eq": True}},
                                         "pagination": {"limit": 5}}))
    assert all(row.archived for row in rows), "archived filter returned a live row"

    hidden_id = rows[0].id
    without_filter = run_case(
        ast_fixture, "implicit.archived_row_hidden_without_the_filter",
        "raw {'filters':{'id':{'$eq':<archived id>}}}",
        lambda: memory.query({"filters": {"id": {"$eq": hidden_id}},
                              "pagination": {"limit": 5}}))
    assert without_filter.id_set == [], (
        f"record {hidden_id} is archived yet visible without an archived filter"
    )


def test_an_empty_filter_object_returns_the_whole_tenant_capped(ast_fixture):
    """``{"filters": {}}`` is legal. The only thing standing between a caller and
    the whole table is the pagination default.

    Junior Tip [why this is worth a test rather than a shrug]: the endpoint has no
    "you must filter something" rule, so a builder bug that drops the filters dict
    does not fail — it returns a full page of somebody else's rows with HTTP 200.
    """
    memory = ast_fixture.memory
    capped = run_case(ast_fixture, "unfiltered.empty_filters_object",
                      'raw {"filters":{},"pagination":{"limit":3}}',
                      lambda: memory.query({"filters": {}, "pagination": {"limit": 3}}))
    assert capped.status == 200
    assert len(capped.ids) == 3

    no_filters_key = run_case(ast_fixture, "unfiltered.no_filters_key",
                              'raw {"pagination":{"limit":3}}',
                              lambda: memory.query({"pagination": {"limit": 3}}))
    assert len(no_filters_key.ids) == 3

    default_page = run_case(ast_fixture, "unfiltered.default_limit_is_50",
                            "raw {}",
                            lambda: memory.query({}),
                            note="no pagination at all: the server applies limit 50")
    assert len(default_page.ids) == 50
