"""Attest pytest plugin — registered via entry point."""

from __future__ import annotations

import asyncio
import logging
from collections.abc import Generator

import pytest

from attest.client import AttestClient
from attest.engine_manager import EngineManager
from attest.expect import ExpectChain
from attest.result import AgentResult

logger = logging.getLogger("attest.plugin")


def pytest_configure(config: pytest.Config) -> None:
    """Register attest markers and configuration."""
    config.addinivalue_line("markers", "attest: mark test as an Attest agent test")
    config.addinivalue_line("markers", "integration: mark test as integration test")


def pytest_addoption(parser: pytest.Parser) -> None:
    """Add attest-specific CLI options."""
    group = parser.getgroup("attest", "Attest agent testing")
    group.addoption(
        "--attest-engine",
        default=None,
        help="Path to attest-engine binary",
    )
    group.addoption(
        "--attest-log-level",
        default="warn",
        choices=["debug", "info", "warn", "error"],
        help="Engine log level",
    )


class AttestEngineFixture:
    """Manages engine lifecycle for pytest session."""

    def __init__(self, engine_path: str | None = None, log_level: str = "warn") -> None:
        self._engine_path = engine_path
        self._log_level = log_level
        self._manager: EngineManager | None = None
        self._client: AttestClient | None = None
        self._loop: asyncio.AbstractEventLoop | None = None

    def start(self) -> None:
        """Start the engine process."""
        self._loop = asyncio.new_event_loop()
        self._manager = EngineManager(
            engine_path=self._engine_path,
            log_level=self._log_level,
        )
        self._loop.run_until_complete(self._manager.start())
        self._client = AttestClient(self._manager)

    def stop(self) -> None:
        """Stop the engine process."""
        if self._manager and self._loop:
            self._loop.run_until_complete(self._manager.stop())
            self._loop.close()

    @property
    def client(self) -> AttestClient:
        assert self._client is not None, "Engine not started"
        return self._client

    def evaluate(self, chain: ExpectChain) -> AgentResult:
        """Evaluate collected assertions synchronously."""
        assert self._loop is not None
        assert self._client is not None

        result = self._loop.run_until_complete(
            self._client.evaluate_batch(chain.trace, chain.assertions)
        )

        return AgentResult(
            trace=chain.trace,
            assertion_results=result.results,
            total_cost=result.total_cost,
            total_duration_ms=result.total_duration_ms,
        )


@pytest.fixture(scope="session")
def attest_engine(request: pytest.FixtureRequest) -> Generator[AttestEngineFixture, None, None]:
    """Session-scoped fixture providing access to the Attest engine."""
    engine_path: str | None = request.config.getoption("--attest-engine", default=None)
    log_level: str = request.config.getoption("--attest-log-level", default="warn")

    fixture = AttestEngineFixture(engine_path=engine_path, log_level=log_level)
    try:
        fixture.start()
    except FileNotFoundError:
        pytest.skip("attest-engine binary not found; build with 'make engine'")

    yield fixture
    fixture.stop()


@pytest.fixture
def attest(attest_engine: AttestEngineFixture) -> AttestEngineFixture:
    """Function-scoped fixture providing the Attest engine for convenience."""
    return attest_engine
