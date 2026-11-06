"""Agent wrapper for capturing traces and running assertions."""

from __future__ import annotations

import asyncio
import functools
import inspect
from collections.abc import Callable
from typing import Any

from attest._proto.types import Trace
from attest.adapters import BaseAdapter
from attest.adapters._base import BaseProviderAdapter
from attest.adapters.manual import ManualAdapter
from attest.result import AgentResult
from attest.trace import TraceBuilder


def _bind_positional(
    fn: Callable[..., Any], args: tuple[Any, ...], kwargs: dict[str, Any]
) -> dict[str, Any]:
    """Name positional ``args`` after the parameters of ``fn`` that follow ``builder``."""
    if not args:
        return kwargs
    names = [
        p.name
        for p in inspect.signature(fn).parameters.values()
        if p.name != "builder"
        and p.kind in (inspect.Parameter.POSITIONAL_ONLY, inspect.Parameter.POSITIONAL_OR_KEYWORD)
    ]
    if len(args) > len(names):
        raise TypeError(
            f"{getattr(fn, '__name__', 'agent')}() takes {len(names)} positional "
            f"argument(s) besides builder but {len(args)} were given"
        )
    bound = dict(zip(names, args, strict=False))
    clash = bound.keys() & kwargs.keys()
    if clash:
        raise TypeError(f"multiple values for argument(s): {', '.join(sorted(clash))}")
    return {**bound, **kwargs}


def _record_output(builder: TraceBuilder, output: Any, adapter: BaseAdapter | None) -> None:
    """Store the agent's return value as the trace output."""
    if isinstance(output, dict):
        builder.set_output_dict(output)
    elif isinstance(output, str):
        builder.set_output_dict({"message": output})
    elif isinstance(adapter, BaseProviderAdapter) and output is not None:
        converted = adapter.trace_from_response(output)
        for step in converted.steps:
            builder.add_step(step)
        builder.set_output_dict(converted.output)
        meta = converted.metadata
        if meta is not None:
            builder.set_metadata(
                total_tokens=meta.total_tokens,
                cost_usd=meta.cost_usd,
                latency_ms=meta.latency_ms,
                model=meta.model,
                timestamp=meta.timestamp,
            )
    else:
        builder.set_output_dict({"result": output})


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
        adapter: BaseAdapter | None = None,
    ) -> None:
        self.name = name
        self._fn = fn
        if adapter is not None and not isinstance(adapter, (BaseProviderAdapter, ManualAdapter)):
            raise TypeError(
                f"adapter= must be a provider adapter (converts the return value) or a "
                f"ManualAdapter; {type(adapter).__name__} cannot convert an agent's return value"
            )
        self._adapter: BaseAdapter = adapter or ManualAdapter(agent_id=name)

    def run(self, *args: Any, **kwargs: Any) -> AgentResult:
        """Run the agent synchronously and capture the trace."""
        from attest.simulation._context import _active_builder

        if self._fn is None:
            raise RuntimeError("No agent function provided. Pass fn= to Agent().")

        kwargs = _bind_positional(self._fn, args, kwargs)
        builder = TraceBuilder(agent_id=self.name)
        builder.set_input_dict(kwargs)

        token = _active_builder.set(builder)
        try:
            output = self._fn(builder=builder, **kwargs)
        finally:
            _active_builder.reset(token)

        _record_output(builder, output, self._adapter)

        trace = builder.build()
        return AgentResult(trace=trace)

    async def arun(self, *args: Any, **kwargs: Any) -> AgentResult:
        """Run the agent asynchronously and capture the trace."""
        from attest.simulation._context import _active_builder

        if self._fn is None:
            raise RuntimeError("No agent function provided. Pass fn= to Agent().")

        kwargs = _bind_positional(self._fn, args, kwargs)
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

        _record_output(builder, output, self._adapter)

        trace = builder.build()
        return AgentResult(trace=trace)

    def with_trace(self, trace: Trace) -> AgentResult:
        """Create an AgentResult from a pre-built trace."""
        return AgentResult(trace=trace)


def agent(
    name: str, adapter: BaseAdapter | None = None
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
        def wrapper(*args: Any, **kwargs: Any) -> AgentResult:
            return wrapped.run(*args, **kwargs)

        wrapper.agent = wrapped  # type: ignore[attr-defined]
        return wrapper

    return decorator
