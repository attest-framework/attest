"""Integration tests for Google ADK adapter with real/duck-typed framework objects."""

from __future__ import annotations

import types

import pytest

adk_mod = pytest.importorskip("google.adk")
genai_mod = pytest.importorskip("google.genai")

from attest.adapters.google_adk import GoogleADKAdapter  # noqa: E402

pytestmark = pytest.mark.integration


def _make_event(
    *,
    is_final: bool = False,
    text: str | None = None,
    total_tokens: int | None = None,
    tool_calls: list[types.SimpleNamespace] | None = None,
    author: str | None = None,
    model_version: str | None = None,
) -> types.SimpleNamespace:
    """Build a duck-typed ADK Event using SimpleNamespace.

    ADK Event constructors require session context, so we construct
    attribute-compatible objects that satisfy the adapter's getattr-based
    duck typing.
    """
    content = None
    if text is not None:
        part = types.SimpleNamespace(text=text)
        content = types.SimpleNamespace(parts=[part])

    usage = None
    if total_tokens is not None:
        usage = types.SimpleNamespace(total_token_count=total_tokens)

    actions = types.SimpleNamespace(
        tool_calls=tool_calls or [],
        tool_results=[],
        transfer_to_agent=None,
    )

    llm_response = None
    if model_version is not None:
        llm_response = types.SimpleNamespace(model_version=model_version)

    return types.SimpleNamespace(
        actions=actions,
        usage_metadata=usage,
        content=content,
        author=author,
        timestamp=None,
        llm_response=llm_response,
        is_final_response=lambda: is_final,
    )


class TestGoogleADKAdapterIntegration:
    """Tests using duck-typed ADK events against the Attest adapter."""

    def test_from_events_with_duck_typed_events(self) -> None:
        """from_events() produces a valid trace from duck-typed events."""
        events = [
            _make_event(author="agent-1", total_tokens=50),
            _make_event(
                is_final=True,
                text="The answer is 42",
                total_tokens=100,
                model_version="gemini-2.0-flash",
            ),
        ]

        trace = GoogleADKAdapter.from_events(
            events,
            agent_id="adk-agent",
            input_message="What is the answer?",
        )

        assert trace.input is not None
        assert trace.input["message"] == "What is the answer?"
        assert trace.output is not None
        assert "42" in trace.output["message"]
        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 150
        assert trace.metadata.model == "gemini-2.0-flash"

    def test_real_content_part_extraction(self) -> None:
        """Output text extraction from events with content parts."""
        events = [
            _make_event(
                is_final=True,
                text="Response text here",
                model_version="gemini-2.0-flash",
            ),
        ]

        trace = GoogleADKAdapter.from_events(events, agent_id="adk-test")
        assert trace.output is not None
        assert trace.output["message"] == "Response text here"

    def test_adapter_handles_empty_events(self) -> None:
        """Empty event list produces a valid trace with empty output."""
        trace = GoogleADKAdapter.from_events([], agent_id="adk-empty")
        assert trace.output is not None
        assert trace.output["message"] == ""

    def test_tool_calls_become_steps(self) -> None:
        """Tool calls in events produce tool_call steps."""
        tool = types.SimpleNamespace(name="search_web", args={"query": "test"})
        events = [
            _make_event(tool_calls=[tool], author="agent-1"),
            _make_event(is_final=True, text="Found results"),
        ]

        trace = GoogleADKAdapter.from_events(events, agent_id="adk-tools")
        tool_steps = [s for s in trace.steps if s.type == "tool_call"]
        assert len(tool_steps) >= 1
        assert tool_steps[0].name == "search_web"
