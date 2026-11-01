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


def test_pass_rate_none_when_nothing_evaluated() -> None:
    result = _result(_row("judge", "not_evaluated", "simulation", 6))

    assert result.pass_rate is None
    assert result.fail_count == 0


def test_render_header_counts_only_not_evaluated() -> None:
    result = _result(_row("judge", "not_evaluated", "simulation", 6))

    assert "1 of 1 assertions not evaluated" in result.render_diagnostics()


def test_render_header_with_passing_rows_counts_failures_not_rows() -> None:
    from attest.cli.diagnostics import render_diagnostics

    result = _result(_row("ok", "pass"), _row("bad", "hard_fail"))

    block = render_diagnostics(result, include_passing=True)

    assert "1 of 2 assertions failed:" in block


@pytest.mark.asyncio
async def test_evaluate_async_enforces_and_inspect_async_does_not() -> None:
    fixture = AttestEngineFixture()
    failing = _result(_row("loop", "hard_fail"))

    async def fake_inspect(chain: Any, *, budget: float | None = None) -> AgentResult:
        return failing

    fixture.inspect_async = fake_inspect  # type: ignore[method-assign]

    assert (await fixture.inspect_async(None)) is failing
    with pytest.raises(pytest.fail.Exception):
        await fixture.evaluate_async(None)  # type: ignore[arg-type]


def test_start_rejects_engine_without_simulation_mode(monkeypatch: pytest.MonkeyPatch) -> None:
    from attest import plugin
    from attest._proto.types import InitializeResult

    class FakeManager:
        def __init__(self, **_: Any) -> None:
            self.stopped = False

        async def start(self) -> InitializeResult:
            return InitializeResult.from_dict(
                {
                    "engine_version": "0.9.0",
                    "protocol_version": 1,
                    "capabilities": ["layers_1_4"],
                    "missing": [],
                    "compatible": True,
                }
            )

        async def stop(self) -> None:
            self.stopped = True

    monkeypatch.setattr(plugin, "EngineManager", FakeManager)
    monkeypatch.setenv("ATTEST_SIMULATION", "1")
    fixture = AttestEngineFixture()

    with pytest.raises(RuntimeError, match="does not support simulation mode"):
        fixture.start()
