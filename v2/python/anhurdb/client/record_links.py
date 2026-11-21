"""Batch status and graph-link writes on existing records.

One domain: the PATCH routes the consolidation pipeline uses to move records
between statuses and to wire parent/child/related edges — none of them create
or delete a record, they only change how existing records relate.

Split out of ``client/__init__.py`` in 4.0.0 (house 300-line cut).

Junior Tip [the old names are gone — 4.0.0]: ``mark_consolidated`` (→
``batch_update_status(ids, "consolidated")``), ``link_to_consolidated`` and
``update_consolidate_ids`` (→ ``link_consolidated``) were forwarding aliases
that only emitted a ``DeprecationWarning``. Three names for one route is how a
fix lands on one spelling and not the others; each route now has exactly one
method.
"""

from typing import Any, Dict, List

from .connection import HTTPConnection
from .exceptions import AnhurError


class RecordLinkMixin:
    """Batch status updates and append-only graph links."""

    _connection: HTTPConnection

    async def batch_update_status(self, ids: List[int], status: str) -> Dict[str, Any]:
        """Update the status for a batch of records at once.

        Args:
            ids:    List of record IDs to update.
            status: New status (e.g. consolidated, hubbed, processing,
                    completed, failed).

        Returns:
            Confirmation dict with count of updated records."""
        return await self._connection.patch(
            "/api/v1/records/mark-consolidated",
            {"ids": ids, "status": status},
        )

    async def link_consolidated(
        self,
        ids: List[int],
        consolidate_id: int,
    ) -> Dict[str, Any]:
        """Set the parent consolidated record for a batch of child records.

        Links child records to their summary ("star") record after
        consolidation. Batched so N children pointing at the same star cost
        ONE server round-trip instead of N.

        Args:
            ids:             List of child record IDs.
            consolidate_id:  ID of the summary (parent) record.

        Returns:
            Confirmation dict (empty when ``ids`` is empty — no-op)."""
        if not ids:
            return {}
        if consolidate_id <= 0:
            raise AnhurError("link_consolidated: consolidate_id must be > 0")
        return await self._connection.patch(
            "/api/v1/records/consolidate-ids",
            {"ids": ids, "consolidate_id": consolidate_id},
        )

    async def append_main_ids(
        self,
        record_id: int,
        main_ids: List[int],
    ) -> Dict[str, Any]:
        """Append parent record IDs to the ``main_ids`` array of a single record.

        Server-side this reads, deduplicates, and writes back — idempotent on
        Go ``Memory.AppendMainIDs`` and TS ``appendMainIds``.

        Args:
            record_id: Child record that receives the parents.
            main_ids:  Parent IDs to append.

        Returns:
            Confirmation dict (empty when ``main_ids`` is empty — no-op)."""
        if record_id <= 0:
            raise AnhurError("append_main_ids: record_id must be > 0")
        if not main_ids:
            return {}
        payload = {"ids": [record_id], "main_ids_to_append": main_ids}
        return await self._connection.patch("/api/v1/records/append-main-ids", payload)

    async def append_related_ids(
        self,
        record_id: int,
        related_ids: List[int],
    ) -> Dict[str, Any]:
        """Append related record IDs to the ``related_ids`` array of a single record.

        Server-side this reads, deduplicates, and writes back — idempotent on
        mirror of ``append_main_ids`` on the sibling REST route
        ``PATCH /api/v1/records/append-related-ids`` (payload key
        ``related_ids_to_append``); keeps the Go/Python/TS SDK trio in lockstep
        (parity invariant #13). Append, never replace.

        Args:
            record_id:   Record that receives the related links.
            related_ids: Related IDs to append.

        Returns:
            Confirmation dict (empty when ``related_ids`` is empty — no-op)."""
        if record_id <= 0:
            raise AnhurError("append_related_ids: record_id must be > 0")
        if not related_ids:
            return {}
        payload = {"ids": [record_id], "related_ids_to_append": related_ids}
        return await self._connection.patch("/api/v1/records/append-related-ids", payload)

    async def append_main_links(
        self,
        ids: List[int],
        main_ids_to_append: List[int],
    ) -> Dict[str, Any]:
        """Append parent record IDs to a BATCH of records (non-destructive).

        Does NOT replace existing ``main_ids`` — only adds new links. Use this
        to build parent-child relationships in the knowledge graph across many
        records at once. For a single record, prefer ``append_main_ids``.

        Args:
            ids:                 Records to update.
            main_ids_to_append:  Parent IDs to add to each record's ``main_ids``.

        Returns:
            Confirmation dict."""
        return await self._connection.patch(
            "/api/v1/records/append-main-ids",
            {"ids": ids, "main_ids_to_append": main_ids_to_append},
        )


__all__ = ["RecordLinkMixin"]
