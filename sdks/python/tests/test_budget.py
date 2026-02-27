"""P11 — Budget enforcement tests.

Tests that budget limits propagate correctly through the plugin's
evaluate path and that budget exceeded triggers pytest.fail.
"""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from attest._proto.types import (
    Assertion,
    AssertionResult,
    EvaluateBatchResult,
    Trace,
    STATUS_PASS,
    STATUS_SOFT_FAIL,
)
from attest.expect import ExpectChain
from attest.plugin import AttestEngineFixture
from attest.result import AgentResult


def _make_trace() -> Trace:
    """Create a minimal Trace for testing."""
    return Trace(
        trace_id="trc_budget_test",
        output={"message": "hello"},
    )


def _make_chain() -> ExpectChain:
    """Create a minimal ExpectChain backed by an AgentResult."""
    trace = _make_trace()
    result = AgentResult(trace=trace)
    chain = ExpectChain(result)
    return chain


def _make_batch_result(cost: float, soft_fails: int = 0) -> EvaluateBatchResult:
    """Create an EvaluateBatchResult with a given total cost."""
    results = [
        AssertionResult(
            assertion_id="a1",
            status=STATUS_SOFT_FAIL if soft_fails > 0 else STATUS_PASS,
            score=1.0,
            explanation="test",
            cost=cost,
            duration_ms=10,
        ),
    ]
    return EvaluateBatchResult(
        results=results,
        total_cost=cost,
        total_duration_ms=10,
    )


class TestBudgetEnforcement:
    """Budget limit enforcement in AttestEngineFixture._process_result."""

    def setup_method(self) -> None:
        """Reset session cost before each test."""
        import attest.plugin
        attest.plugin._session_cost = 0.0
        attest.plugin._session_soft_failures = 0

    def _make_fixture(self) -> AttestEngineFixture:
        """Create an AttestEngineFixture without real engine."""
        fixture = AttestEngineFixture.__new__(AttestEngineFixture)
        fixture._engine_path = None
        fixture._log_level = "warn"
        fixture._manager = None
        fixture._client = None
        fixture._loop = None
        fixture._thread = None
        return fixture

    def test_process_result_accumulates_cost(self) -> None:
        import attest.plugin
        fixture = self._make_fixture()
        chain = _make_chain()
        result = _make_batch_result(cost=0.005)

        fixture._process_result(chain, result, budget=None)
        assert attest.plugin._session_cost == pytest.approx(0.005)

        fixture._process_result(chain, result, budget=None)
        assert attest.plugin._session_cost == pytest.approx(0.010)

    def test_process_result_no_budget_never_fails(self) -> None:
        import attest.plugin
        fixture = self._make_fixture()
        chain = _make_chain()
        result = _make_batch_result(cost=999.0)

        # No budget set — should not raise even with huge cost
        agent_result = fixture._process_result(chain, result, budget=None)
        assert agent_result is not None

    def test_process_result_under_budget_passes(self) -> None:
        fixture = self._make_fixture()
        chain = _make_chain()
        result = _make_batch_result(cost=0.001)

        agent_result = fixture._process_result(chain, result, budget=1.0)
        assert agent_result.total_cost == 0.001

    def test_process_result_exceeds_budget_raises(self) -> None:
        fixture = self._make_fixture()
        chain = _make_chain()
        result = _make_batch_result(cost=0.05)

        with pytest.raises(pytest.fail.Exception, match="budget exceeded"):
            fixture._process_result(chain, result, budget=0.01)

    def test_process_result_cumulative_budget_exceeded(self) -> None:
        """Budget is checked against cumulative session cost, not single eval cost."""
        fixture = self._make_fixture()
        chain = _make_chain()
        small_result = _make_batch_result(cost=0.006)

        # First call: 0.006 < 0.01 budget
        fixture._process_result(chain, small_result, budget=0.01)

        # Second call: cumulative 0.012 > 0.01 budget
        with pytest.raises(pytest.fail.Exception, match="budget exceeded"):
            fixture._process_result(chain, small_result, budget=0.01)

    def test_process_result_tracks_soft_failures(self) -> None:
        import attest.plugin
        fixture = self._make_fixture()
        chain = _make_chain()
        result = _make_batch_result(cost=0.0, soft_fails=1)

        fixture._process_result(chain, result, budget=None)
        assert attest.plugin._session_soft_failures == 1

    def test_budget_message_includes_cost_and_limit(self) -> None:
        fixture = self._make_fixture()
        chain = _make_chain()
        result = _make_batch_result(cost=0.05)

        with pytest.raises(pytest.fail.Exception) as exc_info:
            fixture._process_result(chain, result, budget=0.01)

        msg = str(exc_info.value)
        assert "$0.050000" in msg
        assert "$0.010000" in msg


class TestBudgetWithSimulation:
    """Budget enforcement works in simulation mode (zero cost)."""

    def setup_method(self) -> None:
        import attest.plugin
        attest.plugin._session_cost = 0.0
        attest.plugin._session_soft_failures = 0

    def test_simulation_zero_cost_under_any_budget(self) -> None:
        fixture = AttestEngineFixture.__new__(AttestEngineFixture)
        fixture._engine_path = None
        fixture._log_level = "warn"
        fixture._manager = None
        fixture._client = None
        fixture._loop = None
        fixture._thread = None

        chain = _make_chain()
        result = _make_batch_result(cost=0.0)

        agent_result = fixture._process_result(chain, result, budget=0.0001)
        assert agent_result.total_cost == 0.0


class TestBudgetCLIOption:
    """Tests for --attest-budget pytest CLI option."""

    def test_budget_option_registered(self) -> None:
        """pytest_addoption registers --attest-budget."""
        from attest.plugin import pytest_addoption

        parser = MagicMock()
        group = MagicMock()
        parser.getgroup.return_value = group

        pytest_addoption(parser)

        call_args_list = group.addoption.call_args_list
        budget_calls = [c for c in call_args_list if "--attest-budget" in c.args]
        assert len(budget_calls) == 1
        assert budget_calls[0].kwargs.get("type") is float
