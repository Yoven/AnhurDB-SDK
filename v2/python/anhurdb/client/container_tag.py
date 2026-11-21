"""The container tag — how it is derived and how it rides on every write.

One domain: the SDK-owned ``container_tag`` that groups a client's records for
recall and profile. It is derived once from the API key (unless the caller
pins ``user_id``) and stamped into the metadata JSON of every record this SDK
writes, so container-scoped search and profile can find them again.

Split out of ``client/__init__.py`` in 4.0.0 (house 300-line cut). The tag is
NEVER a session substitute; sessions live in ``sessions.py``.
"""

import hashlib
import json
from typing import Any, Dict, Optional


def _derive_container_tag(api_key: str) -> str:
    """Derive a short, stable hex tag from the API key using SHA-256.

    The first 12 hex characters of the hash are used, prefixed with
    ``mem-``. This matches the algorithm in the TypeScript and Go SDKs
    so the same API key always produces the same container tag across
    all three languages.

    Args:
        api_key: The raw API key string.

    Returns:
        A container tag like ``mem-a1b2c3d4e5f6``."""
    digest = hashlib.sha256(api_key.encode("utf-8")).hexdigest()
    return f"mem-{digest[:12]}"


def _build_metadata_json(
    container_tag: str,
    extra_metadata: Optional[Dict[str, Any]] = None,
) -> str:
    """Wrap ``container_tag`` into the canonical metadata JSON envelope
    ``{"container_tag": "<tag>"}``, merged over the caller's own keys.

    Returns ``"{}"`` when there is nothing to write so the column always holds
    a parseable JSON object.

    Junior Tip [the SDK tag wins over a caller key of the same name]: the
    caller's dict is copied first and the tag written last. A caller metadata
    key ``container_tag`` would otherwise move the record out of the container
    this client recalls from — a write that succeeds and is never found."""
    envelope: Dict[str, Any] = {}
    if extra_metadata:
        envelope.update(extra_metadata)
    if container_tag:
        envelope["container_tag"] = container_tag
    if not envelope:
        return "{}"
    return json.dumps(envelope)


__all__ = ["_derive_container_tag", "_build_metadata_json"]
