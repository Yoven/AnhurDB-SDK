"""
The ``POST /api/v1/query`` AST grammar, mirrored client-side.

This module owns ONE thing: what the server's AST query engine actually
accepts, and the rejections the SDK issues on its behalf. The fluent surface
lives in ``builder.py``; it calls in here and never re-implements a rule.

Grammar established from the server source and confirmed against the live
router on 2026-09-13 (``service/record_ast_query.go``):

  * 17 filter columns, CASE-SENSITIVE.
  * 6 operators: ``$eq $gt $gte $lt $lte $in``. There is no ``$ne``, ``$or``,
    ``$and``, ``$not``, ``$like`` and no ``$exists``.
  * ``filters`` is FLAT (depth 2) and every predicate is ANDed.

Junior Tip [why a client-side rejection exists at all, 2026-09-14]: the server
is the authority and every check here is a duplicate of one of its rules — so a
duplicate only earns its place when it buys something the server cannot. Two
things qualify:

  1. A query the server ACCEPTS (HTTP 200) but that can never match a row. The
     caller gets an empty list and no signal at all. Silent wrong answers are
     this project's number-one failure mode, so the SDK refuses to build them.
  2. A round trip whose only possible outcome is a 400 the SDK already knows
     the text of.

Everything else — anything where the server's answer could legitimately differ
from our guess — is left to the server on purpose, so the SDK cannot drift into
forbidding something the server allows.
"""

from typing import Any, Dict, Optional

from ..client.exceptions import AnhurError, AnhurQueryError
from .operators import QueryOperator


# Columns the server allows in filters and sort.
# Must match the server AST query whitelist (record_ast_query.go:37-44).
ALLOWED_WHERE_COLUMNS = {
    "id", "uuid", "type", "dimension", "weight", "score",
    "status", "consolidated", "archived", "created_at", "updated_at",
    "prefix", "metadata", "summary",
    "superseded_by", "valid_from", "valid_until",
}

# Operator suffix → QueryOperator mapping.
# Only operators the server actually implements are included.
# $neq, $nin, $like were removed — server rejects them with HTTP 400.
OPERATOR_SUFFIXES: Dict[str, QueryOperator] = {
    "eq": QueryOperator.EQ,
    "gt": QueryOperator.GT,
    "gte": QueryOperator.GTE,
    "lt": QueryOperator.LT,
    "lte": QueryOperator.LTE,
    "in": QueryOperator.IN,
}

# The real semantic retrieval path, named in every rejection that points away
# from the AST endpoint. Kept as a constant so the three messages below cannot
# drift apart from each other.
SEMANTIC_SEARCH_ALTERNATIVE = (
    "Memory.search(...) / POST /api/v1/search, which this SDK already exposes"
)


def query_rejection(message: str) -> AnhurQueryError:
    """Build the ONE exception type every query rejection uses.

    Junior Tip [why not ValueError, 2026-09-14]: until 2.1.0 a bad column
    raised ``ValueError`` locally while the identical mistake written as a raw
    dict came back from the server as ``AnhurQueryError(kind="invalid_request")``.
    A caller had to write two ``except`` blocks for one bug class, and anyone
    who wrote only the obvious one caught half the cases. Both halves are now
    ``AnhurQueryError`` with ``kind == "invalid_request"``; the only difference
    left is ``status_code``, which is ``None`` when the request never left the
    process and ``400`` when the server answered. That distinction is real and
    worth keeping — the exception TYPE was never the place to encode it.
    """
    return AnhurQueryError(message, kind=AnhurError.KIND_INVALID_REQUEST)


def assert_filter_column(field: str, clause: str = "filters") -> None:
    """Reject a column the server's whitelist does not contain.

    ``clause`` names where the column was used so the message reads the same
    way it always has (``"... in filters"`` / ``"... in order_by"``).
    """
    if field not in ALLOWED_WHERE_COLUMNS:
        raise query_rejection(
            f"Field '{field}' is not allowed in {clause}. "
            f"Allowed: {sorted(ALLOWED_WHERE_COLUMNS)}"
        )


def assert_operator_suffix(op_suffix: str) -> None:
    """Reject an operator the server does not implement."""
    if op_suffix not in OPERATOR_SUFFIXES:
        raise query_rejection(
            f"Operator suffix '{op_suffix}' is not supported. "
            f"Allowed: {sorted(OPERATOR_SUFFIXES.keys())}"
        )


def assert_filter_value(field: str, op_suffix: str, value: Any) -> None:
    """Reject a value that is legal on the wire but can never match a row.

    Two shapes are refused here. Both are things the SDK can only learn the
    hard way otherwise:

    ``None`` as a comparison operand
        The server's scalar set includes ``nil``, so ``{"$eq": None}`` is
        accepted and answered ``200`` — but it compiles to SQL ``col = NULL``,
        which is NULL, never true, for every row in the table. The query is a
        guaranteed empty result on every input, forever. It is refused rather
        than warned about because the grammar offers NO way to express the
        intent behind it (see the message text).

    An empty ``$in`` list
        The server answers ``400 'filter "X": $in requires a non-empty array of
        values'``. The outcome is already known, so the round trip buys
        nothing.
    """
    if op_suffix == "in":
        _assert_in_list(field, value)
        return

    if value is None:
        raise query_rejection(_null_operand_message(field, op_suffix))


def _assert_in_list(field: str, value: Any) -> None:
    """Enforce the server's ``$in`` rule: a non-empty array of scalars."""
    if isinstance(value, (list, tuple, set, frozenset)):
        elements = list(value)
    else:
        # Not a sequence at all — the server owns that verdict, and its message
        # names the concrete type it received. Passing it through keeps the SDK
        # from guessing at a rule it did not write.
        return

    if not elements:
        raise query_rejection(
            f"Filter '{field}' $in [] is empty. The server rejects an empty "
            f'$in with HTTP 400 (filter "{field}": $in requires a non-empty '
            "array of values), so sending it costs a round trip to learn "
            "nothing. Pass at least one value, or drop the predicate entirely "
            "— an empty set of allowed values matches no row, which is what "
            "omitting the filter and returning early expresses honestly."
        )

    for element in elements:
        if element is None:
            raise query_rejection(_null_operand_message(field, "in"))


def _null_operand_message(field: str, op_suffix: str) -> str:
    """Explain WHY ``None`` is refused, not merely that it is.

    Junior Tip [2026-09-14]: a message that only says "None is not allowed"
    sends the reader looking for the spelling that IS allowed. There is none —
    the capability is absent from the grammar — so the message has to say that
    out loud, or the next person burns an afternoon hunting for ``$isnull``.
    """
    operator = OPERATOR_SUFFIXES[op_suffix].value
    return (
        f"Filter '{field}' {operator} None would match nothing. The server "
        f"accepts null as a scalar and answers HTTP 200, but it compiles to "
        f"SQL '{field} {_sql_shape(op_suffix)} NULL', which is never true for "
        "any row — so the query is a guaranteed empty result on every input, "
        "with no error to tell you. This is not a spelling problem: the AST "
        "grammar has no $exists, no $ne and no IS NULL, so \"this column is "
        "null\" cannot be expressed through POST /api/v1/query at all. Filter "
        "on a real value, or drop the predicate and test for null yourself "
        "once the rows come back."
    )


def _sql_shape(op_suffix: str) -> str:
    """The SQL comparison the operator becomes, for the message above."""
    return {
        "eq": "=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=", "in": "IN",
    }[op_suffix]


def semantic_search_rejection(mode: Optional[str] = None) -> AnhurQueryError:
    """Explain that the AST endpoint has no semantic leg, and name the one that has.

    Junior Tip [why this raises instead of being deleted, 2026-09-14]:
    ``QueryBuilder.semantic_search()`` shipped as a public method and is in the
    2.0 CHANGELOG. Deleting it turns every existing call into an
    ``AttributeError`` that names nothing and points nowhere. Raising turns the
    same call into a message that names the endpoint that actually does the
    work. Both are breaking — only one of them teaches. Nothing inside
    ``anhurdb/`` ever called it, so the blast radius is user code, which is
    exactly the code that needs to read this.
    """
    mode_note = f" (mode {mode!r})" if mode else ""
    return query_rejection(
        f"QueryBuilder.semantic_search(){mode_note} does nothing and has been "
        "disabled. The AST query engine accepts the 'semantic_search' key and "
        "then SKIPS it (service/record_ast_query.go:169-172 logs the block and "
        "continues), so it contributes no term to the SQL: the rows that came "
        "back were the plain filter query's rows, ranked by the plain filter "
        "query's ORDER BY, with no semantic ranking anywhere. Calling it was "
        f"indistinguishable from not calling it. Use {SEMANTIC_SEARCH_ALTERNATIVE} "
        "— that is the path that actually runs the vector/hybrid retrieval."
    )
