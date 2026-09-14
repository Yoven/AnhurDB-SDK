"""The client-side half of the AST query error contract.

Every case here is a query the SDK refuses to build or send, and the point of
each refusal is that the ALTERNATIVE is silent. Three shapes appear:

  * ``$eq None`` (and every other comparison against ``None``) — the server
    answers HTTP 200 and zero rows, forever, on every input.
  * ``$in []`` — the server answers HTTP 400 with a message the SDK already
    knows, so the round trip buys nothing.
  * ``semantic_search()`` — the server accepts the key and SKIPS it, so the
    caller gets a plain filter query while believing they got semantic ranking.

The fourth assertion running through all of them is the TYPE. Until 2.1.0 a
rejection caught locally was a ``ValueError`` / ``TypeError`` / ``RuntimeError``
with no ``status_code``, while the identical mistake caught by the server was an
``AnhurQueryError`` with ``kind="invalid_request"`` and ``status_code=400``. One
bug class, two except blocks, and the local half silently uncaught by anyone who
wrote the obvious one. These tests pin the single shape.

No mocks and no network: these drive the REAL ``QueryBuilder`` and assert on the
exception it raises. The grammar they are written against was established from
``service/record_ast_query.go`` and confirmed live on 2026-09-13.
"""

from __future__ import annotations

import asyncio

import pytest

from anhurdb.client.exceptions import AnhurError, AnhurQueryError
from anhurdb.client.query_argument import compile_query_argument
from anhurdb.query import Filter, QueryBuilder
from anhurdb.query.operators import SemanticMode


def assert_client_rejection_shape(error: AnhurQueryError) -> None:
    """The one shape every client-side query rejection must have.

    Junior Tip [why status_code must be None and not 400]: the type unifies the
    two halves so one ``except`` block catches both; ``status_code`` is what
    still tells them apart. A rejection that never left the process must not
    claim an HTTP status it never received — a caller counting 400s to decide
    "the server is unhappy with me" would be counting its own bugs.
    """
    assert isinstance(error, AnhurQueryError)
    assert isinstance(error, AnhurError)
    assert error.kind == AnhurError.KIND_INVALID_REQUEST
    assert error.status_code is None
    assert error.retryable is False
    assert str(error), "an unexplained rejection is unactionable"


# ── $eq None and friends: accepted by the wire, never true in SQL ───────────

@pytest.mark.parametrize("kwargs", [
    {"type": None},
    {"type__eq": None},
    {"status": None},
    {"superseded_by": None},
])
def test_a_null_equality_filter_is_refused_because_it_can_never_match(kwargs):
    """``col = NULL`` is NULL, not true — 200 with zero rows on every input."""
    with pytest.raises(AnhurQueryError) as caught:
        QueryBuilder().where(**kwargs)
    assert_client_rejection_shape(caught.value)


@pytest.mark.parametrize("suffix,sql", [
    ("gt", ">"), ("gte", ">="), ("lt", "<"), ("lte", "<="),
])
def test_every_comparison_against_none_is_refused_for_the_same_reason(suffix, sql):
    """$gt/$gte/$lt/$lte against NULL are equally never-true; same refusal."""
    with pytest.raises(AnhurQueryError) as caught:
        QueryBuilder().where(**{f"weight__{suffix}": None})
    assert_client_rejection_shape(caught.value)
    assert f"weight {sql} NULL" in str(caught.value)


def test_a_null_inside_an_in_list_is_refused_too():
    """``col IN (NULL, 'fact')`` never matches the NULL branch — same trap."""
    with pytest.raises(AnhurQueryError) as caught:
        QueryBuilder().where(type__in=["fact", None])
    assert_client_rejection_shape(caught.value)


def test_the_null_refusal_explains_that_the_intent_is_inexpressible():
    """A message that only forbids sends the reader hunting for $isnull.

    The grammar has no $exists, no $ne and no IS NULL, so there is no spelling
    that works. The message has to say so, and name the way out.
    """
    with pytest.raises(AnhurQueryError) as caught:
        QueryBuilder().where(status=None)
    message = str(caught.value)
    assert "$exists" in message
    assert "$ne" in message
    assert "IS NULL" in message
    assert "never true" in message
    assert "HTTP 200" in message


def test_a_refused_value_never_lands_in_the_filters_dict():
    """The check fires before mutation: a rejected builder is still usable."""
    builder = QueryBuilder().where(type="fact")
    with pytest.raises(AnhurQueryError):
        builder.where(status=None)
    assert builder.build_ast()["filters"] == {"type": {"$eq": "fact"}}


def test_false_and_zero_and_empty_string_are_still_legal_values():
    """Only None is refused. Falsy is not null, and the server matches on it."""
    compiled = QueryBuilder().where(
        archived=False, weight__gte=0, summary="",
    ).build_ast()
    assert compiled["filters"]["archived"] == {"$eq": False}
    assert compiled["filters"]["weight"] == {"$gte": 0}
    assert compiled["filters"]["summary"] == {"$eq": ""}


# ── $in []: a 400 the SDK can predict, so it should not cost a round trip ───

@pytest.mark.parametrize("empty", [[], (), set(), frozenset()])
def test_an_empty_in_list_is_refused_before_the_socket_is_touched(empty):
    with pytest.raises(AnhurQueryError) as caught:
        QueryBuilder().where(type__in=empty)
    assert_client_rejection_shape(caught.value)
    assert "$in requires a non-empty array of values" in str(caught.value)


def test_a_single_element_in_list_is_fine():
    """The refusal is about EMPTY, not about size."""
    compiled = QueryBuilder().where(type__in=["fact"]).build_ast()
    assert compiled["filters"]["type"]["$in"] == ["fact"]


def test_a_non_sequence_in_value_is_left_to_the_server():
    """Deliberate: the server owns that verdict and names the concrete type.

    Junior Tip [why the SDK stops short here]: a client-side rule the server
    did not write is a rule that drifts. The two checks above are safe to
    duplicate because their outcome is fixed by SQL semantics and by an error
    string that is in the server source. "Is this thing array-like enough" is
    not — so it goes on the wire and comes back as a 400 with the server's own
    wording.
    """
    compiled = QueryBuilder().where(type__in="fact").build_ast()
    assert compiled["filters"]["type"]["$in"] == "fact"


# ── semantic_search(): a public method that provably did nothing ────────────

def test_semantic_search_refuses_and_names_the_endpoint_that_works():
    with pytest.raises(AnhurQueryError) as caught:
        QueryBuilder().semantic_search("cluster health")
    assert_client_rejection_shape(caught.value)
    message = str(caught.value)
    assert "record_ast_query.go:169-172" in message, "cite the skip, not just the ban"
    assert "POST /api/v1/search" in message
    assert "Memory.search" in message


def test_semantic_search_refuses_for_every_mode():
    for mode in SemanticMode:
        with pytest.raises(AnhurQueryError):
            QueryBuilder().semantic_search("anything", mode)


def test_the_semantic_search_pseudo_key_can_still_be_sent_through_filter():
    """The escape hatch stays open — refusing the builder is not censorship.

    Anyone probing what the server does with the key can still put it on the
    wire by hand. What they cannot do any more is get it there by calling a
    method whose name promises semantics it never delivered.
    """
    compiled = Filter({"semantic_search": {"query": "x", "mode": "$hybrid"}}).ast()
    assert compiled["filters"]["semantic_search"] == {"query": "x", "mode": "$hybrid"}


# ── The other two rejections that used to escape the AnhurQueryError net ────

def test_query_with_a_non_ast_argument_raises_the_query_error_type():
    with pytest.raises(AnhurQueryError) as caught:
        compile_query_argument("not an ast")
    assert_client_rejection_shape(caught.value)
    assert "needs an AST dict or a QueryBuilder/Filter" in str(caught.value)
    assert "got str" in str(caught.value)


def test_execute_without_an_executor_raises_the_query_error_type():
    with pytest.raises(AnhurQueryError) as caught:
        asyncio.run(QueryBuilder().execute())
    assert_client_rejection_shape(caught.value)
    assert "No executor was provided" in str(caught.value)


# ── The whole point: ONE except block ───────────────────────────────────────

def test_one_except_block_catches_every_client_side_query_rejection():
    """Written the way a caller would write it, over all the shapes at once."""
    offenders = [
        lambda: QueryBuilder().where(bogus_col="x"),
        lambda: QueryBuilder().where(TYPE="fact"),
        lambda: QueryBuilder().where(type__ne="x"),
        lambda: QueryBuilder().where(type=None),
        lambda: QueryBuilder().where(type__in=[]),
        lambda: QueryBuilder().semantic_search("x"),
        lambda: QueryBuilder().order_by("bogus", "asc"),
        lambda: QueryBuilder().order_by("id", "sideways"),
        lambda: QueryBuilder().limit(0),
        lambda: QueryBuilder().limit(1001),
        lambda: QueryBuilder().offset(-1),
        lambda: compile_query_argument(object()),
    ]
    for offender in offenders:
        try:
            offender()
        except AnhurQueryError as rejection:
            assert_client_rejection_shape(rejection)
        else:
            pytest.fail(f"{offender} was accepted and should not have been")
