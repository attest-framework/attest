"""LangGraph ReAct agent with weather tools.

This module has ZERO Attest imports — it is pure application code.
"""

from __future__ import annotations

import os

from langchain_core.tools import tool
from langchain_openai import ChatOpenAI
from langgraph.prebuilt import create_react_agent


@tool
def get_weather(city: str) -> str:
    """Get the current weather for a city."""
    # Stub: in production, call a real weather API
    return f'{{"city": "{city}", "temp_f": 72, "condition": "sunny"}}'


@tool
def convert_temperature(temp_f: float) -> str:
    """Convert Fahrenheit to Celsius."""
    temp_c = round((temp_f - 32) * 5 / 9, 1)
    return f'{{"temp_c": {temp_c}, "temp_f": {temp_f}}}'


def create_agent() -> object:
    """Build and return the ReAct agent."""
    model = ChatOpenAI(model=os.environ.get("AGENT_MODEL", "gpt-4.1-mini"))
    return create_react_agent(model, tools=[get_weather, convert_temperature])


def run_agent(query: str, *, callbacks: list[object] | None = None) -> str:
    """Invoke the agent with a query string and return the final output."""
    agent = create_agent()
    config: dict[str, object] = {}
    if callbacks:
        config["callbacks"] = callbacks
    result = agent.invoke({"messages": [("user", query)]}, config=config)
    messages = result["messages"]
    return messages[-1].content if messages else ""
