"""Tests for Agent wrapper and decorator."""

from __future__ import annotations

from typing import Any

import pytest

from attest._proto.types import Trace
from attest.agent import Agent, agent
from attest.trace import TraceBuilder


def test_agent_run() -> None:
    def my_fn(builder: TraceBuilder, **kwargs: Any) -> dict[str, Any]:
        builder.add_tool_call("search", args={"q": kwargs.get("query", "")})
        return {"message": f"Found results for: {kwargs.get('query', '')}"}

    a = Agent("test-agent", fn=my_fn)
    result = a.run(query="test")
    assert result.trace.agent_id == "test-agent"
    assert result.trace.output["message"] == "Found results for: test"
    assert len(result.trace.steps) == 1


def test_agent_no_fn() -> None:
    a = Agent("test")
    with pytest.raises(RuntimeError, match="No agent function"):
        a.run()


def test_agent_with_trace() -> None:
    trace = Trace(trace_id="trc_1", output={"message": "ok"})
    a = Agent("test")
    result = a.with_trace(trace)
    assert result.trace.trace_id == "trc_1"


def test_agent_decorator() -> None:
    @agent("decorated-agent")
    def my_agent(builder: TraceBuilder, **kwargs: Any) -> dict[str, Any]:
        builder.add_tool_call("fetch")
        return {"response": "done"}

    result = my_agent(user_input="hello")
    assert result.trace.agent_id == "decorated-agent"
    assert result.trace.output["response"] == "done"
    assert len(result.trace.steps) == 1


def test_agent_decorator_has_agent_attr() -> None:
    @agent("my-agent")
    def my_agent(builder: TraceBuilder, **kwargs: Any) -> dict[str, Any]:
        return {"result": "ok"}

    assert hasattr(my_agent, "agent")
    assert isinstance(my_agent.agent, Agent)


@pytest.mark.asyncio
async def test_agent_decorator_async_fn() -> None:
    """Decorator wraps async functions with an async wrapper."""
    import asyncio

    @agent("async-agent")
    async def my_async_agent(builder: TraceBuilder, **kwargs: Any) -> dict[str, Any]:
        await asyncio.sleep(0)
        return {"response": "async-done"}

    import inspect
    assert inspect.iscoroutinefunction(my_async_agent)

    result = await my_async_agent(user_input="hello")
    assert result.trace.agent_id == "async-agent"
    assert result.trace.output["response"] == "async-done"


@pytest.mark.asyncio
async def test_agent_decorator_async_has_agent_attr() -> None:
    @agent("async-agent-2")
    async def my_async_agent(builder: TraceBuilder, **kwargs: Any) -> dict[str, Any]:
        return {"result": "ok"}

    assert hasattr(my_async_agent, "agent")
    assert isinstance(my_async_agent.agent, Agent)
