"""Booking agent — demonstrates Layer 7 (Simulation) with personas and mock tools."""

from __future__ import annotations

from typing import Any

from attest.agent import agent
from attest.trace import TraceBuilder


@agent("booking-agent")
def book_flight(
    builder: TraceBuilder,
    origin: str,
    destination: str,
    date: str,
) -> dict[str, Any]:
    """Book a flight using search and payment tools.

    In simulation mode, MockToolRegistry intercepts tool calls and returns
    controlled responses — no real APIs are called.
    """
    # Step 1: Search for flights
    builder.add_tool_call(
        name="search_flights",
        args={"origin": origin, "destination": destination, "date": date},
        result={
            "flights": [
                {"id": "FL-100", "price": 299.00, "departure": "08:00", "airline": "AirTest"},
                {"id": "FL-200", "price": 450.00, "departure": "14:30", "airline": "SkyMock"},
            ],
        },
    )

    # Step 2: LLM selects the best option
    builder.add_llm_call(
        name="gpt-4.1",
        args={
            "model": "gpt-4.1",
            "messages": [{"role": "user", "content": f"Book cheapest flight {origin}->{destination}"}],
        },
        result={"completion": "I'll book FL-100 at $299.00 departing at 08:00."},
    )

    # Step 3: Process payment
    builder.add_tool_call(
        name="process_payment",
        args={"flight_id": "FL-100", "amount": 299.00},
        result={"confirmation": "BK-12345", "status": "confirmed"},
    )

    builder.set_metadata(total_tokens=200, cost_usd=0.005, latency_ms=2000, model="gpt-4.1")

    return {
        "message": (
            "Flight booked successfully. Confirmation: BK-12345. "
            "FL-100 departing 08:00 from SFO to JFK at $299.00."
        ),
        "structured": {
            "confirmation": "BK-12345",
            "flight_id": "FL-100",
            "price": 299.00,
            "status": "confirmed",
        },
    }
