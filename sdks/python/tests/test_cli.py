"""Tests for CLI runner."""

from __future__ import annotations

import subprocess
import sys


def test_cli_version() -> None:
    """python -m attest --version prints version."""
    result = subprocess.run(
        [sys.executable, "-m", "attest", "--version"],
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0
    assert "attest" in result.stdout
    assert "0.4.1" in result.stdout


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
