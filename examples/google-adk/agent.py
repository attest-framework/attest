"""Travel assistant agent built with Google ADK."""

from __future__ import annotations

import os

from google.adk import agents

MODEL = os.environ["ATTEST_ADK_MODEL"]


def get_weather(city: str) -> dict[str, str]:
    """Get current weather for a city.

    Args:
        city: Name of the city.

    Returns:
        Weather information including temperature and conditions.
    """
    return {
        "city": city,
        "temperature": "22C",
        "conditions": "Partly cloudy",
    }


def search_flights(origin: str, destination: str, date: str) -> dict[str, object]:
    """Search for available flights.

    Args:
        origin: Departure city or airport code.
        destination: Arrival city or airport code.
        date: Travel date in YYYY-MM-DD format.

    Returns:
        Available flights with prices.
    """
    return {
        "origin": origin,
        "destination": destination,
        "date": date,
        "flights": [
            {"airline": "AA", "price": 350, "departure": "08:00"},
            {"airline": "UA", "price": 420, "departure": "14:30"},
        ],
    }


travel_assistant = agents.LlmAgent(
    name="travel-assistant",
    model=MODEL,
    instruction=(
        "You are a helpful travel assistant. Help users plan trips by "
        "checking weather and searching for flights. Always check the "
        "weather at the destination before searching for flights."
    ),
    tools=[get_weather, search_flights],
)
