"""``add()`` — store text, choosing the write door from what the caller pinned.

One domain: the convenience write. Plain text goes to the extraction pipeline
(``POST /api/v1/ingest``); a call that pins ``score``, ``type`` or non-empty
``metadata`` (or asks for ``mode="regular"``) goes to the typed create
(``POST /api/v1/records``), the only door that persists those fields.

Split out of ``client/__init__.py`` in 4.0.0 (house 300-line cut). The typed
``create()`` / ``create_in_session()`` live in ``record_create.py``.

Junior Tip [no ingest-404 fallback since 4.0.0]: ``add()`` used to retry a
404 from ``/ingest`` on ``/records`` ("OSS fallback") and then remember the
404 for the life of the client. ``POST /api/v1/ingest`` is mounted
unconditionally (``server/router.go:135``), so a 404 there is a real error
(wrong URL, a proxy) — rerouting it silently wrote the text WITHOUT extraction
and hid the misconfiguration forever. It now raises like any other 404.
"""

from typing import Any, Dict, Optional

from .container_tag import _build_metadata_json
from .sessions import SessionMixin
from .write_receipt import _add_result_from_ingest, _add_result_from_record
from ..models import AddResult, CreateRequest, MemoryType


class AddMixin(SessionMixin):
    """``add()`` and the two private doors it routes to.

    Inherits ``SessionMixin`` because every add resolves its write session
    through ``_resolve_write_session_id`` — a real dependency, declared as one.
    """

    async def add(
        self,
        text: str,
        *,
        mode: str = "ingest",
        score: Optional[int] = None,
        type: Optional[MemoryType] = None,
        metadata: Optional[Dict[str, Any]] = None,
        session_id: str = "",
    ) -> AddResult:
        """Store text in the current session (session-first write contract).

        Call ``await create_session()`` (or ``await open_session()``) before the
        first write. ``session_id`` is always sent — from the ``session_id``
        argument or ``self.session_id``.

        Agent UX — pick the write path explicitly via ``mode``:
        - ``mode="ingest"`` (default) → ``POST /api/v1/ingest`` with
          ``content``, ``container_tag``, ``session_id`` (extraction LLM).
        - ``mode="regular"`` → ``POST /api/v1/records`` (typed create;
          default type episodic). Use when you need ``score`` / ``type`` /
          ``metadata`` persisted without extraction.

        Trap (PARITY_SPEC.md ~L84): pinning ``score``, ``type``, or a
        non-empty ``metadata`` also forces ``/records`` even under
        ``mode="ingest"`` — the ingest endpoint's body has no field for any
        of the three, so a plain ingest call would silently drop the pin.
        Mirrors Go (``Add`` / ``forceRecordsPath``) and TypeScript (``add`` /
        ``forceRecordsPath``); all three SDKs agree. Any error from
        ``/ingest`` — a 404 included — is raised, never rerouted.

        Args:
            text:       The text to remember (required, non-empty).
            mode:       ``"ingest"`` or ``"regular"``.
            score:      Importance 1-10. Pinning it (any mode) forces the
                        ``/records`` path.
            type:       Memory type. Pinning it (any mode) forces the
                        ``/records`` path.
            metadata:   Optional metadata. A non-empty dict (any mode)
                        forces the ``/records`` path; an empty dict or
                        ``None`` does not (matches Go's
                        ``len(cfg.metadata) > 0`` guard).
            session_id: Override session; defaults to ``self.session_id``.

        Returns:
            ``AddResult``. ``mode`` tells you WHICH door ran: ``"ingest"``
            means the server's extraction pipeline accepted the text and
            ``records`` may hold MORE than one row; ``"records"`` means the
            SDK wrote exactly one record directly. Reading ``records`` without
            reading ``mode`` is how "the extractor found three facts" gets
            mistaken for "I wrote three records".

        Raises:
            ValueError: If ``text`` is empty or ``mode`` is invalid.
            AnhurQueryError: Any HTTP error from the chosen door (404 too).

        Example::

            session_id = await mem.create_session()
            await mem.add("User prefers dark mode", mode="ingest",
                          session_id=session_id)
            # Pinned score forces /records even though mode="ingest" — the
            # score is honored, not silently dropped.
            await mem.add("Pinned fact", mode="ingest", score=8,
                          type=MemoryType.PREFERENCE)"""
        if not text:
            raise ValueError("text cannot be empty")
        if mode not in ("ingest", "regular"):
            raise ValueError('mode must be "ingest" or "regular"')

        # Junior Tip [never let a pin evaporate]: POST /api/v1/ingest's body
        # is only {content, container_tag, session_id} — score, type, and
        # metadata have no field to land in on that endpoint. Routing on
        # `mode` alone once sent add(text, score=8) to ingest, where the
        # pinned score was silently discarded: no exception, no warning, a
        # record without the importance the caller asked for. That is silent
        # data loss, the failure this project treats as the worst kind, so a
        # pinned call goes straight to _create_record(), the only path that
        # persists all three fields. An empty metadata dict is NOT a pin
        # (Go's `len(cfg.metadata) > 0`).
        pinned = score is not None or type is not None or bool(metadata)

        if mode == "ingest" and not pinned:
            return await self._ingest(text, session_id)
        return await self._create_record(text, score, type, metadata, session_id)

    async def _ingest(self, text: str, session_id: str = "") -> AddResult:
        """Write through the extraction pipeline at ``POST /api/v1/ingest``.

        Always sends ``session_id``. Errors propagate unchanged."""
        effective_session_id = self._resolve_write_session_id(session_id)
        payload = {
            "content": text,
            "container_tag": self._container_tag,
            "session_id": effective_session_id,
        }
        data = await self._connection.post("/api/v1/ingest", payload)
        return _add_result_from_ingest(data, session_id=effective_session_id)

    async def _create_record(
        self,
        text: str,
        score: Optional[int],
        mem_type: Optional[MemoryType],
        metadata: Optional[Dict[str, Any]],
        session_id: str = "",
    ) -> AddResult:
        """Create a record directly via ``POST /api/v1/records``.

        This is the only write path that persists ``score`` and ``type``
        (the ingest endpoint drops them — see ``add()``). Without server-side
        embedding, text is stored in both ``summary`` (for keyword search) and
        ``content`` (for full retrieval)."""
        summary = text[:200] + "..." if len(text) > 200 else text

        effective_score = 5 if score is None else score
        effective_type = MemoryType.EPISODIC if mem_type is None else mem_type
        effective_session_id = self._resolve_write_session_id(session_id)

        request = CreateRequest(
            session_id=effective_session_id,
            type=effective_type,
            summary=summary,
            content=text,
            score=effective_score,
            weight=effective_score / 10,
            metadata=_build_metadata_json(self._container_tag, metadata),
        )

        data = await self._connection.post(
            "/api/v1/records",
            request.model_dump(exclude_none=True),
        )

        return _add_result_from_record(
            data,
            session_id=effective_session_id,
            record_type=effective_type.value,
            summary=summary,
        )


__all__ = ["AddMixin"]
