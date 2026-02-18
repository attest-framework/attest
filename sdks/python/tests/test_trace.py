"""Tests for trace builder and manual adapter."""

from __future__ import annotations

import pytest

from attest._proto.types import Step, Trace
from attest.adapters.manual import ManualAdapter
from attest.trace import TraceBuilder


def test_trace_builder_basic() -> None:
    """Build a basic trace with input, steps, and output."""
    trace = (
        TraceBuilder(agent_id="test-agent")
        .set_input(user_message="hello")
        .add_tool_call("search", args={"q": "test"}, result={"found": True})
        .set_output(message="response")
        .build()
    )
    assert trace.agent_id == "test-agent"
    assert trace.trace_id.startswith("trc_")
    assert trace.input == {"user_message": "hello"}
    assert len(trace.steps) == 1
    assert trace.steps[0].type == "tool_call"
    assert trace.steps[0].name == "search"
    assert trace.output == {"message": "response"}


def test_trace_builder_all_step_types() -> None:
    """Builder supports all step types."""
    trace = (
        TraceBuilder()
        .add_llm_call("think", result={"completion": "I'll help"})
        .add_tool_call("lookup", args={"id": "123"})
        .add_retrieval("vector_search", args={"query": "test"})
        .set_output(message="done")
        .build()
    )
    assert len(trace.steps) == 3
    assert trace.steps[0].type == "llm_call"
    assert trace.steps[1].type == "tool_call"
    assert trace.steps[2].type == "retrieval"


def test_trace_builder_with_metadata() -> None:
    """Builder sets trace metadata."""
    trace = (
        TraceBuilder()
        .set_metadata(cost_usd=0.005, latency_ms=1200, model="gpt-4.1")
        .set_output(message="done")
        .build()
    )
    assert trace.metadata is not None
    assert trace.metadata.cost_usd == 0.005
    assert trace.metadata.latency_ms == 1200


def test_trace_builder_custom_trace_id() -> None:
    """Builder allows custom trace ID."""
    trace = (
        TraceBuilder()
        .set_trace_id("trc_custom123")
        .set_output(message="ok")
        .build()
    )
    assert trace.trace_id == "trc_custom123"


def test_trace_builder_missing_output() -> None:
    """Builder raises if output not set."""
    with pytest.raises(ValueError, match="output is required"):
        TraceBuilder().build()


def test_trace_builder_add_step() -> None:
    """Builder accepts raw Step objects."""
    step = Step(type="tool_call", name="custom", args={"a": 1})
    trace = (
        TraceBuilder()
        .add_step(step)
        .set_output(message="ok")
        .build()
    )
    assert trace.steps[0] is step


def test_manual_adapter_capture() -> None:
    """ManualAdapter.capture() executes builder function."""
    adapter = ManualAdapter(agent_id="my-agent")

    def build_trace(b: TraceBuilder) -> None:
        b.add_tool_call("search", result={"found": True})
        b.set_output(message="result")

    trace = adapter.capture(build_trace)
    assert trace.agent_id == "my-agent"
    assert len(trace.steps) == 1


def test_manual_adapter_create_builder() -> None:
    """ManualAdapter.create_builder() returns a TraceBuilder."""
    adapter = ManualAdapter(agent_id="my-agent")
    builder = adapter.create_builder()
    trace = builder.set_output(message="ok").build()
    assert trace.agent_id == "my-agent"


def test_trace_to_dict_round_trip() -> None:
    """Trace can be serialized to dict and back."""
    trace = (
        TraceBuilder(agent_id="test")
        .set_input(user_message="hi")
        .add_tool_call("search", args={"q": "test"}, result={"found": True})
        .set_output(message="done")
        .set_metadata(cost_usd=0.01)
        .build()
    )
    d = trace.to_dict()
    trace2 = Trace.from_dict(d)
    assert trace2.agent_id == trace.agent_id
    assert trace2.output == trace.output
    assert len(trace2.steps) == len(trace.steps)
