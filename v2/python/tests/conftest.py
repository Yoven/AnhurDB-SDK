"""Pytest fixtures shared by the live AST suite.

Junior Tip [why the fixture lives here and not in the test module]: it is
``scope="session"`` and it WRITES to a real database. Defined in a test module
and imported into a second one, pytest registers it twice and seeds (and then
deletes) the disposable session twice — double the writes against production for
no extra coverage. A conftest registration is shared by every module below it,
so exactly one session is seeded per ``pytest`` run.
"""

from ast_live_harness import ast_fixture  # noqa: F401
