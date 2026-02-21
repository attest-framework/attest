"""Attest pytest plugin — registered via entry point."""

from __future__ import annotations

import asyncio
import logging
from collections.abc import Generator
from typing import Any

import pytest

from attest.client import AttestClient
from attest.engine_manager import EngineManager
from attest.expect import ExpectChain
from attest.result import AgentResult

logger = logging.getLogger("attest.plugin")

# Session-level cost accumulator — updated by evaluate() calls
_session_cost: float = 0.0
_session_soft_failures: int = 0


def pytest_configure(config: pytest.Config) -> None:
    """Register attest markers and configuration."""
    config.addinivalue_line("markers", "attest: mark test as an Attest agent test")
    config.addinivalue_line("markers", "integration: mark test as integration test")
    config.addinivalue_line(
        "markers",
        "attest_tier(level): mark test with an Attest tier level (1, 2, or 3)",
    )


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
    group.addoption(
        "--attest-tier",
        default=None,
        type=int,
        help="Only run tests with _attest_tier <= this value (e.g. --attest-tier=1)",
    )
    group.addoption(
        "--attest-budget",
        default=None,
        type=float,
        help="Abort test session if cumulative LLM cost exceeds this USD amount",
    )
    group.addoption(
        "--attest-cost-report",
        action="store_true",
        default=False,
        help="Print a cost report at the end of the test session",
    )


def pytest_collection_modifyitems(
    config: pytest.Config, items: list[pytest.Item]
) -> None:
    """Filter test items by tier if --attest-tier is specified."""
    tier_filter: int | None = config.getoption("--attest-tier", default=None)
    if tier_filter is None:
        return

    selected = []
    deselected = []
    for item in items:
        fn = getattr(item, "function", None)
        item_tier: int | None = getattr(fn, "_attest_tier", None) if fn is not None else None
        if item_tier is None or item_tier <= tier_filter:
            selected.append(item)
        else:
            deselected.append(item)

    config.hook.pytest_deselected(items=deselected)
    items[:] = selected


def pytest_terminal_summary(
    terminalreporter: Any, exitstatus: int, config: pytest.Config
) -> None:
    """Print cost report if --attest-cost-report is set."""
    if not config.getoption("--attest-cost-report", default=False):
        return

    terminalreporter.write_sep("=", "Attest Cost Report")
    terminalreporter.write_line(f"Total LLM cost this session: ${_session_cost:.6f} USD")
    terminalreporter.write_line(f"Soft failures recorded:       {_session_soft_failures}")


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

    def evaluate(
        self,
        chain: ExpectChain,
        *,
        budget: float | None = None,
    ) -> AgentResult:
        """Evaluate collected assertions synchronously.

        Args:
            chain: The ExpectChain with assertions to evaluate.
            budget: Optional per-call cost ceiling in USD. If the session
                    cumulative cost exceeds this, the session is aborted.
        """
        global _session_cost, _session_soft_failures

        assert self._loop is not None
        assert self._client is not None

        result = self._loop.run_until_complete(
            self._client.evaluate_batch(chain.trace, chain.assertions)
        )

        _session_cost += result.total_cost

        agent_result = AgentResult(
            trace=chain.trace,
            assertion_results=result.results,
            total_cost=result.total_cost,
            total_duration_ms=result.total_duration_ms,
        )

        # Count soft failures
        from attest._proto.types import STATUS_SOFT_FAIL
        _session_soft_failures += sum(
            1 for r in result.results if r.status == STATUS_SOFT_FAIL
        )

        # Enforce budget
        if budget is not None and _session_cost > budget:
            pytest.fail(
                f"Attest budget exceeded: cost ${_session_cost:.6f}"
                f" > budget ${budget:.6f}",
                pytrace=False,
            )

        return agent_result


@pytest.fixture(scope="session")
def attest_engine(request: pytest.FixtureRequest) -> Generator[AttestEngineFixture, None, None]:
    """Session-scoped fixture providing access to the Attest engine."""
    engine_path: str | None = request.config.getoption("--attest-engine", default=None)
    log_level: str = request.config.getoption("--attest-log-level", default="warn")

    fixture = AttestEngineFixture(engine_path=engine_path, log_level=log_level)
    try:
        fixture.start()
    except FileNotFoundError as exc:
        pytest.fail(
            f"attest-engine binary not found: {exc}\n"
            "Set ATTEST_ENGINE_PATH or allow auto-download.",
            pytrace=False,
        )

    yield fixture
    fixture.stop()


@pytest.fixture
def attest(attest_engine: AttestEngineFixture) -> AttestEngineFixture:
    """Function-scoped fixture providing the Attest engine for convenience."""
    return attest_engine
