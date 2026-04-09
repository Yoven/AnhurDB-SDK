"""
Compressed payload storage for AnhurDB records.

Handles reading gzip-compressed JSON files from the NFS-backed storage
layer, with a REST API fallback when direct disk access is unavailable.

Security:
    - Path traversal protection: ``tenant_id``, ``uuid``, and ``record_id``
      are validated against directory escape characters (``..``, ``/``, ``\\``)
      before constructing file paths.
    - Symlink resolution: ``os.path.realpath`` is used to verify the resolved
      path stays within the configured ``base_path``.
"""

import os
import gzip
import json
import re
from typing import Any, Dict, Optional

import requests

# Regex that rejects any path component containing directory traversal
# or absolute path characters. Only allows alphanumeric, hyphens, underscores,
# and dots (not leading dots).
_SAFE_PATH_COMPONENT = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9._-]*$")


def _validate_path_component(value: str, name: str) -> None:
    """
    Validate that a path component is safe against directory traversal.

    Rejects:
        - Empty strings
        - Components containing ``..``
        - Components containing ``/`` or ``\\``
        - Components starting with ``.``
        - Components with null bytes

    Args:
        value: The path component to validate.
        name:  Human-readable name for error messages (e.g. "tenant_id").

    Raises:
        ValueError: If the component is unsafe.
    """
    if not value:
        raise ValueError(f"{name} cannot be empty")
    if "\x00" in value:
        raise ValueError(f"{name} contains null byte")
    if ".." in value:
        raise ValueError(f"{name} contains directory traversal sequence '..'")
    if "/" in value or "\\" in value:
        raise ValueError(f"{name} contains path separator")
    if not _SAFE_PATH_COMPONENT.match(value):
        raise ValueError(
            f"{name} contains invalid characters: only alphanumeric, "
            f"hyphens, underscores, and non-leading dots are allowed"
        )


class FileStorage:
    """
    Read compressed cognitive payload files from the NFS storage layer.

    AnhurDB stores record content as gzip-compressed JSON files at
    ``{base_path}/{tenant_id}/{uuid}/{record_id}.gz``.

    Args:
        base_path: Root directory for record storage (e.g. ``/data/storage``).
    """

    def __init__(self, base_path: str):
        self.base_path = os.path.realpath(base_path)

    def build_path(self, tenant_id: str, uuid: str, record_id: int) -> str:
        """
        Construct and validate the filesystem path for a record payload.

        All components are validated against path traversal before joining.
        The resulting path is verified to stay within ``base_path`` via
        ``os.path.realpath`` comparison.

        Args:
            tenant_id: Hex-encoded tenant identifier.
            uuid:      Session UUID.
            record_id: Numeric record ID.

        Returns:
            Absolute path to the ``.gz`` file.

        Raises:
            ValueError: If any component contains traversal characters.
        """
        _validate_path_component(tenant_id, "tenant_id")
        _validate_path_component(uuid, "uuid")

        if record_id < 0:
            raise ValueError("record_id must be non-negative")

        path = os.path.join(self.base_path, tenant_id, uuid, f"{record_id}.gz")

        # Final defense: verify resolved path is under base_path.
        real_path = os.path.realpath(path)
        if not real_path.startswith(self.base_path + os.sep):
            raise ValueError(
                f"Resolved path {real_path} escapes base directory {self.base_path}"
            )

        return real_path

    def read_json(self, tenant_id: str, uuid: str, record_id: int) -> Dict[str, Any]:
        """
        Read a gzip-compressed JSON payload from disk.

        Args:
            tenant_id: Hex-encoded tenant identifier.
            uuid:      Session UUID.
            record_id: Numeric record ID.

        Returns:
            Parsed JSON dict.

        Raises:
            FileNotFoundError: If the file does not exist.
            ValueError: If path components are unsafe.
        """
        path = self.build_path(tenant_id, uuid, record_id)
        if not os.path.exists(path):
            raise FileNotFoundError(f"Record payload not found: {path}")

        with gzip.open(path, "rt", encoding="utf-8") as f:
            return json.load(f)

    def read_json_with_fallback(
        self,
        tenant_id: str,
        uuid: str,
        record_id: int,
        api_url: Optional[str] = None,
        api_key: Optional[str] = None,
    ) -> Dict[str, Any]:
        """
        Read payload from disk, falling back to the REST API if unavailable.

        Tries direct disk read first (fastest). If the file doesn't exist
        (e.g. due to NFS isolation), transparently falls back to the
        ``/api/v1/records/{id}/content`` REST endpoint.

        Args:
            tenant_id: Hex-encoded tenant identifier.
            uuid:      Session UUID.
            record_id: Numeric record ID.
            api_url:   AnhurDB server URL for fallback (optional).
            api_key:   API key for fallback authentication (optional).

        Returns:
            Parsed JSON dict.

        Raises:
            FileNotFoundError: If both disk and API fallback fail.
            ValueError: If path components are unsafe.
        """
        path = self.build_path(tenant_id, uuid, record_id)
        if os.path.exists(path):
            with gzip.open(path, "rt", encoding="utf-8") as f:
                return json.load(f)

        # Fallback to REST API.
        if not api_url:
            raise FileNotFoundError(
                f"Record payload not found at {path} and no API fallback URL."
            )

        headers: Dict[str, str] = {}
        if api_key:
            headers["X-API-Key"] = api_key
        if tenant_id:
            headers["X-Tenant-ID"] = tenant_id

        # Validate record_id is numeric to prevent injection in URL.
        url = f"{api_url.rstrip('/')}/api/v1/records/{int(record_id)}/content"
        resp = requests.get(url, headers=headers, timeout=30)
        if not resp.ok:
            raise FileNotFoundError(
                f"Record not found locally and API fallback returned "
                f"HTTP {resp.status_code}"
            )

        return resp.json()
