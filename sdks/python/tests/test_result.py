"""Tests for AgentResult model."""

from __future__ import annotations

from attest.result import AgentResult
from attest._proto.types import (
    AssertionResult,
    Trace,
    STATUS_PASS,
    STATUS_HARD_FAIL,
    STATUS_SOFT_FAIL,
)


def test_agent_result_all_pass() -> None:
    trace = Trace(trace_id="trc_1", output={"message": "ok"})
    results = [
        AssertionResult(assertion_id="a1", status=STATUS_PASS, score=1.0, explanation="ok"),
        AssertionResult(assertion_id="a2", status=STATUS_PASS, score=1.0, explanation="ok"),
    ]
    ar = AgentResult(trace=trace, assertion_results=results)
    assert ar.passed is True
    assert ar.fail_count == 0
    assert ar.pass_count == 2
    assert ar.failed_assertions == []
    assert ar.hard_failures == []
    assert ar.soft_failures == []


def test_agent_result_with_hard_failure() -> None:
    trace = Trace(trace_id="trc_2", output={"message": "ok"})
    results = [
        AssertionResult(assertion_id="a1", status=STATUS_PASS, score=1.0, explanation="ok"),
        AssertionResult(assertion_id="a2", status=STATUS_HARD_FAIL, score=0.0, explanation="fail"),
        AssertionResult(assertion_id="a3", status=STATUS_SOFT_FAIL, score=0.5, explanation="soft"),
    ]
    ar = AgentResult(trace=trace, assertion_results=results)
    assert ar.passed is False
    assert ar.fail_count == 2
    assert ar.pass_count == 1
    assert len(ar.failed_assertions) == 2
    assert len(ar.hard_failures) == 1
    assert ar.hard_failures[0].assertion_id == "a2"
    assert len(ar.soft_failures) == 1
    assert ar.soft_failures[0].assertion_id == "a3"


def test_agent_result_empty() -> None:
    trace = Trace(trace_id="trc_3", output={"message": "ok"})
    ar = AgentResult(trace=trace)
    assert ar.passed is True
    assert ar.fail_count == 0
    assert ar.pass_count == 0
    assert ar.failed_assertions == []


def test_agent_result_cost_and_duration() -> None:
    trace = Trace(trace_id="trc_4", output={"message": "ok"})
    ar = AgentResult(trace=trace, total_cost=0.05, total_duration_ms=1200)
    assert ar.total_cost == 0.05
    assert ar.total_duration_ms == 1200


def test_agent_result_all_fail() -> None:
    trace = Trace(trace_id="trc_5", output={"message": "ok"})
    results = [
        AssertionResult(assertion_id="a1", status=STATUS_HARD_FAIL, score=0.0, explanation="f1"),
        AssertionResult(assertion_id="a2", status=STATUS_HARD_FAIL, score=0.0, explanation="f2"),
    ]
    ar = AgentResult(trace=trace, assertion_results=results)
    assert ar.passed is False
    assert ar.pass_count == 0
    assert ar.fail_count == 2
    assert len(ar.hard_failures) == 2
    assert ar.soft_failures == []
