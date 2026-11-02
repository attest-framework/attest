"""Per-test state of the pytest plugin: deterministic assertion IDs, the
``attest_budget`` marker and fixed-N trials (``--attest-trials=N``).

Trials run the test body N times inside one pytest item, tag every result row
with its 1-based ``trial`` and enforce only the aggregate outcome. The
statistical verdict over trials (pass / regressed / inconclusive against a
pinned baseline) is not computed here: it comes from
``attest policy evaluate --baseline <tag>`` with a ``regression`` rule, which
groups the report's rows by ``assertion_id``.
"""

from __future__ import annotations

import hashlib
import math
import threading
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, field

from attest._proto.types import (
    FAILURE_CLASS_BROKEN_CODE,
    STATUS_HARD_FAIL,
    AssertionResult,
    BatchBudget,
    Trace,
    Usage,
)
from attest.result import (
    OUTCOME_FAIL,
    OUTCOME_PASS,
    OUTCOME_SKIP,
    AgentResult,
    Verdict,
    format_skip_reason,
    trials_diagnostics,
    trials_not_evaluated_reasons,
)
from attest.simulation.repeat import RepeatResult

BUDGET_MARKER = "attest_budget"
_BUDGET_KWARGS = ("max_judge_calls", "max_cost_usd")


def assertion_id_for(test_id: str, ordinal: int) -> str:
    """Deterministic assertion ID: ``assert_`` + 12 hex chars of
    sha256(``"{test_id}#{ordinal}"``). Stable across trials and runs, so
    baselines and trials match rows by ``assertion_id``."""
    digest = hashlib.sha256(f"{test_id}#{ordinal}".encode()).hexdigest()
    return f"assert_{digest[:12]}"


@dataclass(frozen=True)
class TestBudget:
    """Limits of ``@pytest.mark.attest_budget(max_judge_calls=, max_cost_usd=)``
    for the whole test (every trial). ``None`` means no limit."""

    __test__ = False  # not a pytest test class

    max_judge_calls: int | None = None
    max_cost_usd: float | None = None

    @classmethod
    def from_marker(cls, args: Sequence[object], kwargs: Mapping[str, object]) -> TestBudget:
        """Validate the marker's arguments; raise ``ValueError`` on misuse."""
        if args:
            raise ValueError(
                f"{BUDGET_MARKER} takes keyword arguments only "
                f"({', '.join(_BUDGET_KWARGS)}), got positional {list(args)!r}"
            )
        unknown = sorted(set(kwargs) - set(_BUDGET_KWARGS))
        if unknown:
            raise ValueError(
                f"{BUDGET_MARKER}: unknown argument(s) {', '.join(unknown)}; "
                f"expected {', '.join(_BUDGET_KWARGS)}"
            )
        judge = kwargs.get("max_judge_calls")
        if judge is not None:
            if isinstance(judge, bool) or not isinstance(judge, int):
                raise ValueError(f"{BUDGET_MARKER}: max_judge_calls must be an int, got {judge!r}")
            if judge < 0:
                raise ValueError(f"{BUDGET_MARKER}: max_judge_calls must be >= 0, got {judge}")
        cost = kwargs.get("max_cost_usd")
        if cost is not None:
            if isinstance(cost, bool) or not isinstance(cost, (int, float)):
                raise ValueError(f"{BUDGET_MARKER}: max_cost_usd must be a number, got {cost!r}")
            if not math.isfinite(cost) or cost < 0:
                raise ValueError(
                    f"{BUDGET_MARKER}: max_cost_usd must be a finite number >= 0, got {cost}"
                )
        return cls(
            max_judge_calls=judge,
            max_cost_usd=None if cost is None else float(cost),
        )


@dataclass
class TestRun:
    """State of one running test (pytest call phase of a test using the
    ``attest`` fixture): assertion ordinal, current trial, usage and agent
    spend so far, and every evaluation grouped by trial."""

    __test__ = False  # not a pytest test class

    test_id: str
    budget: TestBudget | None = None
    trial: int | None = None
    usage: Usage = field(default_factory=Usage)
    agent_spend_usd: float = 0.0
    results_by_trial: dict[int, list[AgentResult]] = field(default_factory=dict)
    _ordinal: int = 0
    _spent_trace_ids: set[str] = field(default_factory=set)
    _lock: threading.Lock = field(default_factory=threading.Lock, repr=False)

    def next_assertion_id(self) -> str:
        """The next deterministic ID; the ordinal restarts with each trial."""
        with self._lock:
            ordinal = self._ordinal
            self._ordinal += 1
        return assertion_id_for(self.test_id, ordinal)

    def start_trial(self, trial: int) -> None:
        """Enter trial ``trial`` (1-based): restart the ID ordinal."""
        with self._lock:
            self.trial = trial
            self._ordinal = 0
            self.results_by_trial[trial] = []

    def spent_usd(self) -> float:
        """Attest cost plus agent spend so far in this test."""
        return self.usage.cost_usd + self.agent_spend_usd

    def batch_budget(self) -> BatchBudget | None:
        """The remainder of the test budget for the next ``evaluate_batch``,
        floored at 0; ``None`` when the test has no budget."""
        if self.budget is None:
            return None
        judge = self.budget.max_judge_calls
        cost = self.budget.max_cost_usd
        if judge is None and cost is None:
            return None
        return BatchBudget(
            max_judge_calls=None if judge is None else max(0, judge - self.usage.judge_calls),
            max_cost_usd=None if cost is None else max(0.0, cost - self.spent_usd()),
        )

    def cost_budget_spent(self) -> bool:
        """True when the test's cost budget is set and attest cost plus
        agent spend has reached it (checked between trials)."""
        if self.budget is None or self.budget.max_cost_usd is None:
            return False
        return self.spent_usd() >= self.budget.max_cost_usd

    def record(self, agent_result: AgentResult) -> None:
        """Count one evaluation's usage and its trace's agent spend (once per
        trace), and file it under the current trial with ``trial`` set on
        every row."""
        with self._lock:
            self.usage = self.usage + agent_result.usage
            trace = agent_result.trace
            if (
                trace.metadata is not None
                and trace.metadata.cost_usd is not None
                and trace.trace_id not in self._spent_trace_ids
            ):
                self._spent_trace_ids.add(trace.trace_id)
                self.agent_spend_usd += trace.metadata.cost_usd
            if self.trial is not None:
                for row in agent_result.assertion_results:
                    row.trial = self.trial
                self.results_by_trial[self.trial].append(agent_result)


_current_run: TestRun | None = None


def current_run() -> TestRun | None:
    """The running test's state, or ``None`` outside the pytest call phase of
    a test using the ``attest`` fixture."""
    return _current_run


def begin_run(run: TestRun) -> None:
    """Make ``run`` the running test (pytest serialises tests in a worker)."""
    global _current_run
    _current_run = run


def end_run() -> None:
    """Leave the running test; assertion IDs are random again."""
    global _current_run
    _current_run = None


def _trial_outcome(evaluations: Sequence[AgentResult]) -> str:
    """C1 outcome of one trial over all its rows; a trial without rows is
    not evaluated."""
    if not any(r.assertion_results for r in evaluations):
        return OUTCOME_SKIP
    return RepeatResult(results=list[Verdict](evaluations)).outcome()


@dataclass
class TrialsResult:
    """Aggregate of fixed-N trials. A trial passes when its own outcome
    (any ``hard_fail`` → fail, else any ``not_evaluated`` → skip, else pass)
    is pass; a trial that raised carries ``hard_fail`` rows and fails.

    Test outcome: no trial evaluated → skip with the summed not-evaluated
    reasons (fail when ``strict``); otherwise no trial passed → fail;
    otherwise pass. Use ``attest policy evaluate --baseline <tag>`` with a
    ``regression`` rule for the statistical verdict against a baseline.
    """

    #: Evaluations of each trial that ran, in trial order.
    trials: list[list[AgentResult]]
    #: Trials requested (``--attest-trials``).
    planned: int
    #: True when the test's cost budget stopped the loop early.
    stopped_by_budget: bool = False

    def _outcomes(self) -> list[str]:
        return [_trial_outcome(t) for t in self.trials]

    @property
    def passed(self) -> int:
        return self._outcomes().count(OUTCOME_PASS)

    @property
    def evaluated(self) -> int:
        """Trials whose outcome is pass or fail."""
        return sum(1 for o in self._outcomes() if o != OUTCOME_SKIP)

    @property
    def not_evaluated(self) -> int:
        return self._outcomes().count(OUTCOME_SKIP)

    @property
    def not_evaluated_reasons(self) -> dict[str, int]:
        """Not-evaluated reasons summed across trials."""
        return trials_not_evaluated_reasons(self._verdicts())

    def _verdicts(self) -> list[RepeatResult]:
        return [RepeatResult(results=list[Verdict](t)) for t in self.trials]

    def outcome(self, *, strict: bool = False) -> str:
        if not any(r.assertion_results for t in self.trials for r in t):
            # No trial produced a row: the test does not evaluate (today's N=1 behavior).
            return OUTCOME_PASS
        if self.evaluated == 0:
            return OUTCOME_FAIL if strict else OUTCOME_SKIP
        if self.passed == 0:
            return OUTCOME_FAIL
        return OUTCOME_PASS

    def skip_reason(self) -> str:
        return format_skip_reason(self.not_evaluated_reasons)

    def render_diagnostics(self, *, color: bool = False) -> str:
        """Diagnostics of every trial that did not pass."""
        return trials_diagnostics(self._verdicts(), label="trial", color=color)

    def summary_lines(self, test_id: str) -> list[str]:
        """Terminal lines: the pass count, and the budget stop when it happened."""
        lines = [
            f"{test_id}: {self.passed}/{self.evaluated} trials passed "
            f"({self.not_evaluated} not evaluated)"
        ]
        if self.stopped_by_budget:
            lines.append(
                f"{test_id}: stopped after {len(self.trials)} of {self.planned} trials: "
                "test budget spent"
            )
        return lines


def raised_trial_result(
    test_id: str, trial: int, exc: Exception, seen: Mapping[str, AssertionResult]
) -> AgentResult:
    """One ``hard_fail`` row per assertion id in ``seen`` for a trial that raised,
    so pass rates count the trial. ``seen`` maps each id to its first row,
    whose layer and type the new row copies. The trial produced no trace; the
    result carries an empty one."""
    explanation = f"trial {trial} raised {type(exc).__name__}: {exc}"
    rows = [
        AssertionResult(
            assertion_id=aid,
            status=STATUS_HARD_FAIL,
            score=0.0,
            explanation=explanation,
            layer=first.layer,
            type=first.type,
            failure_class=FAILURE_CLASS_BROKEN_CODE,
            trial=trial,
        )
        for aid, first in seen.items()
    ]
    return AgentResult(
        trace=Trace(trace_id=f"{test_id}#trial{trial}", output={}),
        assertion_results=rows,
    )


def run_trials(
    run: TestRun,
    body: Callable[[], object],
    n: int,
    on_raised: Callable[[AgentResult], None],
) -> TrialsResult:
    """Run ``body`` as trials 1..n of ``run`` and return the aggregate.

    Before each trial after the first, stop when the test's cost budget is
    spent. A trial raising an ``Exception`` counts as failed (pytest outcome
    exceptions such as skip and fail are ``BaseException`` and propagate):
    after the loop it gets one ``hard_fail`` row per assertion id seen in the
    test's trials that it did not evaluate itself, passed to ``on_raised``. When no trial
    produced any assertion, the first exception is re-raised.
    """
    if n < 1:
        raise ValueError(f"trials must be >= 1, got {n}")
    raised: dict[int, Exception] = {}
    stopped = False
    try:
        for i in range(1, n + 1):
            if i > 1 and run.cost_budget_spent():
                stopped = True
                break
            run.start_trial(i)
            try:
                body()
            except Exception as exc:  # a raising trial is a failed trial
                raised[i] = exc
    finally:
        run.trial = None

    seen: dict[str, AssertionResult] = {}
    for i in sorted(run.results_by_trial):
        for agent_result in run.results_by_trial[i]:
            for row in agent_result.assertion_results:
                seen.setdefault(row.assertion_id, row)
    if raised and not seen:
        raise raised[min(raised)]
    for i in sorted(raised):
        # Assertions the trial evaluated before raising keep their rows; only
        # the ones it never reached become hard_fail, so no trial counts twice.
        done = {
            row.assertion_id
            for agent_result in run.results_by_trial.get(i, [])
            for row in agent_result.assertion_results
        }
        missing = {aid: row for aid, row in seen.items() if aid not in done}
        if not missing:
            continue
        result = raised_trial_result(run.test_id, i, raised[i], missing)
        run.results_by_trial[i].append(result)
        on_raised(result)
    return TrialsResult(
        trials=[run.results_by_trial[i] for i in sorted(run.results_by_trial)],
        planned=n,
        stopped_by_budget=stopped,
    )
