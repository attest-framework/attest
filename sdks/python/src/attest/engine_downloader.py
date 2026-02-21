"""Auto-download attest-engine binary from GitHub Releases."""

from __future__ import annotations

import hashlib
import os
import platform
import stat
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

from attest import ENGINE_VERSION

_GITHUB_RELEASE_BASE = (
    "https://github.com/attest-framework/attest/releases/download"
)

_PLATFORM_MAP: dict[str, str] = {
    "Darwin-arm64": "darwin-arm64",
    "Darwin-x86_64": "darwin-amd64",
    "Linux-aarch64": "linux-arm64",
    "Linux-x86_64": "linux-amd64",
    "Windows-AMD64": "windows-amd64",
    "Windows-ARM64": "windows-arm64",
}


def _attest_bin_dir() -> Path:
    """Return ``~/.attest/bin/``, creating it if absent."""
    bin_dir = Path.home() / ".attest" / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    return bin_dir


def _platform_key() -> str:
    """Map current OS/arch to the release asset naming convention."""
    system = platform.system()
    machine = platform.machine()
    key = f"{system}-{machine}"
    mapped = _PLATFORM_MAP.get(key)
    if mapped is None:
        raise RuntimeError(
            f"Unsupported platform: {key}. "
            f"Supported: {', '.join(sorted(_PLATFORM_MAP.values()))}"
        )
    return mapped


def _binary_filename() -> str:
    """Return the engine binary filename for the current OS."""
    if platform.system() == "Windows":
        return "attest-engine.exe"
    return "attest-engine"


def _version_file() -> Path:
    """Return the path to the cached engine version marker."""
    return _attest_bin_dir() / ".engine-version"


def cached_engine_path() -> Path | None:
    """Return the cached binary path if it exists and matches ENGINE_VERSION."""
    bin_path = _attest_bin_dir() / _binary_filename()
    if not bin_path.is_file():
        return None

    ver_file = _version_file()
    if not ver_file.is_file():
        return None

    cached_version = ver_file.read_text().strip()
    if cached_version != ENGINE_VERSION:
        return None

    return bin_path


def _parse_checksums(text: str) -> dict[str, str]:
    """Parse a checksums file with ``{hash}  {filename}`` lines."""
    checksums: dict[str, str] = {}
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        # Format: sha256hash  filename (two spaces)
        parts = line.split(None, 1)
        if len(parts) == 2:
            checksums[parts[1].strip()] = parts[0]
    return checksums


def _url_read(url: str) -> bytes:
    """Fetch a URL, following redirects. Stdlib-only."""
    request = urllib.request.Request(url, headers={"User-Agent": "attest-sdk"})
    with urllib.request.urlopen(request, timeout=120) as resp:
        return resp.read()  # type: ignore[no-any-return]


def download_engine() -> Path:
    """Download the engine binary from GitHub Releases with SHA256 verification.

    Returns the path to the downloaded binary.

    Raises:
        RuntimeError: On download failure, checksum mismatch, or unsupported platform.
    """
    plat = _platform_key()
    ver = ENGINE_VERSION
    bin_name = _binary_filename()
    asset_name = f"attest-engine-{plat}"
    if platform.system() == "Windows":
        asset_name += ".exe"

    binary_url = f"{_GITHUB_RELEASE_BASE}/v{ver}/{asset_name}"
    checksums_url = f"{_GITHUB_RELEASE_BASE}/v{ver}/checksums-sha256.txt"

    sys.stderr.write(f"attest: downloading engine v{ver} for {plat}...\n")

    # Fetch checksums first
    try:
        checksums_text = _url_read(checksums_url).decode("utf-8")
    except (urllib.error.URLError, OSError) as exc:
        raise RuntimeError(
            f"Failed to download checksums from {checksums_url}: {exc}\n"
            "Verify the release exists at "
            f"https://github.com/attest-framework/attest/releases/tag/v{ver}"
        ) from exc

    checksums = _parse_checksums(checksums_text)
    expected_hash = checksums.get(asset_name)
    if expected_hash is None:
        raise RuntimeError(
            f"No checksum found for '{asset_name}' in checksums-sha256.txt. "
            f"Available assets: {', '.join(sorted(checksums.keys()))}"
        )

    # Download binary
    try:
        binary_data = _url_read(binary_url)
    except (urllib.error.URLError, OSError) as exc:
        raise RuntimeError(
            f"Failed to download engine from {binary_url}: {exc}"
        ) from exc

    # Verify SHA256
    actual_hash = hashlib.sha256(binary_data).hexdigest()
    if actual_hash != expected_hash:
        raise RuntimeError(
            f"SHA256 mismatch for {asset_name}:\n"
            f"  expected: {expected_hash}\n"
            f"  actual:   {actual_hash}\n"
            "The download may be corrupted. Retry or download manually."
        )

    # Atomic write: temp file + rename
    bin_dir = _attest_bin_dir()
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

    # Write version marker atomically
    ver_file = _version_file()
    fd_v, tmp_ver = tempfile.mkstemp(dir=bin_dir, prefix=".engine-version-tmp-")
    try:
        with os.fdopen(fd_v, "w") as f:
            f.write(ver)
        os.replace(tmp_ver, ver_file)
    except BaseException:
        Path(tmp_ver).unlink(missing_ok=True)
        raise

    sys.stderr.write(f"attest: engine v{ver} installed to {target}\n")
    return target
