"""LIVE error paths for POST /api/v1/query, and where the rejection happens.

Two different contracts hide in this file and the three SDKs do not implement the
same split:

  * CLIENT-SIDE: ``QueryBuilder`` raises ``AnhurQueryError`` (kind
    ``invalid_request``, ``status_code`` None, ``retryable=False``) before any
    HTTP request. Cheap and offline, but it is the SDK's copy of the whitelist
    talking, so it drifts silently when the server's whitelist changes.
  * SERVER-SIDE: the same mistake written as a raw dict reaches the server and
    comes back as ``AnhurQueryError`` (HTTP 400, kind ``invalid_request``,
    ``retryable=False``) carrying the server's own message.

Both halves are the SAME exception type since 2.1.0, so one except block covers
the bug class; ``status_code`` (None vs 400) is what says which side caught it.

Every test below asserts WHICH of the two fired and the exact text, because that
is what a caller's error handling is written against — and what phase 3 compares
across Go, TypeScript and Python.
"""

from __future__ import annotations

import pytest

from anhurdb.client.exceptions import AnhurError, AnhurQueryError
from anhurdb.query import QueryBuilder

from ast_live_cases import run_case
from ast_live_harness import AstFixture


def scoped(fixture: AstFixture) -> QueryBuilder:
    return QueryBuilder().where(uuid=fixture.session_id).limit(200)


# ── 1. REJECTED CLIENT-SIDE BY THE BUILDER (no request is ever sent) ────────

def test_builder_rejects_unknown_fields_and_operators_offline(ast_fixture):
    """The builder's own whitelist stops these before the socket is touched."""
    memory = ast_fixture.memory
    expectations = [
        ("err.builder_unknown_field", "where(bogus_col='x')",
         lambda: memory.query(QueryBuilder().where(bogus_col="x")),
         "Field 'bogus_col' is not allowed in filters."),
        ("err.builder_uppercase_field", "where(TYPE='fact')",
         lambda: memory.query(QueryBuilder().where(TYPE="fact")),
         "Field 'TYPE' is not allowed in filters."),
        ("err.builder_consolidate_id_field", "where(consolidate_id=1)",
         lambda: memory.query(QueryBuilder().where(consolidate_id=1)),
         "Field 'consolidate_id' is not allowed in filters."),
        ("err.builder_operator_ne", "where(type__ne='x')",
         lambda: memory.query(QueryBuilder().where(type__ne="x")),
         "Operator suffix 'ne' is not supported."),
        ("err.builder_operator_like", "where(summary__like='x')",
         lambda: memory.query(QueryBuilder().where(summary__like="x")),
         "Operator suffix 'like' is not supported."),
        ("err.builder_operator_nin", "where(type__nin=['x'])",
         lambda: memory.query(QueryBuilder().where(type__nin=["x"])),
         "Operator suffix 'nin' is not supported."),
        ("err.builder_sort_bad_field", "order_by('bogus','asc')",
         lambda: memory.query(scoped(ast_fixture).order_by("bogus", "asc")),
         "Field 'bogus' is not allowed in order_by."),
        ("err.builder_sort_bad_direction", "order_by('id','sideways')",
         lambda: memory.query(scoped(ast_fixture).order_by("id", "sideways")),
         "order_by direction must be 'asc' or 'desc'."),
        ("err.builder_limit_zero", "limit(0)",
         lambda: memory.query(scoped(ast_fixture).limit(0)),
         "Limit must be between 1 and 1000."),
        ("err.builder_limit_1001", "limit(1001)",
         lambda: memory.query(scoped(ast_fixture).limit(1001)),
         "Limit must be between 1 and 1000."),
        ("err.builder_offset_negative", "offset(-1)",
         lambda: memory.query(scoped(ast_fixture).offset(-1)),
         "Offset cannot be negative."),
    ]
    for case, call_text, factory, expected_message in expectations:
        result = run_case(ast_fixture, case, call_text, factory,
                          note="rejected CLIENT-SIDE: no HTTP request is made")
        assert result.status is None, f"{case} reached the server: {result.status}"
        assert isinstance(result.error_object, AnhurQueryError), case
        assert result.error_object.kind == AnhurError.KIND_INVALID_REQUEST, case
        assert result.error_object.status_code is None, case
        assert result.error_object.retryable is False, case
        assert expected_message in str(result.error_object), case


def test_query_rejects_a_non_ast_argument(ast_fixture):
    """A string is neither a dict nor a builder: refused here, never a 400."""
    result = run_case(ast_fixture, "err.query_bad_argument", "mem.query('not an ast')",
                      lambda: ast_fixture.memory.query("not an ast"))
    assert isinstance(result.error_object, AnhurQueryError)
    assert result.error_object.kind == AnhurError.KIND_INVALID_REQUEST
    assert result.error_object.status_code is None
    assert "needs an AST dict or a QueryBuilder/Filter" in str(result.error_object)


def test_execute_without_an_executor_is_a_query_error(ast_fixture):
    result = run_case(ast_fixture, "err.execute_without_executor", "QueryBuilder().execute()",
                      lambda: QueryBuilder().execute())
    assert isinstance(result.error_object, AnhurQueryError)
    assert result.error_object.kind == AnhurError.KIND_INVALID_REQUEST
    assert result.error_object.status_code is None
    assert "No executor was provided" in str(result.error_object)


# ── 2. REJECTED SERVER-SIDE (a raw dict bypasses every client check) ───────

SERVER_REJECTIONS = [
    ("err.raw_unknown_field", {"filters": {"bogus_col": {"$eq": 1}}},
     'invalid filter field: \\"bogus_col\\"'),
    ("err.raw_uppercase_field", {"filters": {"TYPE": {"$eq": "fact"}}},
     'invalid filter field: \\"TYPE\\"'),
    ("err.raw_or_operator", {"filters": {"$or": [{"type": {"$eq": "fact"}}]}},
     'invalid filter field: \\"$or\\"'),
    ("err.raw_unknown_operator", {"filters": {"type": {"$ne": "x"}}},
     'unsupported operator \\"$ne\\"'),
    ("err.raw_uppercase_operator", {"filters": {"type": {"$EQ": "x"}}},
     'unsupported operator \\"$EQ\\"'),
    ("err.raw_bare_value", {"filters": {"type": "episodic"}},
     "must be an object of operators"),
    ("err.raw_empty_operator_object", {"filters": {"type": {}}},
     "has no operator: use one of $eq, $gt, $gte, $lt, $lte, $in"),
    ("err.raw_in_empty_list", {"filters": {"type": {"$in": []}}},
     "$in requires a non-empty array of values"),
    ("err.raw_in_scalar", {"filters": {"type": {"$in": "risk"}}},
     "$in requires a non-empty array of values"),
    ("err.raw_in_nested_array", {"filters": {"type": {"$in": [["risk"]]}}},
     "expects a single value (string, number, boolean or null), got []interface {}"),
    ("err.raw_eq_object", {"filters": {"type": {"$eq": {"a": 1}}}},
     "expects a single value (string, number, boolean or null), got map[string]interface {}"),
    ("err.raw_eq_array", {"filters": {"type": {"$eq": ["a"]}}},
     "expects a single value (string, number, boolean or null), got []interface {}"),
    ("err.raw_nested_group_depth3", {"filters": {"type": {"$eq": {"$eq": "fact"}}}},
     "expects a single value (string, number, boolean or null), got map[string]interface {}"),
    ("err.raw_filters_not_object", {"filters": [1, 2]},
     "invalid json ast — filters must be an OBJECT keyed by column"),
    ("err.raw_sort_not_an_array", {"sort": {"field": "id"}},
     "invalid json ast — filters must be an OBJECT keyed by column"),
    ("err.raw_sort_non_string_order", {"sort": [{"field": "id", "order": 123}]},
     "invalid json ast — filters must be an OBJECT keyed by column"),
    ("err.raw_sort_bad_field", {"sort": [{"field": "bogus", "order": "asc"}]},
     'invalid sort field: \\"bogus\\"'),
    ("err.raw_sort_missing_field", {"sort": [{"order": "asc"}]},
     'invalid sort field: \\"\\"'),
    ("err.raw_top_level_limit", {"limit": 5},
     'limit/offset must live inside the \\"pagination\\" object'),
    ("err.raw_top_level_offset", {"offset": 5},
     'limit/offset must live inside the \\"pagination\\" object'),
]


@pytest.mark.parametrize("case,payload_fragment,expected_message", SERVER_REJECTIONS,
                         ids=[entry[0] for entry in SERVER_REJECTIONS])
def test_raw_ast_is_rejected_by_the_server_with_the_documented_message(
        ast_fixture, case, payload_fragment, expected_message):
    """Every grammar violation the builder cannot see is a 400 with exact text.

    Junior Tip [why the message text is asserted and not just the status]: this
    endpoint answers 400 for a dozen different mistakes, and the ONLY thing that
    tells a caller which one it made is the string. A refactor that collapses
    them into one generic message is a silent break of the caller's contract, and
    a status-only test would not notice.
    """
    payload = {"pagination": {"limit": 5}}
    payload.update(payload_fragment)
    result = run_case(ast_fixture, case, f"mem.query({payload_fragment!r})",
                      lambda: ast_fixture.memory.query(dict(payload)),
                      note="rejected SERVER-SIDE")
    assert result.status == 400, f"{case} expected 400, got {result.status}"
    assert isinstance(result.error_object, AnhurQueryError)
    assert result.error_object.kind == AnhurError.KIND_INVALID_REQUEST
    assert result.error_object.retryable is False
    assert expected_message in str(result.error_object), (
        f"{case}: message drifted.\nwant fragment: {expected_message}\n"
        f"got: {result.error_object}")


def test_body_over_one_mebibyte_gets_the_short_message(ast_fixture):
    """The 1 MiB cap answers a DIFFERENT, shorter message than a bad AST.

    Junior Tip [why this matters to a caller]: ``invalid json ast`` (oversize) is
    a strict prefix of ``invalid json ast — filters must be an OBJECT…`` (bad
    shape). Code that branches on the long message silently misclassifies an
    oversize body as a network hiccup and retries a request that can never fit.
    """
    oversize_value = "x" * (1100 * 1024)
    result = run_case(ast_fixture, "err.body_over_1mib",
                      "where(uuid=S, summary=<1.1 MiB string>)",
                      lambda: ast_fixture.memory.query(
                          scoped(ast_fixture).where(summary=oversize_value)),
                      note="handler MaxBytesReader(1<<20) fires before parsing")
    assert result.status == 400
    assert '{"error":"invalid json ast"}' in str(result.error_object)


def test_bound_parameter_limit_turns_a_caller_mistake_into_a_500(ast_fixture):
    """A caller mistake reported as a SERVER error, and marked retryable.

    Measured live on 2026-09-14 against https://anhurdb.yoven.ai: the endpoint
    answers 200 while the query needs at most 32 766 bound parameters and 500
    ``{"error":"database error"}`` at 32 767. That is SQLITE_MAX_VARIABLE_NUMBER,
    and the budget is shared by the WHOLE query, not by one operator: with a
    ``uuid`` filter already spending one parameter, a 32 766-element ``$in`` is
    enough to blow it. The body at that size is ~218 KB, far under the 1 MiB cap,
    so nothing earlier in the stack catches it.

    Junior Tip [why this is worse than a 400]: ``kind`` comes out ``server`` and
    ``retryable`` comes out TRUE, so a caller with a retry policy will hammer
    production with a request that can never succeed. The fix belongs on the
    server (reject the oversized list as a ValidationError); until then this test
    pins the real behaviour so nobody "discovers" it in a customer incident.
    """
    session = ast_fixture.session_id
    at_the_limit = run_case(
        ast_fixture, "in.at_bound_parameter_limit_32766",
        "where(uuid=S, id__in=[...32765 values...])  # 32766 parameters total",
        lambda: ast_fixture.memory.query(
            QueryBuilder().where(uuid=session).where(id__in=list(range(1, 32766))).limit(200)))
    assert at_the_limit.status == 200
    assert at_the_limit.id_set == ast_fixture.visible_ids

    over_the_limit = run_case(
        ast_fixture, "in.over_bound_parameter_limit_32767",
        "where(uuid=S, id__in=[...32766 values...])  # 32767 parameters total",
        lambda: ast_fixture.memory.query(
            QueryBuilder().where(uuid=session).where(id__in=list(range(1, 32767))).limit(200)),
        note="SQLITE_MAX_VARIABLE_NUMBER exceeded — surfaces as 500, retryable=True")
    assert over_the_limit.status == 500
    assert over_the_limit.error_object.kind == AnhurError.KIND_SERVER
    assert over_the_limit.error_object.retryable is True
    assert '{"error":"database error"}' in str(over_the_limit.error_object)


# ── 3. ACCEPT-AND-IGNORE: the quiet half of the contract ──────────────────

def test_the_server_silently_ignores_select_semantic_search_and_unknown_keys(ast_fixture):
    """Three inputs that look meaningful and change nothing at all."""
    memory = ast_fixture.memory
    session = ast_fixture.session_id
    baseline = ast_fixture.visible_ids

    select_ignored = run_case(ast_fixture, "ignore.select", "select('id').where(uuid=S)",
                              lambda: memory.query(scoped(ast_fixture).select("id")),
                              note="select is parsed and never used; full rows come back")
    assert select_ignored.id_set == baseline
    rows = ast_fixture.run(memory.query(scoped(ast_fixture).select("id")))
    assert rows[0].summary, "select('id') must NOT strip the other columns"

    # The builder refuses to emit this key now (it provably did nothing), so the
    # live proof that the SERVER skips it is sent as a raw AST instead. Losing
    # this case would lose the evidence the refusal is based on.
    semantic_ast = scoped(ast_fixture).where(type="risk").build_ast()
    semantic_ast["filters"]["semantic_search"] = {"query": "anything", "mode": "$hybrid"}
    semantic = run_case(ast_fixture, "ignore.semantic_search",
                        "raw filters['semantic_search'] + where(type='risk')",
                        lambda: memory.query(semantic_ast),
                        note="semantic_search is skipped before the whitelist check")
    assert semantic.id_set == ast_fixture.expect(lambda row: row["type"] == "risk")

    unknown_key = run_case(ast_fixture, "ignore.unknown_top_level_key",
                           'raw {"unknown_top":"x", ...}',
                           lambda: memory.query({"unknown_top": "x",
                                                 "filters": {"uuid": {"$eq": session}},
                                                 "pagination": {"limit": 200}}),
                           note="unknown top-level keys are dropped, unlike limit/offset")
    assert unknown_key.id_set == baseline

    unknown_direction = run_case(
        ast_fixture, "ignore.sort_unknown_direction",
        'raw sort [{"field":"id","order":"sideways"}]',
        lambda: memory.query({"filters": {"uuid": {"$eq": session}},
                              "sort": [{"field": "id", "order": "sideways"}],
                              "pagination": {"limit": 200}}),
        note="an unrecognised direction silently becomes DESC")
    assert unknown_direction.ids == list(reversed(baseline))


def test_query_with_session_uuid_mutates_the_callers_dict(ast_fixture):
    """DEFECT pinned by test: ``query()`` shallow-copies, then writes into filters.

    ``compiled_ast = dict(ast)`` copies the TOP level only, so
    ``compiled_ast["filters"]`` is the caller's own dict and the injected
    ``uuid`` filter lands in it. A caller that reuses one AST dict for two
    sessions silently queries the first session twice.

    Junior Tip [why the test asserts the bug instead of the fix]: this is the
    live, shipped behaviour of 2.1.0. Pinning it makes the defect visible in CI
    and turns the eventual fix (``copy.deepcopy``) into a deliberate, reviewed
    change rather than a surprise. Flip the assertion when it is fixed.
    """
    caller_ast = {"filters": {"type": {"$eq": "fact"}}, "pagination": {"limit": 5}}
    ast_fixture.run(ast_fixture.memory.query(caller_ast, session_uuid=ast_fixture.session_id))
    ast_fixture.record_case("defect.caller_dict_mutated",
                            "mem.query(caller_ast, session_uuid=S)", 200, None, None,
                            note="caller's dict is mutated by the uuid injection")
    assert caller_ast["filters"].get("uuid") == {"$eq": ast_fixture.session_id}, (
        "mutation no longer happens — query() now copies properly; update this test"
    )
