"""Integration test fixtures requiring a built engine binary."""

from __future__ import annotations

import os
from collections.abc import Generator

import pytest

from attest.plugin import AttestEngineFixture


def _engine_binary_path() -> str:
    """Resolve path to the engine binary built by `make engine`."""
    repo_root = os.path.abspath(
        os.path.join(os.path.dirname(__file__), "..", "..", "..", "..")
    )
    return os.path.join(repo_root, "bin", "attest-engine")


@pytest.fixture(scope="session")
def engine(request: pytest.FixtureRequest) -> Generator[AttestEngineFixture, None, None]:
    """Session-scoped fixture providing a running engine for integration tests."""
    path = request.config.getoption("--attest-engine", default=None) or _engine_binary_path()

    fixture = AttestEngineFixture(engine_path=path, log_level="warn")
    try:
        fixture.start()
    except FileNotFoundError:
        pytest.skip("attest-engine binary not found; build with 'make engine'")

    yield fixture
    fixture.stop()
