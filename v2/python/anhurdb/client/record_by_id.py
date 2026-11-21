"""By-id record reads and writes, addressed to one store.

One domain in one sentence: read or change one record (or a batch) by id, in
the store the caller names with ``scope=``, or in the caller's own store when
no scope is given. Parity: Go ``client/record_by_id.go`` +
``client/record_mutate.go``, TypeScript ``src/recordsApi.ts``.

Moved out of ``client/__init__.py`` (past the house 300-line cut) on
2026-11-20 when every one of these methods gained the same ``scope`` keyword;
the bodies are unchanged apart from it.

Server truth (AnhurDB fix/l2-lib-scope): ``server/router.go`` wraps
GET/PATCH/DELETE ``/api/v1/records/{id}`` and ``/content`` in
``rh.ScopedByID``; ``POST /records/batch-content`` and ``/records/supersede``
read ``?scope=`` themselves (``handler/record_batch.go``,
``handler/record_supersede.go``). A row outside the named plane is 404 for a
single id and silently OMITTED from a batch.

Junior Tip [why a mutation needs the scope even more than a read]: an id taken
from a tenant_shared hit names a row of ``{c}_{t}_lib``; the same integer names
an unrelated chat row in ``{c}_{t}``. An unscoped PATCH/DELETE/supersede would
change THAT chat row and answer 200 — a valid-looking success on the wrong
record. With a scope the server proves the row lives in the plane or answers
404.
"""

from typing import Any, Dict, List, Optional

from .connection import HTTPConnection
from .record_metadata_patch import encode_update_metadata
from .record_scope import resolve_record_scope, scope_params


class RecordByIdMixin:
    """get / read_content / batch_read_content / update / set_score / delete / supersede."""

    _connection: HTTPConnection

    async def get(self, record_id: int, *, scope: Optional[str] = None) -> Dict[str, Any]:
        """Get a record's metadata by ID.

        Args:
            record_id: The record ID.
            scope:     Plane the id lives in (``sessions`` | ``tenant_shared`` |
                       ``tenant_shared_legacy`` | ``client_shared``). For a
                       tenant_shared hit use ``tenant_shared_scope_for_hit``.
                       ``None`` = your own store (unchanged behaviour).

        Returns:
            Record metadata dict."""
        resolved_scope = resolve_record_scope("get", scope)
        return await self._connection.get(
            f"/api/v1/records/{record_id}", params=scope_params(resolved_scope)
        )

    async def read_content(self, record_id: int, *, scope: Optional[str] = None) -> Any:
        """Read the full content payload for a record.

        Args:
            record_id: The record ID to read.
            scope:     Plane the id lives in (see ``get``).

        Returns:
            The content payload. Type depends on what was stored:
            a dict for structured records, a string for plain text."""
        resolved_scope = resolve_record_scope("read_content", scope)
        return await self._connection.get(
            f"/api/v1/records/{record_id}/content",
            params=scope_params(resolved_scope),
            raw_text=True,
        )

    async def batch_read_content(
        self, ids: List[int], *, scope: Optional[str] = None
    ) -> Dict[str, Any]:
        """Fetch full content for multiple records in a single call (max 100).

        Eliminates the N+1 pattern of calling ``read_content`` in a loop. With
        ``scope``, ids whose row is outside that plane are OMITTED from the
        result (the server treats them like missing ids) — check the keys.

        Args:
            ids:   List of record IDs (max 100).
            scope: Plane the ids live in (see ``get``).

        Returns:
            Dict mapping ``record_id → content_payload``."""
        resolved_scope = resolve_record_scope("batch_read_content", scope)
        data = await self._connection.post(
            "/api/v1/records/batch-content",
            {"ids": ids},
            params=scope_params(resolved_scope),
        )
        return data if isinstance(data, dict) else {}

    async def update(self, record_id: int, *, scope: Optional[str] = None, **fields: Any) -> None:
        """Partially update a record.

        ``metadata`` is an RFC 7396 JSON merge patch applied by the server onto
        the stored metadata object (AnhurDB ADR-0035): keys present overwrite,
        a ``None``/null value deletes the key, keys absent from the patch are
        left untouched, nested objects merge and lists are replaced whole. It
        is NOT a full replace: to drop a key, send it as ``None``. Pass it as a
        JSON string (unchanged since 3.x) or as a ``dict`` — the dict is
        JSON-encoded with ``None`` kept as null (see
        ``record_metadata_patch.py``). A patch that is not a JSON object, or an
        update over stored metadata that is not a JSON object, is refused by
        the server with 400 (``AnhurQueryError``, ``kind == "invalid_request"``).

        Junior Tip [read-merge-write is no longer needed]: before ADR-0035 the
        server replaced the whole metadata column, so callers read the record,
        merged their keys and wrote the full dict back — and a concurrent
        writer's keys were lost in between. Send only the keys you change; the
        merge runs inside the Raft apply, so two patches on different keys both
        survive.

        Args:
            record_id: The record ID to update.
            scope:     Plane the id lives in (see ``get``). Keyword-only, so it
                       can never be mistaken for a record field.
            **fields:  Keyword arguments for fields to update
                       (e.g. ``summary="new"``, ``status="archived"``,
                       ``metadata={"stale_key": None, "owner": "ana"}``).

        Raises:
            ValueError: if ``score`` is among the fields — use
                :meth:`set_score`, which reaches the endpoint that
                actually persists it.
            AnhurQueryError: invalid_request (400) before any request when a
                ``metadata`` dict cannot be encoded as JSON.

        Example::

            await mem.update(42, summary="Updated summary")
            await mem.update(42, scope="tenant_shared", summary="Library fix")
            await mem.update(42, metadata={"reviewed": True, "draft_of": None})"""
        if "score" in fields:
            raise ValueError(
                "update() cannot change score: PATCH /api/v1/records/{id} has no "
                "score field and answers 200 while dropping the key. Use "
                "set_score(record_id, score), which posts to "
                "/api/v1/records/set-score (a replicated command). Measured "
                "2026-08-15; this call used to succeed and write nothing."
            )
        resolved_scope = resolve_record_scope("update", scope)
        encoded_fields = encode_update_metadata("update", fields)
        await self._connection.patch(
            f"/api/v1/records/{record_id}", encoded_fields, params=scope_params(resolved_scope)
        )

    async def set_score(self, record_id: int, score: int) -> None:
        """Set a record's importance score (1-10) durably.

        ``update(record_id, score=...)`` cannot do this: the PATCH handler has
        no score field, so it returns success and changes nothing — the same
        shape as an earlier defect with ``archived``. This method posts to
        ``/api/v1/records/set-score``, which dispatches a replicated command
        and invalidates the read cache.

        The score feeds the value term of the cognitive weight, which in turn
        selects the record's embedding fidelity band — so a correction here
        propagates to ranking on the next maintenance pass, not instantly.

        Args:
            record_id: The record ID.
            score:     Importance, 1-10.

        Raises:
            ValueError: if ``score`` is outside 1-10.

        Example::

            await mem.set_score(42, 8)"""
        if score < 1 or score > 10:
            raise ValueError("score must be between 1 and 10, got %r" % (score,))
        await self._connection.post(
            "/api/v1/records/set-score", {"ids": [record_id], "score": score}
        )

    async def delete(self, record_id: int, *, scope: Optional[str] = None) -> None:
        """Archive a record by ID (soft delete — the server sets ``archived=1``
        and answers "record archived": the record becomes invisible to default
        queries but is NOT physically removed; no hard-delete route exists for
        a record id).

        Args:
            record_id: The record ID to archive.
            scope:     Plane the id lives in (see ``get``)."""
        resolved_scope = resolve_record_scope("delete", scope)
        await self._connection.delete(
            f"/api/v1/records/{record_id}", params=scope_params(resolved_scope)
        )

    async def supersede(
        self, old_id: int, new_id: int, *, scope: Optional[str] = None
    ) -> Dict[str, Any]:
        """Mark an old record as superseded by a new one.

        This implements temporal versioning — the old record remains in the
        graph but is annotated with ``superseded_by`` pointing to the new
        record. Search results prefer the newer version.

        Args:
            old_id: The record being superseded.
            new_id: The replacement record.
            scope:  Plane BOTH ids live in (see ``get``); the server answers
                    404 and supersedes nothing when either is outside it.

        Returns:
            Confirmation dict."""
        resolved_scope = resolve_record_scope("supersede", scope)
        return await self._connection.post(
            "/api/v1/records/supersede",
            {"old_id": old_id, "new_id": new_id},
            params=scope_params(resolved_scope),
        )


__all__ = ["RecordByIdMixin"]
