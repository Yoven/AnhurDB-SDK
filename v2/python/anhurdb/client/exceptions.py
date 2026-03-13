class AnhurError(Exception):
    """Base exception for all AnhurDB errors."""
    pass

class AnhurAuthError(AnhurError):
    """Raised when authentication fails (e.g., invalid API key)."""
    pass

class AnhurQueryError(AnhurError):
    """Raised when a query is invalid or rejected by the server."""
    pass

class AnhurConnectionError(AnhurError):
    """Raised when the client cannot reach the AnhurDB server."""
    pass
