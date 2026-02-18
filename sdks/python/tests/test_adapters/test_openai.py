"""Tests for OpenAI adapter."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from attest.adapters.openai import OpenAIAdapter


@dataclass
class MockUsage:
    total_tokens: int = 100
    prompt_tokens: int = 50
    completion_tokens: int = 50


@dataclass
class MockFunctionCall:
    name: str = "search"
    arguments: str = '{"query": "test"}'


@dataclass
class MockToolCall:
    id: str = "tc_1"
    type: str = "function"
    function: MockFunctionCall = field(default_factory=MockFunctionCall)


@dataclass
class MockMessage:
    content: str = "Hello, world!"
    role: str = "assistant"
    tool_calls: list[MockToolCall] | None = None


@dataclass
class MockChoice:
    message: MockMessage = field(default_factory=MockMessage)
    index: int = 0


@dataclass
class MockResponse:
    choices: list[MockChoice] = field(default_factory=lambda: [MockChoice()])
    model: str = "gpt-4.1"
    usage: MockUsage = field(default_factory=MockUsage)


def test_openai_basic_response() -> None:
    adapter = OpenAIAdapter(agent_id="test-agent")
    response = MockResponse()
    trace = adapter.trace_from_response(
        response, input_messages=[{"role": "user", "content": "hi"}]
    )
    assert trace.agent_id == "test-agent"
    assert trace.output["message"] == "Hello, world!"
    assert len(trace.steps) >= 1
    assert trace.steps[0].type == "llm_call"


def test_openai_with_tool_calls() -> None:
    adapter = OpenAIAdapter()
    msg = MockMessage(content="Let me search", tool_calls=[MockToolCall()])
    response = MockResponse(choices=[MockChoice(message=msg)])
    trace = adapter.trace_from_response(response)
    tool_steps = [s for s in trace.steps if s.type == "tool_call"]
    assert len(tool_steps) == 1
    assert tool_steps[0].name == "search"


def test_openai_metadata() -> None:
    adapter = OpenAIAdapter()
    response = MockResponse()
    trace = adapter.trace_from_response(response, cost_usd=0.003, latency_ms=500)
    assert trace.metadata is not None
    assert trace.metadata.total_tokens == 100
    assert trace.metadata.cost_usd == 0.003
    assert trace.metadata.latency_ms == 500


def test_openai_model_captured() -> None:
    adapter = OpenAIAdapter()
    response = MockResponse(model="gpt-4.1-mini")
    trace = adapter.trace_from_response(response)
    assert trace.metadata is not None
    assert trace.metadata.model == "gpt-4.1-mini"


def test_openai_no_input_messages() -> None:
    adapter = OpenAIAdapter()
    response = MockResponse()
    trace = adapter.trace_from_response(response)
    assert trace.input is None


def test_openai_input_messages_stored() -> None:
    adapter = OpenAIAdapter()
    msgs = [{"role": "user", "content": "hello"}]
    response = MockResponse()
    trace = adapter.trace_from_response(response, input_messages=msgs)
    assert trace.input == {"messages": msgs}
