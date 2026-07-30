"""Turn rendering, chunking, and the context blocks injected into the model.

Standard library only — see the Junior Tip at the top of :mod:`config`.
"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any, Dict, List, Sequence

from .memory_queue import BacklogSummary

# Opening/closing markers of the block injected into the model context. Kept
# identical to the Claude Code plugin so a person reading either transcript
# recognises the same thing.
MEMORY_BLOCK_OPEN = '<anhur-memory backend="AnhurDB" container="{container}">'
MEMORY_BLOCK_CLOSE = "</anhur-memory>"


def format_turn(user_content: str, assistant_content: str) -> str:
    """Render one completed turn as the text that gets persisted.

    Returns ``""`` when there is nothing worth remembering, so the caller can
    skip the write instead of storing an empty record.

    Junior Tip [o formato ROLE: é o que a camada cognitiva do AnhurDB espera]:
    o pipeline de extração (fato/decisão/emoção) foi calibrado sobre diálogo
    limpo, no mesmo formato que o plugin do Claude Code envia. Mudar isto aqui
    muda silenciosamente a qualidade da extração lá — que é medida em outro
    repositório e não quebraria nenhum teste deste.
    """
    rendered_lines: List[str] = []
    if user_content and user_content.strip():
        rendered_lines.append("  USER: " + user_content.strip())
    if assistant_content and assistant_content.strip():
        rendered_lines.append("  ASSISTANT: " + assistant_content.strip())
    return "\n".join(rendered_lines)


def build_chunk_header(session_id: str, part_index: int, part_count: int) -> str:
    """Header line prepended to every persisted chunk."""
    part_label = f" [part {part_index}/{part_count}]" if part_count > 1 else ""
    timestamp = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    return (
        f"Hermes session {session_id} — conversation excerpt{part_label} "
        f"({timestamp}):"
    )


def split_into_chunks(text: str, max_characters: int) -> List[str]:
    """Split ``text`` into pieces of at most ``max_characters``, on line breaks.

    Junior Tip [chunking sem perda — herdado do plugin Go]: a versão antiga
    TRUNCAVA para os últimos N caracteres e mandava só isso, então o começo de
    um turno longo simplesmente sumia. Aqui todo pedaço é persistido (ou
    enfileirado); nada é descartado. Uma linha maior que o limite é cortada em
    fronteira de caractere — nunca no meio de um caractere multibyte, porque
    Python fatia por code point.
    """
    if max_characters <= 0 or len(text) <= max_characters:
        return [text] if text else []

    chunks: List[str] = []
    current_chunk: List[str] = []
    current_length = 0

    def flush_current() -> None:
        nonlocal current_length
        if current_chunk:
            chunks.append("".join(current_chunk))
            current_chunk.clear()
            current_length = 0

    for line in text.splitlines(keepends=True):
        if len(line) > max_characters:
            flush_current()
            for offset in range(0, len(line), max_characters):
                chunks.append(line[offset : offset + max_characters])
            continue
        if current_length + len(line) > max_characters:
            flush_current()
        current_chunk.append(line)
        current_length += len(line)

    flush_current()
    return chunks


def format_profile_block(
    container: str,
    profile: Dict[str, Any],
    recall_limit: int,
    backlog: BacklogSummary,
) -> str:
    """Render the static ``<anhur-memory>`` block for the system prompt."""
    lines: List[str] = [MEMORY_BLOCK_OPEN.format(container=container)]
    lines.append(
        "You have persistent long-term memory in AnhurDB. This is what you "
        "remember across sessions — trust it, build on it, and keep it "
        "accurate. Everything you and the user say is persisted automatically; "
        "you do not invoke that."
    )
    lines.extend(_backlog_warning_lines(backlog))

    static_section = profile.get("static") if isinstance(profile, dict) else {}
    dynamic_section = profile.get("dynamic") if isinstance(profile, dict) else {}
    stats_section = profile.get("stats") if isinstance(profile, dict) else {}

    for title, section, key in (
        ("Decisions", static_section, "decisions"),
        ("Facts", static_section, "facts"),
        ("Preferences", static_section, "preferences"),
        ("Recent topics", dynamic_section, "recent_topics"),
        ("Open tasks", dynamic_section, "recent_tasks"),
    ):
        items = _string_list(section, key)[:recall_limit]
        if not items:
            continue
        lines.append("")
        lines.append(f"## {title}")
        lines.extend(f"- {item}" for item in items)

    total_records = _numeric_field(stats_section, "total_records")
    total_sessions = _numeric_field(stats_section, "sessions")
    lines.append("")
    lines.append(
        f"({total_records} records across {total_sessions} sessions. "
        "Use anhurdb_recall / anhurdb_search to look up anything not shown here.)"
    )
    lines.append(MEMORY_BLOCK_CLOSE)
    return "\n".join(lines)


def format_recall_block(
    query: str,
    hits: Sequence[Dict[str, Any]],
    backlog: BacklogSummary,
) -> str:
    """Render the per-turn recall block returned by ``prefetch``.

    Returns ``""`` when there is nothing to say — an empty string is the
    documented "inject nothing" signal, and injecting an empty header every
    turn would burn context for no information.
    """
    warning_lines = _backlog_warning_lines(backlog)
    if not hits and not warning_lines:
        return ""

    lines: List[str] = ["<anhur-recall>"]
    lines.extend(warning_lines)
    if hits:
        lines.append(f"Relevant long-term memories for: {query.strip()[:200]}")
        for hit in hits:
            summary = str(hit.get("summary", "") or "").strip().replace("\n", " ")
            if not summary:
                continue
            lines.append(
                f"- [{hit.get('type', 'memory')}] {summary} "
                f"(similarity {float(hit.get('similarity', 0.0)):.2f})"
            )
    lines.append("</anhur-recall>")
    return "\n".join(lines)


def _backlog_warning_lines(backlog: BacklogSummary) -> List[str]:
    """Warning shown ABOVE the memory, because the memory is incomplete.

    Junior Tip [o aviso vem ANTES do conteúdo, não depois]: fila presa
    significa turnos que nunca chegaram ao AnhurDB, então tudo o que vem
    abaixo está sabidamente incompleto. O modelo precisa ler a ressalva ANTES
    de agir sobre o conteúdo — e precisa contar ao usuário, que não tem outro
    jeito de descobrir.
    """
    if backlog is None or not backlog.needs_human_attention:
        return []

    lines: List[str] = []
    if backlog.pending_count > 0:
        lines.append("")
        lines.append("## ⚠️ Unpersisted backlog — the memory below is INCOMPLETE")
        lines.append(
            f"- {backlog.pending_count} turn(s) are stuck on disk and never "
            f"reached AnhurDB (oldest queued {backlog.oldest_pending or 'unknown'})."
        )
        if backlog.last_error:
            lines.append(f"- Last retry failed with: {backlog.last_error}")
        lines.append(
            "- They are NOT lost (retried on every turn), but they will not "
            "appear in recall until they drain."
        )
        lines.append(
            "- TELL THE USER about this now, in your first reply: they cannot "
            "see it otherwise."
        )
    if backlog.quarantined_count > 0:
        lines.append("")
        lines.append(
            "## 🚨 Quarantined turns — missing memory that will NOT self-heal"
        )
        lines.append(
            f"- {backlog.quarantined_count} turn(s) had no identifiable session "
            f"and were parked in the queue's quarantine/ directory (oldest "
            f"{backlog.oldest_quarantined or 'unknown'})."
        )
        lines.append(
            "- They are NEVER persisted automatically: writing them into another "
            "conversation's session would merge sessions (1 Hermes session = "
            "1 AnhurDB session, inviolable)."
        )
        lines.append(
            "- TELL THE USER about this now: only a human can decide where they belong."
        )
    return lines


def _string_list(section: Any, key: str) -> List[str]:
    """Coerce ``section[key]`` into a list of non-empty strings."""
    if not isinstance(section, dict):
        return []
    raw_items = section.get(key)
    if not isinstance(raw_items, list):
        return []
    return [
        item.strip()
        for item in raw_items
        if isinstance(item, str) and item.strip()
    ]


def _numeric_field(section: Any, key: str) -> int:
    """Read a numeric stat that may arrive as int or float (JSON)."""
    if not isinstance(section, dict):
        return 0
    raw_value = section.get(key)
    if isinstance(raw_value, bool):
        return 0
    if isinstance(raw_value, (int, float)):
        return int(raw_value)
    return 0
