from __future__ import annotations

import json
from typing import Any

from attest._proto.types import (
    STATUS_HARD_FAIL,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
    TYPE_CONTENT,
    TYPE_LLM_JUDGE,
    AssertionResult,
    JudgeMetadata,
    Trace,
)
from attest.cli.diagnostics import (
    render_assertion_failure,
    render_diagnostics,
    render_summary,
)
from attest.result import AgentResult


def _make_agent_result(*assertions: AssertionResult) -> AgentResult:
    trace = Trace(
        trace_id="trc_test",
        output={"message": "hello"},
    )
    return AgentResult(
        trace=trace,
        assertion_results=list(assertions),
        total_cost=sum(r.cost for r in assertions),
        total_duration_ms=sum(r.duration_ms for r in assertions),
    )


def test_render_assertion_failure_includes_all_diagnostic_fields() -> None:
    r = AssertionResult(
        assertion_id="judge_001",
        status=STATUS_HARD_FAIL,
        score=0.4,
        explanation="weak rationale",
        cost=0.012,
        duration_ms=1820,
        layer=6,
        type=TYPE_LLM_JUDGE,
        trace_node_path="output.answer",
        expected="judge_score >= 0.80",
        actual="judge_score=0.40",
        suggested_action="Calibrate judge or refine rubric.",
        threshold_source="static",
        judge_metadata=JudgeMetadata(
            model="gpt-4.1",
            rubric_name="correctness",
            rubric_version="v3",
            prompt_hash="abcd1234",
            sample_scores=[0.4, 0.4, 0.6],
            score_mean=0.466,
            score_stddev=0.115,
            high_variance=False,
        ),
    )
    block = render_assertion_failure(r)

    for needle in [
        "FAIL",
        "judge_001",
        "L6 llm_judge",
        "trace path: output.answer",
        "expected:   judge_score >= 0.80",
        "actual:     judge_score=0.40",
        "judge:      gpt-4.1 / correctness @ v3",
        "prompt:     #abcd1234",
        "samples:    [0.40, 0.40, 0.60] mean=0.47 stddev=0.12",
        "hint:       Calibrate judge",
        "cost/lat:",
    ]:
        assert needle in block, f"missing {needle!r} in block:\n{block}"


def test_render_assertion_failure_omits_threshold_when_static() -> None:
    r = AssertionResult(
        assertion_id="a",
        status=STATUS_PASS,
        score=1.0,
        explanation="ok",
        threshold_source="static",
    )
    block = render_assertion_failure(r)
    assert "threshold:" not in block


def test_render_assertion_failure_shows_threshold_when_dynamic_unavailable() -> None:
    r = AssertionResult(
        assertion_id="dyn",
        status=STATUS_SOFT_FAIL,
        score=0.5,
        explanation="dynamic unavailable",
        threshold_source="dynamic_unavailable",
    )
    block = render_assertion_failure(r)
    assert "threshold:  dynamic_unavailable" in block


def test_render_summary_one_line() -> None:
    result = _make_agent_result(
        AssertionResult(
            assertion_id="ok",
            status=STATUS_PASS,
            score=1.0,
            explanation="",
            cost=0.01,
            duration_ms=10,
        ),
        AssertionResult(
            assertion_id="bad",
            status=STATUS_HARD_FAIL,
            score=0.0,
            explanation="missing",
            type=TYPE_CONTENT,
            layer=4,
            duration_ms=2,
        ),
    )
    summary = render_summary(result)
    assert "PASS 1" in summary
    assert "FAIL 1" in summary
    assert "cost $0.01" in summary
    assert "dur 12ms" in summary


def test_render_diagnostics_failures_only_by_default() -> None:
    result = _make_agent_result(
        AssertionResult(assertion_id="ok", status=STATUS_PASS, score=1.0, explanation=""),
        AssertionResult(
            assertion_id="bad",
            status=STATUS_HARD_FAIL,
            score=0.0,
            explanation="missing",
            type=TYPE_CONTENT,
            layer=4,
            trace_node_path="output.message",
            expected='contains "thanks"',
            actual="hello",
        ),
    )
    block = render_diagnostics(result)
    assert "1 of 2 assertions failed" in block
    assert "bad" in block
    assert "ok" not in block


def test_render_diagnostics_includes_passing_when_requested() -> None:
    result = _make_agent_result(
        AssertionResult(assertion_id="ok", status=STATUS_PASS, score=1.0, explanation=""),
    )
    block = render_diagnostics(result, include_passing=True)
    assert "ok" in block


def test_render_diagnostics_no_failures_returns_dim_message() -> None:
    result = _make_agent_result(
        AssertionResult(assertion_id="ok", status=STATUS_PASS, score=1.0, explanation=""),
    )
    block = render_diagnostics(result)
    assert "(no failures)" in block


def test_color_mode_emits_ansi() -> None:
    r = AssertionResult(
        assertion_id="a",
        status=STATUS_HARD_FAIL,
        score=0.0,
        explanation="missing",
    )
    block = render_assertion_failure(r, color=True)
    # Just confirm SOME ansi escape ended up in the output.
    assert "\x1b[" in block


def test_agent_result_render_diagnostics_method() -> None:
    """AgentResult.render_diagnostics is the documented helper for asserts."""
    result = _make_agent_result(
        AssertionResult(
            assertion_id="bad",
            status=STATUS_HARD_FAIL,
            score=0.0,
            explanation="missing",
        ),
    )
    msg = result.render_diagnostics()
    assert "Attest diagnostic" in msg
    assert "bad" in msg


def test_assertion_result_diagnostic_fields_serialise_to_json() -> None:
    """Diagnostic fields must round-trip through JSON serialisation."""
    r = AssertionResult(
        assertion_id="a",
        status=STATUS_HARD_FAIL,
        score=0.0,
        explanation="x",
        layer=4,
        type=TYPE_CONTENT,
        trace_node_path="output",
        expected="contains thanks",
        actual="hello",
        suggested_action="prompt update",
    )
    payload: dict[str, Any] = r.to_dict()
    blob = json.dumps(payload)
    assert "trace_node_path" in blob
    assert "suggested_action" in blob
    restored = AssertionResult.from_dict(json.loads(blob))
    assert restored.trace_node_path == "output"
    assert restored.suggested_action == "prompt update"
