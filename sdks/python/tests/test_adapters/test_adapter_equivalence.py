"""Cross-adapter equivalence tests.

Validates that LangChain and Google ADK adapters produce structurally
equivalent traces when processing the same agent logic: identical step
types, temporal fields populated, and equivalent metadata.
"""

from __future__ import annotations

from collections.abc import Generator
from contextlib import contextmanager
from typing import Any
from unittest.mock import MagicMock, patch
from uuid import uuid4

import pytest

from attest._proto.types import STEP_LLM_CALL, STEP_TOOL_CALL, Trace
from attest.adapters.google_adk import GoogleADKAdapter
from attest.adapters.langchain import LangChainCallbackHandler


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


@contextmanager
def _adk_available() -> Generator[None, None, None]:
    with patch("attest.adapters.google_adk._require_adk"):
        yield


@contextmanager
def _langchain_available() -> Generator[None, None, None]:
    with patch("attest.adapters.langchain._require_langchain"):
        yield


def _make_adk_event(
    *,
    author: str = "",
    is_final: bool = False,
    tool_calls: list[MagicMock] | None = None,
    usage: int | None = None,
    content_text: str | None = None,
    model_version: str | None = None,
    timestamp: float | None = None,
) -> MagicMock:
    event = MagicMock()
    event.author = author
    event.is_final_response.return_value = is_final
    event.timestamp = timestamp

    actions = MagicMock()
    actions.tool_calls = tool_calls or []
    actions.tool_results = []
    actions.transfer_to_agent = None
    event.actions = actions

    if usage is not None:
        um = MagicMock()
        um.total_token_count = usage
        event.usage_metadata = um
    else:
        event.usage_metadata = None

    if content_text is not None:
        part = MagicMock()
        part.text = content_text
        content = MagicMock()
        content.parts = [part]
        event.content = content
    else:
        event.content = None

    if model_version is not None:
        llm_resp = MagicMock()
        llm_resp.model_version = model_version
        event.llm_response = llm_resp
    else:
        event.llm_response = None

    return event


def _make_adk_tool_call(name: str, args: dict[str, object] | None = None) -> MagicMock:
    tc = MagicMock()
    tc.name = name
    tc.args = args
    return tc


def _make_llm_response(
    text: str = "Hello",
    prompt_tokens: int = 10,
    completion_tokens: int = 5,
    model_name: str | None = None,
) -> MagicMock:
    gen = MagicMock()
    gen.text = text
    response = MagicMock()
    response.generations = [[gen]]
    response.llm_output = {
        "token_usage": {
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
        },
    }
    if model_name:
        response.llm_output["model_name"] = model_name
    return response


def _build_adk_trace() -> Trace:
    """Build a trace using the ADK adapter with a tool call + final response."""
    tc = _make_adk_tool_call("get_weather", {"city": "Paris"})
    e1 = _make_adk_event(
        tool_calls=[tc], usage=20, author="planner", timestamp=1000.0
    )
    e2 = _make_adk_event(
        is_final=True,
        content_text="The weather is sunny.",
        usage=30,
        model_version="gemini-2.0-flash",
        timestamp=1001.0,
    )

    with _adk_available():
        return GoogleADKAdapter.from_events(
            [e1, e2], agent_id="weather-agent", input_message="What is the weather?"
        )


def _build_langchain_trace() -> Trace:
    """Build a trace using the LangChain adapter with a tool call + LLM response."""
    with _langchain_available():
        handler = LangChainCallbackHandler(agent_id="weather-agent")
        root_id = uuid4()

        handler.on_chain_start(
            serialized={},
            inputs={"input": "What is the weather?"},
            run_id=root_id,
            parent_run_id=None,
        )

        # Tool call
        tool_id = uuid4()
        handler.on_tool_start(
            serialized={"name": "get_weather"},
            input_str='{"city": "Paris"}',
            run_id=tool_id,
        )
        handler.on_tool_end(output='{"temp": 22}', run_id=tool_id)

        # LLM call
        llm_id = uuid4()
        handler.on_chat_model_start(
            serialized={},
            messages=[[]],
            run_id=llm_id,
            invocation_params={"model_name": "gemini-2.0-flash"},
        )
        handler.on_llm_end(
            response=_make_llm_response(
                text="The weather is sunny.",
                prompt_tokens=20,
                completion_tokens=30,
                model_name="gemini-2.0-flash",
            ),
            run_id=llm_id,
        )

        handler.on_chain_end(
            outputs={"output": "The weather is sunny."},
            run_id=root_id,
            parent_run_id=None,
        )

        return handler.build_trace()


# ---------------------------------------------------------------------------
# Equivalence tests
# ---------------------------------------------------------------------------


class TestAdapterEquivalence:
    """Verify structural parity between LangChain and ADK adapter traces."""

    def test_both_produce_valid_traces(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        assert adk_trace.trace_id.startswith("trc_")
        assert lc_trace.trace_id.startswith("trc_")

    def test_agent_id_matches(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        assert adk_trace.agent_id == "weather-agent"
        assert lc_trace.agent_id == "weather-agent"

    def test_both_contain_tool_call_step(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        adk_tool_steps = [s for s in adk_trace.steps if s.type == STEP_TOOL_CALL]
        lc_tool_steps = [s for s in lc_trace.steps if s.type == STEP_TOOL_CALL]

        assert len(adk_tool_steps) >= 1
        assert len(lc_tool_steps) >= 1
        assert adk_tool_steps[0].name == "get_weather"
        assert lc_tool_steps[0].name == "get_weather"

    def test_both_contain_llm_call_step(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        adk_llm_steps = [s for s in adk_trace.steps if s.type == STEP_LLM_CALL]
        lc_llm_steps = [s for s in lc_trace.steps if s.type == STEP_LLM_CALL]

        assert len(adk_llm_steps) >= 1
        assert len(lc_llm_steps) >= 1

    def test_step_types_are_subset_of_standard_types(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        valid_types = {STEP_LLM_CALL, STEP_TOOL_CALL}

        for step in adk_trace.steps:
            assert step.type in valid_types, f"ADK step type {step.type!r} not standard"
        for step in lc_trace.steps:
            assert step.type in valid_types, f"LangChain step type {step.type!r} not standard"

    def test_output_message_populated(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        assert adk_trace.output.get("message") == "The weather is sunny."
        assert lc_trace.output.get("message") == "The weather is sunny."

    def test_metadata_model_populated(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        assert adk_trace.metadata is not None
        assert lc_trace.metadata is not None
        assert adk_trace.metadata.model == "gemini-2.0-flash"
        assert lc_trace.metadata.model == "gemini-2.0-flash"

    def test_metadata_tokens_populated(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        assert adk_trace.metadata is not None
        assert lc_trace.metadata is not None
        assert adk_trace.metadata.total_tokens is not None
        assert adk_trace.metadata.total_tokens > 0
        assert lc_trace.metadata.total_tokens is not None
        assert lc_trace.metadata.total_tokens > 0

    def test_temporal_fields_populated_on_adk_steps(self) -> None:
        adk_trace = _build_adk_trace()

        for step in adk_trace.steps:
            assert step.started_at_ms is not None, (
                f"ADK step {step.name!r} missing started_at_ms"
            )
            assert step.ended_at_ms is not None, (
                f"ADK step {step.name!r} missing ended_at_ms"
            )

    def test_temporal_fields_populated_on_langchain_steps(self) -> None:
        lc_trace = _build_langchain_trace()

        for step in lc_trace.steps:
            assert step.started_at_ms is not None, (
                f"LangChain step {step.name!r} missing started_at_ms"
            )
            assert step.ended_at_ms is not None, (
                f"LangChain step {step.name!r} missing ended_at_ms"
            )

    def test_agent_id_populated_on_langchain_steps(self) -> None:
        lc_trace = _build_langchain_trace()

        for step in lc_trace.steps:
            assert step.agent_id == "weather-agent", (
                f"LangChain step {step.name!r} missing agent_id"
            )

    def test_agent_id_populated_on_adk_tool_steps(self) -> None:
        adk_trace = _build_adk_trace()

        tool_steps = [s for s in adk_trace.steps if s.type == STEP_TOOL_CALL]
        for step in tool_steps:
            assert step.agent_id is not None, (
                f"ADK tool step {step.name!r} missing agent_id"
            )

    def test_input_populated(self) -> None:
        adk_trace = _build_adk_trace()
        lc_trace = _build_langchain_trace()

        assert adk_trace.input is not None
        assert lc_trace.input is not None
        assert adk_trace.input.get("message") == "What is the weather?"
        assert lc_trace.input.get("message") == "What is the weather?"
