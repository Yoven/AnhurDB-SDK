"""LIVE sort and pagination for POST /api/v1/query.

Split from ``test_ast_query_live.py`` to keep each file to one responsibility:
that file proves WHICH rows come back, this one proves IN WHAT ORDER and IN WHAT
SLICES. Both share the seeded session from ``conftest.py``.
"""

from __future__ import annotations

from anhurdb.query import QueryBuilder

from ast_live_cases import run_case
from ast_live_harness import AstFixture

# Every case this module and its siblings record before the anti-vacuity check.
MIN_EXPECTED_CASES = 55


def scoped(fixture: AstFixture) -> QueryBuilder:
    """A builder already pinned to the disposable session, with room for all rows."""
    return QueryBuilder().where(uuid=fixture.session_id).limit(200)


# ── 5. SORT (order is the only observable, so order is what we assert) ─────

def test_sort_controls_the_row_order(ast_fixture):
    """asc, desc, multi-term and the default (id DESC) when no sort is given."""
    memory = ast_fixture.memory
    ascending = run_case(ast_fixture, "sort.single_asc", "order_by('id','asc')",
                         lambda: memory.query(scoped(ast_fixture).order_by("id", "asc")))
    assert ascending.ids == ast_fixture.visible_ids

    descending = run_case(ast_fixture, "sort.single_desc", "order_by('id','desc')",
                          lambda: memory.query(scoped(ast_fixture).order_by("id", "desc")))
    assert descending.ids == list(reversed(ast_fixture.visible_ids))

    default_order = run_case(ast_fixture, "sort.default_is_id_desc", "no order_by()",
                             lambda: memory.query(scoped(ast_fixture)))
    assert default_order.ids == list(reversed(ast_fixture.visible_ids))

    upper = run_case(ast_fixture, "sort.uppercase_direction", "order_by('id','ASC')",
                     lambda: memory.query(scoped(ast_fixture).order_by("id", "ASC")),
                     note="the builder lowercases the direction before sending it")
    assert upper.ids == ast_fixture.visible_ids

    multi = run_case(ast_fixture, "sort.multi_term",
                     "order_by('type','asc').order_by('id','desc')",
                     lambda: memory.query(
                         scoped(ast_fixture).order_by("type", "asc").order_by("id", "desc")))
    by_key = {row["id"]: row for row in ast_fixture.oracle}
    expected_order = sorted(ast_fixture.visible_ids,
                            key=lambda record_id: (by_key[record_id]["type"], -record_id))
    assert multi.ids == expected_order


# ── 6. PAGINATION ─────────────────────────────────────────────────────────

def test_pagination_walks_the_result_set(ast_fixture):
    """limit, offset, a page past the end, and the server's silent 1000 clamp."""
    memory = ast_fixture.memory
    ascending = ast_fixture.visible_ids

    first = run_case(ast_fixture, "page.limit_1", "limit(1) with id asc",
                     lambda: memory.query(
                         QueryBuilder().where(uuid=ast_fixture.session_id)
                         .order_by("id", "asc").limit(1)))
    assert first.ids == ascending[:1]

    second_page = run_case(ast_fixture, "page.offset_paging", "limit(3).offset(3), id asc",
                           lambda: memory.query(
                               QueryBuilder().where(uuid=ast_fixture.session_id)
                               .order_by("id", "asc").limit(3).offset(3)))
    assert second_page.ids == ascending[3:6]

    past_end = run_case(ast_fixture, "page.offset_beyond_end", "offset(9999)",
                        lambda: memory.query(
                            QueryBuilder().where(uuid=ast_fixture.session_id)
                            .limit(10).offset(9999)),
                        note='server sends "records": null; the SDK must yield []')
    assert past_end.ids == []

    clamped = run_case(ast_fixture, "page.raw_limit_5000_clamped",
                       'raw pagination {"limit":5000}',
                       lambda: memory.query({"filters": {"uuid": {"$eq": ast_fixture.session_id}},
                                             "pagination": {"limit": 5000}}),
                       note="server clamps to 1000 silently, never an error")
    assert clamped.id_set == ascending

    defaulted = run_case(ast_fixture, "page.raw_no_pagination_key",
                         'raw {"filters":{...}} with no pagination key',
                         lambda: memory.query(
                             {"filters": {"uuid": {"$eq": ast_fixture.session_id}}}),
                         note="server default limit is 50")
    assert defaulted.id_set == ascending


def test_the_suite_actually_ran(ast_fixture):
    """Anti-vacuity: a run that executed nothing must not report success.

    Junior Tip [why a count and not just green tests]: if the fixture or the
    parametrisation ever degenerates, pytest happily reports "0 failed". The
    recorded-case count is the receipt that the wire log was written by this run.
    """
    assert len(ast_fixture.recorded_cases) >= MIN_EXPECTED_CASES, (
        f"only {len(ast_fixture.recorded_cases)} AST cases reached the server; "
        f"expected at least {MIN_EXPECTED_CASES}"
    )
