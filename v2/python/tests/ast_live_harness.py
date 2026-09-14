"""Shared fixture and wire recorder for the LIVE AST-query suite.

WHY THIS FILE EXISTS
--------------------
``POST /api/v1/query`` is the least exercised surface of this SDK, and the one
place where a mock is actively harmful: a fake server written from the SDK's own
assumptions agrees with the SDK by construction and proves nothing. Every test
that imports this module therefore talks to a REAL AnhurDB and asserts on the
RECORDS that come back, not on the HTTP status.

Junior Tip [why the fixture seeds its own session and then reads it back]: the
server rewrites parts of what you write — ``weight`` is recomputed from
``score``, ``status`` becomes ``completed`` when enrichment finishes, and
``metadata`` grows keys behind your back. Asserting against the values we SENT
would fail for reasons that have nothing to do with the query grammar. So the
fixture writes known rows, reads them back once, and that read-back list is the
ORACLE. Expectations are then computed in Python from the oracle and compared
with what SQL returns — two independent evaluations of the same predicate.

Junior Tip [the fixture-did-not-enter guard]: a live suite that silently ends up
with an empty fixture would report a wall of green while testing nothing. This
is the exact failure this project has been bitten by. ``AstFixture.guard()``
therefore FAILS (never skips) when the seeded rows are not all present, and
``tests/test_ast_query_live.py`` ends with a test that asserts the recorded case
count, so a run that quietly executed nothing cannot pass.

Write discipline: every row lives in a throwaway session whose id starts with
``ast-teste-``; nothing outside that session is ever written, patched or
deleted. The teardown hard-deletes exactly the ids it created.
"""

from __future__ import annotations

import asyncio
import json
import os
import time
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

import pytest

from anhurdb import Memory
from anhurdb.models.record import CreateRequest

# One env var arms the live suite. Without it the whole file skips, so a normal
# `pytest` run on a laptop with no key stays green and fast.
LIVE_ENABLED = os.environ.get("ANHUR_LIVE_AST") == "1"
API_KEY = os.environ.get("ANHUR_API_KEY", "")
BASE_URL = os.environ.get("ANHUR_URL", "https://anhurdb.yoven.ai")

WIRE_LOG_PATH = os.environ.get(
    "AST_WIRE_LOG",
    "/tmp/claude-1000/-home-junior-Projects-yoven-Anhur/"
    "e352e771-a074-42d0-a82e-a6151ebb7083/scratchpad/ast/py_wire.jsonl",
)

requires_live = pytest.mark.skipif(
    not (LIVE_ENABLED and API_KEY),
    reason="live AST suite needs ANHUR_LIVE_AST=1 and ANHUR_API_KEY",
)

# The rows the fixture seeds. score is the one numeric column the server keeps
# verbatim, so it is the axis every comparison operator is exercised on.
SEED_PLAN: List[Dict[str, Any]] = [
    dict(key="alpha", type="episodic", score=1, summary="ast-teste alpha episodic"),
    dict(key="bravo", type="fact", score=3, summary="ast-teste bravo fact"),
    dict(key="charlie", type="decision", score=5, summary="ast-teste charlie decision"),
    dict(key="delta", type="risk", score=7, summary="ast-teste delta risk"),
    dict(key="echo", type="task", score=9, summary="ast-teste echo task"),
    dict(key="foxtrot", type="preference", score=10, summary="ast-teste foxtrot preference"),
    dict(key="golf", type="fact", score=4, summary="ast-teste golf fact"),
    dict(key="hotel", type="fact", score=2, summary="ast-teste hotel fact"),
    # india is superseded by juliett, which proves the unconditional
    # `superseded_by IS NULL` predicate by making india invisible.
    dict(key="india", type="fact", score=2, summary="ast-teste india superseded-old"),
    dict(key="juliett", type="fact", score=2, summary="ast-teste juliett superseder"),
    # kilo carries valid_from/valid_until. Junior Tip: REST `POST /api/v1/records`
    # does NOT decode top-level valid_from/valid_until — service/record_create.go
    # only picks them up from the metadata JSON. Sending them as CreateRequest
    # fields is silently dropped, so the fixture smuggles them through metadata.
    dict(key="kilo", type="fact", score=6, summary="ast-teste kilo temporal",
         metadata={"valid_from": "2020-01-01T00:00:00Z",
                   "valid_until": "2030-01-01T00:00:00Z"}),
]

VISIBLE_KEYS = [entry["key"] for entry in SEED_PLAN if entry["key"] != "india"]


@dataclass
class AstFixture:
    """A seeded, disposable session plus the oracle read back from the server.

    Junior Tip [why this carries its own event loop instead of being an async
    fixture]: the seeded session must outlive a single test — reseeding eleven
    live records per test would multiply the write load by a hundred. A
    module-scoped ASYNC fixture, however, needs the test loop to be module-scoped
    too, and how you ask for that changed between pytest-asyncio releases (0.23
    wants an overridden ``event_loop``; 0.24+ wants ``loop_scope=``). Owning one
    loop here makes the suite independent of that moving target: the tests are
    plain sync functions that call ``fixture.run(coro)``.
    """

    memory: Memory
    session_id: str
    ids_by_key: Dict[str, int]
    loop: Any = None
    oracle: List[Dict[str, Any]] = field(default_factory=list)
    recorded_cases: List[str] = field(default_factory=list)

    def run(self, coroutine: Any) -> Any:
        """Drive one coroutine on the fixture's own loop."""
        return self.loop.run_until_complete(coroutine)

    # ---- oracle helpers -------------------------------------------------
    @property
    def visible_ids(self) -> List[int]:
        """Ids the query endpoint may return for this session, ascending."""
        return sorted(self.ids_by_key[key] for key in VISIBLE_KEYS)

    def row(self, key: str) -> Dict[str, Any]:
        record_id = self.ids_by_key[key]
        for candidate in self.oracle:
            if candidate["id"] == record_id:
                return candidate
        raise AssertionError(f"seeded row {key!r} (id {record_id}) is not in the oracle")

    def expect(self, predicate) -> List[int]:
        """Ids the oracle says a predicate must select — evaluated in Python."""
        return sorted(row["id"] for row in self.oracle if predicate(row))

    def refresh_oracle(self) -> None:
        """Re-read the seeded rows. See ``run_case_against_oracle`` for why."""
        response = self.run(self.memory._connection.post(
            "/api/v1/query",
            {"filters": {"uuid": {"$eq": self.session_id}},
             "pagination": {"limit": 200},
             "sort": [{"field": "id", "order": "asc"}]},
        ))
        self.oracle = list(response.get("records") or [])
        _LAST_WIRE["body"] = None  # an oracle read is not a recorded case

    def guard(self) -> None:
        """Fail loudly when the fixture did not enter. Never skip."""
        missing = [key for key in VISIBLE_KEYS
                   if self.ids_by_key[key] not in {row["id"] for row in self.oracle}]
        assert not missing, (
            f"fixture did not enter: seeded rows missing from the live query "
            f"response: {missing}. Every assertion below would be vacuous."
        )
        assert len(self.oracle) == len(VISIBLE_KEYS), (
            f"oracle has {len(self.oracle)} rows, expected {len(VISIBLE_KEYS)}"
        )

    # ---- wire recording -------------------------------------------------
    def record_case(self, case: str, builder_call: str, http_status: Optional[int],
                    result_ids: Optional[List[int]], error: Optional[str],
                    note: str = "") -> None:
        """Append one line to the wire log so the three SDKs can be diffed."""
        self.recorded_cases.append(case)
        line = {
            "case": case,
            "builder_call": builder_call,
            "wire_body": _LAST_WIRE.get("body"),
            "http_status": http_status,
            "result_ids": result_ids,
            "error": error,
            "note": note,
        }
        with open(WIRE_LOG_PATH, "a") as handle:
            handle.write(json.dumps(line) + "\n")
        _LAST_WIRE["body"] = None


_LAST_WIRE: Dict[str, Any] = {"body": None}


def _instrument(connection: Any) -> None:
    """Capture the EXACT bytes aiohttp will put on the wire for /api/v1/query.

    Junior Tip [why json.dumps here and not a re-dump of the dict later]:
    aiohttp's default ``json_serialize`` IS ``json.dumps``, so this string is the
    request body byte for byte — including key ORDER, which is the thing a
    cross-SDK diff is looking for. Re-serialising the dict afterwards would
    normalise exactly the difference we want to see.
    """
    original = connection._request

    async def wrapped(method, path, body=None, params=None, raw_text=False):
        if path == "/api/v1/query":
            _LAST_WIRE["body"] = json.dumps(body)
        return await original(method, path, body=body, params=params, raw_text=raw_text)

    connection._request = wrapped


def _read_until_settled(loop: Any, memory: Memory, session_id: str,
                        attempts: int = 20, pause_seconds: float = 3.0) -> List[Dict[str, Any]]:
    """Read the seeded rows until two consecutive reads agree, then freeze them.

    Junior Tip [why the oracle must wait for the write path to go quiet]: a new
    record is not final. Enrichment recomputes ``weight``, flips ``status`` to
    ``completed``, rewrites ``metadata`` and bumps ``updated_at`` seconds after
    the create returns. An oracle captured mid-flight makes every $eq assertion on
    those columns a coin flip — the test would fail because the row CHANGED, not
    because the operator broke, and that kind of flake is how a suite gets muted.
    Two identical consecutive reads mean the background writers have let go.
    """
    previous: Optional[str] = None
    rows: List[Dict[str, Any]] = []
    for _ in range(attempts):
        response = loop.run_until_complete(memory._connection.post(
            "/api/v1/query",
            {"filters": {"uuid": {"$eq": session_id}},
             "pagination": {"limit": 200},
             "sort": [{"field": "id", "order": "asc"}]},
        ))
        rows = list(response.get("records") or [])
        snapshot = json.dumps(rows, sort_keys=True, default=str)
        if snapshot == previous:
            return rows
        previous = snapshot
        time.sleep(pause_seconds)
    raise AssertionError(
        "seeded rows never stopped changing: the oracle would be unstable and "
        "every $eq assertion on status/weight/metadata would be a coin flip"
    )


@pytest.fixture(scope="session")
def ast_fixture():
    """Seed a throwaway session, yield the oracle, then delete exactly what we made."""
    if not (LIVE_ENABLED and API_KEY):
        pytest.skip("live AST suite needs ANHUR_LIVE_AST=1 and ANHUR_API_KEY")

    loop = asyncio.new_event_loop()
    session_id = f"ast-teste-py-{os.getpid()}-{int(time.time())}"
    memory = Memory(api_key=API_KEY, url=BASE_URL, timeout=180.0)
    loop.run_until_complete(memory.connect())
    _instrument(memory._connection)

    ids_by_key: Dict[str, int] = {}
    try:
        registered = loop.run_until_complete(
            memory.create_session(session_id=session_id, metadata={"purpose": "ast-teste"})
        )
        for entry in SEED_PLAN:
            # 3.0.0 signature: session and content are required positionals,
            # everything else is keyword-only. `summary` is derived from
            # `content` by the SDK (same rule as Go's truncateSummary), so it
            # is no longer something a caller can pin.
            response = loop.run_until_complete(
                memory.create(
                    registered,
                    "ast-teste content for " + entry["summary"],
                    type=entry["type"],
                    score=entry["score"],
                    metadata=entry.get("metadata") or None,
                )
            )
            ids_by_key[entry["key"]] = int(response.id or 0)

        loop.run_until_complete(memory.supersede(ids_by_key["india"], ids_by_key["juliett"]))

        rows = _read_until_settled(loop, memory, registered)
        _LAST_WIRE["body"] = None  # the oracle read is not a recorded case
        fixture = AstFixture(memory=memory, session_id=registered,
                             ids_by_key=ids_by_key, loop=loop,
                             oracle=rows)
        fixture.guard()
        # First line of the wire log: what the fixture actually is. A cross-SDK
        # diff is meaningless without the row set the cases were asserted against.
        fixture.record_case(
            "fixture.seeded_oracle", "seed + supersede + settle", 200,
            fixture.visible_ids, None,
            note=json.dumps({"session_id": registered, "ids_by_key": ids_by_key,
                             "oracle": rows}, default=str))
        yield fixture
    finally:
        for record_id in ids_by_key.values():
            try:
                loop.run_until_complete(memory.delete(record_id))
            except Exception as cleanup_error:  # pragma: no cover - best effort
                print(f"CLEANUP FAILED for record {record_id}: {cleanup_error}")
        loop.run_until_complete(memory.close())
        loop.close()
