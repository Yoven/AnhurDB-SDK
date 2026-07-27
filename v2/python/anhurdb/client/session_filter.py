"""Session filter (ADR-0014) — the ``sessions`` argument every search carries.

One grammar, three SDKs, one server. The error strings in this module are
byte-identical to the Go and TypeScript SDKs and to the AnhurDB server, so a
single support answer covers every client.
"""

from collections.abc import Sequence as AbstractSequence
from typing import Any, List

from .exceptions import AnhurError

# Junior Tip [why a wildcard instead of an optional argument]: the defect this
# model replaces was a session filter that was silently NOT applied — the caller
# asked for one chat and got five. Making the argument optional with an implicit
# "all" default reintroduces exactly that class of bug: a client that forgets the
# field, or computes an empty one, widens the query to the whole tenant with no
# signal at all. Requiring the caller to spell out "*" turns widening into a
# deliberate, auditable choice.
SESSION_WILDCARD = "*"

# Junior Tip: this is a product limit, not a technical one — the server's
# vendored SQLite accepts ~32k bound parameters. The cap exists so that "I need
# thousands of sessions" surfaces as a design conversation (a grouping label)
# instead of an ever-growing IN clause. The SDK enforces it locally so the caller
# learns before paying for the round trip; the server enforces the same number
# independently, because a client-side check is a convenience, never a guarantee.
MAX_SESSION_FILTER_UUIDS = 1000


def sessions_all() -> List[str]:
    """Return the wildcard filter — every session inside the search scope.

    Prefer it over a hand-written ``["*"]`` so the intent reads at the call
    site::

        hits = await mem.search("what does this user do?", sessions_all())

    Mirrors Go ``client.SessionsAll()`` and TypeScript ``sessionsAll()``.
    """
    return [SESSION_WILDCARD]


def normalize_sessions(raw_sessions: Any) -> List[str]:
    """Validate the caller's ``sessions`` argument and return the wire value.

    The contract, in full (ADR-0014 D1/D2)::

        ["*"]                  → every session in the scope boundary
        ["uuid-a"]             → exactly that session
        ["uuid-a","uuid-b"]    → exactly those, up to MAX_SESSION_FILTER_UUIDS
        []                     → error: ambiguous
        None                   → error: the argument is mandatory
        ["*","uuid-a"]         → error: contradictory

    Junior Tip [why empty and contradictory are errors, not lenient defaults]:
    every rejected case here is a caller that does not know what it wants.
    Guessing on their behalf is how a scoping bug becomes a data-exposure bug.
    Failing gives them a message they can act on; guessing gives them results
    they cannot audit.

    Raises:
        AnhurError: with an ``INVALID_PARAM: ...`` message on every rejection.
    """
    if raw_sessions is None:
        raise AnhurError(
            "INVALID_PARAM: 'sessions' is required; "
            'use ["*"] for every session in scope'
        )

    # Junior Tip [why str is rejected explicitly]: a bare string is iterable in
    # Python, so `sessions="conv-a"` would silently become the character list
    # ['c','o','n','v',...] — 6 nonsense session ids and an empty result set the
    # caller would spend an afternoon debugging. Fail on the type instead.
    if isinstance(raw_sessions, (str, bytes)) or not isinstance(
        raw_sessions, AbstractSequence
    ):
        raise AnhurError("INVALID_PARAM: 'sessions' must be a list of strings")

    if len(raw_sessions) == 0:
        raise AnhurError(
            "INVALID_PARAM: 'sessions' cannot be empty; "
            'use ["*"] for every session in scope'
        )

    cleaned: List[str] = []
    wildcard_seen = False
    for raw_session in raw_sessions:
        if not isinstance(raw_session, str):
            raise AnhurError("INVALID_PARAM: 'sessions' must be a list of strings")
        session = raw_session.strip()
        if session == "":
            raise AnhurError("INVALID_PARAM: 'sessions' contains an empty entry")
        if session == SESSION_WILDCARD:
            wildcard_seen = True
            continue
        cleaned.append(session)

    if wildcard_seen:
        if cleaned:
            raise AnhurError(
                f'INVALID_PARAM: \'sessions\' mixes "{SESSION_WILDCARD}" with '
                f"{len(cleaned)} explicit session(s); the wildcard must stand alone"
            )
        return [SESSION_WILDCARD]

    # Junior Tip: dedup before the cap so a caller repeating the same session is
    # not punished for it, and so the server never binds the same value twice.
    deduplicated: List[str] = []
    seen = set()
    for session in cleaned:
        if session in seen:
            continue
        seen.add(session)
        deduplicated.append(session)

    if len(deduplicated) > MAX_SESSION_FILTER_UUIDS:
        raise AnhurError(
            f"INVALID_PARAM: at most {MAX_SESSION_FILTER_UUIDS} sessions per "
            f'request (got {len(deduplicated)}); use ["*"] for all'
        )

    return deduplicated
