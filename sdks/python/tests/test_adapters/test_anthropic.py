"""Tests for Anthropic adapter."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from attest.adapters.anthropic import AnthropicAdapter


@dataclass
class MockUsage:
    input_tokens: int = 40
    output_tokens: int = 60


@dataclass
class MockTextBlock:
    type: str = "text"
    text: str = "Hello from Claude."


@dataclass
class MockToolUseBlock:
    type: str = "tool_use"
    id: str = "tu_1"
    name: str = "calculator"
    input: dict[str, Any] = field(default_factory=lambda: {"expression": "2+2"})


@dataclass
class MockResponse:
    content: list[Any] = field(default_factory=lambda: [MockTextBlock()])
    model: str = "claude-opus-4-6"
    usage: MockUsage = field(default_factory=MockUsage)


def test_anthropic_basic_response() -> None:
    adapter = AnthropicAdapter(agent_id="claude-agent")
    response = MockResponse()
    trace = adapter.trace_from_response(
        response, input_messages=[{"role": "user", "content": "hi"}]
    )
    assert trace.agent_id == "claude-agent"
    assert trace.output["message"] == "Hello from Claude."
    llm_steps = [s for s in trace.steps if s.type == "llm_call"]
    assert len(llm_steps) == 1


def test_anthropic_with_tool_use() -> None:
    adapter = AnthropicAdapter()
    response = MockResponse(content=[MockTextBlock(text="Computing..."), MockToolUseBlock()])
    trace = adapter.trace_from_response(response)
    tool_steps = [s for s in trace.steps if s.type == "tool_call"]
    assert len(tool_steps) == 1
    assert tool_steps[0].name == "calculator"
    assert tool_steps[0].args == {"expression": "2+2"}


def test_anthropic_metadata() -> None:
    adapter = AnthropicAdapter()
    response = MockResponse()
    trace = adapter.trace_from_response(response, cost_usd=0.005, latency_ms=800)
    assert trace.metadata is not None
    assert trace.metadata.total_tokens == 100  # 40 + 60
    assert trace.metadata.cost_usd == 0.005
    assert trace.metadata.latency_ms == 800


def test_anthropic_model_captured() -> None:
    adapter = AnthropicAdapter()
    response = MockResponse(model="claude-sonnet-4-6")
    trace = adapter.trace_from_response(response)
    assert trace.metadata is not None
    assert trace.metadata.model == "claude-sonnet-4-6"


def test_anthropic_multi_text_blocks_joined() -> None:
    adapter = AnthropicAdapter()
    response = MockResponse(content=[
        MockTextBlock(text="Part 1"),
        MockTextBlock(text="Part 2"),
    ])
    trace = adapter.trace_from_response(response)
    assert trace.output["message"] == "Part 1\nPart 2"


def test_anthropic_no_input_messages() -> None:
    adapter = AnthropicAdapter()
    response = MockResponse()
    trace = adapter.trace_from_response(response)
    assert trace.input is None
