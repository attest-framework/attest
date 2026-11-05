"""Integration test fixtures requiring a built engine binary."""

from __future__ import annotations

import os
from collections.abc import Generator

import pytest

from attest.plugin import AttestEngineFixture
from attest.trials import TestRun, begin_run, end_run


def _engine_binary_path() -> str:
    """Resolve path to the engine binary built by `make engine`."""
    repo_root = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", "..", ".."))
    return os.path.join(repo_root, "bin", "attest-engine")


@pytest.fixture(scope="session")
def engine(request: pytest.FixtureRequest) -> Generator[AttestEngineFixture, None, None]:
    """Session-scoped fixture providing a running engine for integration tests."""
    path = request.config.getoption("--attest-engine", default=None) or _engine_binary_path()

    if not os.path.exists(path):
        pytest.skip("attest-engine binary not found; build with 'make engine'")

    # Engine-spawning tests must not pick up a developer's judge configuration.
    with pytest.MonkeyPatch.context() as mp:
        for key in ("OPENAI_API_KEY", "ATTEST_JUDGE_API_KEY", "ATTEST_JUDGE_BASE_URL"):
            mp.delenv(key, raising=False)
        fixture = AttestEngineFixture(engine_path=path, log_level="warn")
        fixture.start()
        yield fixture
        fixture.stop()


@pytest.fixture(autouse=True)
def _assertion_id_scope(request: pytest.FixtureRequest) -> Generator[None, None, None]:
    """Integration tests use the ``engine`` fixture, which the plugin does not track as a
    test run; scope their assertion ids to the node id the way the plugin does."""
    begin_run(TestRun(test_id=request.node.nodeid))
    yield
    end_run()
