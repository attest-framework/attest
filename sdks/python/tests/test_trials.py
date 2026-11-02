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
    TestBudget,
    TestRun,
    TrialsResult,
    assertion_id_for,
    begin_run,
    current_run,
    end_run,
    run_trials,
)

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


def _trials(run: TestRun, bodies: list[Callable[[], object]]) -> TrialsResult:
    calls = iter(bodies)
    return run_trials(run, lambda: next(calls)(), len(bodies), lambda r: None)


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
    assert TestRun(test_id="t").batch_budget() is None
    assert TestRun(test_id="t", budget=TestBudget()).batch_budget() is None


def test_batch_budget_sends_remainder_of_judge_calls_and_cost_with_agent_spend() -> None:
    test_run = TestRun(test_id="t", budget=TestBudget(max_judge_calls=5, max_cost_usd=1.0))

    test_run.record(
        _result(usage=Usage(judge_calls=2, cost_usd=0.25), trace_id="a", agent_cost=0.5)
    )
    # The same trace evaluated again does not count its agent spend twice.
    test_run.record(
        _result(usage=Usage(judge_calls=1, cost_usd=0.05), trace_id="a", agent_cost=0.5)
    )

    assert test_run.batch_budget() == BatchBudget(
        max_judge_calls=2, max_cost_usd=pytest.approx(0.2)
    )


def test_batch_budget_floors_remainder_at_zero() -> None:
    test_run = TestRun(test_id="t", budget=TestBudget(max_judge_calls=1, max_cost_usd=0.1))

    test_run.record(_result(usage=Usage(judge_calls=4, cost_usd=0.3)))

    assert test_run.batch_budget() == BatchBudget(max_judge_calls=0, max_cost_usd=0.0)
    assert test_run.cost_budget_spent()


def test_plugin_batch_budget_follows_running_test(run: TestRun) -> None:
    assert plugin._batch_budget() is None
    run.budget = TestBudget(max_judge_calls=2)

    assert plugin._batch_budget() == BatchBudget(max_judge_calls=2)


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
    assert result.outcome() == OUTCOME_PASS
    assert result.summary_lines("tid") == ["tid: 2/3 trials passed (0 not evaluated)"]


def test_run_trials_raised_trial_adds_hard_fail_row_per_seen_assertion_id(run: TestRun) -> None:
    recorded: list[AgentResult] = []

    def boom() -> None:
        raise ConnectionError("HTTP 500")

    bodies: list[Callable[[], object]] = [
        lambda: run.record(_result(_row("a", "pass"), _row("b", "pass"))),
        boom,
        lambda: run.record(_result(_row("a", "pass"), _row("b", "hard_fail"))),
    ]
    calls = iter(bodies)
    result = run_trials(run, lambda: next(calls)(), 3, recorded.append)

    rows = recorded[0].assertion_results
    assert [(r.assertion_id, r.status, r.trial, r.failure_class) for r in rows] == [
        ("a", "hard_fail", 2, "broken_code"),
        ("b", "hard_fail", 2, "broken_code"),
    ]
    assert rows[0].score == 0.0
    assert rows[0].explanation == "trial 2 raised ConnectionError: HTTP 500"
    assert result.trials[1] == recorded
    assert (result.passed, result.evaluated) == (1, 3)
    assert result.outcome() == OUTCOME_PASS


def test_run_trials_raise_after_evaluating_adds_rows_only_for_unreached_ids(
    run: TestRun,
) -> None:
    recorded: list[AgentResult] = []

    def partial_then_raise() -> None:
        run.record(_result(_row("a", "pass")))
        raise ConnectionError("HTTP 500")

    bodies: list[Callable[[], object]] = [
        lambda: run.record(_result(_row("a", "pass"), _row("b", "pass"))),
        partial_then_raise,
    ]
    calls = iter(bodies)
    run_trials(run, lambda: next(calls)(), 2, recorded.append)

    rows = [r for agent_result in recorded for r in agent_result.assertion_results]
    assert [(r.assertion_id, r.status, r.trial) for r in rows] == [("b", "hard_fail", 2)]


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
