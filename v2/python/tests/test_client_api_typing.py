"""Runs mypy on tests/typing so the `async with ClientAPI(...) as api` typing
contract is enforced by the ordinary test suite, not only by a manual run."""
import pathlib
import subprocess
import sys
import unittest

PACKAGE_ROOT = pathlib.Path(__file__).resolve().parent.parent


class TestClientAPIContextManagerTyping(unittest.TestCase):
    """`async with ClientAPI(...) as api` must type as ClientAPI."""

    def test_mypy_accepts_methods_on_the_context_manager_value(self) -> None:
        try:
            import mypy  # noqa: F401
        except ImportError:  # pragma: no cover - dev dependency missing
            self.skipTest("mypy is not installed")
        completed = subprocess.run(
            [sys.executable, "-m", "mypy", "--no-incremental", "tests/typing/client_api_context.py"],
            cwd=PACKAGE_ROOT,
            capture_output=True,
            text=True,
        )
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)


if __name__ == "__main__":
    unittest.main()
