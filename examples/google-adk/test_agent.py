"""Tests for the travel assistant agent using Google ADK."""

from __future__ import annotations

from typing import Any

import pytest

from google.adk.runners import Runner
from google.adk.sessions import InMemorySessionService

from agent import travel_assistant
from attest.adapters.google_adk import GoogleADKAdapter
from attest.expect import expect
from attest.result import AgentResult


@pytest.fixture
def runner() -> Runner:
    """Create an ADK Runner with in-memory session."""
    session_service = InMemorySessionService()
    return Runner(
        agent=travel_assistant,
        app_name="attest-travel-test",
        session_service=session_service,
    )


@pytest.mark.asyncio
async def test_travel_assistant_weather_and_flights(attest: Any, runner: Runner) -> None:
    """Test the travel assistant checks weather and searches flights.

    Verifies:
    - Agent calls get_weather before search_flights
    - Both required tools are invoked
    - Output mentions flight or weather information
    - Token usage is within bounds
    """
    adapter = GoogleADKAdapter(agent_id="travel-assistant")
    trace = await adapter.capture_async(
        runner=runner,
        user_id="test-user",
        session_id="test-session",
        message="I want to fly from NYC to Paris on 2026-03-15. What's the weather like there?",
    )
    result = AgentResult(trace=trace)

    chain = (
        expect(result)
        .required_tools(["get_weather", "search_flights"])
        .tools_called_in_order(["get_weather", "search_flights"])
        .tokens_under(5000)
        .output_contains("Paris")
    )

    attest.evaluate(chain)


@pytest.mark.asyncio
async def test_travel_assistant_weather_only(attest: Any, runner: Runner) -> None:
    """Test the travel assistant handles weather-only queries.

    Verifies:
    - Agent calls get_weather
    - search_flights is not called for weather-only queries
    - Output contains weather information
    """
    adapter = GoogleADKAdapter(agent_id="travel-assistant")
    trace = await adapter.capture_async(
        runner=runner,
        user_id="test-user",
        session_id="test-session-2",
        message="What's the weather in London right now?",
    )
    result = AgentResult(trace=trace)

    chain = (
        expect(result)
        .required_tools(["get_weather"])
        .forbidden_tools(["search_flights"])
        .output_contains("London")
    )

    attest.evaluate(chain)
