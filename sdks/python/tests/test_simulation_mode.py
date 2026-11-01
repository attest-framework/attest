"""Tests for simulation mode toggle.

Verifies the simulation toggle and that the client never fabricates
results: evaluate_batch always goes to the engine, which disables
layers 5-6 in simulation mode.
"""

from __future__ import annotations

import os
from unittest.mock import AsyncMock

import pytest

from attest._proto.types import (
    STATUS_NOT_EVALUATED,
    Assertion,
    EvaluateBatchResult,
    Trace,
)
from attest.client import AttestClient
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
# AttestClient.evaluate_batch: simulation is decided by the engine
# ---------------------------------------------------------------------------


def _engine_payload() -> dict[str, object]:
    return {
        "results": [
            {
                "assertion_id": "assert_001",
                "status": "not_evaluated",
                "reason": "simulation",
                "score": 0.0,
                "explanation": "not run: simulation mode",
            }
        ],
        "total_cost": 0.0,
        "total_duration_ms": 0,
    }


class TestClientSimulationMode:
    def test_engine_payload_without_simulated_marker_defaults_false(self) -> None:
        # Older engines omit "simulated"; callers must not mistake a real run
        # for a simulated one.
        payload = {"results": [], "total_cost": 0.0, "total_duration_ms": 0}

        result = EvaluateBatchResult.from_dict(payload)

        assert result.simulated is False

    @pytest.mark.asyncio
    @pytest.mark.parametrize("enable", ["config", "env"])
    async def test_evaluate_batch_simulation_still_calls_engine(self, enable: str) -> None:
        if enable == "config":
            config(simulation=True)
            os.environ["ATTEST_SIMULATION"] = "1"  # the plugin sets this before engine start
        else:
            os.environ["ATTEST_SIMULATION"] = "1"
        engine = AsyncMock()
        client = AttestClient(engine=engine)
        client.send_request = AsyncMock(return_value=_engine_payload())  # type: ignore[method-assign]

        result = await client.evaluate_batch(_make_trace(), [_make_assertions()[0]])

        client.send_request.assert_awaited_once()
        assert result.results[0].status == STATUS_NOT_EVALUATED
        assert result.results[0].reason == "simulation"


@pytest.mark.asyncio
async def test_config_simulation_without_env_refuses_to_reach_providers() -> None:
    config(simulation=True)
    os.environ.pop("ATTEST_SIMULATION", None)
    client = AttestClient(engine=AsyncMock())
    client.send_request = AsyncMock(return_value=_engine_payload())  # type: ignore[method-assign]

    with pytest.raises(RuntimeError, match="ATTEST_SIMULATION is not in the environment"):
        await client.evaluate_batch(_make_trace(), [_make_assertions()[0]])

    client.send_request.assert_not_awaited()


def test_env_var_is_case_insensitive() -> None:
    os.environ["ATTEST_SIMULATION"] = "True"

    assert is_simulation_mode() is True
