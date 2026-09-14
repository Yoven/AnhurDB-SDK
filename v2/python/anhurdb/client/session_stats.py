"""Auto-pagination for ``GET /api/v1/sessions/stats``.

WHY THIS FILE EXISTS
--------------------
The endpoint is paged, and it says so honestly in its envelope. Measured live
against the owner tenant on 2026-09-14::

    no params   -> 50 sessions, has_more: true,  next_offset: 50
    ?limit=500  -> 99 sessions, has_more: false, next_offset: 0

``Memory.list_sessions()`` used to send no paging params at all and return
``data.get("sessions", data)`` — whatever the FIRST page happened to hold, with
``has_more`` thrown away. A caller asking for "all my sessions" silently got 50
of 99: no error, no warning, and no way to distinguish a small tenant from a
truncated answer.

The Go SDK (``client/client.go``, ``ListSessions``) has always paged correctly.
This module is Python catching up, deliberately mirroring Go's page size and its
advance rule so the three SDKs put identical requests on the wire.

It lives in its own file because ``anhurdb/client/__init__.py`` is long past the
~300-line house cut, and house law forbids growing a file already over it.
"""

from __future__ import annotations

from typing import Any, Dict, List

from .exceptions import AnhurError

# Page size requested for every sessions/stats call.
#
# 500 is the server's documented maximum and the exact value Go's
# ``listSessionsPageLimit`` uses. Keeping the three SDKs on one number means a
# server-side paging change is caught by all of them at once, instead of by
# whichever one happened to ask for the unlucky size.
SESSION_STATS_PAGE_LIMIT = 500

# Hard ceiling on pages followed in one ``list_sessions()`` call.
#
# Junior Tip [why a ceiling AND a monotonic check, 2026-09-14]: ``while
# has_more`` is a loop whose exit condition is owned entirely by the remote end.
# A server bug that answers ``has_more: true, next_offset: 0`` forever turns an
# SDK call into an infinite loop that allocates until the process dies. Two
# independent brakes guard against that:
#
#   1. the offset must strictly INCREASE every iteration (mirroring Go), so a
#      stuck ``next_offset`` falls back to ``offset + limit`` and still moves;
#   2. this ceiling, so even a server that advances forever terminates.
#
# 1000 pages x 500 rows = 500,000 sessions, several orders of magnitude past any
# real tenant, so the ceiling cannot truncate an honest answer.
#
# Hitting it RAISES rather than returning the partial list. Returning what we
# have would recreate the exact defect this module exists to fix: a short answer
# that looks complete. Silent loss is the failure mode this project has been
# bitten by most; a loud error is the correct trade.
SESSION_STATS_MAX_PAGES = 1000


async def fetch_all_session_stats(connection: Any) -> List[Dict[str, Any]]:
    """Fetch EVERY session's aggregate stats, following the server's pagination.

    Requests ``limit=500&offset=N`` and keeps going while the envelope reports
    ``has_more: true``, concatenating the pages in server order.

    Two response shapes are accepted, matching Go:

    * the envelope ``{"sessions": [...], "has_more": ..., "next_offset": ...}``
    * a legacy BARE LIST ``[...]``, treated as one complete page.

    Junior Tip [the bare list is not dead code]: older deployments answer with a
    naked array and no envelope. The pre-2026-09-14 Python code handled that
    (``data.get("sessions", data)``) and it must keep working, or a tenant full
    of sessions reports as having none on those deployments.

    Args:
        connection: An ``HTTPConnection``-shaped object exposing
                    ``await get(path, params)``.

    Returns:
        Every session in the tenant, in server order.

    Raises:
        AnhurError: kind ``"server"``, when the server never stops paging."""
    all_sessions: List[Dict[str, Any]] = []
    page_offset = 0

    for _page_number in range(SESSION_STATS_MAX_PAGES):
        response_body = await connection.get(
            "/api/v1/sessions/stats",
            params={
                "limit": str(SESSION_STATS_PAGE_LIMIT),
                "offset": str(page_offset),
            },
        )

        # Legacy bare list: one page, no envelope, nothing to follow.
        if isinstance(response_body, list):
            return all_sessions + response_body
        # Empty body or JSON null: no sessions, and not an error.
        if not isinstance(response_body, dict):
            return all_sessions

        page_sessions = response_body.get("sessions")
        if isinstance(page_sessions, list):
            all_sessions.extend(page_sessions)

        if response_body.get("has_more") is not True:
            return all_sessions

        # Prefer the server's next_offset, but only when it actually moved
        # forward; otherwise step by our own page size. Identical to Go.
        advertised_next_offset = response_body.get("next_offset")
        if isinstance(advertised_next_offset, int) and advertised_next_offset > page_offset:
            page_offset = advertised_next_offset
        else:
            page_offset += SESSION_STATS_PAGE_LIMIT

    raise AnhurError(
        "list_sessions: GET /api/v1/sessions/stats still reported has_more=true "
        f"after {SESSION_STATS_MAX_PAGES} pages of {SESSION_STATS_PAGE_LIMIT} "
        f"({SESSION_STATS_MAX_PAGES * SESSION_STATS_PAGE_LIMIT} sessions read, "
        f"last offset {page_offset}). The server is not terminating its own "
        "pagination; returning the partial list here would hide that behind a "
        "plausible-looking answer, which is the bug this pagination was added "
        "to fix.",
        kind=AnhurError.KIND_SERVER,
    )
