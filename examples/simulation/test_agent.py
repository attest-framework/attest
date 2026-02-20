"""Tests demonstrating Layer 7 — Simulation with personas and mock tools.

Layer 7 enables testing agent behavior under controlled conditions:
- MockToolRegistry: intercepts tool calls with deterministic responses
- mock_tool decorator: registers mock functions for specific tools
- Personas: predefined user behavior profiles (friendly, adversarial, confused)
"""

from __future__ import annotations

from typing import Any

from agent import book_flight
from attest.expect import expect
from attest.simulation.mock_tools import MockToolRegistry, mock_tool
from attest.simulation.personas import (
    ADVERSARIAL_USER,
    CONFUSED_USER,
    COOPERATIVE_USER,
    FRIENDLY_USER,
)


def test_booking_with_mock_tools(attest) -> None:
    """Mock tool responses for deterministic testing."""
    registry = MockToolRegistry()

    # Register mock implementations
    registry.register(
        "search_flights",
        lambda origin, destination, date, **kw: {
            "flights": [
                {"id": "FL-MOCK-1", "price": 199.00, "departure": "10:00", "airline": "MockAir"},
            ],
        },
    )
    registry.register(
        "process_payment",
        lambda flight_id, amount, **kw: {
            "confirmation": "BK-MOCK-001",
            "status": "confirmed",
        },
    )

    with registry:
        result = book_flight(origin="SFO", destination="JFK", date="2026-03-15")

    chain = (
        expect(result)
        .output_contains("booked")
        .cost_under(0.01)
        .tools_called_in_order(["search_flights", "process_payment"])
        .required_tools(["search_flights", "process_payment"])
    )

    attest.evaluate(chain)


def test_booking_with_mock_tool_decorator(attest) -> None:
    """Use the @mock_tool decorator for cleaner mock definitions."""

    @mock_tool("search_flights")
    def mock_search(**kwargs: Any) -> dict[str, Any]:
        return {
            "flights": [
                {"id": "FL-DEC-1", "price": 350.00, "departure": "16:00", "airline": "DecorAir"},
            ],
        }

    @mock_tool("process_payment")
    def mock_payment(**kwargs: Any) -> dict[str, Any]:
        return {"confirmation": "BK-DEC-001", "status": "confirmed"}

    # mock_tool decorated functions carry a _mock_tool_name attribute
    registry = MockToolRegistry()
    registry.register(mock_search._mock_tool_name, mock_search)
    registry.register(mock_payment._mock_tool_name, mock_payment)

    with registry:
        result = book_flight(origin="LAX", destination="ORD", date="2026-04-01")

    chain = (
        expect(result)
        .output_contains("booked")
        .tools_called_in_order(["search_flights", "process_payment"])
    )

    attest.evaluate(chain)


def test_personas_are_configured() -> None:
    """Verify built-in personas have expected properties.

    Personas define simulated user behavior for testing how agents
    respond to different user types.
    """
    # Friendly: cooperative, clear requests
    assert FRIENDLY_USER.name == "friendly_user"
    assert FRIENDLY_USER.style == "friendly"
    assert FRIENDLY_USER.temperature == 0.7

    # Adversarial: edge cases, malformed inputs
    assert ADVERSARIAL_USER.name == "adversarial_user"
    assert ADVERSARIAL_USER.style == "adversarial"
    assert ADVERSARIAL_USER.temperature == 0.9

    # Confused: vague, contradictory instructions
    assert CONFUSED_USER.name == "confused_user"
    assert CONFUSED_USER.style == "confused"
    assert CONFUSED_USER.temperature == 0.8

    # Cooperative: precise, follows instructions
    assert COOPERATIVE_USER.name == "cooperative_user"
    assert COOPERATIVE_USER.style == "cooperative"
    assert COOPERATIVE_USER.temperature == 0.6


def test_booking_forbidden_tools(attest) -> None:
    """Verify the agent doesn't call forbidden tools."""
    result = book_flight(origin="SFO", destination="JFK", date="2026-03-15")

    chain = (
        expect(result)
        # Agent must not call dangerous tools
        .forbidden_tools(["delete_booking", "admin_override", "cancel_all"])
        # Agent must not loop on search
        .no_tool_loops("search_flights", max_repetitions=2)
    )

    attest.evaluate(chain)
