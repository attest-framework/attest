"""Tests for Gemini adapter."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from attest.adapters.gemini import GeminiAdapter


@dataclass
class MockFunctionCall:
    name: str = "web_search"
    args: dict[str, Any] = field(default_factory=lambda: {"query": "test"})


@dataclass
class MockPart:
    text: str = "Gemini response text."
    function_call: MockFunctionCall | None = None


@dataclass
class MockContent:
    parts: list[MockPart] = field(default_factory=lambda: [MockPart()])


@dataclass
class MockCandidate:
    content: MockContent = field(default_factory=MockContent)


@dataclass
class MockResponse:
    candidates: list[MockCandidate] = field(default_factory=lambda: [MockCandidate()])
    text: str | None = None


def test_gemini_basic_response_via_text() -> None:
    adapter = GeminiAdapter(agent_id="gemini-agent")
    response = MockResponse(text="Simple answer.")
    trace = adapter.trace_from_response(response, input_text="What is 2+2?")
    assert trace.agent_id == "gemini-agent"
    assert trace.output["message"] == "Simple answer."
    assert trace.input == {"text": "What is 2+2?"}


def test_gemini_basic_response_via_candidates() -> None:
    adapter = GeminiAdapter()
    response = MockResponse(text=None)
    trace = adapter.trace_from_response(response)
    assert trace.output["message"] == "Gemini response text."


def test_gemini_with_function_call() -> None:
    adapter = GeminiAdapter()
    fc_part = MockPart(text="", function_call=MockFunctionCall())
    candidate = MockCandidate(content=MockContent(parts=[fc_part]))
    response = MockResponse(candidates=[candidate], text=None)
    trace = adapter.trace_from_response(response)
    tool_steps = [s for s in trace.steps if s.type == "tool_call"]
    assert len(tool_steps) == 1
    assert tool_steps[0].name == "web_search"
    assert tool_steps[0].args == {"query": "test"}


def test_gemini_metadata() -> None:
    adapter = GeminiAdapter()
    response = MockResponse(text="ok")
    trace = adapter.trace_from_response(
        response, cost_usd=0.001, latency_ms=300, model="gemini-2.0-flash"
    )
    assert trace.metadata is not None
    assert trace.metadata.cost_usd == 0.001
    assert trace.metadata.latency_ms == 300
    assert trace.metadata.model == "gemini-2.0-flash"


def test_gemini_no_input_text() -> None:
    adapter = GeminiAdapter()
    response = MockResponse(text="answer")
    trace = adapter.trace_from_response(response)
    assert trace.input is None
