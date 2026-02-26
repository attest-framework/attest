"""Agent wrapper for capturing traces and running assertions."""

from __future__ import annotations

import asyncio
import functools
import inspect
from collections.abc import Callable
from typing import Any

from attest._proto.types import Trace
from attest.adapters import TraceAdapter
from attest.adapters.manual import ManualAdapter
from attest.result import AgentResult
from attest.trace import TraceBuilder


class Agent:
    """Wraps an agent callable for testing with Attest.

    Usage:
        agent = Agent("my-agent", fn=my_agent_fn)
        result = agent.run(user_message="hello")
    """

    def __init__(
        self,
        name: str,
        fn: Callable[..., Any] | None = None,
        adapter: TraceAdapter | None = None,
    ) -> None:
        self.name = name
        self._fn = fn
        self._adapter: TraceAdapter = adapter or ManualAdapter(agent_id=name)

    def run(self, **kwargs: Any) -> AgentResult:
        """Run the agent synchronously and capture the trace."""
        from attest.simulation._context import _active_builder

        if self._fn is None:
            raise RuntimeError("No agent function provided. Pass fn= to Agent().")

        builder = TraceBuilder(agent_id=self.name)
        builder.set_input_dict(kwargs)

        token = _active_builder.set(builder)
        try:
            output = self._fn(builder=builder, **kwargs)
        finally:
            _active_builder.reset(token)

        if isinstance(output, dict):
            builder.set_output_dict(output)
        elif output is not None:
            builder.set_output_dict({"result": output})
        else:
            builder.set_output_dict({"result": None})

        trace = builder.build()
        return AgentResult(trace=trace)

    async def arun(self, **kwargs: Any) -> AgentResult:
        """Run the agent asynchronously and capture the trace."""
        from attest.simulation._context import _active_builder

        if self._fn is None:
            raise RuntimeError("No agent function provided. Pass fn= to Agent().")

        builder = TraceBuilder(agent_id=self.name)
        builder.set_input_dict(kwargs)

        token = _active_builder.set(builder)
        try:
            if asyncio.iscoroutinefunction(self._fn):
                output = await self._fn(builder=builder, **kwargs)
            else:
                output = self._fn(builder=builder, **kwargs)
        finally:
            _active_builder.reset(token)

        if isinstance(output, dict):
            builder.set_output_dict(output)
        elif output is not None:
            builder.set_output_dict({"result": output})
        else:
            builder.set_output_dict({"result": None})

        trace = builder.build()
        return AgentResult(trace=trace)

    def with_trace(self, trace: Trace) -> AgentResult:
        """Create an AgentResult from a pre-built trace."""
        return AgentResult(trace=trace)


def agent(
    name: str, adapter: TraceAdapter | None = None
) -> Callable[[Callable[..., Any]], Callable[..., AgentResult]]:
    """Decorator to wrap a function as an Attest agent.

    Usage:
        @agent("my-agent")
        def my_agent(builder, user_message):
            builder.add_tool_call("search", args={"q": user_message})
            return {"message": "result"}

        result = my_agent(user_message="hello")
    """

    def decorator(fn: Callable[..., Any]) -> Callable[..., AgentResult]:
        wrapped = Agent(name=name, fn=fn, adapter=adapter)

        if inspect.iscoroutinefunction(fn):
            @functools.wraps(fn)
            async def async_wrapper(**kwargs: Any) -> AgentResult:
                return await wrapped.arun(**kwargs)

            async_wrapper.agent = wrapped  # type: ignore[attr-defined]
            return async_wrapper  # type: ignore[return-value]

        @functools.wraps(fn)
        def wrapper(**kwargs: Any) -> AgentResult:
            return wrapped.run(**kwargs)

        wrapper.agent = wrapped  # type: ignore[attr-defined]
        return wrapper

    return decorator
