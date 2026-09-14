"""What the SDK puts ON THE WIRE for the 3.0.0 parity round.

One responsibility: prove the REQUEST is right. The sibling
``test_parity_models_offline.py`` proves the RESPONSE decoding is right.

Every test here fails against the pre-3.0.0 code. Each docstring names the
exact old behaviour it catches, because a regression test whose failure mode is
not written down is a test the next maintainer will "fix" by deleting.
"""

from __future__ import annotations

import json
from typing import Any, Dict, List, Tuple

import pytest

from anhurdb import Memory, MemoryType


class RecordingConnection:
    """A stand-in for ``HTTPConnection`` that captures the call instead of sending it."""

    def __init__(self, response: Any = None) -> None:
        self.response = response if response is not None else {}
        self.calls: List[Tuple[str, str, Any, Any]] = []

    async def get(self, path: str, params: Any = None, raw_text: bool = False) -> Any:
        self.calls.append(("GET", path, params, None))
        return self.response

    async def post(self, path: str, payload: Any = None) -> Any:
        self.calls.append(("POST", path, None, payload))
        return self.response

    @property
    def last(self) -> Tuple[str, str, Any, Any]:
        return self.calls[-1]


def _memory(response: Any = None) -> Memory:
    memory = Memory(api_key="test-key-000000000000000000000000")
    memory._connection = RecordingConnection(response)  # type: ignore[assignment]
    return memory


# ── §2 entity graph depth ────────────────────────────────────────────────


class TestEntityGraphDepthIsTheServersDefault:
    """Python used to default ``depth=2`` and SEND it.

    The handler's own default is 1 (``handler/entity.go:280``) and Go/TypeScript
    both omit the parameter. Live 2026-09-14: omitted -> ``node_count:1``,
    ``?depth=2`` -> ``node_count:2``. So the identical call answered a strictly
    larger graph in Python than in the other two arms, silently.
    """

    @pytest.mark.asyncio
    async def test_omitted_depth_sends_no_depth_param(self) -> None:
        memory = _memory({"entity_id": 7, "depth": 1, "node_count": 1, "nodes": []})
        await memory.get_entity_graph(7)
        _, path, params, _ = memory._connection.last  # type: ignore[attr-defined]
        assert path == "/api/v1/entities/7/graph"
        # The OLD code sent {"depth": "2"} here. Anything non-empty is a
        # restatement of a default the server already owns.
        assert not params, f"depth must not be sent when unset, got {params!r}"

    @pytest.mark.asyncio
    async def test_explicit_depth_is_still_sent(self) -> None:
        memory = _memory({"entity_id": 7, "depth": 3, "node_count": 5, "nodes": []})
        await memory.get_entity_graph(7, depth=3)
        _, _, params, _ = memory._connection.last  # type: ignore[attr-defined]
        assert params == {"depth": "3"}

    @pytest.mark.asyncio
    async def test_alias_entity_graph_has_the_same_default(self) -> None:
        """``entity_graph`` is an alias — an alias that defaults differently is two APIs."""
        memory = _memory({"entity_id": 7, "depth": 1, "node_count": 1, "nodes": []})
        await memory.entity_graph(7)
        _, _, params, _ = memory._connection.last  # type: ignore[attr-defined]
        assert not params


# ── §6 profile(tag) ──────────────────────────────────────────────────────


class TestProfileTag:
    """``tag`` is an IN-TENANT filter and it may never be sent empty."""

    @pytest.mark.asyncio
    async def test_explicit_tag_reaches_the_wire(self) -> None:
        memory = _memory({"static": {}, "dynamic": {}, "stats": {}})
        await memory.profile("hermes-1")
        _, path, params, _ = memory._connection.last  # type: ignore[attr-defined]
        assert path == "/api/v1/profile"
        assert params == {"tag": "hermes-1"}

    @pytest.mark.asyncio
    async def test_default_tag_is_the_clients_own(self) -> None:
        memory = _memory({"static": {}, "dynamic": {}, "stats": {}})
        await memory.profile()
        _, _, params, _ = memory._connection.last  # type: ignore[attr-defined]
        assert params == {"tag": memory.container_tag}

    @pytest.mark.asyncio
    async def test_empty_tag_is_refused_locally(self) -> None:
        """An empty ``tag`` is a GUARANTEED HTTP 400 (``tag: tag is required``).

        Spending a round trip to learn a string we already have is the same
        waste the AST builder already refuses for ``$in []``.
        """
        memory = _memory({"static": {}, "dynamic": {}, "stats": {}})
        with pytest.raises(ValueError) as caught:
            await memory.profile("")
        assert "tag" in str(caught.value)
        assert memory._connection.calls == []  # type: ignore[attr-defined]

    @pytest.mark.asyncio
    async def test_unknown_tag_is_an_empty_profile_not_an_exception(self) -> None:
        """Live: an unknown tag answers HTTP 200 with every counter at zero.

        Translating that into an exception would hide a typo'd tag behind a
        fake outage.
        """
        empty_body = {
            "static": {"facts": [], "preferences": [], "decisions": [],
                       "risks": [], "emotions": [], "highlight": []},
            "dynamic": {"recent_tasks": [], "recent_topics": []},
            "stats": {"total_records": 0, "sessions": 0, "last_active": ""},
        }
        memory = _memory(empty_body)
        profile = await memory.profile("totally-not-a-real-tag-xyz")
        assert profile.stats.total_records == 0
        assert profile.stats.last_active == ""
        assert profile.static.facts == []


# ── §7 create() ──────────────────────────────────────────────────────────


class TestCreateConvention:
    """One convention: required session, required content, typed optionals."""

    @pytest.mark.asyncio
    async def test_positional_session_and_content(self) -> None:
        memory = _memory({"id": 100, "status": "saved"})
        result = await memory.create("sess-1", "the body", type=MemoryType.FACT, score=8)
        _, path, _, payload = memory._connection.last  # type: ignore[attr-defined]
        assert path == "/api/v1/records"
        assert payload["session_id"] == "sess-1"
        assert payload["content"] == "the body"
        assert payload["type"] == "fact"
        assert payload["score"] == 8
        assert result.id == 100
        assert result.records[0].id == 100
        assert result.mode == "oss"

    @pytest.mark.asyncio
    async def test_type_accepts_a_plain_string_as_well_as_the_enum(self) -> None:
        """``type="fact"`` must work, not just ``MemoryType.FACT``.

        Caught during the live run: reading ``type.value`` off the ARGUMENT
        raised ``AttributeError: 'str' object has no attribute 'value'`` on the
        string path only — invisible to every enum-using test, and this repo's
        own AST harness passes strings.
        """
        memory = _memory({"id": 5})
        result = await memory.create("sess-1", "body", type="fact")
        _, _, _, payload = memory._connection.last  # type: ignore[attr-defined]
        assert payload["type"] == "fact"
        assert result.records[0].type == "fact"

    @pytest.mark.asyncio
    async def test_legacy_uuid_alias_is_gone_from_the_payload(self) -> None:
        """The server field is ``session_id``; ``uuid`` was belt-and-braces.

        Sending both is how a field that stopped meaning anything survives for
        years — and how the next reader concludes the server accepts either.
        """
        memory = _memory({"id": 1})
        await memory.create("sess-1", "body")
        _, _, _, payload = memory._connection.last  # type: ignore[attr-defined]
        assert "uuid" not in payload, "the legacy uuid wire alias must not be sent"

    @pytest.mark.asyncio
    async def test_temporal_window_travels_in_metadata(self) -> None:
        """``POST /records`` reads the bi-temporal window ONLY from metadata.

        ``service/record_create.go`` never fills the top-level fields on this
        route, so a caller who sent them there got HTTP 200 and a record with
        no window at all — the pin evaporated with nothing to catch. Go folds
        them into the envelope (``parity.go:83-98``); this proves Python does.
        """
        memory = _memory({"id": 1})
        await memory.create(
            "sess-1",
            "body",
            valid_from="2020-01-01T00:00:00Z",
            valid_until="2030-01-01T00:00:00Z",
        )
        _, _, _, payload = memory._connection.last  # type: ignore[attr-defined]
        metadata: Dict[str, Any] = json.loads(payload["metadata"])
        assert metadata["valid_from"] == "2020-01-01T00:00:00Z"
        assert metadata["valid_until"] == "2030-01-01T00:00:00Z"
        assert "valid_from" not in payload
        assert "valid_until" not in payload

    @pytest.mark.asyncio
    async def test_caller_metadata_is_merged_not_replaced(self) -> None:
        memory = _memory({"id": 1})
        await memory.create("sess-1", "body", metadata={"source": "unit-test"},
                            valid_from="2020-01-01T00:00:00Z")
        _, _, _, payload = memory._connection.last  # type: ignore[attr-defined]
        metadata = json.loads(payload["metadata"])
        assert metadata["source"] == "unit-test"
        assert metadata["valid_from"] == "2020-01-01T00:00:00Z"
        assert metadata["container_tag"] == memory.container_tag

    @pytest.mark.asyncio
    async def test_empty_session_is_refused_before_the_round_trip(self) -> None:
        memory = _memory({})
        with pytest.raises(ValueError):
            await memory.create("", "body")
        assert memory._connection.calls == []  # type: ignore[attr-defined]

    @pytest.mark.asyncio
    async def test_empty_content_is_refused_before_the_round_trip(self) -> None:
        memory = _memory({})
        with pytest.raises(ValueError):
            await memory.create("sess-1", "")
        assert memory._connection.calls == []  # type: ignore[attr-defined]

    @pytest.mark.asyncio
    async def test_create_request_is_no_longer_accepted(self) -> None:
        """``create(req)`` is REMOVED, not aliased.

        Accepting either a ``str`` or a ``CreateRequest`` as argument 1 is the
        guessing this parity round exists to delete. A model in argument 1
        must fail, not be quietly unwrapped.
        """
        from anhurdb.models import CreateRequest

        memory = _memory({"id": 1})
        with pytest.raises((ValueError, TypeError, AttributeError)):
            await memory.create(CreateRequest(session_id="s", content="c"))  # type: ignore[arg-type]

    def test_create_request_optional_fields_only_returns_what_was_set(self) -> None:
        """The migration bridge must not turn model defaults into pinned values."""
        from anhurdb.models import CreateRequest

        bare = CreateRequest(session_id="s", content="c")
        assert bare.optional_fields() == {}

        pinned = CreateRequest(session_id="s", content="c", score=9, status="draft")
        assert pinned.optional_fields() == {"score": 9, "status": "draft"}


# ── §4 smart search request shape (response shape lives in the sibling) ──


class TestSmartSearchRequest:
    @pytest.mark.asyncio
    async def test_query_scope_and_type_reach_the_wire(self) -> None:
        memory = _memory({"results": None, "count": 0, "scope": "sessions",
                          "bundle_hash": "h", "bundle_ordering": "smart_relevance"})
        await memory.smart_search("memory", ["*"], limit=2, memory_type="fact")
        _, path, params, _ = memory._connection.last  # type: ignore[attr-defined]
        assert path == "/api/v1/search/smart"
        assert ("q", "memory") in params
        assert ("limit", "2") in params
        assert ("scope", "sessions") in params
        assert ("type", "fact") in params

    @pytest.mark.asyncio
    async def test_returns_a_typed_envelope_not_the_raw_dict(self) -> None:
        """BREAKING in 3.0.0 — ``result["results"]`` used to work; it no longer does.

        Go answered ``[]byte`` and Python answered an untyped dict for the same
        endpoint TypeScript already modelled correctly. A dict return means the
        nullable ``results`` and the lexical ``relevance`` are documented
        nowhere the caller can see.
        """
        from anhurdb.models import SmartSearchResponse

        body = {
            "results": [{
                "id": 7, "uuid": "u-7", "type": "fact", "summary": "a hit",
                "metadata": "{}", "score": 5.0, "weight": 0.5, "status": "saved",
                "relevance": 1.75, "bm25": 3.2,
                "created_at": "2026-09-01T00:00:00Z",
                "updated_at": "2026-09-01T00:00:00Z",
            }],
            "count": 1, "scope": "sessions", "bundle_hash": "abc",
            "bundle_ordering": "smart_relevance",
        }
        memory = _memory(body)
        response = await memory.smart_search("memory", ["*"], limit=2)
        assert isinstance(response, SmartSearchResponse)
        assert response.results[0].relevance == 1.75
        assert response.bundle_ordering == "smart_relevance"

    @pytest.mark.asyncio
    async def test_no_matches_decodes_to_none_through_the_client(self) -> None:
        """The nil-slice ``null`` must survive the CLIENT, not just the model."""
        from anhurdb.models import SmartSearchResponse

        memory = _memory({"results": None, "count": 0, "scope": "sessions",
                          "bundle_hash": "", "bundle_ordering": "smart_relevance"})
        response = await memory.smart_search("nothing-matches-this", ["*"])
        assert isinstance(response, SmartSearchResponse)
        assert response.results is None
