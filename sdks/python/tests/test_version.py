"""Test that the package version is importable and correct."""

from __future__ import annotations

from attest import __version__


def test_version() -> None:
    """Verify attest version string."""
    assert __version__ == "0.4.1"
