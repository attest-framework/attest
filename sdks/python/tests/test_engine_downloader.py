"""Tests for engine_downloader: versioned cache and checksum-verified download."""

from __future__ import annotations

import hashlib
import os
import platform
from pathlib import Path
from unittest.mock import patch

import pytest

from attest import ENGINE_VERSION
from attest.engine_downloader import (
    _binary_filename,
    _parse_checksums,
    _platform_key,
    cached_engine_path,
    download_engine,
    versioned_bin_dir,
)

# ── _platform_key ──────────────────────────────────────────────────────


@patch("attest.engine_downloader.platform")
def test_platform_key_darwin_arm64(mock_platform: object) -> None:
    """Maps Darwin arm64 to darwin-arm64."""
    mock_platform.system = lambda: "Darwin"  # type: ignore[attr-defined]
    mock_platform.machine = lambda: "arm64"  # type: ignore[attr-defined]
    assert _platform_key() == "darwin-arm64"


@patch("attest.engine_downloader.platform")
def test_platform_key_linux_x86_64(mock_platform: object) -> None:
    """Maps Linux x86_64 to linux-amd64."""
    mock_platform.system = lambda: "Linux"  # type: ignore[attr-defined]
    mock_platform.machine = lambda: "x86_64"  # type: ignore[attr-defined]
    assert _platform_key() == "linux-amd64"


@patch("attest.engine_downloader.platform")
def test_platform_key_unsupported(mock_platform: object) -> None:
    """Raises RuntimeError for unsupported platform."""
    mock_platform.system = lambda: "FreeBSD"  # type: ignore[attr-defined]
    mock_platform.machine = lambda: "mips"  # type: ignore[attr-defined]
    with pytest.raises(RuntimeError, match="Unsupported platform"):
        _platform_key()


# ── _binary_filename ───────────────────────────────────────────────────


@patch("attest.engine_downloader.platform")
def test_binary_filename_unix(mock_platform: object) -> None:
    """Returns 'attest-engine' on non-Windows."""
    mock_platform.system = lambda: "Linux"  # type: ignore[attr-defined]
    assert _binary_filename() == "attest-engine"


@patch("attest.engine_downloader.platform")
def test_binary_filename_windows(mock_platform: object) -> None:
    """Returns 'attest-engine.exe' on Windows."""
    mock_platform.system = lambda: "Windows"  # type: ignore[attr-defined]
    assert _binary_filename() == "attest-engine.exe"


# ── _parse_checksums ───────────────────────────────────────────────────


def test_parse_checksums() -> None:
    """Parses standard checksums-sha256.txt format."""
    text = (
        "abc123def456  attest-engine-darwin-arm64\n"
        "789fed012345  attest-engine-linux-amd64\n"
        "\n"
        "deadbeef0000  attest-engine-windows-amd64.exe\n"
    )
    result = _parse_checksums(text)
    assert result == {
        "attest-engine-darwin-arm64": "abc123def456",
        "attest-engine-linux-amd64": "789fed012345",
        "attest-engine-windows-amd64.exe": "deadbeef0000",
    }


def test_parse_checksums_empty() -> None:
    """Handles empty input gracefully."""
    assert _parse_checksums("") == {}


# ── versioned cache ────────────────────────────────────────────────────


def test_versioned_bin_dir_uses_canonical_engine_version(isolated_engine_env: Path) -> None:
    assert versioned_bin_dir() == isolated_engine_env / ".attest" / "bin" / ENGINE_VERSION


def test_cached_engine_path_missing_returns_none(isolated_engine_env: Path) -> None:
    assert cached_engine_path() is None


def test_cached_engine_path_ignores_legacy_unversioned_binary(isolated_engine_env: Path) -> None:
    legacy = isolated_engine_env / ".attest" / "bin" / _binary_filename()
    legacy.parent.mkdir(parents=True)
    legacy.write_bytes(b"old")

    assert cached_engine_path() is None


def test_cached_engine_path_present_returns_versioned_binary(isolated_engine_env: Path) -> None:
    binary = versioned_bin_dir() / _binary_filename()
    binary.parent.mkdir(parents=True)
    binary.write_bytes(b"engine")

    assert cached_engine_path() == binary


# ── download_engine ────────────────────────────────────────────────────


def _release(binary: bytes, digest: str) -> dict[str, bytes]:
    asset = f"attest-engine-{_platform_key()}"
    if platform.system() == "Windows":
        asset += ".exe"
    base = f"https://github.com/attest-framework/attest/releases/download/v{ENGINE_VERSION}"
    return {
        f"{base}/checksums-sha256.txt": f"{digest}  {asset}\n".encode(),
        f"{base}/{asset}": binary,
    }


def test_download_engine_writes_versioned_cache_and_keeps_legacy(
    isolated_engine_env: Path,
) -> None:
    legacy = isolated_engine_env / ".attest" / "bin" / _binary_filename()
    legacy.parent.mkdir(parents=True)
    legacy.write_bytes(b"old engine")
    binary = b"new engine"
    release = _release(binary, hashlib.sha256(binary).hexdigest())

    with patch("attest.engine_downloader._url_read", side_effect=release.__getitem__):
        target = download_engine()

    assert target == versioned_bin_dir() / _binary_filename()
    assert target.read_bytes() == binary
    assert os.access(target, os.X_OK)
    assert legacy.read_bytes() == b"old engine"
    assert sorted(p.name for p in versioned_bin_dir().iterdir()) == [_binary_filename()]


def test_download_engine_tampered_binary_raises_and_writes_nothing(
    isolated_engine_env: Path,
) -> None:
    release = _release(b"tampered", hashlib.sha256(b"published").hexdigest())

    with (
        patch("attest.engine_downloader._url_read", side_effect=release.__getitem__),
        pytest.raises(RuntimeError, match="SHA256 mismatch"),
    ):
        download_engine()

    assert cached_engine_path() is None
