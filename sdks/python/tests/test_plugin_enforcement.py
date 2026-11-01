"""evaluate() enforces the C1 aggregate; inspect() never does."""

from __future__ import annotations

from typing import Any

import pytest

from attest._proto.types import AssertionResult, Trace
from attest.plugin import AttestEngineFixture
from attest.result import AgentResult


def _row(aid: str, status: str, reason: str | None = None, layer: int = 3) -> AssertionResult:
    return AssertionResult(
        assertion_id=aid,
        status=status,
        score=1.0 if status == "pass" else 0.0,
        explanation=f"{aid} {status}",
        reason=reason,
        layer=layer,
        type="trace" if layer == 3 else "llm_judge",
        trace_node_path="trace.loop_detection" if layer == 3 else "output.message",
    )


def _result(*rows: AssertionResult) -> AgentResult:
    return AgentResult(trace=Trace(trace_id="t", output={}), assertion_results=list(rows))


def _enforce(result: AgentResult, *, strict: bool = False) -> Any:
    return AttestEngineFixture(strict=strict)._enforce(result)


def test_enforce_hard_fail_with_not_evaluated_fails_listing_both() -> None:
    result = _result(
        _row("loop", "hard_fail"), _row("judge", "not_evaluated", "short_circuited", 6)
    )

    with pytest.raises(pytest.fail.Exception) as exc:
        _enforce(result)

    msg = str(exc.value)
    assert "1 of 2 assertions failed, 1 not evaluated" in msg
    assert "trace.loop_detection" in msg
    assert "not evaluated: short_circuited" in msg


def test_enforce_only_not_evaluated_skips_with_reason() -> None:
    result = _result(_row("judge", "not_evaluated", "simulation", 6))

    with pytest.raises(pytest.skip.Exception) as exc:
        _enforce(result)

    assert str(exc.value.msg) == "not evaluated: simulation"


def test_enforce_strict_not_evaluated_fails() -> None:
    result = _result(_row("ok", "pass"), _row("judge", "not_evaluated", "provider_unavailable", 6))

    with pytest.raises(pytest.fail.Exception) as exc:
        _enforce(result, strict=True)

    assert "--attest-strict: not evaluated: provider_unavailable" in str(exc.value)


def test_enforce_soft_fail_passes() -> None:
    result = _result(_row("ok", "pass"), _row("soft", "soft_fail"))

    returned = _enforce(result)

    assert returned is result


def test_skip_reason_counts_repeated_reasons() -> None:
    result = _result(
        _row("a", "not_evaluated", "simulation", 6),
        _row("b", "not_evaluated", "simulation", 6),
        _row("c", "not_evaluated", "missing_evidence", 2),
    )

    assert result.skip_reason() == "not evaluated: simulation ×2, missing_evidence"


def test_unknown_status_is_never_a_pass() -> None:
    row = AssertionResult.from_dict(
        {"assertion_id": "x", "status": "mystery", "score": 1.0, "explanation": "?"}
    )

    assert row.status == "not_evaluated"
    assert _result(row).outcome() == "skip"
