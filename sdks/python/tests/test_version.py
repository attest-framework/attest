"""Test that the package version is importable and correct."""

from __future__ import annotations

import re

from attest import __version__

# Loose semver: <major>.<minor>.<patch>[-<prerelease>] where major/minor/patch
# are digits and the optional pre-release segment is alphanumeric (e.g. "rc",
# "rc.1", "alpha"). Matches both "1.0.0" and "1.0.0-rc".
_SEMVER = re.compile(r"^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$")


def test_version() -> None:
    """Verify attest version string parses as semver (release or pre-release)."""
    assert _SEMVER.match(__version__), f"version {__version__!r} is not valid semver"
