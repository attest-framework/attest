"""Attest pytest plugin — registered via entry point."""

from __future__ import annotations

import asyncio
import inspect
import logging
import os
import threading
from collections.abc import Callable, Coroutine, Generator
from dataclasses import dataclass
from pathlib import Path
from typing import Any, TypeVar

import pytest

from attest import egress
from attest._proto.types import (
    TYPE_PLUGIN,
    BatchBudget,
    BatchContext,
    EvaluateBatchResult,
    Trace,
    Usage,
)
from attest.cli.diagnostics import render_diagnostics
from attest.client import AttestClient, require_simulation_capability
from attest.config import is_simulation_mode
from attest.engine_manager import EngineManager
from attest.exceptions import EngineVersionMismatchError
from attest.expect import ExpectChain
from attest.plugins import (
    PLUGIN_TYPE_ASSERTION,
    PluginRegistry,
    execute_plugin_assertion,
    load_entrypoint_plugins,
)
from attest.replay import (
    ReplayError,
    ReplayNotEvaluatedError,
    ReplaySession,
    ReplaySummary,
    fixtures_dir_for,
    live_line,
    resolve_mode,
    start_replay,
)
from attest.report_v2 import (
    TEST_OUTCOMES,
    TestRecord,
    build_report,
    merge_reports,
    report_traces_enabled,
    write_document,
    write_report,
)
from attest.result import OUTCOME_FAIL, OUTCOME_SKIP, AgentResult, Verdict
from attest.trials import (
    BUDGET_MARKER,
    DEFAULT_MIN_PASS_RATE,
    MIN_PASS_RATE_MARKER,
    TestBudget,
    TestRun,
    begin_run,
    current_run,
    end_run,
    min_pass_rate_from_marker,
    run_trials,
    validate_min_pass_rate,
)
from attest.world import WorldError, WorldSession, start_world

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

# Replay summaries per test, filled when a test's replay sessions finish, and the
# sessions each running test has started (attest_replay fixture).
_session_replay: dict[str, list[ReplaySummary]] = {}
_item_replays: dict[str, list[ReplaySession]] = {}
# The worlds each running test has started (attest_world fixture).
_item_worlds: dict[str, list[WorldSession]] = {}
# Per running session: did it install the egress guard (and so release it)?
_egress_installed: list[Callable[[], None] | None] = []
# A strict replay failure recorded on the item: no xfail marker may turn it into XFAIL.
_REPLAY_FAILURE = pytest.StashKey[str]()


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
    config.addinivalue_line(
        "markers",
        f"{MIN_PASS_RATE_MARKER}(rate): lowest pass rate over evaluated trials "
        "(0..1) for this test under --attest-trials; overrides --attest-min-pass-rate",
    )
    trials = config.getoption("--attest-trials", default=1)
    if trials < 1:
        raise pytest.UsageError(f"--attest-trials must be an integer >= 1, got {trials}")
    try:
        validate_min_pass_rate(
            config.getoption("--attest-min-pass-rate", default=DEFAULT_MIN_PASS_RATE),
            "--attest-min-pass-rate",
        )
    except ValueError as exc:
        raise pytest.UsageError(str(exc)) from exc


def pytest_addoption(parser: pytest.Parser) -> None:
    """Add attest-specific CLI options."""
    group = parser.getgroup("attest", "Attest agent testing")
    group.addoption(
        "--attest-engine",
        default=None,
        help="Path to attest-engine binary",
    )
    group.addoption(
        "--attest-replay",
        default=None,
        choices=["strict", "auto", "off"],
        help="Replay mode for tests bound to a fixture with attest_replay(): strict fails a "
        "request with no recording and names what changed; auto goes live when a provider key "
        "is set, otherwise the test is not evaluated (replay_miss); off runs live. "
        "Default: ATTEST_REPLAY, else strict when CI is set, else auto",
    )
    group.addoption(
        "--attest-egress",
        default=None,
        choices=["block", "allow"],
        help="block makes connections to model-provider hosts (api.openai.com, "
        "api.anthropic.com, ...) from this process fail; loopback is always allowed. "
        "Default: ATTEST_EGRESS, else allow",
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
        "--attest-min-pass-rate",
        default=DEFAULT_MIN_PASS_RATE,
        type=float,
        metavar="RATE",
        help="Under --attest-trials > 1, fail a test whose pass rate over evaluated trials "
        "is below RATE (0..1, default 1.0: any failing trial fails the test); the "
        "attest_min_pass_rate marker overrides it per test",
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
    replay = replay_line(_session_replay)
    if replay is not None:
        terminalreporter.write_line(replay)


def replay_line(replay: dict[str, list[ReplaySummary]]) -> str | None:
    """The replay summary line (``N live model calls · $X.XX``), or ``None`` when no test
    ran against a fixture."""
    summaries = [s for sums in replay.values() for s in sums]
    if not summaries:
        return None
    replayed = sum(s.replayed for s in summaries)
    live = live_line(
        sum(s.live for s in summaries),
        sum(s.live_cost_usd for s in summaries),
        sum(s.unpriced_live_calls for s in summaries),
    )
    misses = sum(s.misses for s in summaries)
    suffix = f" · {misses} missed" if misses else ""
    return f"attest replay: {replayed} replayed · {live}{suffix}"


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
    rate_marker = item.get_closest_marker(MIN_PASS_RATE_MARKER)
    min_pass_rate: float | None = None
    if rate_marker is not None:
        try:
            min_pass_rate = min_pass_rate_from_marker(rate_marker.args, rate_marker.kwargs)
        except ValueError as exc:
            pytest.fail(f"{item.nodeid}: {exc}", pytrace=False)
    run = (
        TestRun(
            test_id=item.nodeid,
            budget=budget,
            min_pass_rate=min_pass_rate,
            file=Path(item.location[0]).as_posix(),
            line=None if item.location[1] is None else item.location[1] + 1,
        )
        if _uses_attest(item)
        else None
    )
    if run is not None:
        begin_run(run)
    try:
        outcome = yield
    finally:
        if run is not None:
            end_run()
    if run is not None and outcome.excinfo is None:
        _fail_unevaluated_chains(run, outcome)
    _finish_replays(item, outcome)


def _fail_unevaluated_chains(run: TestRun, outcome: Any) -> None:
    """Fail a test that otherwise passed when it built ``expect()`` chains it never evaluated:
    such a test checked nothing."""
    count = len(run.unevaluated_chains())
    if count == 0:
        return
    noun = "chain" if count == 1 else "chains"
    try:
        pytest.fail(f"{count} expect() {noun} built but never evaluated", pytrace=False)
    except pytest.fail.Exception as exc:
        outcome.force_exception(exc)


def _settle_replays(item: pytest.Item) -> BaseException | None:
    """Stop the test's replay sessions, record what they served and return the exception a
    miss turns into: a failure naming the miss in strict mode (whatever the agent raised),
    a skip with ``not evaluated: replay_miss`` in auto mode (a failure under
    ``--attest-strict``). A failure is also stashed on the item so that no ``xfail`` marker
    can turn it into an expected failure."""
    sessions = _item_replays.pop(item.nodeid, [])
    error: Exception | None = None
    for session in sessions:
        try:
            summary = session.finish()
        except ReplayError as exc:
            error = error or exc
            continue
        if summary is None:
            continue
        _session_replay.setdefault(item.nodeid, []).append(summary)
        error = error or session.error()
    if error is None:
        return None
    strict = bool(item.config.getoption("--attest-strict", default=False)) or (
        os.environ.get("ATTEST_STRICT", "").strip().lower() in ("1", "true", "yes")
    )
    try:
        if isinstance(error, ReplayNotEvaluatedError) and not strict:
            pytest.skip(f"not evaluated: replay_miss: {error}")
        item.stash[_REPLAY_FAILURE] = str(error)
        pytest.fail(str(error), pytrace=False)
    except (pytest.skip.Exception, pytest.fail.Exception) as exc:
        return exc
    return None


def _finish_replays(item: pytest.Item, outcome: Any) -> None:
    """Apply :func:`_settle_replays` to the call's outcome."""
    exc = _settle_replays(item)
    if exc is not None:
        outcome.force_exception(exc)


def _egress_mode(config: pytest.Config) -> str:
    mode: str | None = config.getoption("--attest-egress", default=None)
    mode = mode or os.environ.get("ATTEST_EGRESS", "").strip().lower() or "allow"
    if mode not in ("block", "allow"):
        raise pytest.UsageError(f"ATTEST_EGRESS must be block or allow, got {mode!r}")
    return mode


@pytest.fixture
def attest_replay(
    request: pytest.FixtureRequest,
) -> Generator[Callable[..., ReplaySession], None, None]:
    """Bind the test to a fixture: ``attest_replay("inc-1042")`` starts the replay proxy and
    points ``OPENAI_BASE_URL`` / ``ANTHROPIC_BASE_URL`` at it, so the agent's real code runs
    with its model responses served from the recording.

    Construct model clients after the call. Use it as ``with attest_replay("name"):`` to end
    the replay at the end of the block, or call it and let the test end it. The mode is
    ``--attest-replay`` (strict, auto or off). A miss fails the test in strict mode, with the
    message naming the changed component and the ``attest capture --refresh`` command; in
    auto mode without a provider key the test is ``not_evaluated`` (``replay_miss``).

    Keyword arguments: ``mode``, ``fixtures_dir`` (default ``tests/attest/fixtures`` under
    the pytest root) and ``engine_path`` (default ``--attest-engine``).
    """
    config = request.config
    nodeid = request.node.nodeid

    def start(
        name: str,
        *,
        mode: str | None = None,
        fixtures_dir: str | os.PathLike[str] | None = None,
        engine_path: str | None = None,
    ) -> ReplaySession:
        chosen = resolve_mode(mode or config.getoption("--attest-replay", default=None))
        session = start_replay(
            name,
            mode=chosen,
            fixtures_dir=fixtures_dir_for(config.rootpath, fixtures_dir),
            engine_path=engine_path or config.getoption("--attest-engine", default=None),
            no_live=_egress_mode(config) == "block",
        )
        _item_replays.setdefault(nodeid, []).append(session)
        return session

    yield start
    for session in _item_replays.pop(nodeid, []):
        session.finish()


def _stop_worlds(nodeid: str) -> None:
    """Stop the worlds the test started; every one is stopped before the first error raises."""
    error: WorldError | None = None
    for world in _item_worlds.pop(nodeid, []):
        try:
            world.stop()
        except WorldError as exc:
            error = error or exc
    if error is not None:
        raise error


@pytest.fixture
def attest_world(
    request: pytest.FixtureRequest,
) -> Generator[Callable[..., WorldSession], None, None]:
    """Host a world for the test: ``world = attest_world("tests/payments.world.yaml")``
    starts it and sets its base URL variable (``PAYMENTS_BASE_URL``) until the test ends.

    HTTP clients read the variable when constructed, so construct them after the call. Read
    the effects with ``world.state()`` and assert on them with
    ``expect(trace).world(world.state()).count(...)``. Composes with ``attest_replay`` in the
    same test. Keyword argument: ``engine_path`` (default ``--attest-engine``).
    """
    config = request.config
    nodeid = request.node.nodeid

    def start(path: str | os.PathLike[str], *, engine_path: str | None = None) -> WorldSession:
        session = start_world(
            path, engine_path=engine_path or config.getoption("--attest-engine", default=None)
        )
        _item_worlds.setdefault(nodeid, []).append(session)
        return session

    yield start
    _stop_worlds(nodeid)


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

    def one_trial() -> Any:
        # Each trial's replay and world sessions end with the trial; a second trial would otherwise
        # find the first one's still running.
        try:
            return testfunction(**testargs)
        finally:
            try:
                _stop_worlds(nodeid)
            finally:
                # Settled even when a world fails to stop, so the replay lock and env are released.
                exc = _settle_replays(pyfuncitem)
                if exc is not None:
                    raise exc  # noqa: B012 (a miss outranks whatever the trial raised)

    result = run_trials(run, one_trial, n, _replace_test_results, trials_run)
    _session_trial_lines[pyfuncitem.nodeid] = result.summary_lines(pyfuncitem.nodeid)
    cli_rate: float = pyfuncitem.config.getoption(
        "--attest-min-pass-rate", default=DEFAULT_MIN_PASS_RATE
    )
    result.min_pass_rate = run.min_pass_rate if run.min_pass_rate is not None else cli_rate
    fixture.enforce(result)
    return True


@pytest.hookimpl(hookwrapper=True, tryfirst=True)
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
    if call.when == "setup" and item.nodeid in _session_tests:
        # A new attempt (a rerun plugin retrying the test) starts its replay summaries over;
        # drop them before this setup settles any of its own.
        _session_replay.pop(item.nodeid, None)
    if (
        call.when == "setup"
        and item.nodeid in _item_replays
        and (report.outcome == "failed" or hasattr(report, "wasxfail"))
    ):
        # A fixture ran the agent under replay during setup and setup failed: settle the
        # replay now, so a miss is reported as one instead of as the agent's exception
        # (or, under xfail, as an expected failure).
        settled = _settle_replays(item)
        if isinstance(settled, pytest.skip.Exception):
            if hasattr(report, "wasxfail"):
                del report.wasxfail
            _, lineno, _ = item.location
            report.outcome = "skipped"
            report.longrepr = (str(item.path), (lineno or 0) + 1, f"Skipped: {settled.msg}")
    replay_failure = item.stash.get(_REPLAY_FAILURE, None)
    if call.when in ("setup", "call") and replay_failure is not None:
        # Outermost wrapper: the xfail machinery has run. A strict replay miss is a failure
        # of the test, not an expected one.
        if hasattr(report, "wasxfail"):
            del report.wasxfail
        report.outcome = "failed"
        report.longrepr = replay_failure
        del item.stash[_REPLAY_FAILURE]
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
        record.replay = []
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
    if report.when == "call" or (report.when == "setup" and report.outcome != "passed"):
        # A setup that failed or skipped ran no call phase: its replay is the test's.
        record.replay = list(_session_replay.get(item.nodeid, []))
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
    replay: dict[str, list[ReplaySummary]]


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
            replay=dict(_session_replay),
        )
    )
    _session_replay.clear()
    block = _egress_mode(session.config) == "block"
    _egress_installed.append(egress.install_egress_guard() if block else None)
    _session_tests.clear()
    _session_trials_run.clear()
    _session_trial_lines.clear()
    _session_usage = Usage()
    end_run()


@pytest.hookimpl(hookwrapper=True, tryfirst=True)
def pytest_sessionfinish(session: pytest.Session, exitstatus: int) -> Generator[None, Any, None]:
    """Write the --attest-report file, let the terminal summary run, then
    restore the enclosing session's state."""
    if _is_xdist_worker(session.config) and _report_target(session) is not None:
        # Before the yield: xdist's own wrapper sends ``workeroutput`` to the controller as
        # this hook finishes, so the report document must already be in it.
        _send_worker_report(session)
    yield
    _write_session_report(session)
    release = _egress_installed.pop()
    if release is not None:
        release()
    global _session_usage
    saved = _saved_sessions.pop()
    _session_replay.clear()
    _session_replay.update(saved.replay)
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


# pytest-xdist: each worker's report document, by worker id, kept on the controller's config.
# The collected-test count needs no stash: xdist sets the controller's session.testscollected.
_XDIST_WORKER_REPORTS = pytest.StashKey[dict[str, dict[str, Any]]]()
_XDIST_MISSING_WORKERS = pytest.StashKey[list[str]]()
_WORKER_REPORT_KEY = "attest_report"


def _is_xdist_worker(config: pytest.Config) -> bool:
    return hasattr(config, "workerinput")


def _is_xdist_controller(config: pytest.Config) -> bool:
    return not _is_xdist_worker(config) and config.pluginmanager.has_plugin("dsession")


@pytest.hookimpl(optionalhook=True)
def pytest_testnodedown(node: Any, error: object) -> None:
    """Take a finished worker's report document; note a worker that left none (it crashed)."""
    config: pytest.Config = node.config
    document = getattr(node, "workeroutput", {}).get(_WORKER_REPORT_KEY)
    worker_id: str = node.workerinput["workerid"]
    if document is None:
        config.stash.setdefault(_XDIST_MISSING_WORKERS, []).append(worker_id)
        return
    config.stash.setdefault(_XDIST_WORKER_REPORTS, {})[worker_id] = document


def _report_target(session: pytest.Session) -> Path | None:
    target: str | None = session.config.getoption("--attest-report", default=None)
    if not target:
        return None
    path = Path(target)
    if not path.is_absolute():
        path = Path(session.config.invocation_params.dir) / path
    return path


def _send_worker_report(session: pytest.Session) -> None:
    """Put this worker's report document in the ``workeroutput`` the controller receives."""
    session.config.workeroutput[_WORKER_REPORT_KEY] = build_report(  # type: ignore[attr-defined]
        list(_session_tests.values()),
        simulated=is_simulation_mode(),
        usage=_session_usage,
        include_traces=report_traces_enabled(),
    )


def _write_session_report(session: pytest.Session) -> None:
    """Write the --attest-report file once every test has run.

    Under pytest-xdist a worker hands its report document to the controller through
    ``workeroutput`` (``_send_worker_report``) and writes nothing; the controller merges every
    worker's document.
    """
    path = _report_target(session)
    if path is None:
        return
    config = session.config
    if _is_xdist_worker(config):
        return
    if _is_xdist_controller(config):
        try:
            document = _merge_worker_reports(config, session.testscollected)
        except _IncompleteReportError as exc:
            # Raising here would be an uncaught traceback after the run; fail the run instead.
            # An older report at the same path must not survive to be graded as this run's.
            path.unlink(missing_ok=True)
            config.get_terminal_writer().line(f"ERROR: {exc}", red=True)
            session.exitstatus = pytest.ExitCode.INTERNAL_ERROR
            return
        write_document(path, document)
        return
    write_report(
        path,
        list(_session_tests.values()),
        simulated=is_simulation_mode(),
        usage=_session_usage,
        tests_collected=session.testscollected,
    )


class _IncompleteReportError(RuntimeError):
    """The controller cannot build a complete report: a worker's rows are missing."""


def _merge_worker_reports(config: pytest.Config, tests_collected: int) -> dict[str, Any]:
    """The controller's report: every worker's rows, or a loud error when one is missing."""
    missing = config.stash.get(_XDIST_MISSING_WORKERS, [])
    if missing:
        raise _IncompleteReportError(
            f"--attest-report not written: worker(s) {', '.join(sorted(missing))} ended "
            "without sending their report (crashed?); the report would be incomplete"
        )
    reports = config.stash.get(_XDIST_WORKER_REPORTS, {})
    if not reports:
        raise _IncompleteReportError(
            "--attest-report not written: no pytest-xdist worker sent a report"
        )
    ordered = [reports[w] for w in sorted(reports, key=_worker_order)]
    usage = Usage.total(Usage.from_dict(doc["summary"]["usage"]) for doc in ordered)
    return merge_reports(ordered, tests_collected=tests_collected, usage=usage)


def _worker_order(worker_id: str) -> tuple[int, str]:
    digits = worker_id.removeprefix("gw")
    return (int(digits) if digits.isdigit() else 0, worker_id)


def soft_failure_lines(soft_counts: dict[str, int], outcomes: dict[str, str]) -> list[str]:
    """One line per test with soft failures that pytest did not report as
    failed (failed tests already show their diagnostics in pytest's report)."""
    return [
        f"  {nid}: {n} soft ({outcomes.get(nid, 'passed')})"
        for nid, n in soft_counts.items()
        if n and outcomes.get(nid) != "failed"
    ]


def _batch_context() -> BatchContext | None:
    """The running test's identity, sent with each ``evaluate_batch``."""
    run = current_run()
    return run.batch_context() if run is not None else None


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
        #: Python implementations of ``.plugin()`` assertions, by ``plugin_id``; entry points
        #: of the ``attest.plugins`` group are loaded at construction.
        self.plugin_registry = PluginRegistry()
        load_entrypoint_plugins(self.plugin_registry)

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

        async def _start_reader() -> None:
            assert self._client is not None
            self._client.start_reader()

        # Every response is routed to its request by id from here on.
        self._loop.run_until_complete(_start_reader())

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
            manager = self._manager

            async def _shutdown() -> None:
                # The reader owns stdout until it stops; shutdown reads its reply.
                if self._client is not None:
                    await self._client.stop_reader()
                await manager.stop()

            future = asyncio.run_coroutine_threadsafe(_shutdown(), self._loop)
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

    def _evaluate_chain(self, chain: ExpectChain) -> Coroutine[Any, Any, EvaluateBatchResult]:
        """Prepare ``chain`` in the calling thread and return the coroutine that runs its
        plugin assertions and the ``evaluate_batch`` on the engine loop.

        Preparation gives the assertions their ids in the running test's scope, in evaluation
        order, and marks the chain evaluated.

        Raises:
            Failed: The chain has no assertions.
            RuntimeError: No test using the ``attest`` fixture is running, so there is no scope.
        """
        assert self._client is not None
        if not chain.assertions:
            pytest.fail(
                "expect() chain has no assertions; add a check before evaluating it",
                pytrace=False,
            )
        run = current_run()
        if run is None:
            raise RuntimeError(
                "evaluating an expect() chain needs a running test that uses the attest "
                "fixture: assertion ids are scoped to the test node id"
            )
        run.assign_ids(chain.assertions)
        chain.evaluated = True
        return self._run_chain(chain, budget=_batch_budget(chain.trace), context=_batch_context())

    async def _run_chain(
        self, chain: ExpectChain, *, budget: BatchBudget | None, context: BatchContext | None
    ) -> EvaluateBatchResult:
        """Submit each plugin assertion's result, then evaluate the batch (the engine reads
        the submitted result at evaluation, so the submit goes first)."""
        assert self._client is not None
        trace = chain.trace
        for assertion in chain.assertions:
            if assertion.type != TYPE_PLUGIN:
                continue
            plugin_id = assertion.spec.get("plugin_id")
            plugin = self.plugin_registry.get(PLUGIN_TYPE_ASSERTION, str(plugin_id))
            if plugin is None:
                # No submission: the engine reports not_evaluated / missing_evidence.
                continue
            await execute_plugin_assertion(
                plugin,
                trace,
                assertion.spec,
                self._client,
                trace.trace_id,
                assertion.assertion_id,
                plugin_name=str(plugin_id),
            )
        return await self._client.evaluate_batch(
            trace, chain.assertions, budget=budget, context=context
        )

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
            budget_exceeded=result.budget_exceeded,
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
        result = self._run_on_engine_loop(self._evaluate_chain(chain))
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
        future = asyncio.run_coroutine_threadsafe(self._evaluate_chain(chain), self._loop)
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
