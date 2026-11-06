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


def test_agent_string_return_is_wrapped_as_message() -> None:
    @agent("plain")
    def plain(builder: TraceBuilder, user_message: str) -> str:
        return f"echo {user_message}"

    result = plain(user_message="hi")

    assert result.trace.output == {"message": "echo hi"}


def test_agent_decorator_accepts_positional_arguments() -> None:
    @agent("positional")
    def positional(builder: TraceBuilder, user_message: str, limit: int = 1) -> str:
        return f"{user_message}:{limit}"

    result = positional("hi", 3)

    assert result.trace.output == {"message": "hi:3"}
    assert result.trace.input == {"user_message": "hi", "limit": 3}


def test_agent_decorator_rejects_excess_positional_arguments() -> None:
    @agent("positional")
    def positional(builder: TraceBuilder, user_message: str) -> str:
        return user_message

    with pytest.raises(TypeError):
        positional("a", "b")


def test_agent_decorator_accepts_variadic_positional_arguments() -> None:
    @agent("variadic")
    def fn(builder: TraceBuilder, *values: str) -> str:
        return " ".join(values)

    result = fn("a", "b")

    assert result.trace.output == {"message": "a b"}
    assert result.trace.input == {"values": ["a", "b"]}


def test_agent_decorator_accepts_mixed_variadic_arguments() -> None:
    @agent("mixed")
    def fn(builder: TraceBuilder, first: str, *rest: str, sep: str = "-", **extra: int) -> str:
        return sep.join((first, *rest)) + str(sorted(extra.items()))

    result = fn("a", "b", "c", sep="+", n=1)

    assert result.trace.output == {"message": "a+b+c[('n', 1)]"}
    assert result.trace.input == {"first": "a", "rest": ["b", "c"], "sep": "+", "n": 1}


def test_agent_decorator_builder_not_first_parameter_keeps_working() -> None:
    @agent("late-builder")
    def fn(user_message: str, builder: TraceBuilder) -> str:
        return user_message

    assert fn("hi").trace.output == {"message": "hi"}


def test_agent_arun_accepts_variadic_positional_arguments() -> None:
    import asyncio

    async def fn(builder: TraceBuilder, *values: str) -> str:
        return " ".join(values)

    result = asyncio.run(Agent("variadic-async", fn=fn).arun("a", "b"))

    assert result.trace.output == {"message": "a b"}


def test_agent_unsupported_adapter_raises_at_decoration() -> None:
    from attest.adapters import BaseAdapter

    with pytest.raises(TypeError, match="adapter="):
        agent("x", adapter=BaseAdapter())(lambda builder: "x")


def test_agent_provider_adapter_converts_return_value() -> None:
    from types import SimpleNamespace

    from attest.adapters.openai import OpenAIAdapter

    response = SimpleNamespace(
        object="chat.completion",
        model="gpt-4.1",
        usage=SimpleNamespace(total_tokens=7, prompt_tokens=3, completion_tokens=4),
        choices=[
            SimpleNamespace(
                message=SimpleNamespace(content="converted", tool_calls=None, refusal=None)
            )
        ],
    )

    @agent("provider", adapter=OpenAIAdapter(agent_id="provider"))
    def provider(builder: TraceBuilder) -> Any:
        return response

    result = provider()

    assert result.trace.output["message"] == "converted"
    assert result.trace.metadata is not None
    assert result.trace.metadata.total_tokens == 7
