"""The single source of truth for this SDK's version.

Junior Tip [why a module and not just ``pyproject.toml``]: before 2.1.0 this
package carried FOUR mutually inconsistent version claims — ``pyproject.toml``
said 2.0.0, the ``User-Agent`` on every request said 2.1, the README pinned a
2.0.12 wheel, and the newest CHANGELOG heading said 2.0.2. The one a server
operator actually SEES in an access log is the User-Agent, so the number that
travels on the wire has to be derived, never retyped: a literal is a fourth
truth waiting to drift.

``pyproject.toml`` still carries its own copy because Poetry cannot read a
version out of the package at build time. That copy is locked to this one by
``tests/test_version.py`` — the release pipeline rewrites the manifest with
``sed``, so without the lock a release would ship a wheel whose metadata and
whose ``User-Agent`` disagreed, silently.
"""

__version__ = "2.1.0"

# The exact value sent as the HTTP ``User-Agent`` header on every request.
# Shape mirrors the Go and TypeScript SDKs (``AnhurSDK-Golang/<v>`` and
# ``AnhurSDK-TypeScript/<v>``) so one grep over server logs answers "which SDK,
# which version" for all three languages.
USER_AGENT = f"AnhurSDK-Python/{__version__}"

__all__ = ["__version__", "USER_AGENT"]
