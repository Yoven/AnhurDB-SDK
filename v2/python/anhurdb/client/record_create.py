"""Typed record creation — exactly one record, no extraction.

One domain: ``POST /api/v1/records`` when the caller already knows what the
record IS. ``create()`` takes the full optional field set; ``create_in_session``
is the fixed-shape episodic anchor write into a caller-owned session.

Split out of ``client/__init__.py`` in 4.0.0 (house 300-line cut). The
convenience ``add()`` lives in ``write_add.py``.

Junior Tip [``session_id`` is the only session key on the wire — 4.0.0]: the
server reads ``uuid`` OR ``session_id`` and copies one into the other
(``server/handler/record_crud.go:49-52``). The SDK speaks ``session_id`` only;
``CreateRequest(uuid=...)`` raises, and no payload built here carries a
``uuid`` key.
"""

from typing import Any, Dict, List, Optional, Union

from .connection import HTTPConnection
from .container_tag import _build_metadata_json
from .exceptions import AnhurError
from .write_receipt import _add_result_from_record
from ..models import AddResult, CreateRequest, MemoryType


class RecordCreateMixin:
    """``create()`` and ``create_in_session()``."""

    _connection: HTTPConnection
    _container_tag: str

    async def create(
        self,
        session_id: str,
        content: str,
        *,
        type: Optional[Union[MemoryType, str]] = None,
        score: Optional[int] = None,
        status: Optional[str] = None,
        related_ids: Optional[List[int]] = None,
        valid_from: Optional[str] = None,
        valid_until: Optional[str] = None,
        metadata: Optional[Dict[str, Any]] = None,
    ) -> AddResult:
        """Create exactly one typed record (no extraction).

        Agent UX — write path: use when you already know ``type`` + content.
        For raw text use ``add(text, mode="ingest")`` / MCP ``ingest_memory``.
        Hits ``POST /api/v1/records``.

        BREAKING in 3.0.0 — this used to take a single ``CreateRequest``. The
        migration is mechanical::

            await mem.create(req)
            # becomes
            await mem.create(req.session_id, req.content, **req.optional_fields())

        Junior Tip [why session and content are required POSITIONALS]:
        ``POST /api/v1/records`` cannot succeed without an anchor session and a
        body — a call missing either is an HTTP 422 every single time. Making
        them positional moves that failure from a network round trip to the
        function signature, and it is the ONE convention all three SDKs share
        (Go ``Create(ctx, sessionUUID, content, opts...)``, TypeScript
        ``create(sessionUuid, text, options?)``).

        Args:
            session_id:  Session from ``create_session()`` (required).
            content:     The record body (required).
            type:        Memory type — a ``MemoryType`` or its plain string
                         value. Server default ``episodic`` when unset.
            score:       Importance 1-10. Server default 5 when unset.
            status:      Record status. Server default ``saved`` when unset.
            related_ids: Graph edges to sibling records.
            valid_from:  RFC3339 start of the validity window. Delivered as a
                         METADATA key — this route ignores the top-level field.
            valid_until: RFC3339 end of the validity window, same delivery.
            metadata:    Caller metadata, merged under the SDK container tag.

        Returns:
            ``AddResult`` — ``id``/``status`` come from the created record,
            ``records`` carries the one summary, ``mode`` is ``"records"``
            (the direct ``/records`` door, never the extraction pipeline).

        Raises:
            ValueError: If ``session_id`` or ``content`` is empty.
            AnhurQueryError: HTTP 422 when the anchor does not exist. Never
                fabricate a synthetic anchor client-side — 422 stays 422."""
        resolved_session_id = (session_id or "").strip()
        if not resolved_session_id:
            raise ValueError(
                "session_id is required — create a session first "
                "(await create_session())"
            )
        if not content:
            raise ValueError("content cannot be empty")

        # Junior Tip [why the payload is still built through CreateRequest]:
        # the model owns the server's own defaults (weight seeding, the
        # embedding fields that must be sent as zero values). Rebuilding the
        # dict by hand here would fork that knowledge into a second place.
        request_fields: Dict[str, Any] = {
            "session_id": resolved_session_id,
            "content": content,
            "summary": content[:200] + "..." if len(content) > 200 else content,
        }
        if type is not None:
            request_fields["type"] = type
        if score is not None:
            request_fields["score"] = score
            request_fields["weight"] = round(score / 10, 4)
        if status is not None:
            request_fields["status"] = status
        if related_ids is not None:
            request_fields["related_ids"] = related_ids

        request = CreateRequest(**request_fields)
        payload = request.model_dump(exclude_none=True)

        # Junior Tip [the bi-temporal window travels in METADATA on this route]:
        # ``service/record_create.go`` only reads ``valid_from``/``valid_until``
        # from the metadata JSON; the top-level fields of the REST create body
        # are never filled by this handler. A caller who sent them as top-level
        # keys got HTTP 200 and a record with NO validity window — the pin
        # evaporated with no error to read. Go folds them into the envelope for
        # exactly this reason; Python matches, so the same call produces the
        # same record in both arms.
        window_metadata: Dict[str, Any] = dict(metadata) if metadata else {}
        if valid_from is not None:
            window_metadata["valid_from"] = valid_from
        if valid_until is not None:
            window_metadata["valid_until"] = valid_until

        # Inject the SDK-owned container_tag into metadata (same as add() and
        # the Go/TS create paths) so records stay visible to container-scoped
        # search/profile.
        payload["metadata"] = _build_metadata_json(
            self._container_tag, window_metadata or None
        )

        # One request. Missing episodic anchor → HTTP 422, surfaced as
        # AnhurQueryError. Never fabricate a synthetic anchor client-side.
        data = await self._connection.post("/api/v1/records", payload)
        # Junior Tip [read the type back off the VALIDATED model]: callers pass
        # either ``MemoryType.FACT`` or the plain string ``"fact"`` (this repo's
        # own AST harness passes strings), and ``type.value`` on a ``str`` is an
        # AttributeError that only fires on the string path. ``CreateRequest``
        # has already coerced both into the enum by here, so there is exactly
        # one shape left to read.
        resolved_type = request.type
        return _add_result_from_record(
            data,
            session_id=resolved_session_id,
            record_type=getattr(resolved_type, "value", str(resolved_type)),
            summary=request_fields["summary"],
        )

    async def create_in_session(
        self,
        text: str,
        session_uuid: str,
    ) -> AddResult:
        """Store ``text`` directly as an episodic record under ``session_uuid``.

        The session must be registered via ``create_session()`` / POST
        ``/api/v1/sessions`` before calling this on session-first servers.

        Args:
            text:         Record text (stored in both summary and content).
            session_uuid: The session UUID to place the record under (required).

        Returns:
            ``AddResult`` with ``session_id`` and ``records`` (the new
            episodic anchor); ``mode`` is ``"records"``."""
        if not session_uuid:
            raise AnhurError("create_in_session: session_uuid is required")
        summary = text[:200] + "..." if len(text) > 200 else text
        payload: Dict[str, Any] = {
            "session_id": session_uuid,
            "type": "episodic",
            "dimension": 0,
            "prefix": "",
            "weight": 0.5,
            "score": 5,
            "vector": "",
            "related_ids": [],
            "main_ids": [],
            "consolidate_id": 0,
            "metadata": _build_metadata_json(self._container_tag),
            "summary": summary,
            "content": text,
            "consolidated": False,
            "status": "saved",
        }
        data = await self._connection.post("/api/v1/records", payload)
        return _add_result_from_record(
            data,
            session_id=session_uuid,
            record_type="episodic",
            summary=summary,
        )


__all__ = ["RecordCreateMixin"]
