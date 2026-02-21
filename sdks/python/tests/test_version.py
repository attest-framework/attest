"""Test that the package version is importable and correct."""

from __future__ import annotations

from attest import __version__


def test_version() -> None:
    """Verify attest version string is a valid semver."""
    parts = __version__.split(".")
    assert len(parts) == 3
    assert all(part.isdigit() for part in parts)
