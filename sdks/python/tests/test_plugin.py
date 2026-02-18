"""Tests for pytest plugin registration."""

from __future__ import annotations

import pytest
from attest.plugin import AttestEngineFixture


def test_plugin_fixture_class_exists() -> None:
    """AttestEngineFixture is importable."""
    assert AttestEngineFixture is not None


def test_plugin_markers(pytestconfig: pytest.Config) -> None:
    """Attest markers are registered."""
    marker_names = []
    for m in pytestconfig.getini("markers"):
        if isinstance(m, str):
            marker_names.append(m.split(":")[0].strip())
    assert "attest" in marker_names
