"""Fixed-N trials, per-test budgets, deterministic assertion IDs and usage."""

from __future__ import annotations

import hashlib
import json
from collections.abc import Callable, Generator
from pathlib import Path
from typing import Any

import jsonschema
import pytest

from attest import plugin
from attest._proto.types import (
    REASON_BUDGET_EXHAUSTED,
    REASON_JUDGE_ABSTAINED,
    AssertionResult,
    BatchBudget,
    EvaluateBatchResult,
    Trace,
    TraceMetadata,
    Usage,
)
from attest.expect import ExpectChain
from attest.report_v2 import TestRecord, build_report
from attest.result import OUTCOME_FAIL, OUTCOME_PASS, OUTCOME_SKIP, AgentResult
from attest.trials import (
    DEFAULT_MIN_PASS_RATE,
    TestBudget,
    TestRun,
    TrialsResult,
    assertion_id_for,
    begin_run,
    current_run,
    end_run,
    min_pass_rate_from_marker,
    run_trials,
    validate_min_pass_rate,
)

pytest_plugins = ["pytester"]

_SCHEMA: dict[str, Any] = json.loads(
    (
        Path(__file__).resolve().parents[3] / "proto" / "attest" / "v1" / "report-v2.schema.json"
    ).read_text()
)


def _row(aid: str, status: str, reason: str | None = None) -> AssertionResult:
    return AssertionResult(
        assertion_id=aid,
        status=status,
        score=1.0 if status == "pass" else 0.0,
        explanation=f"{aid} {status}",
        reason=reason,
        layer=4,
        type="content",
    )


def _result(
    *rows: AssertionResult,
    usage: Usage | None = None,
    trace_id: str = "t",
    agent_cost: float | None = None,
) -> AgentResult:
    metadata = TraceMetadata(cost_usd=agent_cost) if agent_cost is not None else None
    return AgentResult(
        trace=Trace(trace_id=trace_id, output={}, metadata=metadata),
        assertion_results=list(rows),
        usage=usage if usage is not None else Usage(),
    )


@pytest.fixture
def run() -> Generator[TestRun, None, None]:
    test_run = TestRun(test_id="tests/test_x.py::test_refund")
    begin_run(test_run)
    yield test_run
    end_run()


def _no_count(count: int) -> None:
    return None


def _trials(run: TestRun, bodies: list[Callable[[], object]]) -> TrialsResult:
    calls = iter(bodies)
    return run_trials(run, lambda: next(calls)(), len(bodies), lambda old, new: None, _no_count)


# ── Deterministic assertion IDs ──


def test_assertion_id_for_is_sha256_of_test_id_and_ordinal() -> None:
    digest = hashlib.sha256(b"tests/a.py::test_x#3").hexdigest()

    assert assertion_id_for("tests/a.py::test_x", 3) == f"assert_{digest[:12]}"


def test_expect_inside_run_ids_are_deterministic_and_restart_each_trial(run: TestRun) -> None:
    result = _result()

    run.start_trial(1)
    first = [
        a.assertion_id for a in ExpectChain(result).output_contains("a").cost_under(1).assertions
    ]
    run.start_trial(2)
    second = [
        a.assertion_id for a in ExpectChain(result).output_contains("a").cost_under(1).assertions
    ]

    assert first == second == [assertion_id_for(run.test_id, 0), assertion_id_for(run.test_id, 1)]


def test_expect_ids_unique_within_one_test_without_trials(run: TestRun) -> None:
    ids = [a.assertion_id for a in ExpectChain(_result()).output_contains("a").assertions]
    ids += [a.assertion_id for a in ExpectChain(_result()).output_contains("b").assertions]

    assert ids == [assertion_id_for(run.test_id, 0), assertion_id_for(run.test_id, 1)]


def test_expect_outside_run_ids_are_random() -> None:
    assert current_run() is None
    chain = ExpectChain(_result()).output_contains("a").output_contains("a")

    first, second = (a.assertion_id for a in chain.assertions)

    assert first != second
    assert len(first) == len("assert_") + 8


# ── Per-test budget ──


def test_budget_marker_valid_kwargs_parse() -> None:
    budget = TestBudget.from_marker((), {"max_judge_calls": 3, "max_cost_usd": 1})

    assert budget == TestBudget(max_judge_calls=3, max_cost_usd=1.0)


@pytest.mark.parametrize(
    ("args", "kwargs", "message"),
    [
        ((5,), {}, "keyword arguments only"),
        ((), {"max_calls": 1}, "unknown argument(s) max_calls"),
        ((), {"max_judge_calls": -1}, "max_judge_calls must be >= 0"),
        ((), {"max_cost_usd": -0.5}, "max_cost_usd must be a finite number >= 0"),
        ((), {"max_judge_calls": 1.5}, "max_judge_calls must be an int"),
        ((), {"max_cost_usd": "1"}, "max_cost_usd must be a number"),
    ],
)
def test_budget_marker_invalid_arguments_raise(
    args: tuple[object, ...], kwargs: dict[str, object], message: str
) -> None:
    with pytest.raises(ValueError, match=message.replace("(", r"\(").replace(")", r"\)")):
        TestBudget.from_marker(args, kwargs)


def test_batch_budget_without_marker_is_none() -> None:
    trace = Trace(trace_id="t", output={})

    assert TestRun(test_id="t").batch_budget(trace) is None
    assert TestRun(test_id="t", budget=TestBudget()).batch_budget(trace) is None


def test_batch_budget_sends_remainder_of_judge_calls_and_cost_with_agent_spend() -> None:
    test_run = TestRun(test_id="t", budget=TestBudget(max_judge_calls=5, max_cost_usd=1.0))
    trace_a = _result(trace_id="a", agent_cost=0.5).trace

    # The trace about to be evaluated counts before the remainder is computed.
    assert test_run.batch_budget(trace_a) == BatchBudget(max_judge_calls=5, max_cost_usd=0.5)
    test_run.record(
        _result(usage=Usage(judge_calls=2, cost_usd=0.25), trace_id="a", agent_cost=0.5)
    )
    # The same trace evaluated again does not count its agent spend twice.
    test_run.record(
        _result(usage=Usage(judge_calls=1, cost_usd=0.05), trace_id="a", agent_cost=0.5)
    )

    assert test_run.batch_budget(trace_a) == BatchBudget(
        max_judge_calls=2, max_cost_usd=pytest.approx(0.2)
    )


def test_batch_budget_floors_remainder_at_zero() -> None:
    test_run = TestRun(test_id="t", budget=TestBudget(max_judge_calls=1, max_cost_usd=0.1))

    test_run.record(_result(usage=Usage(judge_calls=4, cost_usd=0.3)))

    assert test_run.batch_budget(Trace(trace_id="t", output={})) == BatchBudget(
        max_judge_calls=0, max_cost_usd=0.0
    )
    assert test_run.cost_budget_spent()


def test_plugin_batch_budget_follows_running_test(run: TestRun) -> None:
    trace = Trace(trace_id="t", output={})
    assert plugin._batch_budget(trace) is None
    run.budget = TestBudget(max_judge_calls=2)

    assert plugin._batch_budget(trace) == BatchBudget(max_judge_calls=2)


# ── Fixed-N trials ──


def test_run_trials_tags_rows_and_passes_when_some_trial_passes(run: TestRun) -> None:
    result = _trials(
        run,
        [
            lambda: run.record(_result(_row("a", "pass"))),
            lambda: run.record(_result(_row("a", "hard_fail"))),
            lambda: run.record(_result(_row("a", "pass"))),
        ],
    )

    assert [r.assertion_results[0].trial for t in result.trials for r in t] == [1, 2, 3]
    assert (result.passed, result.evaluated, result.not_evaluated) == (2, 3, 0)
    assert result.outcome() == OUTCOME_FAIL  # default min pass rate 1.0
    result.min_pass_rate = 0.6
    assert result.outcome() == OUTCOME_PASS
    assert result.summary_lines("tid") == ["tid: 2/3 trials passed (0 not evaluated)"]


def test_run_trials_raised_trial_adds_hard_fail_row_per_seen_assertion_id(run: TestRun) -> None:
    replaced: list[tuple[list[AgentResult], AgentResult]] = []

    def boom() -> None:
        raise ConnectionError("HTTP 500")

    bodies: list[Callable[[], object]] = [
        lambda: run.record(_result(_row("a", "pass"), _row("b", "pass"))),
        boom,
        lambda: run.record(_result(_row("a", "pass"), _row("b", "hard_fail"))),
    ]
    calls = iter(bodies)
    result = run_trials(
        run,
        lambda: next(calls)(),
        3,
        lambda old, new: replaced.append((old, new)),
        _no_count,
    )

    removed, replacement = replaced[0]
    rows = replacement.assertion_results
    assert removed == []
    assert [(r.assertion_id, r.status, r.trial, r.failure_class) for r in rows] == [
        ("a", "hard_fail", 2, "broken_code"),
        ("b", "hard_fail", 2, "broken_code"),
    ]
    assert rows[0].score == 0.0
    assert rows[0].explanation == "trial 2 raised ConnectionError: HTTP 500"
    assert result.trials[1] == [replacement]
    assert result.raised == frozenset({2})
    assert (result.passed, result.evaluated) == (1, 3)
    assert result.outcome() == OUTCOME_FAIL  # default min pass rate 1.0
    result.min_pass_rate = 0.3
    assert result.outcome() == OUTCOME_PASS


def test_run_trials_raise_after_passing_evaluation_replaces_rows_and_fails_trial(
    run: TestRun,
) -> None:
    replaced: list[tuple[list[AgentResult], AgentResult]] = []

    def evaluate_then_raise() -> None:
        run.record(_result(_row("a", "pass")))
        raise AssertionError("checked after evaluate")

    calls = iter([evaluate_then_raise] * 3)
    result = run_trials(
        run,
        lambda: next(calls)(),
        3,
        lambda old, new: replaced.append((old, new)),
        _no_count,
    )

    assert [len(old) for old, _ in replaced] == [1, 1, 1]
    rows = [row for t in result.trials for r in t for row in r.assertion_results]
    assert [(r.assertion_id, r.status, r.trial) for r in rows] == [
        ("a", "hard_fail", 1),
        ("a", "hard_fail", 2),
        ("a", "hard_fail", 3),
    ]
    assert rows[0].explanation == (
        "trial 1 raised AssertionError: checked after evaluate"
        " (engine status before the error: pass)"
    )
    assert (result.passed, result.evaluated) == (0, 3)
    assert result.outcome() == OUTCOME_FAIL


def test_run_trials_reports_trials_run_when_reraising(run: TestRun) -> None:
    counts: list[int] = []

    def boom() -> None:
        raise KeyError("x")

    with pytest.raises(KeyError):
        run_trials(run, boom, 2, lambda old, new: None, counts.append)

    assert counts == [2]


def test_replace_test_results_drops_stale_rows_of_raised_trial() -> None:
    plugin._take_test_results()
    kept, stale = _result(_row("a", "pass")), _result(_row("b", "pass"))
    replacement = _result(_row("b", "hard_fail"))
    plugin._record_result(kept)
    plugin._record_result(stale)

    plugin._replace_test_results([stale], replacement)

    assert plugin._take_test_results() == [kept, replacement]


def test_trials_without_assertions_skip_with_no_assertions_reason() -> None:
    result = TrialsResult(trials=[[], []], planned=2)

    assert result.outcome() == OUTCOME_SKIP
    assert result.outcome(strict=True) == OUTCOME_FAIL
    assert result.skip_reason() == "not evaluated: no assertions in 2 trials"


def test_run_trials_none_passed_fails_with_failing_trial_diagnostics(run: TestRun) -> None:
    def boom() -> None:
        raise RuntimeError("down")

    result = _trials(run, [lambda: run.record(_result(_row("a", "hard_fail"))), boom])

    assert result.outcome() == OUTCOME_FAIL
    diagnostics = result.render_diagnostics()
    assert "trial 1/2:" in diagnostics
    assert "trial 2/2:" in diagnostics
    assert "trial 2 raised RuntimeError: down" in diagnostics


def test_run_trials_all_not_evaluated_skips_with_summed_reasons(run: TestRun) -> None:
    result = _trials(
        run,
        [
            lambda: run.record(_result(_row("j", "not_evaluated", REASON_BUDGET_EXHAUSTED))),
            lambda: run.record(_result(_row("j", "not_evaluated", REASON_BUDGET_EXHAUSTED))),
            lambda: run.record(_result(_row("j", "not_evaluated", REASON_JUDGE_ABSTAINED))),
        ],
    )

    assert result.outcome() == OUTCOME_SKIP
    assert result.outcome(strict=True) == OUTCOME_FAIL
    assert result.skip_reason() == "not evaluated: budget_exhausted ×2, judge_abstained"
    assert result.summary_lines("tid") == ["tid: 0/0 trials passed (3 not evaluated)"]


def test_run_trials_skipped_trials_do_not_count_against_passes(run: TestRun) -> None:
    result = _trials(
        run,
        [
            lambda: run.record(_result(_row("j", "not_evaluated", REASON_BUDGET_EXHAUSTED))),
            lambda: run.record(_result(_row("j", "pass"))),
        ],
    )

    assert (result.passed, result.evaluated, result.not_evaluated) == (1, 1, 1)
    assert result.outcome() == OUTCOME_PASS


def test_run_trials_no_assertion_reraises_first_exception(run: TestRun) -> None:
    def first() -> None:
        raise KeyError("first")

    def second() -> None:
        raise KeyError("second")

    with pytest.raises(KeyError, match="first"):
        _trials(run, [first, second])
    assert run.trial is None


def test_run_trials_pytest_outcome_exceptions_propagate(run: TestRun) -> None:
    def skip() -> None:
        pytest.skip("not today")

    with pytest.raises(pytest.skip.Exception):
        _trials(run, [lambda: run.record(_result(_row("a", "pass"))), skip])


def test_run_trials_stops_when_cost_budget_spent(run: TestRun) -> None:
    run.budget = TestBudget(max_cost_usd=0.01)

    result = _trials(
        run,
        [
            lambda: run.record(_result(_row("a", "pass"), agent_cost=0.02)),
            lambda: pytest.fail("trial 2 must not run"),
            lambda: pytest.fail("trial 3 must not run"),
        ],
    )

    assert len(result.trials) == 1
    assert result.summary_lines("tid") == [
        "tid: 1/1 trials passed (0 not evaluated)",
        "tid: stopped after 1 of 3 trials: test budget spent",
    ]


def test_enforce_is_noop_during_trial(run: TestRun) -> None:
    failing = _result(_row("a", "hard_fail"))
    run.start_trial(1)

    assert plugin.AttestEngineFixture().enforce(failing) is failing


# ── Usage ──


def test_usage_sums_and_formats_like_engine() -> None:
    total = Usage.total(
        [
            Usage(model_calls=2, judge_calls=2, input_tokens=100, output_tokens=20, cost_usd=0.01),
            Usage(model_calls=1, cached_tokens=40, retry_cost_usd=0.002, cost_usd=0.002),
        ]
    )

    assert total == Usage(
        model_calls=3,
        judge_calls=2,
        input_tokens=100,
        output_tokens=20,
        cached_tokens=40,
        cost_usd=pytest.approx(0.012),
        retry_cost_usd=0.002,
    )
    assert plugin.usage_line(total) == (
        "attest usage: 3 model calls · 120 tokens (40 cached) · $0.0120 · retry cost $0.0020"
    )


def test_usage_line_appends_refused_and_unpriced_and_hides_when_idle() -> None:
    usage = Usage(budget_refused=2, unpriced_calls=1)

    assert plugin.usage_line(usage) == (
        "attest usage: 0 model calls · 0 tokens (0 cached) · $0.0000 · retry cost $0.0000"
        " · 2 calls refused by budget · 1 unpriced calls not in $"
    )
    assert plugin.usage_line(Usage(unpriced_calls=1)) is None


def test_usage_from_dict_requires_every_field() -> None:
    with pytest.raises(ValueError, match="budget_refused"):
        Usage.from_dict({k: 0 for k in Usage().to_dict() if k != "budget_refused"})
    with pytest.raises(ValueError, match="usage"):
        EvaluateBatchResult.from_dict({"results": []})


def test_process_result_attaches_usage_and_records_into_run(run: TestRun) -> None:
    run.start_trial(2)
    batch = EvaluateBatchResult(results=[_row("a", "pass")], usage=Usage(model_calls=1))
    chain = ExpectChain(_result())

    agent_result = plugin.AttestEngineFixture()._process_result(chain, batch, None)
    plugin._take_test_results()

    assert agent_result.usage == Usage(model_calls=1)
    assert agent_result.assertion_results[0].trial == 2
    assert run.results_by_trial[2] == [agent_result]


# ── Report ──


def test_report_rows_carry_trial_test_row_trials_and_summary_usage() -> None:
    first = _row("a", "pass")
    first.trial = 1
    second = _row("a", "hard_fail")
    second.trial = 2
    record = TestRecord(
        test_id="tests/test_x.py::test_refund",
        name="test_refund",
        file="tests/test_x.py",
        line=3,
        agent_results=[_result(first), _result(second)],
        trials=2,
    )
    usage = Usage(model_calls=4, judge_calls=4, cost_usd=0.02)

    report = build_report([record], simulated=False, usage=usage)

    jsonschema.validate(report, _SCHEMA, format_checker=jsonschema.FormatChecker())
    assert [r["trial"] for r in report["results"]] == [1, 2]
    assert report["tests"][0]["trials"] == 2
    assert report["summary"]["usage"] == usage.to_dict()


def test_report_single_run_omits_trial_fields() -> None:
    record = TestRecord(
        test_id="t", name="t", file="t.py", line=1, agent_results=[_result(_row("a", "pass"))]
    )

    report = build_report([record], simulated=False, usage=Usage())

    jsonschema.validate(report, _SCHEMA, format_checker=jsonschema.FormatChecker())
    assert "trial" not in report["results"][0]
    assert "trials" not in report["tests"][0]
    assert report["summary"]["usage"]["model_calls"] == 0


def test_nested_session_keeps_outer_usage_and_running_test(pytester: pytest.Pytester) -> None:
    outer = TestRun(test_id="outer")
    begin_run(outer)
    plugin._session_usage = Usage(model_calls=7)
    try:
        pytester.makepyfile(test_inner="def test_ok():\n    pass\n")

        pytester.runpytest_inprocess("-p", "no:cacheprovider").assert_outcomes(passed=1)

        assert current_run() is outer
        assert plugin._session_usage == Usage(model_calls=7)
    finally:
        end_run()
        plugin._session_usage = Usage()


# ── Min pass rate ──


def _rate_trials(passed: int, failed: int, not_evaluated: int = 0) -> TrialsResult:
    trials = (
        [[_result(_row("a", "pass"))]] * passed
        + [[_result(_row("a", "hard_fail"))]] * failed
        + [[_result(_row("a", "not_evaluated", REASON_BUDGET_EXHAUSTED))]] * not_evaluated
    )
    return TrialsResult(trials=trials, planned=len(trials))


def test_min_pass_rate_default_one_fails_on_any_failing_trial() -> None:
    result = _rate_trials(passed=19, failed=1)

    assert DEFAULT_MIN_PASS_RATE == 1.0
    assert result.outcome() == OUTCOME_FAIL
    assert result.render_diagnostics().startswith(
        "19/20 trials passed (pass rate 0.95) below min pass rate 1.00\n"
    )
    assert result.summary_lines("tid") == ["tid: 19/20 trials passed (0 not evaluated)"]


@pytest.mark.parametrize(("passed", "outcome"), [(18, OUTCOME_PASS), (17, OUTCOME_FAIL)])
def test_min_pass_rate_point_nine_boundary(passed: int, outcome: str) -> None:
    result = _rate_trials(passed=passed, failed=20 - passed)
    result.min_pass_rate = 0.9

    assert result.outcome() == outcome


def test_min_pass_rate_excludes_not_evaluated_trials() -> None:
    result = _rate_trials(passed=9, failed=1, not_evaluated=10)
    result.min_pass_rate = 0.9

    assert (result.passed, result.evaluated, result.not_evaluated) == (9, 10, 10)
    assert result.outcome() == OUTCOME_PASS


def test_min_pass_rate_counts_raised_trial_as_evaluated_failure() -> None:
    result = TrialsResult(
        trials=[[_result(_row("a", "pass"))], [_result(_row("a", "hard_fail"))]],
        planned=2,
        raised=frozenset({2}),
        min_pass_rate=0.5,
    )

    assert (result.passed, result.evaluated) == (1, 2)
    assert result.outcome() == OUTCOME_PASS


@pytest.mark.parametrize("value", [-0.1, 1.5, float("nan"), float("inf"), "0.9", True])
def test_validate_min_pass_rate_rejects_invalid(value: object) -> None:
    with pytest.raises(ValueError, match="--attest-min-pass-rate must be"):
        validate_min_pass_rate(value, "--attest-min-pass-rate")


@pytest.mark.parametrize(
    ("args", "kwargs"), [((), {}), ((0.5, 0.6), {}), ((), {"rate": 0.5}), ((2,), {})]
)
def test_min_pass_rate_marker_invalid_raises(
    args: tuple[object, ...], kwargs: dict[str, object]
) -> None:
    with pytest.raises(ValueError, match="attest_min_pass_rate"):
        min_pass_rate_from_marker(args, kwargs)


def test_min_pass_rate_marker_valid() -> None:
    assert min_pass_rate_from_marker((0.8,), {}) == 0.8


@pytest.mark.parametrize("value", ["1.5", "-1", "nan"])
def test_cli_min_pass_rate_invalid_is_usage_error(pytester: pytest.Pytester, value: str) -> None:
    pytester.makepyfile(test_inner="def test_ok():\n    pass\n")

    run = pytester.runpytest_subprocess(f"--attest-min-pass-rate={value}")

    assert run.ret == pytest.ExitCode.USAGE_ERROR
    run.stderr.fnmatch_lines(["*--attest-min-pass-rate must be a finite number between 0 and 1*"])
