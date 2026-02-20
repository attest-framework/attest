"""Tests for the LangChain weather agent using Attest."""

from __future__ import annotations

from agent import run_agent
from attest.adapters.langchain import LangChainAdapter
from attest.expect import expect


def test_weather_agent_uses_tools(attest) -> None:
    """Test that the weather agent calls get_weather and returns a result.

    Demonstrates Attest assertions across Layers 2-4:
    - Layer 2: Cost and latency constraints
    - Layer 3: Required tool usage
    - Layer 4: Content validation
    """
    adapter = LangChainAdapter(agent_id="weather-agent")
    with adapter.capture() as handler:
        run_agent("What is the weather in Paris in Celsius?", callbacks=[handler])

    trace = adapter.trace
    assert trace is not None

    chain = (
        expect(trace)
        # Layer 2: Performance constraints
        .cost_under(0.05)
        .latency_under(30000)
        # Layer 3: Tool usage
        .required_tools(["get_weather", "convert_temperature"])
        .forbidden_tools(["delete_data"])
        # Layer 4: Content
        .output_contains("Paris")
    )

    attest.evaluate(chain)


def test_weather_agent_token_budget(attest) -> None:
    """Test that the agent stays within token budget."""
    adapter = LangChainAdapter(agent_id="weather-agent")
    with adapter.capture() as handler:
        run_agent("What is the weather in London?", callbacks=[handler])

    trace = adapter.trace
    assert trace is not None

    chain = (
        expect(trace)
        .tokens_under(2000)
        .output_not_contains("error")
    )

    attest.evaluate(chain)
