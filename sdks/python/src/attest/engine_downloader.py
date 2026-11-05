"""Auto-download attest-engine binary from GitHub Releases."""

from __future__ import annotations

import hashlib
import http.client
import json
import os
import platform
import re
import ssl
import stat
import sys
import tempfile
import time
import urllib.parse
from pathlib import Path

from attest import ENGINE_VERSION, __version__

_GITHUB_RELEASE_BASE = "https://github.com/attest-framework/attest/releases/download"

_TABLE_PATH = Path(__file__).with_name("engine-checksums.json")
_CONNECT_TIMEOUT_S = 10.0
_TOTAL_TIMEOUT_S = 120.0
_MAX_REDIRECTS = 5
_REDIRECT_STATUSES = frozenset({301, 302, 303, 307, 308})
_SHA256_HEX = re.compile(r"[0-9a-f]{64}")

_PLATFORM_MAP: dict[str, str] = {
    "Darwin-arm64": "darwin-arm64",
    "Darwin-x86_64": "darwin-amd64",
    "Linux-aarch64": "linux-arm64",
    "Linux-x86_64": "linux-amd64",
    "Windows-AMD64": "windows-amd64",
    "Windows-ARM64": "windows-arm64",
}


def versioned_bin_dir() -> Path:
    """Return ``~/.attest/bin/<ENGINE_VERSION>/``, the cache directory for this SDK's engine.

    The directory name is the canonical engine version (for example ``1.0.0``), never the
    normalized Python package version. The legacy unversioned ``~/.attest/bin/attest-engine``
    is neither read nor modified.
    """
    return Path.home() / ".attest" / "bin" / ENGINE_VERSION


def _platform_key() -> str:
    """Map current OS/arch to the release asset naming convention."""
    system = platform.system()
    machine = platform.machine()
    key = f"{system}-{machine}"
    mapped = _PLATFORM_MAP.get(key)
    if mapped is None:
        raise RuntimeError(
            f"Unsupported platform: {key}. Supported: {', '.join(sorted(_PLATFORM_MAP.values()))}"
        )
    return mapped


def _binary_filename() -> str:
    """Return the engine binary filename for the current OS."""
    if platform.system() == "Windows":
        return "attest-engine.exe"
    return "attest-engine"


def cached_engine_path() -> Path | None:
    """Return the engine binary in the versioned cache, or ``None`` if it is absent."""
    bin_path = versioned_bin_dir() / _binary_filename()
    if not bin_path.is_file():
        return None
    return bin_path


def _ssl_context() -> ssl.SSLContext:
    """TLS context for release downloads: system trust store, hostname checking on."""
    return ssl.create_default_context()


def _fetch(url: str) -> bytes:
    """GET ``url`` over https, following redirects that stay on https.

    A redirect to any other scheme is refused. A relative ``Location`` resolves against
    the URL that returned it. The connect phase is bounded by ``_CONNECT_TIMEOUT_S`` and
    the whole fetch, including every redirect hop and body read, by ``_TOTAL_TIMEOUT_S``.
    """
    deadline = time.monotonic() + _TOTAL_TIMEOUT_S

    def remaining() -> float:
        left = deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError(f"exceeded the {_TOTAL_TIMEOUT_S:g}s total timeout")
        return left

    current = url
    for _ in range(_MAX_REDIRECTS + 1):
        parts = urllib.parse.urlsplit(current)
        if parts.scheme != "https" or not parts.hostname:
            raise RuntimeError(f"Refusing to download over a non-https URL: {current}")
        target = parts.path or "/"
        if parts.query:
            target += f"?{parts.query}"
        conn = http.client.HTTPSConnection(
            parts.hostname,
            parts.port,
            timeout=min(_CONNECT_TIMEOUT_S, remaining()),
            context=_ssl_context(),
        )
        try:
            conn.connect()
            sock = conn.sock
            if sock is None:
                raise RuntimeError(f"Connection to {parts.hostname} closed before the request")
            sock.settimeout(remaining())
            conn.request("GET", target, headers={"User-Agent": "attest-sdk"})
            resp = conn.getresponse()
            if resp.status in _REDIRECT_STATUSES:
                location = resp.getheader("Location")
                if not location:
                    raise RuntimeError(f"Redirect {resp.status} without Location from {current}")
                current = urllib.parse.urljoin(current, location)
                continue
            if resp.status != 200:
                raise RuntimeError(f"HTTP {resp.status} fetching {current}")
            chunks: list[bytes] = []
            while not resp.isclosed():
                sock.settimeout(remaining())
                chunks.append(resp.read(65536))
            return b"".join(chunks)
        finally:
            conn.close()
    raise RuntimeError(f"Too many redirects (>{_MAX_REDIRECTS}) following {url}")


def _embedded_hash(plat: str) -> str:
    """Return the sha256 this package ships for ``ENGINE_VERSION`` on ``plat``."""
    if not _TABLE_PATH.is_file():
        raise RuntimeError(
            "This attest-ai install has no embedded engine hash table (engine-checksums.json), "
            "which is how a source or editable install looks, so it never downloads an engine. "
            "Point ATTEST_ENGINE_PATH at an engine binary, or build one with `make engine` "
            "and set ATTEST_ENGINE_PATH=./bin/attest-engine."
        )
    try:
        table = json.loads(_TABLE_PATH.read_text(encoding="utf-8"))
        digest = table[ENGINE_VERSION][plat]
    except (ValueError, KeyError, TypeError) as exc:
        raise RuntimeError(
            f"attest-ai {__version__} ships no usable engine hash for v{ENGINE_VERSION} on "
            f"{plat} in {_TABLE_PATH}: {type(exc).__name__}: {exc}"
        ) from exc
    if not isinstance(digest, str) or _SHA256_HEX.fullmatch(digest) is None:
        raise RuntimeError(
            f"attest-ai {__version__} ships a malformed engine hash for {plat}: {digest!r}"
        )
    return digest


def download_engine() -> Path:
    """Download the engine binary from GitHub Releases and verify it against the shipped hash.

    The only trusted hash is the table embedded in this package (``engine-checksums.json``),
    generated from the same release run's binaries. Nothing the download server sends can
    change what is accepted.

    Returns the path to the downloaded binary.

    Raises:
        RuntimeError: No embedded table (source or editable install), unsupported platform,
            download failure, or a binary that does not match the shipped hash.
    """
    plat = _platform_key()
    ver = ENGINE_VERSION
    bin_name = _binary_filename()
    asset_name = f"attest-engine-{plat}"
    if platform.system() == "Windows":
        asset_name += ".exe"

    expected_hash = _embedded_hash(plat)
    binary_url = f"{_GITHUB_RELEASE_BASE}/v{ver}/{asset_name}"

    sys.stderr.write(f"attest: downloading engine v{ver} for {plat}...\n")
    try:
        binary_data = _fetch(binary_url)
    except (OSError, http.client.HTTPException) as exc:
        raise RuntimeError(f"Failed to download engine from {binary_url}: {exc}") from exc

    actual_hash = hashlib.sha256(binary_data).hexdigest()
    if actual_hash != expected_hash:
        raise RuntimeError(
            f"{asset_name} does not match the hash shipped in attest-ai {__version__}:\n"
            f"  expected: {expected_hash}\n"
            f"  actual:   {actual_hash}\n"
            "Refusing to install it. Retry, or set ATTEST_ENGINE_PATH to a binary you trust."
        )

    # Atomic write: temp file + rename
    bin_dir = versioned_bin_dir()
    bin_dir.mkdir(parents=True, exist_ok=True)
    target = bin_dir / bin_name

    fd, tmp_path = tempfile.mkstemp(dir=bin_dir, prefix=".attest-engine-tmp-")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(binary_data)
        os.chmod(tmp_path, stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP | stat.S_IROTH | stat.S_IXOTH)
        os.replace(tmp_path, target)
    except BaseException:
        Path(tmp_path).unlink(missing_ok=True)
        raise

    sys.stderr.write(f"attest: engine v{ver} installed to {target}\n")
    return target
