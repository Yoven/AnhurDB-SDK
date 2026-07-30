"""End-to-end provider tests against a fake AnhurDB HTTP server.

These exercise the real ``anhurdb`` SDK over real sockets, so a transport
regression in the SDK (like the 2026-07-30 truncated-body bug that made search
return ``[]`` with no error) shows up here instead of in production memory.
"""

from __future__ import annotations

import json

import pytest


HERMES_SESSION_ID = "20260730_143012_a1b2c3d4"


def _search_hit(record_id: int, summary: str, similarity: float = 0.91) -> dict:
    return {
        "record": {
            "id": record_id,
            "uuid": HERMES_SESSION_ID,
            "type": "fact",
            "summary": summary,
        },
        "similarity": similarity,
    }


# ---------------------------------------------------------------------------
# Availability — honest, loud, never "available but inert"
# ---------------------------------------------------------------------------


def test_without_api_key_is_unavailable_with_a_reason(
    make_provider, fake_anhurdb, caplog
):
    _state, base_url = fake_anhurdb
    provider = make_provider(url=base_url, api_key="")

    with caplog.at_level("ERROR"):
        assert provider.is_available() is False

    assert "ANHUR_API_KEY is not set" in provider.availability_reason
    assert "hermes memory setup" in provider.availability_reason
    assert any("ANHUR_API_KEY is not set" in record.message for record in caplog.records)


def test_available_with_key_and_sdk(make_provider, fake_anhurdb):
    _state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    assert provider.is_available() is True
    assert provider.availability_reason == ""


def test_provider_name_is_stable(make_provider, fake_anhurdb):
    _state, base_url = fake_anhurdb
    assert make_provider(url=base_url).name == "anhurdb"


def test_constructor_never_raises_even_with_broken_config(plugin_modules, monkeypatch):
    """The host instantiates with no args and swallows exceptions at debug."""
    provider_module = plugin_modules["provider"]
    monkeypatch.setattr(
        provider_module,
        "load_plugin_config",
        lambda *args, **kwargs: (_ for _ in ()).throw(RuntimeError("boom")),
    )

    provider = provider_module.AnhurDBMemoryProvider()

    assert provider.is_available() is False
    assert "boom" in provider.availability_reason
    assert provider.get_tool_schemas()  # still answers the host


# ---------------------------------------------------------------------------
# Session invariant: 1 Hermes session = 1 AnhurDB session
# ---------------------------------------------------------------------------


def test_initialize_registers_the_hermes_session_verbatim(
    make_provider, fake_anhurdb
):
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)

    provider.initialize(session_id=HERMES_SESSION_ID, hermes_home="/tmp/hermes")

    assert state.created_sessions == [HERMES_SESSION_ID]
    assert provider.session_id == HERMES_SESSION_ID


def test_sync_turn_writes_under_that_session(make_provider, fake_anhurdb):
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    provider.sync_turn("what did we decide?", "we decided to ship")

    persisted = state.ingested_for(HERMES_SESSION_ID)
    assert len(persisted) == 1
    assert "USER: what did we decide?" in persisted[0]
    assert "ASSISTANT: we decided to ship" in persisted[0]
    assert f"Hermes session {HERMES_SESSION_ID}" in persisted[0]
    assert state.ingested[0]["container_tag"] == "hermes-test"


def test_session_switch_never_merges_two_conversations(
    make_provider, fake_anhurdb
):
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id="session-one")
    provider.sync_turn("first conversation", "ok")

    provider.on_session_switch("session-two", reset=True)
    provider.sync_turn("second conversation", "ok")

    assert state.created_sessions == ["session-one", "session-two"]
    assert state.ingested_for("session-one") and state.ingested_for("session-two")
    assert "second conversation" not in "".join(state.ingested_for("session-one"))
    assert "first conversation" not in "".join(state.ingested_for("session-two"))


def test_explicit_session_id_wins_without_moving_the_providers_session(
    make_provider, fake_anhurdb
):
    """Gateway case: concurrent sessions share one provider."""
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id="session-one")

    provider.sync_turn("group chat turn", "ok", session_id="session-other")

    assert provider.session_id == "session-one"
    assert state.ingested_for("session-other")
    assert state.ingested_for("session-one") == []
    assert "session-other" in state.created_sessions


def test_turn_without_any_session_is_quarantined_not_guessed(
    make_provider, fake_anhurdb, caplog
):
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id="")  # host gave us nothing

    with caplog.at_level("ERROR"):
        provider.sync_turn("homeless turn", "reply")

    assert state.ingested == []
    quarantined = list((provider.config.queue_dir / "quarantine").glob("*.json"))
    assert len(quarantined) == 1
    envelope = json.loads(quarantined[0].read_text(encoding="utf-8"))
    assert "homeless turn" in envelope["content"]
    assert envelope["session_id"] == ""


# ---------------------------------------------------------------------------
# No silent loss
# ---------------------------------------------------------------------------


def test_write_failure_queues_and_next_turn_drains_in_order(
    make_provider, fake_anhurdb
):
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    state.failing_paths.add("/api/v1/ingest")
    provider.sync_turn("turn one", "reply one")

    assert state.ingested == []
    assert len(list(provider.config.queue_dir.glob("*.json"))) == 1

    state.failing_paths.clear()
    provider.sync_turn("turn two", "reply two")

    persisted = state.ingested_for(HERMES_SESSION_ID)
    assert len(persisted) == 2
    assert "turn one" in persisted[0]  # recovered first — ordering preserved
    assert "turn two" in persisted[1]
    assert list(provider.config.queue_dir.glob("*.json")) == []


def test_turn_is_queued_even_when_the_key_is_missing(
    make_provider, fake_anhurdb
):
    """The safety net must NOT sit behind the same gate it protects."""
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url, api_key="")
    provider.initialize(session_id=HERMES_SESSION_ID)

    provider.sync_turn("still worth keeping", "yes")

    assert state.ingested == []
    queued = list(provider.config.queue_dir.glob("*.json"))
    assert len(queued) == 1
    envelope = json.loads(queued[0].read_text(encoding="utf-8"))
    assert envelope["session_id"] == HERMES_SESSION_ID
    assert "still worth keeping" in envelope["content"]


def test_session_registration_failure_still_keeps_the_turn(
    make_provider, fake_anhurdb
):
    state, base_url = fake_anhurdb
    state.failing_paths.add("/api/v1/sessions")
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    provider.sync_turn("turn during outage", "reply")

    assert state.ingested == []
    assert len(list(provider.config.queue_dir.glob("*.json"))) == 1

    state.failing_paths.clear()
    provider.sync_turn("turn after recovery", "reply")

    persisted = state.ingested_for(HERMES_SESSION_ID)
    assert len(persisted) == 2
    assert "turn during outage" in persisted[0]


def test_long_turn_is_chunked_and_every_chunk_is_persisted(
    make_provider, fake_anhurdb
):
    state, base_url = fake_anhurdb
    provider = make_provider(
        url=base_url, extra_environment={"ANHUR_MAX_CHUNK_CHARS": "400"}
    )
    provider.initialize(session_id=HERMES_SESSION_ID)

    long_reply = "\n".join(f"line {index} " + "x" * 80 for index in range(20))
    provider.sync_turn("summarise the log", long_reply)

    persisted = state.ingested_for(HERMES_SESSION_ID)
    assert len(persisted) > 1
    assert all("[part " in chunk for chunk in persisted)
    rebuilt = "".join(persisted)
    for index in range(20):
        assert f"line {index} " in rebuilt  # nothing truncated away


def test_non_primary_context_does_not_write(make_provider, fake_anhurdb):
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID, agent_context="subagent")

    provider.sync_turn("subagent chatter", "reply")

    assert state.ingested == []
    assert list(provider.config.queue_dir.glob("*.json")) == []


# ---------------------------------------------------------------------------
# Reads: profile block, prefetch, tools
# ---------------------------------------------------------------------------


def test_system_prompt_block_carries_the_profile(make_provider, fake_anhurdb):
    _state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    block = provider.system_prompt_block()

    assert '<anhur-memory backend="AnhurDB" container="hermes-test">' in block
    assert "the user ships sovereign memory" in block
    assert "42 records across 3 sessions" in block
    assert block.rstrip().endswith("</anhur-memory>")


def test_prefetch_returns_recalled_memories(make_provider, fake_anhurdb):
    state, base_url = fake_anhurdb
    state.search_results = [_search_hit(7, "the user prefers explicit names")]
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    block = provider.prefetch("what naming style?")

    assert "<anhur-recall>" in block
    assert "the user prefers explicit names" in block
    assert "similarity 0.91" in block


def test_prefetch_degrades_quietly_but_reports_the_backlog(
    make_provider, fake_anhurdb
):
    state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    state.failing_paths.add("/api/v1/ingest")
    provider.sync_turn("queued turn", "reply")
    state.failing_paths.add("/api/v1/search")

    block = provider.prefetch("anything")

    assert "Unpersisted backlog" in block
    assert "TELL THE USER" in block


def test_prefetch_with_nothing_to_say_injects_nothing(make_provider, fake_anhurdb):
    _state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    assert provider.prefetch("anything") == ""


def test_tools_return_json_and_never_raise(make_provider, fake_anhurdb):
    state, base_url = fake_anhurdb
    state.search_results = [_search_hit(9, "decision: dogfood the python sdk")]
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    recalled = json.loads(
        provider.handle_tool_call("anhurdb_recall", {"query": "sdk decision"})
    )
    assert recalled["count"] == 1
    assert "dogfood" in recalled["memories"][0]["summary"]

    searched = json.loads(
        provider.handle_tool_call(
            "anhurdb_search", {"query": "sdk", "memory_type": "decision", "limit": 3}
        )
    )
    assert searched["memory_type"] == "decision"

    missing_argument = json.loads(provider.handle_tool_call("anhurdb_recall", {}))
    assert "query is required" in missing_argument["error"]

    unknown_tool = json.loads(provider.handle_tool_call("nope", {"query": "x"}))
    assert "unknown AnhurDB memory tool" in unknown_tool["error"]

    bad_arguments_type = json.loads(provider.handle_tool_call("anhurdb_recall", None))
    assert "error" in bad_arguments_type


def test_tool_failure_is_reported_as_json_not_an_exception(
    make_provider, fake_anhurdb
):
    state, base_url = fake_anhurdb
    state.failing_paths.add("/api/v1/search")
    provider = make_provider(url=base_url)
    provider.initialize(session_id=HERMES_SESSION_ID)

    result = json.loads(
        provider.handle_tool_call("anhurdb_recall", {"query": "anything"})
    )

    assert "recall failed" in result["error"]


def test_tool_limit_is_bounded(plugin_modules):
    tools_module = plugin_modules["tools"]
    schemas_module = plugin_modules["schemas"]

    assert tools_module._clean_limit(999) == schemas_module.MAX_TOOL_RESULT_LIMIT
    assert tools_module._clean_limit(0) == tools_module.DEFAULT_TOOL_RESULT_LIMIT
    assert tools_module._clean_limit("junk") == tools_module.DEFAULT_TOOL_RESULT_LIMIT
    assert tools_module._clean_limit(3) == 3


def test_tool_schemas_have_the_shape_the_host_normalises(make_provider, fake_anhurdb):
    _state, base_url = fake_anhurdb
    schemas = make_provider(url=base_url).get_tool_schemas()

    assert [schema["name"] for schema in schemas] == ["anhurdb_recall", "anhurdb_search"]
    for schema in schemas:
        assert isinstance(schema.get("description"), str) and schema["description"]
        assert schema["parameters"]["type"] == "object"
        assert "query" in schema["parameters"]["required"]


# ---------------------------------------------------------------------------
# Plugin registration surface
# ---------------------------------------------------------------------------


class _CollectorContext:
    """Same surface as ``plugins/memory/__init__.py:_ProviderCollector``."""

    def __init__(self) -> None:
        self.provider = None
        self.tools = {}

    def register_memory_provider(self, provider):
        self.provider = provider

    def register_tool(self, **kwargs):
        self.tools[kwargs["name"]] = kwargs

    def register_hook(self, *args, **kwargs):
        pass

    def register_cli_command(self, *args, **kwargs):
        pass


def test_register_hands_over_provider_and_tools(plugin):
    ctx = _CollectorContext()

    plugin.register(ctx)

    assert ctx.provider is plugin.get_provider()
    assert ctx.provider.name == "anhurdb"
    assert set(ctx.tools) == {"anhurdb_recall", "anhurdb_search"}
    assert ctx.tools["anhurdb_recall"]["toolset"] == "memory"
    assert callable(ctx.tools["anhurdb_recall"]["handler"])


def test_registered_handler_returns_json_when_unconfigured(plugin):
    ctx = _CollectorContext()
    plugin.register(ctx)

    result = ctx.tools["anhurdb_search"]["handler"]({"query": "x"})

    assert isinstance(result, str)
    json.loads(result)  # must parse — success or error, always JSON


def test_register_without_memory_support_fails_loud(plugin, caplog):
    class UselessContext:
        pass

    with caplog.at_level("ERROR"):
        plugin.register(UselessContext())

    assert any(
        "no register_memory_provider" in record.message for record in caplog.records
    )


def test_backup_paths_include_the_queue(make_provider, fake_anhurdb):
    _state, base_url = fake_anhurdb
    provider = make_provider(url=base_url)
    assert str(provider.config.state_dir) in provider.backup_paths()


def test_config_schema_is_env_var_only(make_provider, fake_anhurdb):
    _state, base_url = fake_anhurdb
    schema = make_provider(url=base_url).get_config_schema()

    assert [field["key"] for field in schema] == ["api_key", "url", "container"]
    assert all(field.get("env_var") for field in schema)
    assert schema[0]["secret"] is True


def test_provider_subclasses_the_real_memory_provider_when_available(
    plugin_modules,
):
    """If the Hermes runtime is importable here, prove the real ABC is satisfied."""
    provider_module = plugin_modules["provider"]
    if provider_module.USING_FALLBACK_BASE:
        pytest.skip("hermes-agent runtime not importable in this environment")
    from agent.memory_provider import MemoryProvider

    assert issubclass(provider_module.AnhurDBMemoryProvider, MemoryProvider)


def test_provider_implements_every_abstract_method_name(plugin_modules):
    """Guards the fallback path: the real ABC's contract, checked by name."""
    provider_module = plugin_modules["provider"]
    required_methods = (
        "name",
        "is_available",
        "initialize",
        "sync_turn",
        "prefetch",
        "get_tool_schemas",
        "handle_tool_call",
        "shutdown",
        "system_prompt_block",
        "on_session_switch",
        "get_config_schema",
        "backup_paths",
    )
    for method_name in required_methods:
        assert hasattr(provider_module.AnhurDBMemoryProvider, method_name), method_name
