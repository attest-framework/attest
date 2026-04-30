"""Tests for the FailureClass field on AssertionResult — wire-format
roundtrip and CLI diagnostics rendering.
"""

from __future__ import annotations

from attest._proto.types import (
    FAILURE_CLASS_BAD_RUBRIC,
    FAILURE_CLASS_BROKEN_CODE,
    FAILURE_CLASS_FLAKY_JUDGE,
    FAILURE_CLASS_MISSING_TRACE_DATA,
    FAILURE_CLASS_STOCHASTIC_VARIANCE,
    STATUS_HARD_FAIL,
    AssertionResult,
)


def test_failure_class_constants_match_engine() -> None:
    """Constants must agree with engine/pkg/types/failure_class.go."""
    assert FAILURE_CLASS_BROKEN_CODE == "broken_code"
    assert FAILURE_CLASS_FLAKY_JUDGE == "flaky_judge"
    assert FAILURE_CLASS_BAD_RUBRIC == "bad_rubric"
    assert FAILURE_CLASS_MISSING_TRACE_DATA == "missing_trace_data"
    assert FAILURE_CLASS_STOCHASTIC_VARIANCE == "stochastic_variance"


def test_assertion_result_roundtrip_preserves_failure_class() -> None:
    """from_dict(to_dict(x)) preserves the failure_class field."""
    src = AssertionResult(
        assertion_id="a1",
        status=STATUS_HARD_FAIL,
        score=0.0,
        explanation="schema mismatch",
        failure_class=FAILURE_CLASS_BROKEN_CODE,
    )
    serialized = src.to_dict()
    assert serialized["failure_class"] == "broken_code"
    parsed = AssertionResult.from_dict(serialized)
    assert parsed.failure_class == "broken_code"


def test_assertion_result_to_dict_omits_when_none() -> None:
    """A None failure_class must NOT appear in to_dict output."""
    r = AssertionResult(
        assertion_id="a1",
        status=STATUS_HARD_FAIL,
        score=0.0,
        explanation="x",
    )
    assert "failure_class" not in r.to_dict()


def test_assertion_result_from_dict_handles_missing_failure_class() -> None:
    """Wire payload without the new key decodes with failure_class=None."""
    parsed = AssertionResult.from_dict(
        {
            "assertion_id": "a1",
            "status": "hard_fail",
            "score": 0.0,
            "explanation": "x",
        }
    )
    assert parsed.failure_class is None


def test_diagnostics_renders_failure_class() -> None:
    """The CLI diagnostics renderer surfaces failure_class on a separate line."""
    from attest.cli.diagnostics import render_assertion_failure

    r = AssertionResult(
        assertion_id="a1",
        status=STATUS_HARD_FAIL,
        score=0.0,
        explanation="schema mismatch",
        layer=1,
        type="schema",
        failure_class=FAILURE_CLASS_BROKEN_CODE,
    )
    out = render_assertion_failure(r, color=False)
    assert "broken_code" in out, out
    assert "class:" in out, out
