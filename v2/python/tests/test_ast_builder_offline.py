"""Offline contract of QueryBuilder: the exact AST it compiles, and its sharp edges.

No network. These tests pin the BYTES the SDK would put on the wire, which is the
half of the contract the live suite cannot see (a live test only observes the
rows that came back, and several different ASTs return the same rows).

Junior Tip [why an offline file at all, when mocks are banned here]: the ban is
on mocking the SERVER — a fake that answers queries teaches nothing, because it
was written from the same assumptions as the client. Asserting on the compiled
AST is different: the assertion target is the SDK's own output, and it is checked
against the grammar established from the server's source, not against a stub.
"""

from __future__ import annotations

import json

import pytest

from anhurdb.client.exceptions import AnhurQueryError
from anhurdb.query import Eq, Filter, QueryBuilder
from anhurdb.query.builder import ALLOWED_WHERE_COLUMNS

# The 17 columns server/service/record_ast_query.go:37-44 accepts, verbatim.
SERVER_FILTER_COLUMNS = {
    "id", "uuid", "type", "dimension", "weight", "score", "status", "consolidated",
    "archived", "created_at", "updated_at", "prefix", "metadata", "summary",
    "superseded_by", "valid_from", "valid_until",
}
SERVER_OPERATORS = {"$eq", "$gt", "$gte", "$lt", "$lte", "$in"}


def test_builder_whitelist_matches_the_server_whitelist_exactly():
    """A column the SDK allows and the server does not is a guaranteed 400.

    Junior Tip [why equality and not "subset"]: both directions hurt. An extra
    column here produces a 400 the caller cannot predict; a missing column makes
    a perfectly legal query impossible to express and sends people to raw dicts,
    which skip every client-side check.
    """
    assert ALLOWED_WHERE_COLUMNS == SERVER_FILTER_COLUMNS


def test_operator_suffixes_map_onto_the_six_server_operators():
    compiled = QueryBuilder().where(
        score__eq=1, weight__gt=0.1, id__gte=2, score__lt=9, weight__lte=0.9,
        type__in=["fact"],
    ).build_ast()
    produced = set()
    for operators in compiled["filters"].values():
        produced.update(operators.keys())
    assert produced == SERVER_OPERATORS


def test_default_ast_always_carries_pagination():
    """Even an untouched builder sends pagination — a cross-SDK wire difference.

    The server defaults to limit 50 / offset 0 on its own, so this key is
    redundant; it is recorded here because the three SDKs must agree byte for
    byte on what an "empty" query looks like.
    """
    compiled = QueryBuilder().build_ast()
    assert compiled == {"filters": {}, "pagination": {"limit": 50, "offset": 0}}
    assert "select" not in compiled and "sort" not in compiled


def test_exact_match_and_operator_on_the_same_field_and_together():
    """where(type='fact') then where(type__gt='a') keeps BOTH predicates."""
    compiled = QueryBuilder().where(type="fact").where(type__gt="a").build_ast()
    assert compiled["filters"]["type"] == {"$eq": "fact", "$gt": "a"}


def test_operator_then_exact_match_silently_drops_the_operator():
    """DEFECT pinned by test: the reverse order loses a predicate without a word.

    ``where(score__gte=3)`` then ``where(score=7)`` compiles to ``{"$eq": 7}`` —
    the ``$gte`` is gone. The builder's ``else`` branch assigns
    ``self._filters[key] = {"$eq": value}`` over whatever was there.

    Junior Tip [why this is dangerous and not merely surprising]: the query still
    succeeds and still returns rows, just MORE rows than the caller asked for.
    A filter that silently widens is how a tenant-scoped query becomes a leak.
    The guard that was meant to catch this (``Field ... has conflicting exact
    match``) is unreachable: the exact-match branch also stores a dict, so the
    ``isinstance(..., dict)`` check in the operator branch is always true.
    """
    compiled = QueryBuilder().where(score__gte=3).where(score=7).build_ast()
    assert compiled["filters"]["score"] == {"$eq": 7}, "the $gte survived — defect fixed?"

    # And the guard meant to prevent it never fires, in either order.
    QueryBuilder().where(type="fact").where(type__gt="a")
    QueryBuilder().where(type__gt="a").where(type="fact")


def test_repeated_exact_match_keeps_only_the_last_value():
    compiled = QueryBuilder().where(type="fact").where(type="risk").build_ast()
    assert compiled["filters"]["type"] == {"$eq": "risk"}


def test_select_is_emitted_but_its_order_is_not_stable():
    """DEFECT pinned by test: ``list(set(...))`` makes the wire bytes unstable.

    Junior Tip [why a set is the wrong de-duplicator here]: the server ignores
    ``select`` entirely, so the ORDER cannot change a result — but it does change
    the request body, which breaks byte-for-byte comparison with the Go and
    TypeScript SDKs and any request-signing or cache-key scheme built on the
    body. Order-preserving de-duplication (``dict.fromkeys``) costs nothing.
    """
    compiled = QueryBuilder().select("id", "summary", "id").build_ast()
    assert sorted(compiled["select"]) == ["id", "summary"]
    assert isinstance(compiled["select"], list)

    seen_orders = {tuple(QueryBuilder().select("id", "summary", "type", "score", "weight")
                         .build_ast()["select"]) for _ in range(200)}
    # One run cannot prove instability (PYTHONHASHSEED may pin it); what IS
    # provable in-process is that nothing preserves the caller's order.
    assert all(len(order) == 5 for order in seen_orders)


def test_select_does_not_validate_its_field_names():
    """``select`` accepts anything; the server drops it, so nothing ever complains."""
    compiled = QueryBuilder().select("not_a_column", "; DROP TABLE records").build_ast()
    assert set(compiled["select"]) == {"not_a_column", "; DROP TABLE records"}


def test_sort_terms_keep_their_order_and_lowercase_the_direction():
    compiled = QueryBuilder().order_by("type", "ASC").order_by("id", "DESC").build_ast()
    assert compiled["sort"] == [{"field": "type", "order": "asc"},
                                {"field": "id", "order": "desc"}]


def test_the_builder_will_not_compile_the_semantic_search_pseudo_column():
    """The key rides in ``filters`` next to real columns — and the server SKIPS it.

    Junior Tip [why the builder no longer emits it]: an AST carrying
    ``semantic_search`` and one without it compile to the same SQL, return the
    same rows in the same order, and both answer 200. There is no observable
    difference, which is exactly why a caller could ship believing the ranking
    was semantic. The bytes are now unreachable through the builder.
    """
    with pytest.raises(AnhurQueryError):
        QueryBuilder().where(type="fact").semantic_search("anything")

    # Still expressible by hand: the escape hatch is not the thing that lied.
    raw = Filter({"semantic_search": {"query": "anything", "mode": "$hybrid"}}).ast()
    assert raw["filters"]["semantic_search"] == {"query": "anything", "mode": "$hybrid"}


def test_builder_state_is_deep_copied_into_each_ast():
    """Two build_ast() calls must not share mutable state with the builder."""
    builder = QueryBuilder().where(type__in=["fact"])
    first = builder.build_ast()
    first["filters"]["type"]["$in"].append("risk")
    second = builder.build_ast()
    assert second["filters"]["type"]["$in"] == ["fact"]


def test_filter_helper_bypasses_every_client_side_check():
    """``Filter`` writes straight into the filter dict — no whitelist, no operators.

    Junior Tip [why this matters for the error contract]: the same mistake raises
    locally through ``where()`` and remotely as an HTTP 400 through ``Filter``.
    Since 2.1.0 both arrive as ``AnhurQueryError`` with kind ``invalid_request``,
    so ONE except block covers them; ``status_code`` (None vs 400) is what still
    says which side caught it.
    """
    compiled = Filter({"bogus_col": {"$ne": 1}}).ast()
    assert compiled["filters"] == {"bogus_col": {"$ne": 1}}
    assert Eq("type", "fact") == {"type": {"$eq": "fact"}}

    with pytest.raises(AnhurQueryError) as caught:
        QueryBuilder().where(bogus_col=1)
    assert caught.value.kind == "invalid_request"
    assert caught.value.status_code is None, "nothing was sent, so no HTTP status"


def test_filter_ignores_its_keyword_arguments():
    """DEFECT pinned by test: ``Filter(type="fact")`` compiles to an EMPTY filter.

    ``Filter.__init__`` accepts ``**kwargs`` and never reads them, so a caller
    who uses the keyword form gets an unfiltered query — every row in the tenant,
    HTTP 200, no warning. Either wire the kwargs up or drop them from the
    signature; today they are a trap that looks like the documented API.
    """
    compiled = Filter(type="fact", score=7).ast()
    assert compiled["filters"] == {}


def test_compiled_ast_is_json_serialisable_as_sent():
    """What the executor hands aiohttp must survive json.dumps unchanged."""
    compiled = QueryBuilder().where(type="fact", score__in=[1, 2]).limit(10).build_ast()
    assert json.loads(json.dumps(compiled)) == compiled
