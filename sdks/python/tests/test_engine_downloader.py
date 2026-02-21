"""Tests for engine_downloader and updated discovery chain."""

from __future__ import annotations

import os
from pathlib import Path
from unittest.mock import patch

import pytest

from attest.engine_downloader import (
    _binary_filename,
    _parse_checksums,
    _platform_key,
    cached_engine_path,
)
from attest.engine_manager import _find_engine_binary


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


# ── cached_engine_path ─────────────────────────────────────────────────

def test_cached_engine_path_missing(tmp_path: Path) -> None:
    """Returns None when no cached binary exists."""
    with patch("attest.engine_downloader._attest_bin_dir", return_value=tmp_path):
        assert cached_engine_path() is None


def test_cached_engine_path_version_mismatch(tmp_path: Path) -> None:
    """Returns None when cached version doesn't match ENGINE_VERSION."""
    bin_file = tmp_path / "attest-engine"
    bin_file.write_bytes(b"\x00")
    ver_file = tmp_path / ".engine-version"
    ver_file.write_text("0.0.0")

    with patch("attest.engine_downloader._attest_bin_dir", return_value=tmp_path):
        assert cached_engine_path() is None


def test_cached_engine_path_valid(tmp_path: Path) -> None:
    """Returns path when version matches and binary exists."""
    from attest import ENGINE_VERSION

    bin_file = tmp_path / "attest-engine"
    bin_file.write_bytes(b"\x00")
    ver_file = tmp_path / ".engine-version"
    ver_file.write_text(ENGINE_VERSION)

    with patch("attest.engine_downloader._attest_bin_dir", return_value=tmp_path), \
         patch("attest.engine_downloader._binary_filename", return_value="attest-engine"):
        result = cached_engine_path()
        assert result is not None
        assert result.name == "attest-engine"


# ── _find_engine_binary (discovery chain) ──────────────────────────────

def test_find_engine_env_override(tmp_path: Path) -> None:
    """ATTEST_ENGINE_PATH env var takes priority over all other methods."""
    fake_binary = tmp_path / "attest-engine"
    fake_binary.write_bytes(b"\x00")

    with patch.dict(os.environ, {"ATTEST_ENGINE_PATH": str(fake_binary)}):
        result = _find_engine_binary()
        assert result == str(fake_binary)


def test_find_engine_env_override_missing() -> None:
    """ATTEST_ENGINE_PATH pointing to nonexistent file raises FileNotFoundError."""
    with patch.dict(os.environ, {"ATTEST_ENGINE_PATH": "/nonexistent/attest-engine"}):
        with pytest.raises(FileNotFoundError, match="ATTEST_ENGINE_PATH"):
            _find_engine_binary()


def test_find_engine_no_download_raises() -> None:
    """ATTEST_ENGINE_NO_DOWNLOAD=1 produces actionable error when binary is missing."""
    env = {
        "ATTEST_ENGINE_NO_DOWNLOAD": "1",
        "ATTEST_ENGINE_PATH": "",
    }
    with patch.dict(os.environ, env, clear=False), \
         patch("shutil.which", return_value=None), \
         patch("attest.engine_downloader.cached_engine_path", return_value=None):
        with pytest.raises(FileNotFoundError, match="Cannot find"):
            _find_engine_binary()
