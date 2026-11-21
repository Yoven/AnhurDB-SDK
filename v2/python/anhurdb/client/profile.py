"""The aggregated memory profile — one endpoint, one domain.

``GET /api/v1/profile`` is the only route that answers with a distilled VIEW
of a container tag rather than with records. Split out of
``client/__init__.py`` in 3.0.0 when the return became ``ProfileResult``; the
file was already past the 300-line house cut.
"""

from typing import Optional

from .connection import HTTPConnection
from ..models.profile import ProfileResult


class ProfileMixin:
    """Profile read for a container tag."""

    _connection: HTTPConnection
    _container_tag: str

    async def profile(
        self,
        container_tag: Optional[str] = None,
    ) -> ProfileResult:
        """Get the memory profile for a container tag (user/agent).

        Aggregates the container's memories into identity facts, preferences
        and aggregate statistics. Any HTTP error — a 404 included — is raised:
        ``GET /api/v1/profile`` is mounted unconditionally
        (``server/router.go:136``), so 3.x's "404 means OSS, return an empty
        profile" fallback (which also matched ``"404"`` anywhere in the
        message text) was removed in 4.0.0.

        Args:
            container_tag: User/agent identifier; ``None`` = this Memory's tag.

        Returns:
            ``ProfileResult`` with ``static``, ``dynamic`` and ``stats``.

        Junior Tip [the tag is an IN-TENANT filter, never a tenant selector]:
        the handler takes the tenant from the auth middleware context and reads
        ``tag`` only from the query string (``handler/profile.go:63-66``), so
        passing another container's tag cannot reach another tenant's data.
        Verified live 2026-09-14 with the owner key: ``?tag=hermes-1`` (a
        different container on the same tenant) answered 200 with that
        container's own small counters, and ``?tag=*`` was treated as a literal
        tag, not a wildcard.

        Junior Tip [an UNKNOWN tag is an empty profile with HTTP 200]: live,
        ``?tag=totally-not-a-real-tag-xyz`` returned every counter at zero and
        ``last_active: ""``. Do not translate that into an exception — a typo'd
        tag and a legitimately empty one are indistinguishable to the server,
        and raising would hide the typo behind a fake outage. What IS an error
        is an OMITTED tag (HTTP 400 ``tag: tag is required``), which is why
        this method never sends an empty string.

        Example::

            prof = await mem.profile()
            print(prof.static.facts)  # identity facts"""
        target_tag = container_tag if container_tag is not None else self._container_tag
        # Never send an empty tag: the server answers a guaranteed HTTP 400
        # ("tag: tag is required"), so an empty string is a bug we can catch
        # here instead of a round trip that always fails.
        if not target_tag:
            raise ValueError(
                "container_tag cannot be empty — GET /api/v1/profile requires "
                "a tag and answers HTTP 400 without one"
            )
        data = await self._connection.get(
            "/api/v1/profile",
            params={"tag": target_tag},
        )
        return ProfileResult.model_validate(data)


__all__ = ["ProfileMixin"]
