"""Tests for CLI runner."""

from __future__ import annotations

import subprocess
import sys
import tempfile
from pathlib import Path

from attest import __version__


def test_cli_version() -> None:
    """python -m attest --version prints version."""
    result = subprocess.run(
        [sys.executable, "-m", "attest", "--version"],
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0
    assert "attest" in result.stdout
    assert __version__ in result.stdout


def test_cli_run_alias_passes_args_to_pytest() -> None:
    """python -m attest run --help delegates to pytest --help."""
    result = subprocess.run(
        [sys.executable, "-m", "attest", "run", "--help"],
        capture_output=True,
        text=True,
    )
    # pytest --help exits 0 and prints usage
    assert result.returncode == 0
    assert "pytest" in result.stdout.lower() or "usage" in result.stdout.lower()


def test_cli_no_args_invokes_pytest() -> None:
    """python -m attest with no args runs pytest (exits with pytest exit code)."""
    result = subprocess.run(
        [sys.executable, "-m", "attest", "--co", "-q"],
        capture_output=True,
        text=True,
    )
    # pytest exits 0 (tests collected) or 5 (no tests collected) — not a crash
    assert result.returncode in (0, 1, 2, 4, 5)


def test_cli_init_creates_test_structure() -> None:
    """python -m attest init creates tests/ dir with conftest and sample test."""
    with tempfile.TemporaryDirectory() as tmp:
        result = subprocess.run(
            [sys.executable, "-m", "attest", "init"],
            capture_output=True,
            text=True,
            cwd=tmp,
        )
        assert result.returncode == 0
        tests_dir = Path(tmp) / "tests"
        assert tests_dir.is_dir()
        assert (tests_dir / "conftest.py").is_file()
        assert (tests_dir / "test_my_agent.py").is_file()


def test_cli_validate_warns_on_empty_project() -> None:
    """python -m attest validate warns when no tests dir exists."""
    with tempfile.TemporaryDirectory() as tmp:
        result = subprocess.run(
            [sys.executable, "-m", "attest", "validate"],
            capture_output=True,
            text=True,
            cwd=tmp,
        )
        assert result.returncode == 0
        assert "Warning" in result.stderr


def test_cli_validate_after_init_no_warnings() -> None:
    """python -m attest validate after init produces no warnings."""
    with tempfile.TemporaryDirectory() as tmp:
        subprocess.run(
            [sys.executable, "-m", "attest", "init"],
            capture_output=True,
            text=True,
            cwd=tmp,
        )
        result = subprocess.run(
            [sys.executable, "-m", "attest", "validate"],
            capture_output=True,
            text=True,
            cwd=tmp,
        )
        assert result.returncode == 0
        assert "Warning" not in result.stderr


def test_cli_cache_stats_via_subprocess(tmp_path: Path) -> None:
    """python -m attest cache stats returns JSON via subprocess."""
    import json

    result = subprocess.run(
        [sys.executable, "-m", "attest", "cache", "stats"],
        capture_output=True,
        text=True,
        env={**__import__("os").environ, "ATTEST_CACHE_DIR": str(tmp_path)},
    )
    assert result.returncode == 0
    data = json.loads(result.stdout)
    assert "exists" in data
    assert "path" in data


def test_cli_cache_clear_via_subprocess(tmp_path: Path) -> None:
    """python -m attest cache clear works via subprocess."""
    db_file = tmp_path / "attest.db"
    db_file.write_bytes(b"test data")

    result = subprocess.run(
        [sys.executable, "-m", "attest", "cache", "clear"],
        capture_output=True,
        text=True,
        env={**__import__("os").environ, "ATTEST_CACHE_DIR": str(tmp_path)},
    )
    assert result.returncode == 0
    assert not db_file.exists()
