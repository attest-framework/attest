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


def _call_arguments(
    fn: Callable[..., Any],
    builder: TraceBuilder,
    args: tuple[Any, ...],
    kwargs: dict[str, Any],
) -> tuple[tuple[Any, ...], dict[str, Any], dict[str, Any]]:
    """Inject ``builder`` and return ``(call_args, call_kwargs, recorded_input)``.

    ``builder`` goes first positionally when it is the first parameter (so ``*args`` agents keep
    native positional semantics) and by keyword otherwise.
    """
    sig = inspect.signature(fn)
    params = list(sig.parameters.values())
    first = params[0] if params else None
    builder_first = (
        first is not None
        and first.name == "builder"
        and first.kind
        in (
            inspect.Parameter.POSITIONAL_ONLY,
            inspect.Parameter.POSITIONAL_OR_KEYWORD,
        )
    )
    if builder_first:
        call_args: tuple[Any, ...] = (builder, *args)
        call_kwargs = dict(kwargs)
    else:
        call_args = args
        call_kwargs = {**kwargs, "builder": builder}
    bound = sig.bind(*call_args, **call_kwargs).arguments
    recorded: dict[str, Any] = {}
    for name, value in bound.items():
        if name == "builder":
            continue
        kind = sig.parameters[name].kind
        if kind is inspect.Parameter.VAR_KEYWORD:
            recorded.update(value)
        elif kind is inspect.Parameter.VAR_POSITIONAL:
            recorded[name] = list(value)
        else:
            recorded[name] = value
    return call_args, call_kwargs, recorded


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

        builder = TraceBuilder(agent_id=self.name)
        call_args, call_kwargs, recorded = _call_arguments(self._fn, builder, args, kwargs)
        builder.set_input_dict(recorded)

        token = _active_builder.set(builder)
        try:
            output = self._fn(*call_args, **call_kwargs)
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

        builder = TraceBuilder(agent_id=self.name)
        call_args, call_kwargs, recorded = _call_arguments(self._fn, builder, args, kwargs)
        builder.set_input_dict(recorded)

        token = _active_builder.set(builder)
        try:
            if asyncio.iscoroutinefunction(self._fn):
                output = await self._fn(*call_args, **call_kwargs)
            else:
                output = self._fn(*call_args, **call_kwargs)
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
            async def async_wrapper(*args: Any, **kwargs: Any) -> AgentResult:
                return await wrapped.arun(*args, **kwargs)

            async_wrapper.agent = wrapped  # type: ignore[attr-defined]
            return async_wrapper  # type: ignore[return-value]

        @functools.wraps(fn)
        def wrapper(*args: Any, **kwargs: Any) -> AgentResult:
            return wrapped.run(*args, **kwargs)

        wrapper.agent = wrapped  # type: ignore[attr-defined]
        return wrapper

    return decorator
