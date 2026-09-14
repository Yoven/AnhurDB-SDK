"""LIVE matrix for POST /api/v1/query: every operator, every column, against prod.

Run with::

    ANHUR_LIVE_AST=1 ANHUR_API_KEY=... ANHUR_URL=https://anhurdb.yoven.ai \
        pytest tests/test_ast_query_live.py -q

Junior Tip [why every assertion compares ID SETS and not HTTP codes]: an operator
that is silently ignored still answers 200. ``$ne`` used to be "supported" by
this SDK for exactly that reason — the server dropped it and returned the whole
table, which looks like success. Each test below computes the expected ids from
the oracle (the seeded rows as the SERVER reports them) and demands equality, so
an operator that stops filtering fails loudly.

Error paths live in ``test_ast_query_live_errors.py``; the offline contract of
the builder lives in ``test_ast_builder_offline.py``.
"""

from __future__ import annotations

import time

from anhurdb.query import Eq, Filter, QueryBuilder, QueryExecutor

from ast_live_cases import run_case, run_case_against_oracle
from ast_live_harness import AstFixture

def scoped(fixture: AstFixture) -> QueryBuilder:
    """A builder already pinned to the disposable session, with room for all rows."""
    return QueryBuilder().where(uuid=fixture.session_id).limit(200)


# ── 1. SURFACE: every public way this SDK sends an AST ──────────────────────

def test_every_public_entry_point_returns_the_same_rows(ast_fixture):
    """QueryBuilder, raw dict, Filter, Eq, the executor and the deprecated alias."""
    memory = ast_fixture.memory
    session = ast_fixture.session_id
    expected = ast_fixture.visible_ids

    builder = run_case(ast_fixture, "surface.querybuilder",
                       "mem.query(QueryBuilder().where(uuid=S).limit(200))",
                       lambda: memory.query(scoped(ast_fixture)))
    assert builder.id_set == expected

    raw = run_case(ast_fixture, "surface.raw_dict",
                   'mem.query({"filters":{"uuid":{"$eq":S}},"pagination":{"limit":200}})',
                   lambda: memory.query({"filters": {"uuid": {"$eq": session}},
                                         "pagination": {"limit": 200}}))
    assert raw.id_set == expected

    filter_object = run_case(ast_fixture, "surface.filter_object",
                             'mem.query(Filter({"uuid":{"$eq":S}}))',
                             lambda: memory.query(Filter({"uuid": {"$eq": session}})))
    assert filter_object.id_set == expected

    eq_helper = run_case(ast_fixture, "surface.eq_helper",
                         "mem.query({'filters': Eq('uuid', S)})",
                         lambda: memory.query({"filters": Eq("uuid", session)}))
    assert eq_helper.id_set == expected

    executor = run_case(
        ast_fixture, "surface.querybuilder_execute",
        "QueryBuilder(executor=QueryExecutor(conn)).where(uuid=S).execute()",
        lambda: QueryBuilder(executor=QueryExecutor(memory._connection))
        .where(uuid=session).limit(200).execute(),
        note="execute() returns raw dicts, query() returns Record objects")
    assert executor.id_set == expected

    deprecated = run_case(ast_fixture, "surface.search_with_ast_deprecated",
                          "mem.search_with_ast(Filter(...))",
                          lambda: memory.search_with_ast(Filter({"uuid": {"$eq": session}})))
    assert deprecated.id_set == expected


def test_session_uuid_kwarg_scopes_and_overrides(ast_fixture):
    """``session_uuid=`` is injected as a uuid filter and beats one already there."""
    memory = ast_fixture.memory
    scoped_by_kwarg = run_case(
        ast_fixture, "surface.session_uuid_kwarg",
        "mem.query(QueryBuilder().limit(200), session_uuid=S)",
        lambda: memory.query(QueryBuilder().limit(200), session_uuid=ast_fixture.session_id))
    assert scoped_by_kwarg.id_set == ast_fixture.visible_ids

    overridden = run_case(
        ast_fixture, "surface.session_uuid_overrides_filter",
        "mem.query(QueryBuilder().where(uuid='ast-teste-NOPE'), session_uuid=S)",
        lambda: memory.query(QueryBuilder().where(uuid="ast-teste-NOPE").limit(200),
                             session_uuid=ast_fixture.session_id),
        note="session_uuid wins over a uuid filter already in the AST")
    assert overridden.id_set == ast_fixture.visible_ids


# ── 2. $eq ACROSS EVERY FILTERABLE COLUMN ───────────────────────────────────

def test_eq_on_each_column_selects_exactly_the_oracle_rows(ast_fixture):
    """$eq on the ten columns whose value the response actually carries."""
    memory = ast_fixture.memory
    delta = ast_fixture.row("delta")
    kilo = ast_fixture.row("kilo")

    checks = [
        ("type", "fact", lambda row: row["type"] == "fact"),
        ("status", delta["status"], lambda row: row["status"] == delta["status"]),
        ("score", 7, lambda row: row["score"] == 7),
        ("id", delta["id"], lambda row: row["id"] == delta["id"]),
        ("uuid", ast_fixture.session_id, lambda row: True),
        ("summary", delta["summary"], lambda row: row["summary"] == delta["summary"]),
        ("weight", delta["weight"], lambda row: row["weight"] == delta["weight"]),
        ("metadata", delta["metadata"], lambda row: row["metadata"] == delta["metadata"]),
        ("archived", False, lambda row: not row["archived"]),
        ("consolidated", False, lambda row: not row["consolidated"]),
        ("created_at", delta["created_at"], lambda row: row["created_at"] == delta["created_at"]),
        ("updated_at", delta["updated_at"], lambda row: row["updated_at"] == delta["updated_at"]),
        ("valid_from", kilo["valid_from"], lambda row: row.get("valid_from") == kilo["valid_from"]),
        ("valid_until", kilo["valid_until"], lambda row: row.get("valid_until") == kilo["valid_until"]),
    ]
    for column, value, predicate in checks:
        # run_case_against_oracle re-reads the oracle around the query: status,
        # weight, metadata and updated_at are rewritten by enrichment minutes
        # after seeding, and a stale oracle would fail for that, not for the
        # operator. It still fails hard on a second, consistent disagreement.
        result = run_case_against_oracle(
            ast_fixture, f"eq.{column}", f"where(uuid=S, {column}={value!r})",
            lambda c=column, v=value: memory.query(scoped(ast_fixture).where(**{c: v})),
            predicate)
        assert result.id_set, f"$eq on {column} matched nothing — the case is vacuous"


def test_eq_null_is_accepted_and_always_matches_nothing(ast_fixture):
    """The null trap: ``col = NULL`` is never true, so this 200 is always empty.

    Junior Tip [why this deserves its own test]: every row the endpoint can return
    has ``superseded_by IS NULL`` — the SQL is hard-wired that way — yet asking
    for ``superseded_by = null`` returns ZERO rows, because SQL equality against
    NULL is unknown, not true. Python and TypeScript can express this filter; Go
    cannot. A caller who writes it gets a silently empty page with a 200.
    """
    memory = ast_fixture.memory
    for column in ("superseded_by", "prefix", "valid_from"):
        result = run_case(ast_fixture, f"eq.{column}_null", f"where(uuid=S, {column}=None)",
                          lambda c=column: memory.query(scoped(ast_fixture).where(**{c: None})),
                          note="$eq null compiles to `col = ?` bound to NULL: never true")
        assert result.status == 200
        assert result.id_set == [], f"$eq null on {column} unexpectedly matched rows"


def test_dimension_and_prefix_filter_but_are_never_returned(ast_fixture):
    """Two columns are filterable and invisible: model.Record tags them json:"-".

    Junior Tip [absence in the JSON is not absence in the DB]: ``dimension`` and
    ``prefix`` are SELECTed and scanned by the AST query, then dropped by the
    JSON marshaller. ``dimension > 0`` matching every seeded row while no row
    reports a dimension is the proof — a client that tries to verify a dimension
    filter by reading the field back can only conclude, wrongly, that it failed.
    """
    memory = ast_fixture.memory
    assert all("dimension" not in row for row in ast_fixture.oracle)
    assert all("prefix" not in row for row in ast_fixture.oracle)

    # Junior Tip [why this one POLLS]: the oracle cannot wait for `dimension`,
    # because the oracle cannot SEE it — the settle loop compares the JSON, and
    # the JSON never carries this column. Embedding fills it a few seconds after
    # the create, so the only way to observe the transition is through the filter
    # itself. Polling here is not flake-hiding: failing after the budget means
    # the embedding pipeline really did not run, which is worth a red test.
    expected = ast_fixture.visible_ids
    positive = None
    for _ in range(30):
        positive = run_case(ast_fixture, "cmp.dimension_gt_zero",
                            "where(uuid=S, dimension__gt=0)",
                            lambda: memory.query(scoped(ast_fixture).where(dimension__gt=0)))
        if positive.id_set == expected:
            break
        time.sleep(2.0)
    assert positive is not None and positive.id_set == expected, (
        "dimension > 0 never covered every seeded row: either the filter is "
        "broken or nothing embedded these records within 60s"
    )

    zero = run_case(ast_fixture, "eq.dimension_zero", "where(uuid=S, dimension=0)",
                    lambda: memory.query(scoped(ast_fixture).where(dimension=0)))
    assert zero.id_set == []


# ── 3. COMPARISON OPERATORS ────────────────────────────────────────────────

def test_comparison_operators_on_numeric_and_text_columns(ast_fixture):
    """$gt/$gte/$lt/$lte on score, weight, id, timestamps and a text column."""
    memory = ast_fixture.memory
    charlie = ast_fixture.row("charlie")
    pivot_weight = charlie["weight"]
    pivot_id = charlie["id"]

    checks = [
        ("score__gt", 5, lambda row: row["score"] > 5),
        ("score__gte", 5, lambda row: row["score"] >= 5),
        ("score__lt", 5, lambda row: row["score"] < 5),
        ("score__lte", 5, lambda row: row["score"] <= 5),
        ("weight__gt", pivot_weight, lambda row: row["weight"] > pivot_weight),
        ("weight__lte", pivot_weight, lambda row: row["weight"] <= pivot_weight),
        ("id__gt", pivot_id, lambda row: row["id"] > pivot_id),
        ("id__gte", pivot_id, lambda row: row["id"] >= pivot_id),
        ("id__lt", pivot_id, lambda row: row["id"] < pivot_id),
        ("id__lte", pivot_id, lambda row: row["id"] <= pivot_id),
        ("created_at__gte", charlie["created_at"],
         lambda row: row["created_at"] >= charlie["created_at"]),
        ("updated_at__gte", "2000-01-01T00:00:00Z", lambda row: True),
        # Lexicographic comparison on a text column: 'decision' < 'e' < 'episodic'.
        ("type__gt", "e", lambda row: row["type"] > "e"),
    ]
    for expression, value, predicate in checks:
        result = run_case_against_oracle(
            ast_fixture, f"cmp.{expression}", f"where(uuid=S, {expression}={value!r})",
            lambda e=expression, v=value: memory.query(scoped(ast_fixture).where(**{e: v})),
            predicate)
        assert result.ids is not None, f"{expression} never reached the server"


def test_in_operator_boundaries(ast_fixture):
    """$in with one element, several, unknown values, mixed scalar types, 1000 values."""
    memory = ast_fixture.memory
    delta_id = ast_fixture.row("delta")["id"]
    echo_id = ast_fixture.row("echo")["id"]

    single = run_case(ast_fixture, "in.single_element", "where(uuid=S, type__in=['risk'])",
                      lambda: memory.query(scoped(ast_fixture).where(type__in=["risk"])))
    assert single.id_set == ast_fixture.expect(lambda row: row["type"] == "risk")

    several = run_case(ast_fixture, "in.type_multi", "where(uuid=S, type__in=['risk','task'])",
                       lambda: memory.query(scoped(ast_fixture).where(type__in=["risk", "task"])))
    assert several.id_set == ast_fixture.expect(lambda row: row["type"] in ("risk", "task"))

    with_unknown = run_case(
        ast_fixture, "in.ids_with_unknown", "where(uuid=S, id__in=[delta, echo, 999999999])",
        lambda: memory.query(scoped(ast_fixture).where(id__in=[delta_id, echo_id, 999999999])))
    assert with_unknown.id_set == sorted([delta_id, echo_id])

    mixed = run_case(
        ast_fixture, "in.mixed_scalar_types", "where(uuid=S, score__in=[7,'9',None])",
        lambda: memory.query(scoped(ast_fixture).where(score__in=[7, "9", None])),
        note="SQLite column affinity converts the TEXT '9' and matches score=9")
    assert mixed.id_set == ast_fixture.expect(lambda row: row["score"] in (7, 9))

    large = run_case(
        ast_fixture, "in.large_1000_values", "where(uuid=S, id__in=[...1000 values...])",
        lambda: memory.query(scoped(ast_fixture).where(
            id__in=list(range(1, 1000)) + ast_fixture.visible_ids)))
    assert large.id_set == ast_fixture.visible_ids


# ── 4. COMBINATION: the grammar is flat and everything ANDs ────────────────

def test_predicates_always_and_never_or(ast_fixture):
    """Two operators on one field, two fields, and all six operators at once."""
    memory = ast_fixture.memory

    band = run_case(ast_fixture, "combine.two_ops_one_field",
                    "where(uuid=S, score__gte=3, score__lte=7)",
                    lambda: memory.query(scoped(ast_fixture).where(score__gte=3, score__lte=7)))
    assert band.id_set == ast_fixture.expect(lambda row: 3 <= row["score"] <= 7)

    two_fields = run_case(ast_fixture, "combine.two_fields_and",
                          "where(uuid=S, type='fact', score__gte=4)",
                          lambda: memory.query(scoped(ast_fixture).where(type="fact", score__gte=4)))
    assert two_fields.id_set == ast_fixture.expect(
        lambda row: row["type"] == "fact" and row["score"] >= 4)

    chained = run_case(ast_fixture, "combine.chained_where_calls",
                       "where(uuid=S).where(type='fact').where(score__lt=4)",
                       lambda: memory.query(scoped(ast_fixture).where(type="fact").where(score__lt=4)))
    assert chained.id_set == ast_fixture.expect(
        lambda row: row["type"] == "fact" and row["score"] < 4)

    impossible = run_case(ast_fixture, "combine.impossible_and",
                          "where(uuid=S, score__gt=9, score__lt=3)",
                          lambda: memory.query(scoped(ast_fixture).where(score__gt=9, score__lt=3)))
    assert impossible.id_set == []

    everything = run_case(
        ast_fixture, "combine.all_six_operators",
        "eq + gt + gte + lt + lte + in in one query",
        lambda: memory.query(scoped(ast_fixture).where(
            type="fact", score__gt=1, score__gte=2, score__lt=9, score__lte=6,
            id__in=ast_fixture.visible_ids)))
    assert everything.id_set == ast_fixture.expect(
        lambda row: row["type"] == "fact" and 2 <= row["score"] <= 6)


def test_superseded_row_is_invisible_without_any_filter(ast_fixture):
    """`superseded_by IS NULL` is unconditional — no filter can bring india back."""
    memory = ast_fixture.memory
    india_id = ast_fixture.ids_by_key["india"]
    direct = run_case(ast_fixture, "implicit.superseded_row_is_invisible",
                      "where(uuid=S, id=<india>)",
                      lambda: memory.query(scoped(ast_fixture).where(id=india_id)),
                      note="india was superseded by juliett")
    assert direct.id_set == []
    assert india_id not in ast_fixture.visible_ids
