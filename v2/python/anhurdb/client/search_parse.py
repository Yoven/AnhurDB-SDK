"""Parsing of the ``/api/v1/search`` response envelopes.

One domain: turning the RAW decoded body the server sent into the typed
objects the caller sees. Nothing here issues a request or decides a knob.

Junior Tip [why this is its own file]: the request side and the response side
of search change for different reasons and at different times. A knob is added
to the request when a retrieval leg gains a control; a field is added here when
a leg gains a SIGNAL. Keeping them apart means a change to one cannot quietly
break the other, and neither pushes ``client/__init__.py`` further past the
~300-line cut it was already eight times over.
"""

from typing import Any, Dict, List

from ..models import (
    LegScoreSummary,
    Record,
    RetrievalMeta,
    SearchResponse,
    SearchResult,
)


def _parse_search_results(data: Any) -> List[SearchResult]:
    """Parse a raw search-endpoint envelope into typed ``SearchResult`` objects.

    Returns an empty list when the payload is not the expected envelope object.

    Junior Tip [2026-08-10 — stop discarding fields the server already sends]:
    before this fix, this function hand-picked only ``record``/``similarity``
    off each hit, so ``provenance``/``scope``/``signals`` (present on the wire
    since before this SDK existed — see ``server/model/record.go``
    ``SearchResult``) were read off the network and then thrown away before
    ever reaching a ``SearchResult`` instance. That is silent data loss of the
    exact kind CLAUDE.md calls the project's worst failure mode: a caller
    using ``shared_all`` scope had no SDK-visible way to tell which plane
    (``sessions``/``tenant_shared``/``client_shared``) answered a given hit.
    Passing the WHOLE hit dict through ``SearchResult(**hit_fields)`` (with
    only ``record`` pre-built, since it needs its own nested parse) means any
    field the server adds next also survives without another manual edit
    here — ``extra="ignore"`` on ``SearchResult`` still protects against
    truly unknown keys, it just no longer discards KNOWN ones by omission.
    """
    if not isinstance(data, dict):
        return []
    parsed: List[SearchResult] = []
    for hit in data.get("results", []):
        hit_fields: Dict[str, Any] = dict(hit)
        # Required, same as the original hit["record"] — a hit with no
        # "record" key is a malformed server response (record has no
        # omitempty on the Go side), not a value to paper over with an
        # empty placeholder. Fail loud (KeyError), never fabricate.
        hit_fields["record"] = Record(**hit_fields["record"])
        parsed.append(SearchResult(**hit_fields))
    return parsed


def _parse_search_response(data: Any) -> SearchResponse:
    """Parse a raw search-endpoint envelope into a full ``SearchResponse``
    (typed hits + the optional ADR-0012 ``retrieval`` block).

    Reuses ``_parse_search_results`` for the ``results`` array so the two
    parse paths can never disagree about how a hit is built. ``retrieval`` is
    ``None`` when the server omits the key entirely (``bundle.go``:
    ``if retrieval != nil { payload["retrieval"] = retrieval }``) — a server
    that predates ADR-0012, or a response shape that never attaches it."""
    if not isinstance(data, dict):
        return SearchResponse()
    retrieval_data = data.get("retrieval")
    # ADR-0031 (2026-09-05): ``leg_scores`` rides the envelope next to
    # ``retrieval``, and only when the request asked for ``debug_signals``.
    # ``None`` (key absent) and ``[]`` (key present, no legs) are kept apart
    # for the same reason ``related_nodes`` keeps them apart — see
    # ``SearchResponse.leg_scores``.
    leg_scores_data = data.get("leg_scores")
    parsed_leg_scores = None
    if isinstance(leg_scores_data, list):
        parsed_leg_scores = [
            LegScoreSummary(**leg_summary)
            for leg_summary in leg_scores_data
            if isinstance(leg_summary, dict)
        ]
    return SearchResponse(
        results=_parse_search_results(data),
        retrieval=RetrievalMeta(**retrieval_data) if retrieval_data else None,
        leg_scores=parsed_leg_scores,
    )


def _parse_typed_records(data: Any) -> List[SearchResult]:
    """Parse a BARE-record envelope (``{"records": [{...}, ...], "count": N}``) into
    typed ``SearchResult`` objects.

    Returns an empty list when the payload is not the expected envelope object."""
    if not isinstance(data, dict):
        return []
    return [
        SearchResult(record=Record(**record), similarity=0.0)
        for record in data.get("records", [])
    ]
