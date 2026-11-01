"""Attest pytest plugin — registered via entry point."""

from __future__ import annotations

import asyncio
import logging
import os
import threading
from collections.abc import Generator
from pathlib import Path
from typing import Any, TypeVar

import pytest

from attest.cli.diagnostics import render_diagnostics
from attest.client import AttestClient, require_simulation_capability
from attest.config import is_simulation_mode
from attest.engine_manager import EngineManager
from attest.exceptions import EngineVersionMismatchError
from attest.expect import ExpectChain
from attest.report_v2 import TestRecord, write_report
from attest.result import OUTCOME_FAIL, OUTCOME_SKIP, AgentResult, Verdict

logger = logging.getLogger("attest.plugin")

V = TypeVar("V", bound=Verdict)

# Session-level cost accumulator — updated by evaluate() calls
_session_cost: float = 0.0
_session_soft_failures: int = 0
# Maps nodeid → AgentResult for tests that produced one. Used by the
# terminal summary hook to render diagnostic blocks for failed runs.
_session_results: dict[str, AgentResult] = {}
# Maps nodeid → pytest's call-phase outcome ("passed" / "failed" /
# "skipped") for tests that produced an AgentResult.
_session_outcomes: dict[str, str] = {}
# Maps nodeid → soft failures across every evaluation in the test (all
# trials of a repeat or scenario loop, not only the last result).
_session_soft_counts: dict[str, int] = {}
# Maps nodeid → report-v2 test row (location, outcome, every AgentResult).
# Filled only when --attest-report is set.
_session_tests: dict[str, TestRecord] = {}
# Thread-local-ish slot for the AgentResults produced inside the running
# test. pytest serialises test execution, so a single slot is safe; the
# makereport hook below pops it, keyed on nodeid, as soon as the call
# phase completes.
_test_results: list[AgentResult] = []
_test_results_lock = threading.Lock()


def _record_result(agent_result: AgentResult) -> None:
    """Stash an AgentResult so the makereport hook can index it by nodeid
    when the call phase ends."""
    with _test_results_lock:
        _test_results.append(agent_result)


def _take_test_results() -> list[AgentResult]:
    """Atomically pop every AgentResult stashed during the running test."""
    with _test_results_lock:
        results = list(_test_results)
        _test_results.clear()
        return results


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
        "--attest-strict",
        action="store_true",
        default=False,
        help="Fail (instead of skip) tests with assertions that were not evaluated "
        "(also enabled by ATTEST_STRICT=1)",
    )
    group.addoption(
        "--attest-report",
        default=None,
        metavar="PATH",
        help="Write a report-v2 JSON file (every test with its outcome, file and line, "
        "and every assertion result) for attest policy evaluate and pr-annotate",
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

    soft_lines = soft_failure_lines(_session_soft_counts, _session_outcomes)
    if soft_lines:
        terminalreporter.write_sep("=", "Attest soft failures")
        terminalreporter.write_line(
            f"{len(soft_lines)} passed or skipped test(s) recorded soft failures:", yellow=True
        )
        for line in soft_lines:
            terminalreporter.write_line(line)

    if diagnostics_opt and _session_results:
        terminalreporter.write_sep("=", "Attest Diagnostics")
        for nodeid, agent_result in _session_results.items():
            marker = {OUTCOME_FAIL: "FAILED", OUTCOME_SKIP: "SKIPPED"}.get(
                agent_result.outcome(), "PASSED"
            )
            terminalreporter.write_line(f"{marker} {nodeid}")
            terminalreporter.write_line(
                render_diagnostics(agent_result, test_file=nodeid, include_passing=True)
            )

    if cost_report_opt:
        terminalreporter.write_sep("=", "Attest Cost Report")
        terminalreporter.write_line(f"Total LLM cost this session: ${_session_cost:.6f} USD")
        terminalreporter.write_line(f"Soft failures recorded:       {_session_soft_failures}")


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_makereport(
    item: pytest.Item,
    call: pytest.CallInfo[Any],
) -> Generator[None, Any, None]:
    """Pop the test's AgentResults into the per-nodeid registries (last
    result for diagnostics, soft failures summed over all of them) and
    attribute ``not evaluated`` skips to the test's own location.

    Pytest serialises tests within a worker, so the stashed AgentResults
    always belong to the call phase that just ran.
    """
    outcome = yield
    report: pytest.TestReport = outcome.get_result()
    if item.config.getoption("--attest-report", default=None):
        _record_test_phase(item, report)
    if call.when != "call":
        return
    if report.skipped and isinstance(report.longrepr, tuple):
        # Attribute "not evaluated" skips to the test, not to this plugin.
        path, _, reason = report.longrepr
        if os.path.abspath(path) == os.path.abspath(__file__) and "not evaluated:" in reason:
            _, lineno, _ = item.location
            report.longrepr = (str(item.path), (lineno or 0) + 1, reason)
    results = _take_test_results()
    record = _session_tests.get(item.nodeid)
    if record is not None:
        record.agent_results.extend(results)
    if results:
        _session_results[item.nodeid] = results[-1]
        _session_outcomes[item.nodeid] = report.outcome
        _session_soft_counts[item.nodeid] = sum(len(r.soft_failures) for r in results)


def _record_test_phase(item: pytest.Item, report: pytest.TestReport) -> None:
    """Fold one setup/call/teardown report into the test's report-v2 row.

    The outcome is the call phase's, overridden by a failed or skipped setup and by a
    failed teardown. ``line`` is the 1-based line of the test definition.
    """
    record = _session_tests.get(item.nodeid)
    if record is None:
        path, lineno, _ = item.location
        record = TestRecord(
            test_id=item.nodeid,
            name=item.name,
            file=Path(path).as_posix(),
            line=(lineno or 0) + 1,
        )
        _session_tests[item.nodeid] = record
    record.duration_ms += int(report.duration * 1000)
    if report.when == "call" or report.outcome != "passed":
        if not (report.when == "teardown" and report.outcome == "skipped"):
            record.outcome = report.outcome


def pytest_sessionstart(session: pytest.Session) -> None:
    """Start each session with an empty report (pytester runs share this module)."""
    _session_tests.clear()


def pytest_sessionfinish(session: pytest.Session, exitstatus: int) -> None:
    """Write the --attest-report file once every test has run."""
    target: str | None = session.config.getoption("--attest-report", default=None)
    if not target:
        return
    path = Path(target)
    if not path.is_absolute():
        path = Path(session.config.invocation_params.dir) / path
    write_report(path, list(_session_tests.values()), simulated=is_simulation_mode())


def soft_failure_lines(soft_counts: dict[str, int], outcomes: dict[str, str]) -> list[str]:
    """One line per test with soft failures that pytest did not report as
    failed (failed tests already show their diagnostics in pytest's report)."""
    return [
        f"  {nid}: {n} soft ({outcomes.get(nid, 'passed')})"
        for nid, n in soft_counts.items()
        if n and outcomes.get(nid) != "failed"
    ]


class AttestEngineFixture:
    """Manages engine lifecycle for pytest session."""

    def __init__(
        self,
        engine_path: str | None = None,
        log_level: str = "warn",
        *,
        strict: bool = False,
    ) -> None:
        self._engine_path = engine_path
        self._log_level = log_level
        self._strict = strict
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
        simulation = is_simulation_mode()
        if simulation:
            # The engine decides simulation at startup from its environment;
            # this also covers attest.config(simulation=True). Simulation is
            # process-wide for the session: the variable stays set.
            os.environ["ATTEST_SIMULATION"] = "1"
        self._manager = EngineManager(
            engine_path=self._engine_path,
            log_level=self._log_level,
        )
        init = self._loop.run_until_complete(self._manager.start())
        if simulation:
            try:
                require_simulation_capability(init)
            except RuntimeError:
                self._loop.run_until_complete(self._manager.stop())
                raise
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
        _record_result(agent_result)

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
        """Evaluate collected assertions and enforce the test outcome.

        After the whole batch: any ``hard_fail`` fails the test with
        rendered diagnostics; otherwise any ``not_evaluated`` assertion
        skips it with the reasons (fails under ``--attest-strict``);
        otherwise the test continues, with soft failures summarised at the
        end of the session. Use ``inspect()`` to get results without
        enforcement.
        """
        return self.enforce(self.inspect(chain, budget=budget))

    async def evaluate_async(
        self,
        chain: ExpectChain,
        *,
        budget: float | None = None,
    ) -> AgentResult:
        """Async ``evaluate()``: evaluate and enforce the test outcome."""
        return self.enforce(await self.inspect_async(chain, budget=budget))

    def inspect(
        self,
        chain: ExpectChain,
        *,
        budget: float | None = None,
    ) -> AgentResult:
        """Evaluate collected assertions without enforcing the outcome.

        Submits the evaluation to the engine's background loop and blocks
        the calling thread until the result is ready. Use in trial loops
        (``repeat()``) and enforce only the aggregate verdict. ``budget`` is
        a separate session rule and still fails the test when exceeded.
        """
        assert self._client is not None
        result = self._run_on_engine_loop(
            self._client.evaluate_batch(chain.trace, chain.assertions)
        )
        return self._process_result(chain, result, budget)

    async def inspect_async(
        self,
        chain: ExpectChain,
        *,
        budget: float | None = None,
    ) -> AgentResult:
        """Async ``inspect()``: evaluate without enforcing the outcome.

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

    def enforce(self, verdict: V) -> V:
        """Fail or skip the running test according to ``verdict``'s outcome.

        Accepts an ``AgentResult`` from ``inspect()`` or the aggregate of a
        trial loop (``RepeatResult``, ``ScenarioResult``). Fails with
        diagnostics on any failure; otherwise skips with the not-evaluated
        reasons (fails under ``--attest-strict``); otherwise returns it.
        """
        outcome = verdict.outcome(strict=self._strict)
        if outcome == OUTCOME_FAIL:
            header = ""
            if verdict.outcome() != OUTCOME_FAIL:
                header = f"--attest-strict: {verdict.skip_reason()}\n"
            diagnostics = verdict.render_diagnostics().lstrip("\n")
            pytest.fail(header + diagnostics, pytrace=False)
        if outcome == OUTCOME_SKIP:
            pytest.skip(verdict.skip_reason())
        return verdict


@pytest.fixture(scope="session")
def attest_engine(request: pytest.FixtureRequest) -> Generator[AttestEngineFixture, None, None]:
    """Session-scoped fixture providing access to the Attest engine."""
    engine_path: str | None = request.config.getoption("--attest-engine", default=None)
    log_level: str = request.config.getoption("--attest-log-level", default="warn")

    strict: bool = request.config.getoption("--attest-strict", default=False) or (
        os.environ.get("ATTEST_STRICT", "").strip().lower() in ("1", "true", "yes")
    )

    fixture = AttestEngineFixture(engine_path=engine_path, log_level=log_level, strict=strict)
    try:
        fixture.start()
    except FileNotFoundError as exc:
        pytest.fail(
            f"attest-engine binary not found: {exc}\n"
            "Set ATTEST_ENGINE_PATH or allow auto-download.",
            pytrace=False,
        )
    except EngineVersionMismatchError as exc:
        pytest.fail(f"attest-engine version mismatch: {exc}", pytrace=False)

    yield fixture
    fixture.stop()


@pytest.fixture
def attest(attest_engine: AttestEngineFixture) -> AttestEngineFixture:
    """Function-scoped fixture providing the Attest engine for convenience."""
    return attest_engine
