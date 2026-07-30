"""The disk queue — the safety net. If this lies, memory is lost."""

from __future__ import annotations

import json

import pytest


def _queue(plugin_modules, tmp_path):
    return plugin_modules["memory_queue"].DurableTurnQueue(queue_dir=tmp_path / "queue")


def test_enqueue_then_drain_sends_and_removes(plugin_modules, tmp_path):
    queue = _queue(plugin_modules, tmp_path)
    queue.enqueue("session-a", "USER: hello")

    sent = []
    summary = queue.drain(lambda session_id, content: sent.append((session_id, content)))

    assert sent == [("session-a", "USER: hello")]
    assert summary.drained_count == 1
    assert summary.pending_count == 0
    assert list((tmp_path / "queue").glob("*.json")) == []


def test_drain_preserves_chronological_order(plugin_modules, tmp_path):
    queue = _queue(plugin_modules, tmp_path)
    for turn_number in range(5):
        queue.enqueue("session-a", f"turn-{turn_number}")

    sent = []
    queue.drain(lambda session_id, content: sent.append(content))

    assert sent == [f"turn-{turn_number}" for turn_number in range(5)]


def test_failure_keeps_the_turn_and_stops_the_drain(plugin_modules, tmp_path):
    """Order first: never write turn 2 when turn 1 could not be written."""
    queue = _queue(plugin_modules, tmp_path)
    queue.enqueue("session-a", "first")
    queue.enqueue("session-a", "second")

    attempts = []

    def always_fails(session_id, content):
        attempts.append(content)
        raise RuntimeError("AnhurDB is down")

    summary = queue.drain(always_fails)

    assert attempts == ["first"]  # stopped at the first failure
    assert summary.pending_count == 2
    assert summary.drained_count == 0
    assert "AnhurDB is down" in summary.last_error
    assert summary.oldest_pending.endswith("Z")
    assert summary.needs_human_attention is True
    assert len(list((tmp_path / "queue").glob("*.json"))) == 2


def test_recovery_drains_everything_in_order(plugin_modules, tmp_path):
    queue = _queue(plugin_modules, tmp_path)
    queue.enqueue("session-a", "first")
    queue.enqueue("session-a", "second")

    def always_fails(session_id, content):
        raise RuntimeError("down")

    queue.drain(always_fails)

    sent = []
    summary = queue.drain(lambda session_id, content: sent.append(content))

    assert sent == ["first", "second"]
    assert summary.pending_count == 0


def test_unparseable_envelope_is_quarantined_never_retried(plugin_modules, tmp_path):
    queue = _queue(plugin_modules, tmp_path)
    queue.enqueue("session-a", "good turn")
    corrupt_path = tmp_path / "queue" / "19700101T000000.000000-1-0.json"
    corrupt_path.write_text("{not json", encoding="utf-8")

    sent = []
    summary = queue.drain(lambda session_id, content: sent.append(content))

    assert sent == ["good turn"]
    assert summary.quarantined_count == 1
    assert (queue.quarantine_dir / corrupt_path.name).exists()

    # A second drain must not resurrect it.
    second_summary = queue.drain(lambda session_id, content: sent.append(content))
    assert sent == ["good turn"]
    assert second_summary.quarantined_count == 1


def test_envelope_without_session_is_quarantined(plugin_modules, tmp_path):
    """Attribution beats availability: never guess an owner."""
    queue = _queue(plugin_modules, tmp_path)
    (tmp_path / "queue").mkdir(parents=True)
    orphan_path = tmp_path / "queue" / "19700101T000000.000000-1-1.json"
    orphan_path.write_text(
        json.dumps({"version": 1, "session_id": "", "content": "orphan"}),
        encoding="utf-8",
    )

    sent = []
    summary = queue.drain(lambda session_id, content: sent.append(content))

    assert sent == []
    assert summary.quarantined_count == 1


def test_enqueue_without_session_id_refuses_loudly(plugin_modules, tmp_path):
    queue = _queue(plugin_modules, tmp_path)
    with pytest.raises(plugin_modules["memory_queue"].QueueWriteError):
        queue.enqueue("  ", "content")


def test_quarantine_writes_and_is_counted(plugin_modules, tmp_path):
    queue = _queue(plugin_modules, tmp_path)
    written_path = queue.quarantine("homeless turn", reason="no session id available")

    assert written_path is not None and written_path.exists()
    summary = queue.summarize()
    assert summary.quarantined_count == 1
    assert summary.pending_count == 0
    assert summary.needs_human_attention is True


def test_envelope_is_owner_only_and_carries_the_session(plugin_modules, tmp_path):
    queue = _queue(plugin_modules, tmp_path)
    written_path = queue.enqueue("session-xyz", "USER: secret-ish content")

    envelope = json.loads(written_path.read_text(encoding="utf-8"))
    assert envelope["session_id"] == "session-xyz"
    assert envelope["content"] == "USER: secret-ish content"
    assert envelope["version"] == plugin_modules["memory_queue"].QUEUE_ENVELOPE_VERSION
    assert (written_path.stat().st_mode & 0o077) == 0


def test_summarize_on_missing_directory_is_empty_not_an_error(
    plugin_modules, tmp_path
):
    queue = _queue(plugin_modules, tmp_path)
    summary = queue.summarize()
    assert summary.pending_count == 0
    assert summary.needs_human_attention is False


def test_many_turns_in_the_same_instant_do_not_collide(plugin_modules, tmp_path):
    """Filename collision is silent loss — the Go plugin learned this the hard way."""
    queue = _queue(plugin_modules, tmp_path)
    for turn_number in range(50):
        queue.enqueue("session-a", f"turn-{turn_number}")

    assert len(list((tmp_path / "queue").glob("*.json"))) == 50
