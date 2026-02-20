"""Context manager for delegating work to sub-agents."""

from __future__ import annotations

from collections.abc import Generator
from contextlib import contextmanager

from attest._proto.types import STEP_AGENT_CALL, Step
from attest.simulation._context import _active_builder
from attest.trace import TraceBuilder


@contextmanager
def delegate(agent_id: str) -> Generator[TraceBuilder, None, None]:
    """Context manager for delegating to a sub-agent.

    Creates a child TraceBuilder linked to the parent via parent_trace_id.
    On exit, adds an agent_call step to the parent with the child's built trace.

    Usage:
        with attest.delegate("sub-agent") as child:
            child.add_tool_call("search", args={"q": "test"})
            child.set_output(message="found it")
    """
    parent = _active_builder.get(None)
    if parent is None:
        raise RuntimeError(
            "delegate() must be used within an Agent.run() context. "
            "No active TraceBuilder found."
        )

    child = TraceBuilder(agent_id=agent_id)
    child.set_parent_trace_id(parent._trace_id)

    token = _active_builder.set(child)
    try:
        yield child
    finally:
        _active_builder.reset(token)

    child_trace = child.build()
    parent.add_step(Step(
        type=STEP_AGENT_CALL,
        name=agent_id,
        args=None,
        result=child_trace.output,
        sub_trace=child_trace,
    ))
