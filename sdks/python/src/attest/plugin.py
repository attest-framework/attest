"""Attest pytest plugin — registered via entry point."""

from __future__ import annotations

import asyncio
import logging
import threading
from collections.abc import Generator
from typing import Any

import pytest

from attest.cli.diagnostics import render_diagnostics
from attest.client import AttestClient
from attest.engine_manager import EngineManager
from attest.expect import ExpectChain
from attest.result import AgentResult

logger = logging.getLogger("attest.plugin")

# Session-level cost accumulator — updated by evaluate() calls
_session_cost: float = 0.0
_session_soft_failures: int = 0
# Maps nodeid → AgentResult for tests that produced one. Used by the
# terminal summary hook to render diagnostic blocks for failed runs.
_session_results: dict[str, AgentResult] = {}
# Thread-local-ish slot for the most-recent AgentResult produced inside
# a test. pytest serialises test execution, so a single slot is safe;
# the makereport hook below pops it into _session_results keyed on
# nodeid as soon as the call phase completes.
_last_result: AgentResult | None = None
_last_result_lock = threading.Lock()


def _set_last_result(agent_result: AgentResult) -> None:
    """Stash the most-recent AgentResult so the makereport hook can index
    it by nodeid when the call phase ends."""
    global _last_result
    with _last_result_lock:
        _last_result = agent_result


def _take_last_result() -> AgentResult | None:
    """Atomically pop and return the most-recent stashed AgentResult."""
    global _last_result
    with _last_result_lock:
        result, _last_result = _last_result, None
        return result


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
    group.addoption(
        "--attest-diagnostics",
        action="store_true",
        default=False,
        help="Render the diagnostic block for every Attest failure in the terminal summary",
    )


def pytest_collection_modifyitems(config: pytest.Config, items: list[pytest.Item]) -> None:
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


def pytest_terminal_summary(terminalreporter: Any, exitstatus: int, config: pytest.Config) -> None:
    """Print cost report and per-test diagnostic blocks when requested.

    --attest-cost-report enables the session-cost summary. The
    diagnostic-block renderer fires for any failed run; passing
    assertions are enumerated alongside failures when
    --attest-diagnostics is set so reviewers can see what was
    evaluated even when the suite is green.
    """
    diagnostics_opt = config.getoption("--attest-diagnostics", default=False)
    cost_report_opt = config.getoption("--attest-cost-report", default=False)

    if _session_results:
        if diagnostics_opt:
            entries = list(_session_results.items())
        else:
            entries = [(nid, r) for nid, r in _session_results.items() if not r.passed]
        if entries:
            terminalreporter.write_sep("=", "Attest Diagnostics")
            for nodeid, agent_result in entries:
                marker = "FAILED" if not agent_result.passed else "PASSED"
                terminalreporter.write_line(f"{marker} {nodeid}")
                terminalreporter.write_line(
                    render_diagnostics(
                        agent_result,
                        test_file=nodeid,
                        include_passing=diagnostics_opt,
                    )
                )

    if cost_report_opt:
        terminalreporter.write_sep("=", "Attest Cost Report")
        terminalreporter.write_line(f"Total LLM cost this session: ${_session_cost:.6f} USD")
        terminalreporter.write_line(f"Soft failures recorded:       {_session_soft_failures}")


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_makereport(
    item: pytest.Item,
    call: pytest.CallInfo[Any],
) -> Generator[None, None, None]:
    """Pop the most-recent AgentResult into the per-nodeid registry.

    Pytest serialises tests within a worker, so the latest stashed
    AgentResult always corresponds to the call phase that just ran.
    """
    outcome = yield
    _ = outcome
    if call.when != "call":
        return
    agent_result = _take_last_result()
    if agent_result is not None:
        _session_results[item.nodeid] = agent_result


class AttestEngineFixture:
    """Manages engine lifecycle for pytest session."""

    def __init__(self, engine_path: str | None = None, log_level: str = "warn") -> None:
        self._engine_path = engine_path
        self._log_level = log_level
        self._manager: EngineManager | None = None
        self._client: AttestClient | None = None
        self._loop: asyncio.AbstractEventLoop | None = None
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        """Start the engine process and run its event loop in a background thread.

        The engine subprocess binds its asyncio streams to the loop active at
        creation time.  Running that loop in a dedicated daemon thread allows
        both synchronous ``evaluate()`` and asynchronous ``evaluate_async()``
        (which runs on a *different* loop, e.g. pytest-asyncio) to communicate
        with the engine via ``asyncio.run_coroutine_threadsafe()``.
        """
        self._loop = asyncio.new_event_loop()
        self._manager = EngineManager(
            engine_path=self._engine_path,
            log_level=self._log_level,
        )
        self._loop.run_until_complete(self._manager.start())
        self._client = AttestClient(self._manager)

        # Run the engine loop in a background thread so callers on any event
        # loop (or no loop) can submit coroutines via run_coroutine_threadsafe.
        self._thread = threading.Thread(
            target=self._loop.run_forever,
            daemon=True,
            name="attest-engine-loop",
        )
        self._thread.start()

    def stop(self) -> None:
        """Stop the engine process and its background event loop."""
        if self._manager and self._loop:
            future = asyncio.run_coroutine_threadsafe(
                self._manager.stop(),
                self._loop,
            )
            future.result(timeout=10)
            self._loop.call_soon_threadsafe(self._loop.stop)
            if self._thread is not None:
                self._thread.join(timeout=5)
            self._loop.close()

    @property
    def client(self) -> AttestClient:
        assert self._client is not None, "Engine not started"
        return self._client

    def _run_on_engine_loop(self, coro: Any) -> Any:
        """Submit a coroutine to the engine's background loop and block for the result."""
        assert self._loop is not None
        future = asyncio.run_coroutine_threadsafe(coro, self._loop)
        return future.result()

    def _process_result(
        self,
        chain: ExpectChain,
        result: Any,
        budget: float | None,
    ) -> AgentResult:
        """Shared post-evaluation logic for both sync and async paths."""
        global _session_cost, _session_soft_failures

        _session_cost += result.total_cost

        agent_result = AgentResult(
            trace=chain.trace,
            assertion_results=result.results,
            total_cost=result.total_cost,
            total_duration_ms=result.total_duration_ms,
        )

        from attest._proto.types import STATUS_SOFT_FAIL

        _session_soft_failures += sum(1 for r in result.results if r.status == STATUS_SOFT_FAIL)
        _set_last_result(agent_result)

        if budget is not None and _session_cost > budget:
            pytest.fail(
                f"Attest budget exceeded: cost ${_session_cost:.6f} > budget ${budget:.6f}",
                pytrace=False,
            )

        return agent_result

    def evaluate(
        self,
        chain: ExpectChain,
        *,
        budget: float | None = None,
    ) -> AgentResult:
        """Evaluate collected assertions synchronously.

        Submits the evaluation to the engine's background loop and blocks
        the calling thread until the result is ready.
        """
        assert self._client is not None
        result = self._run_on_engine_loop(
            self._client.evaluate_batch(chain.trace, chain.assertions)
        )
        return self._process_result(chain, result, budget)

    async def evaluate_async(
        self,
        chain: ExpectChain,
        *,
        budget: float | None = None,
    ) -> AgentResult:
        """Evaluate collected assertions asynchronously.

        Submits the evaluation to the engine's background loop and awaits
        the result without blocking the caller's event loop.  Safe to call
        from pytest-asyncio or any other running loop.
        """
        assert self._loop is not None
        assert self._client is not None
        future = asyncio.run_coroutine_threadsafe(
            self._client.evaluate_batch(chain.trace, chain.assertions),
            self._loop,
        )
        result = await asyncio.wrap_future(future)
        return self._process_result(chain, result, budget)


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
