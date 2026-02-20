"""Tests for simulation mode toggle.

Verifies that when simulation mode is active, evaluate_batch returns
deterministic results without spawning the engine or making API calls.
"""

from __future__ import annotations

import os
from unittest.mock import AsyncMock

import pytest

from attest._proto.types import (
    STATUS_PASS,
    Assertion,
    EvaluateBatchResult,
    Trace,
)
from attest.client import AttestClient, _simulation_evaluate_batch
from attest.config import config, is_simulation_mode, reset
from attest.trace import TraceBuilder


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(autouse=True)
def _reset_config() -> None:
    """Reset simulation config after each test."""
    yield  # type: ignore[misc]
    reset()
    os.environ.pop("ATTEST_SIMULATION", None)


def _make_trace() -> Trace:
    builder = TraceBuilder(agent_id="test-agent")
    builder.add_llm_call("completion", result={"text": "hello"})
    builder.set_output(message="hello")
    return builder.build()


def _make_assertions() -> list[Assertion]:
    return [
        Assertion(
            assertion_id="assert_001",
            type="content",
            spec={"target": "output.message", "check": "contains", "value": "hello"},
        ),
        Assertion(
            assertion_id="assert_002",
            type="constraint",
            spec={"field": "metadata.cost_usd", "operator": "lte", "value": 0.01},
        ),
    ]


# ---------------------------------------------------------------------------
# config() API tests
# ---------------------------------------------------------------------------


class TestConfig:
    def test_simulation_default_off(self) -> None:
        assert is_simulation_mode() is False

    def test_config_enables_simulation(self) -> None:
        result = config(simulation=True)
        assert result["simulation"] is True
        assert is_simulation_mode() is True

    def test_config_disables_simulation(self) -> None:
        config(simulation=True)
        config(simulation=False)
        assert is_simulation_mode() is False

    def test_config_returns_current_state(self) -> None:
        result = config()
        assert result["simulation"] is False
        config(simulation=True)
        result = config()
        assert result["simulation"] is True

    def test_env_var_enables_simulation(self) -> None:
        os.environ["ATTEST_SIMULATION"] = "1"
        assert is_simulation_mode() is True

    def test_env_var_true_enables_simulation(self) -> None:
        os.environ["ATTEST_SIMULATION"] = "true"
        assert is_simulation_mode() is True

    def test_env_var_yes_enables_simulation(self) -> None:
        os.environ["ATTEST_SIMULATION"] = "yes"
        assert is_simulation_mode() is True

    def test_env_var_zero_does_not_enable(self) -> None:
        os.environ["ATTEST_SIMULATION"] = "0"
        assert is_simulation_mode() is False

    def test_reset_clears_simulation(self) -> None:
        config(simulation=True)
        reset()
        assert is_simulation_mode() is False


# ---------------------------------------------------------------------------
# _simulation_evaluate_batch tests
# ---------------------------------------------------------------------------


class TestSimulationEvaluateBatch:
    def test_returns_pass_for_all_assertions(self) -> None:
        assertions = _make_assertions()
        result = _simulation_evaluate_batch(assertions)

        assert isinstance(result, EvaluateBatchResult)
        assert len(result.results) == 2

        for r in result.results:
            assert r.status == STATUS_PASS
            assert r.score == 1.0
            assert r.cost == 0.0
            assert r.duration_ms == 0

    def test_assertion_ids_preserved(self) -> None:
        assertions = _make_assertions()
        result = _simulation_evaluate_batch(assertions)

        assert result.results[0].assertion_id == "assert_001"
        assert result.results[1].assertion_id == "assert_002"

    def test_explanation_includes_simulation_marker(self) -> None:
        assertions = _make_assertions()
        result = _simulation_evaluate_batch(assertions)

        for r in result.results:
            assert "[simulation]" in r.explanation

    def test_total_cost_is_zero(self) -> None:
        assertions = _make_assertions()
        result = _simulation_evaluate_batch(assertions)
        assert result.total_cost == 0.0
        assert result.total_duration_ms == 0

    def test_empty_assertions(self) -> None:
        result = _simulation_evaluate_batch([])
        assert result.results == []
        assert result.total_cost == 0.0


# ---------------------------------------------------------------------------
# AttestClient.evaluate_batch simulation mode integration
# ---------------------------------------------------------------------------


class TestClientSimulationMode:
    @pytest.mark.asyncio
    async def test_evaluate_batch_skips_engine_in_simulation(self) -> None:
        config(simulation=True)

        engine = AsyncMock()
        client = AttestClient(engine=engine)

        trace = _make_trace()
        assertions = _make_assertions()

        result = await client.evaluate_batch(trace, assertions)

        assert isinstance(result, EvaluateBatchResult)
        assert len(result.results) == 2
        assert all(r.status == STATUS_PASS for r in result.results)

        # Engine send_request should NOT have been called
        engine.send_request.assert_not_called()

    @pytest.mark.asyncio
    async def test_evaluate_batch_uses_engine_without_simulation(self) -> None:
        # Simulation off — should call through to engine
        engine = AsyncMock()
        engine.send_request.return_value = {
            "results": [
                {
                    "assertion_id": "assert_001",
                    "status": "pass",
                    "score": 1.0,
                    "explanation": "ok",
                }
            ],
            "total_cost": 0.001,
            "total_duration_ms": 50,
        }

        client = AttestClient(engine=engine)
        trace = _make_trace()
        assertions = [_make_assertions()[0]]

        result = await client.evaluate_batch(trace, assertions)

        assert isinstance(result, EvaluateBatchResult)
        engine.send_request.assert_called_once()

    @pytest.mark.asyncio
    async def test_simulation_via_env_var(self) -> None:
        os.environ["ATTEST_SIMULATION"] = "1"

        engine = AsyncMock()
        client = AttestClient(engine=engine)

        trace = _make_trace()
        assertions = _make_assertions()

        result = await client.evaluate_batch(trace, assertions)

        assert len(result.results) == 2
        engine.send_request.assert_not_called()
