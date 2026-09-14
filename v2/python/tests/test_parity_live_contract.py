"""LIVE re-proof of the 3.0.0 response contract against the running server.

The offline suite decodes the field sets CAPTURED on 2026-09-14
(``live_field_sets.py``). This file asks the real server the same questions and
fails when the answer has moved — because a fixture that mirrors the
implementation is not a test, it is a second copy of the assumption.

Armed by ``ANHUR_LIVE_AST=1`` plus ``ANHUR_API_KEY``, the same gate the AST
live suite already uses. Without them the whole module skips.

Junior Tip [why every call here is a GET/read]: this runs against real tenants.
The only writes in this parity round happen in a disposable session whose id
starts with ``paridade-``, driven by ``paridade_live_writes.py`` and deleted
afterwards. A test that writes into a tenant it does not own is a test nobody
can safely re-run.
"""

from __future__ import annotations

import os
from typing import Any, Dict, List, Set

import pytest

from anhurdb import Memory, sessions_all

from live_field_sets import WIRE_CONTRACTS

LIVE_ENABLED = os.environ.get("ANHUR_LIVE_AST") == "1"
API_KEY = os.environ.get("ANHUR_API_KEY", "")
BASE_URL = os.environ.get("ANHUR_URL", "https://anhurdb.yoven.ai")

requires_live = pytest.mark.skipif(
    not (LIVE_ENABLED and API_KEY),
    reason="live parity contract needs ANHUR_LIVE_AST=1 and ANHUR_API_KEY",
)

MODELS_BY_NAME = {contract[0].__name__: contract[0] for contract in WIRE_CONTRACTS}

# A seed that exists in the fable-1 tenant and has neighbours — the same one the
# spec's live evidence used (5 nodes / 7 edges at depth 2).
WALK_SEED_ID = 18


@pytest.fixture
async def live_memory():
    """One client per test. ``Memory`` owns an aiohttp session, so it is opened
    and closed inside the test's own loop rather than shared across loops."""
    if not (LIVE_ENABLED and API_KEY):
        pytest.skip("live parity contract needs ANHUR_LIVE_AST=1 and ANHUR_API_KEY")
    async with Memory(api_key=API_KEY, url=BASE_URL) as memory:
        yield memory


def assert_model_covers(model_name: str, body: Any) -> None:
    """The assertion that actually catches a server that moved.

    ``set(model.model_fields) >= set(live_body.keys())``. A test that only
    checks the fields it already knows about cannot catch a NEW key, and a new
    key is silently dropped by ``extra="ignore"`` — the caller sees a default
    and cannot tell it apart from a real zero.
    """
    assert isinstance(body, dict), f"{model_name}: expected an object, got {type(body)}"
    declared: Set[str] = set(MODELS_BY_NAME[model_name].model_fields)
    undeclared = set(body.keys()) - declared
    assert not undeclared, (
        f"{model_name} does not declare {sorted(undeclared)}, which the LIVE "
        f"server sent. extra='ignore' is dropping them silently."
    )


# ── profile (§6 + §8.2) ──────────────────────────────────────────────────


@requires_live
async def test_profile_envelope_is_exactly_three_blocks(live_memory):
    body = await live_memory._connection.get(
        "/api/v1/profile", params={"tag": live_memory.container_tag}
    )
    assert_model_covers("ProfileResult", body)
    assert set(body.keys()) == {"static", "dynamic", "stats"}, (
        "the profile envelope has no `tag` and no `status` — Go declared both "
        "and received neither"
    )
    assert_model_covers("ProfileStatic", body["static"])
    assert_model_covers("ProfileDynamic", body["dynamic"])
    assert_model_covers("ProfileStats", body["stats"])
    assert "last_active" in body["stats"], "the PROFILE block spells it last_active"


@requires_live
async def test_unknown_profile_tag_is_an_empty_profile_with_200(live_memory):
    """Live 2026-09-14: an unknown tag is 200 + all zeros, never a 404."""
    profile = await live_memory.profile("totally-not-a-real-tag-xyz")
    assert profile.stats.total_records == 0
    assert profile.stats.sessions == 0
    assert profile.stats.last_active == ""


@requires_live
async def test_profile_never_sends_an_empty_tag(live_memory):
    """An omitted tag is a guaranteed HTTP 400; the SDK must refuse locally."""
    with pytest.raises(ValueError):
        await live_memory.profile("")


# ── sessions (§8.6) ──────────────────────────────────────────────────────


@requires_live
async def test_session_stats_row_spells_it_last_activity(live_memory):
    body = await live_memory._connection.get(
        "/api/v1/sessions/stats", params={"limit": "2", "offset": "0"}
    )
    assert "next_offset" in body, "this route DOES carry a cursor"
    rows: List[Dict[str, Any]] = body.get("sessions") or []
    assert rows, "tenant has no sessions — the row shape cannot be proved"
    assert_model_covers("SessionStats", rows[0])
    assert "last_activity" in rows[0], (
        "the SESSION row spells it last_activity; TypeScript read last_active "
        "and therefore read undefined on every session"
    )


# ── entity graph (§2 + §8.11) ────────────────────────────────────────────


@requires_live
async def test_entity_graph_default_depth_is_one(live_memory):
    """The whole point of §2: omitting depth must give the SERVER's default."""
    omitted = await live_memory._connection.get("/api/v1/entities/1/graph")
    explicit_two = await live_memory._connection.get(
        "/api/v1/entities/1/graph", params={"depth": "2"}
    )
    assert_model_covers("EntityGraphResult", omitted)
    assert omitted["depth"] == 1, "handler/entity.go:280 sets depth := 1"
    assert explicit_two["depth"] == 2, "depth is not cosmetic — it changes the graph"

    through_sdk = await live_memory.get_entity_graph(1)
    assert through_sdk.depth == 1, (
        "the SDK with no depth must land on the server default; before 3.0.0 "
        "Python sent depth=2 and got a strictly larger graph than Go/TypeScript"
    )

    nodes = omitted.get("nodes") or []
    if nodes:
        assert_model_covers("EntityGraphNode", nodes[0])
        edges = nodes[0].get("edges") or []
        if edges:
            assert_model_covers("EntityGraphEdge", edges[0])


# ── manifest (§8.7) ──────────────────────────────────────────────────────


@requires_live
async def test_manifest_sends_no_cursor(live_memory):
    body = await live_memory._connection.get(
        "/api/v1/manifest", params={"limit": "2", "offset": "0"}
    )
    assert_model_covers("ManifestResult", body)
    assert "next_offset" not in body, (
        "the manifest routes have no cursor — modelling one would give every "
        "caller a permanently-zero field that reads like page one"
    )


# ── entities list (§8.10 + EntitiesPage) ─────────────────────────────────


@requires_live
async def test_entities_list_carries_the_cursor(live_memory):
    body = await live_memory._connection.get(
        "/api/v1/entities/list", params={"limit": "2", "offset": "0"}
    )
    assert_model_covers("EntitiesPage", body)
    assert "next_offset" in body, (
        "unlike the manifest, THIS route sends a cursor — the two must not be "
        "modelled with one shared page type"
    )
    rows = body.get("entities") or []
    if rows:
        assert "entity_type" in rows[0], "the wire key is entity_type, never type"


# ── smart search (§4) ────────────────────────────────────────────────────


@requires_live
async def test_smart_search_envelope_and_hit(live_memory):
    raw = await live_memory._connection.get(
        "/api/v1/search/smart",
        params=[("q", "memory"), ("sessions", "*"), ("limit", "2"),
                ("scope", "sessions")],
    )
    assert_model_covers("SmartSearchResponse", raw)
    assert raw["bundle_ordering"] == "smart_relevance"

    typed = await live_memory.smart_search("memory", sessions_all(), limit=2)
    rows = raw.get("results")
    if rows:
        assert_model_covers("SmartSearchHit", rows[0])
        assert typed.results is not None
        assert len(typed.results) == len(rows)
    else:
        # The nil-slice `null`, live. This is the case a non-nullable type
        # compiles against and then throws on.
        assert rows is None
        assert typed.results is None


# ── walk (§8.3) ──────────────────────────────────────────────────────────


@requires_live
async def test_walk_envelope_has_truncated_and_no_start_id(live_memory):
    body = await live_memory._connection.post(
        "/api/v1/walk", {"seed_id": WALK_SEED_ID, "depth": 2, "max_nodes": 50}
    )
    assert_model_covers("WalkResult", body)
    assert set(body.keys()) == {"nodes", "edges", "truncated"}, (
        "there is no start_id and no depth on this envelope — Go declared both"
    )
    edges = body.get("edges") or []
    if edges:
        assert set(edges[0].keys()) == {"source", "target"}, (
            "the edge is exactly two ids; TypeScript declared a `type` it "
            "never received"
        )
    nodes = body.get("nodes") or []
    if nodes:
        # Full records, not a lossy {id, summary} projection.
        for required_key in ("uuid", "related_ids", "status", "weight", "metadata"):
            assert required_key in nodes[0], (
                f"walk nodes are FULL records; `{required_key}` is missing, so "
                f"a projection type would be silently lossy"
            )
