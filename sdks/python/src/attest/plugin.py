"""Attest pytest plugin — registered via entry point."""

from __future__ import annotations

import pytest


def pytest_configure(config: pytest.Config) -> None:
    """Register attest marker."""
    config.addinivalue_line("markers", "attest: mark test as an Attest agent test")
