"""Attest pytest plugin — registered via entry point."""

from __future__ import annotations

import asyncio
import inspect
import logging
import os
import threading
from collections.abc import Generator
from dataclasses import dataclass
from pathlib import Path
from typing import Any, TypeVar

import pytest

from attest._proto.types import BatchBudget, Trace, Usage
from attest.cli.diagnostics import render_diagnostics
from attest.client import AttestClient, require_simulation_capability
from attest.config import is_simulation_mode
from attest.engine_manager import EngineManager
from attest.exceptions import EngineVersionMismatchError
from attest.expect import ExpectChain
from attest.report_v2 import TEST_OUTCOMES, TestRecord, write_report
from attest.result import OUTCOME_FAIL, OUTCOME_SKIP, AgentResult, Verdict
from attest.trials import (
    BUDGET_MARKER,
    TestBudget,
    TestRun,
    begin_run,
    current_run,
    end_run,
    run_trials,
)

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
# Attest model usage summed over every evaluation of the session.
_session_usage: Usage = Usage()
# Maps nodeid → trials run, for tests run as fixed-N trials (--attest-trials > 1).
_session_trials_run: dict[str, int] = {}
# Maps nodeid → terminal summary lines of a trials test.
_session_trial_lines: dict[str, list[str]] = {}

_ATTEST_FIXTURES = frozenset({"attest", "attest_engine"})


def _record_result(agent_result: AgentResult) -> None:
    """Stash an AgentResult so the makereport hook can index it by nodeid
    when the call phase ends."""
    with _test_results_lock:
        _test_results.append(agent_result)


def _replace_test_results(removed: list[AgentResult], replacement: AgentResult) -> None:
    """Swap the stashed evaluations of a trial that raised for its hard_fail
    replacement, so the report and session records hold no stale rows."""
    with _test_results_lock:
        _test_results[:] = [r for r in _test_results if all(r is not x for x in removed)]
        _test_results.append(replacement)


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
    config.addinivalue_line(
        "markers",
        f"{BUDGET_MARKER}(max_judge_calls=None, max_cost_usd=None): per-test limit on "
        "attest judge calls and on attest cost plus agent spend (USD), across all trials",
    )
    trials = config.getoption("--attest-trials", default=1)
    if trials < 1:
        raise pytest.UsageError(f"--attest-trials must be an integer >= 1, got {trials}")


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
        "--attest-trials",
        default=1,
        type=int,
        metavar="N",
        help="Run every test using the attest fixture N times as fixed-N trials (sync tests "
        "only); each report row carries its trial. The statistical verdict against a pinned "
        "baseline comes from `attest policy evaluate --baseline <tag>` with a regression rule",
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

    if _session_trial_lines:
        terminalreporter.write_sep("=", "Attest trials")
        for lines in _session_trial_lines.values():
            for line in lines:
                terminalreporter.write_line(line)

    usage = usage_line(_session_usage)
    if usage is not None:
        terminalreporter.write_line(usage)


def usage_line(usage: Usage) -> str | None:
    """The session usage summary line, or ``None`` when no model call was made
    or refused."""
    if usage.model_calls > 0 or usage.budget_refused > 0:
        return f"attest usage: {usage.format()}"
    return None


def _uses_attest(item: pytest.Item) -> bool:
    return not _ATTEST_FIXTURES.isdisjoint(getattr(item, "fixturenames", ()))


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_call(item: pytest.Item) -> Generator[None, Any, None]:
    """Track the running test (deterministic assertion IDs, per-test budget)
    for tests using the attest fixture; validate the ``attest_budget`` marker."""
    marker = item.get_closest_marker(BUDGET_MARKER)
    budget: TestBudget | None = None
    if marker is not None:
        try:
            budget = TestBudget.from_marker(marker.args, marker.kwargs)
        except ValueError as exc:
            pytest.fail(f"{item.nodeid}: {exc}", pytrace=False)
    run = TestRun(test_id=item.nodeid, budget=budget) if _uses_attest(item) else None
    if run is not None:
        begin_run(run)
    try:
        yield
    finally:
        if run is not None:
            end_run()


def _is_coroutine_test(item: pytest.Function) -> bool:
    """True for ``async def`` tests; pytest-asyncio replaces ``item.obj`` with a
    sync wrapper, so the definition on the module or class is checked too."""
    definition = getattr(getattr(item.parent, "obj", None), item.originalname, None)
    return inspect.iscoroutinefunction(item.obj) or inspect.iscoroutinefunction(definition)


@pytest.hookimpl(tryfirst=True)
def pytest_pyfunc_call(pyfuncitem: pytest.Function) -> bool | None:
    """Run a test using the attest fixture as fixed-N trials when
    ``--attest-trials`` > 1 and enforce only the aggregate outcome.

    Each trial's rows carry ``trial``; the report's test row carries
    ``trials``. The statistical verdict (pass / regressed / inconclusive)
    comes from ``attest policy evaluate --baseline <tag>`` with a
    ``regression`` rule.
    """
    n: int = pyfuncitem.config.getoption("--attest-trials", default=1)
    run = current_run()
    if n == 1 or run is None:
        return None
    if _is_coroutine_test(pyfuncitem):
        pytest.fail(
            f"--attest-trials supports synchronous tests; {pyfuncitem.nodeid} is async. "
            "Loop trials in the test with attest.simulation.repeat and "
            "attest.inspect_async(), then enforce the aggregate.",
            pytrace=False,
        )
    fixture = pyfuncitem.funcargs["attest_engine"]
    if not isinstance(fixture, AttestEngineFixture):
        raise TypeError(f"attest_engine fixture is {type(fixture).__name__}")
    testfunction = pyfuncitem.obj
    testargs = {arg: pyfuncitem.funcargs[arg] for arg in pyfuncitem._fixtureinfo.argnames}
    nodeid = pyfuncitem.nodeid

    def trials_run(count: int) -> None:
        _session_trials_run[nodeid] = count

    result = run_trials(run, lambda: testfunction(**testargs), n, _replace_test_results, trials_run)
    _session_trial_lines[pyfuncitem.nodeid] = result.summary_lines(pyfuncitem.nodeid)
    fixture.enforce(result)
    return True


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_makereport(
    item: pytest.Item,
    call: pytest.CallInfo[Any],
) -> Generator[None, Any, None]:
    """Pop the test's AgentResults into the per-nodeid registries (last
    result for diagnostics, soft failures summed over all of them) and
    attribute ``not evaluated`` skips to the test's own location.

    Pytest serialises tests within a worker, so the stashed AgentResults
    always belong to the phase (setup, call or teardown) that just ran.
    """
    outcome = yield
    report: pytest.TestReport = outcome.get_result()
    record = (
        _record_test_phase(item, report)
        if item.config.getoption("--attest-report", default=None)
        else None
    )
    if call.when == "call" and report.skipped and isinstance(report.longrepr, tuple):
        # Attribute "not evaluated" skips to the test, not to this plugin.
        path, _, reason = report.longrepr
        if os.path.abspath(path) == os.path.abspath(__file__) and "not evaluated:" in reason:
            _, lineno, _ = item.location
            report.longrepr = (str(item.path), (lineno or 0) + 1, reason)
    if call.when == "setup":
        # A new attempt (a rerun plugin retrying the test) starts its counts over.
        _session_soft_counts.pop(item.nodeid, None)
    # Evaluations in fixture setup or teardown belong to this test, like the call's.
    results = _take_test_results()
    if record is not None:
        record.agent_results.extend(results)
    if results:
        _session_results[item.nodeid] = results[-1]
        _session_soft_counts[item.nodeid] = _session_soft_counts.get(item.nodeid, 0) + sum(
            len(r.soft_failures) for r in results
        )
    if results or item.nodeid in _session_results:
        if call.when == "call" or report.outcome != "passed":
            _session_outcomes[item.nodeid] = report.outcome


def _record_test_phase(item: pytest.Item, report: pytest.TestReport) -> TestRecord:
    """Fold one setup/call/teardown report into the test's report-v2 row and return it.

    The outcome is the call phase's, overridden by a failed or skipped setup and by a
    failed teardown. A new setup phase for a test that already ran (a rerun plugin
    retrying it) starts the row over, so only the last attempt's outcome and results
    are reported. ``line`` is the 1-based line of the test definition.
    """
    record = _session_tests.get(item.nodeid)
    if record is not None and report.when == "setup":
        record.outcome = "passed"
        record.duration_ms = 0
        record.agent_results.clear()
        record.trials = None
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
    if report.outcome not in TEST_OUTCOMES:
        raise ValueError(f"{item.nodeid}: unknown pytest outcome {report.outcome!r}")
    if report.when == "call" or report.outcome != "passed":
        if not (report.when == "teardown" and report.outcome == "skipped"):
            record.outcome = report.outcome
    if report.when == "call":
        record.trials = _session_trials_run.pop(item.nodeid, None)
    return record


@dataclass
class _SessionState:
    """Module state of an enclosing session, saved while a nested in-process
    session (pytester) runs."""

    tests: dict[str, TestRecord]
    trials_run: dict[str, int]
    trial_lines: dict[str, list[str]]
    usage: Usage
    test_results: list[AgentResult]
    run: TestRun | None


_saved_sessions: list[_SessionState] = []


def pytest_sessionstart(session: pytest.Session) -> None:
    """Start each session with an empty report and usage; the enclosing
    session's state (pytester runs share this module) is restored when this
    session finishes."""
    global _session_usage
    with _test_results_lock:
        stashed = list(_test_results)
        _test_results.clear()
    _saved_sessions.append(
        _SessionState(
            tests=dict(_session_tests),
            trials_run=dict(_session_trials_run),
            trial_lines=dict(_session_trial_lines),
            usage=_session_usage,
            test_results=stashed,
            run=current_run(),
        )
    )
    _session_tests.clear()
    _session_trials_run.clear()
    _session_trial_lines.clear()
    _session_usage = Usage()
    end_run()


@pytest.hookimpl(hookwrapper=True, tryfirst=True)
def pytest_sessionfinish(session: pytest.Session, exitstatus: int) -> Generator[None, Any, None]:
    """Write the --attest-report file, let the terminal summary run, then
    restore the enclosing session's state."""
    yield
    _write_session_report(session)
    global _session_usage
    saved = _saved_sessions.pop()
    _session_tests.clear()
    _session_tests.update(saved.tests)
    _session_trials_run.clear()
    _session_trials_run.update(saved.trials_run)
    _session_trial_lines.clear()
    _session_trial_lines.update(saved.trial_lines)
    _session_usage = saved.usage
    with _test_results_lock:
        _test_results[:] = saved.test_results
    if saved.run is not None:
        begin_run(saved.run)


def _write_session_report(session: pytest.Session) -> None:
    """Write the --attest-report file once every test has run."""
    target: str | None = session.config.getoption("--attest-report", default=None)
    if not target:
        return
    path = Path(target)
    if not path.is_absolute():
        path = Path(session.config.invocation_params.dir) / path
    write_report(
        path,
        list(_session_tests.values()),
        simulated=is_simulation_mode(),
        usage=_session_usage,
    )


def soft_failure_lines(soft_counts: dict[str, int], outcomes: dict[str, str]) -> list[str]:
    """One line per test with soft failures that pytest did not report as
    failed (failed tests already show their diagnostics in pytest's report)."""
    return [
        f"  {nid}: {n} soft ({outcomes.get(nid, 'passed')})"
        for nid, n in soft_counts.items()
        if n and outcomes.get(nid) != "failed"
    ]


def _batch_budget(trace: Trace) -> BatchBudget | None:
    """The running test's remaining ``attest_budget`` for the batch of ``trace``."""
    run = current_run()
    return run.batch_budget(trace) if run is not None else None


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
        global _session_cost, _session_soft_failures, _session_usage

        _session_cost += result.total_cost
        _session_usage = _session_usage + result.usage

        agent_result = AgentResult(
            trace=chain.trace,
            assertion_results=result.results,
            total_cost=result.total_cost,
            total_duration_ms=result.total_duration_ms,
            usage=result.usage,
        )
        run = current_run()
        if run is not None:
            run.record(agent_result)

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
            self._client.evaluate_batch(
                chain.trace, chain.assertions, budget=_batch_budget(chain.trace)
            )
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
            self._client.evaluate_batch(
                chain.trace, chain.assertions, budget=_batch_budget(chain.trace)
            ),
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

        A no-op that returns ``verdict`` during a ``--attest-trials`` trial:
        only the aggregate of all trials is enforced.
        """
        run = current_run()
        if run is not None and run.trial is not None:
            return verdict
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
