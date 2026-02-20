"""Tests for the GoogleADKAdapter."""

from __future__ import annotations

from collections.abc import Generator
from contextlib import contextmanager
from unittest.mock import MagicMock, patch

import pytest

from attest._proto.types import STEP_AGENT_CALL, STEP_LLM_CALL, STEP_TOOL_CALL
from attest.adapters.google_adk import GoogleADKAdapter


def _make_event(
    *,
    author: str = "",
    is_final: bool = False,
    tool_calls: list[MagicMock] | None = None,
    tool_results: list[MagicMock] | None = None,
    transfer: str | None = None,
    usage: int | None = None,
    content_text: str | None = None,
    model_version: str | None = None,
) -> MagicMock:
    """Build a minimal mock ADK Event."""
    event = MagicMock()
    event.author = author
    event.is_final_response.return_value = is_final

    # Actions
    actions = MagicMock()
    actions.tool_calls = tool_calls or []
    actions.tool_results = tool_results or []
    actions.transfer_to_agent = transfer
    event.actions = actions

    # Usage metadata
    if usage is not None:
        um = MagicMock()
        um.total_token_count = usage
        event.usage_metadata = um
    else:
        event.usage_metadata = None

    # Content (for final responses)
    if content_text is not None:
        part = MagicMock()
        part.text = content_text
        content = MagicMock()
        content.parts = [part]
        event.content = content
    else:
        event.content = None

    # LLM response with model version
    if model_version is not None:
        llm_resp = MagicMock()
        llm_resp.model_version = model_version
        event.llm_response = llm_resp
    else:
        event.llm_response = None

    return event


def _make_tool_call(name: str, args: dict[str, object] | None = None) -> MagicMock:
    """Build a mock tool call object."""
    tc = MagicMock()
    tc.name = name
    tc.args = args
    return tc


def _make_tool_result(name: str, result: dict[str, object] | None = None) -> MagicMock:
    """Build a mock tool result object."""
    tr = MagicMock()
    tr.name = name
    tr.result = result
    return tr


@contextmanager
def _adk_available() -> Generator[None, None, None]:
    """Patch _require_adk to be a no-op (simulates google-adk being installed)."""
    with patch("attest.adapters.google_adk._require_adk"):
        yield


class TestGoogleADKAdapterImportGuard:
    """Verify ImportError when google-adk is not installed."""

    def test_raises_import_error_when_adk_missing(self) -> None:
        with pytest.raises(ImportError, match="Install ADK extras"):
            GoogleADKAdapter.from_events([])


class TestGoogleADKAdapterFromEvents:
    """Tests for GoogleADKAdapter.from_events() with mocked events."""

    def test_empty_events_returns_valid_trace(self) -> None:
        with _adk_available():
            trace = GoogleADKAdapter.from_events([], agent_id="test-agent")
        assert trace is not None
        # Should have the llm_call summary step even with no events
        assert len(trace.steps) == 1
        assert trace.steps[0].type == STEP_LLM_CALL

    def test_tool_call_extraction(self) -> None:
        tc = _make_tool_call("get_weather", {"city": "Paris"})
        event = _make_event(tool_calls=[tc])
        with _adk_available():
            trace = GoogleADKAdapter.from_events([event], agent_id="agent-1")
        tool_steps = [s for s in trace.steps if s.type == STEP_TOOL_CALL]
        assert len(tool_steps) == 1
        assert tool_steps[0].name == "get_weather"
        assert tool_steps[0].args is not None
        assert tool_steps[0].args["city"] == "Paris"

    def test_tool_result_extraction(self) -> None:
        tr = _make_tool_result("get_weather", {"temp": 22})
        event = _make_event(tool_results=[tr])
        with _adk_available():
            trace = GoogleADKAdapter.from_events([event])
        tool_steps = [s for s in trace.steps if s.type == STEP_TOOL_CALL]
        assert len(tool_steps) == 1
        assert tool_steps[0].name == "get_weather"
        assert tool_steps[0].args is None
        assert tool_steps[0].result is not None
        assert tool_steps[0].result["temp"] == 22

    def test_token_accumulation(self) -> None:
        e1 = _make_event(usage=100)
        e2 = _make_event(usage=50)
        with _adk_available():
            trace = GoogleADKAdapter.from_events([e1, e2])
        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 150

    def test_final_response_text_extraction(self) -> None:
        event = _make_event(is_final=True, content_text="The weather is sunny.")
        with _adk_available():
            trace = GoogleADKAdapter.from_events([event])
        assert trace.output.get("message") == "The weather is sunny."

    def test_model_extraction(self) -> None:
        event = _make_event(model_version="gemini-2.0-flash")
        with _adk_available():
            trace = GoogleADKAdapter.from_events([event])
        assert trace.metadata is not None
        assert trace.metadata.model == "gemini-2.0-flash"

    def test_model_first_non_none_wins(self) -> None:
        e1 = _make_event(model_version="gemini-2.0-flash")
        e2 = _make_event(model_version="gemini-2.5-pro")
        with _adk_available():
            trace = GoogleADKAdapter.from_events([e1, e2])
        assert trace.metadata is not None
        assert trace.metadata.model == "gemini-2.0-flash"

    def test_sub_agent_transfer_creates_agent_call_step(self) -> None:
        event = _make_event(transfer="booking-agent")
        with _adk_available():
            trace = GoogleADKAdapter.from_events([event])
        agent_steps = [s for s in trace.steps if s.type == STEP_AGENT_CALL]
        assert len(agent_steps) == 1
        assert agent_steps[0].name == "booking-agent"

    def test_agent_id_passed_through(self) -> None:
        with _adk_available():
            trace = GoogleADKAdapter.from_events([], agent_id="my-agent")
        assert trace.agent_id == "my-agent"

    def test_input_message_set(self) -> None:
        with _adk_available():
            trace = GoogleADKAdapter.from_events(
                [], agent_id="agent", input_message="hello"
            )
        assert trace.input is not None
        assert trace.input["message"] == "hello"

    def test_multiple_events_accumulate(self) -> None:
        tc = _make_tool_call("search", {"q": "flights"})
        e1 = _make_event(tool_calls=[tc], usage=30)
        e2 = _make_event(
            is_final=True, content_text="Found 3 flights.", usage=70, model_version="gemini-2.0-flash"
        )
        with _adk_available():
            trace = GoogleADKAdapter.from_events([e1, e2], agent_id="travel")

        # tool_call + llm_call summary
        tool_steps = [s for s in trace.steps if s.type == STEP_TOOL_CALL]
        llm_steps = [s for s in trace.steps if s.type == STEP_LLM_CALL]
        assert len(tool_steps) == 1
        assert len(llm_steps) == 1

        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 100
        assert trace.metadata.model == "gemini-2.0-flash"
        assert trace.output["message"] == "Found 3 flights."

    def test_llm_call_summary_contains_output_and_tokens(self) -> None:
        event = _make_event(
            is_final=True, content_text="Result text", usage=42, model_version="gemini-2.0-flash"
        )
        with _adk_available():
            trace = GoogleADKAdapter.from_events([event])
        llm_step = next(s for s in trace.steps if s.type == STEP_LLM_CALL)
        assert llm_step.name == "generate_content"
        assert llm_step.args is not None
        assert llm_step.args["model"] == "gemini-2.0-flash"
        assert llm_step.result is not None
        assert llm_step.result["completion"] == "Result text"
        assert llm_step.result["total_tokens"] == 42

    def test_no_model_means_no_model_in_llm_args(self) -> None:
        event = _make_event(is_final=True, content_text="hi")
        with _adk_available():
            trace = GoogleADKAdapter.from_events([event])
        llm_step = next(s for s in trace.steps if s.type == STEP_LLM_CALL)
        assert llm_step.args is None


class TestGoogleADKAdapterCaptureAsync:
    """Tests for GoogleADKAdapter.capture_async() with mocked runner."""

    @pytest.mark.asyncio
    async def test_capture_async_collects_events(self) -> None:
        event = _make_event(
            is_final=True, content_text="Hello!", usage=10, model_version="gemini-2.0-flash"
        )

        async def mock_run_async(user_id: str, session_id: str, new_message: Any) -> Any:  # noqa: ANN401
            yield event

        runner = MagicMock()
        runner.run_async = mock_run_async

        adapter = GoogleADKAdapter(agent_id="test-agent")

        # Patch google.genai.types at the module level so the local import resolves
        mock_genai_types = MagicMock()
        with _adk_available(), patch.dict(
            "sys.modules",
            {
                "google": MagicMock(),
                "google.genai": MagicMock(),
                "google.genai.types": mock_genai_types,
            },
        ):
            trace = await adapter.capture_async(
                runner=runner,
                user_id="user-1",
                session_id="sess-1",
                message="Hi there",
            )

        assert trace.agent_id == "test-agent"
        assert trace.output["message"] == "Hello!"
        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 10
