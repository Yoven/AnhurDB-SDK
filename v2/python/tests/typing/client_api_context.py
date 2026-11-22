"""Type-checking fixture (not collected by pytest): run `mypy tests/typing`.

Proves `async with ClientAPI(...) as api` binds a ClientAPI, so its methods
type-check. Before 4.1.0 __aenter__ returned the base ClientKeyTransport and
mypy refused `api.create_child`."""
from anhurdb import ClientAPI, ChildClient


async def create_one_child(client_key: str) -> ChildClient:
    """Create (or confirm) one child through the async context manager."""
    async with ClientAPI(client_key) as api:
        return await api.create_child("account-1")
