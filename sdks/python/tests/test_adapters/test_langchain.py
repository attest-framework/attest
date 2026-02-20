"""Tests for the LangChain adapter."""

from __future__ import annotations

from contextlib import contextmanager
from collections.abc import Generator
from unittest.mock import MagicMock, patch
from uuid import uuid4

import pytest

from attest.adapters.langchain import LangChainCallbackHandler, LangChainAdapter
from attest._proto.types import STEP_LLM_CALL, STEP_TOOL_CALL


@contextmanager
def _langchain_available() -> Generator[None, None, None]:
    """Patch _require_langchain to be a no-op (simulates langchain being installed)."""
    with patch("attest.adapters.langchain._require_langchain"):
        yield


def _make_llm_response(
    text: str = "Hello",
    prompt_tokens: int = 10,
    completion_tokens: int = 5,
    model_name: str | None = None,
) -> MagicMock:
    """Build a minimal mock LLMResult."""
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


class TestLangChainImportGuard:
    """Verify ImportError when langchain_core is not installed."""

    def test_raises_import_error_when_langchain_missing(self) -> None:
        with pytest.raises(ImportError, match="Install langchain extras"):
            LangChainCallbackHandler()


class TestLangChainCallbackHandler:
    """Tests for LangChainCallbackHandler with synthetic callback sequences."""

    def test_empty_handler_returns_trace_with_no_steps(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            trace = handler.build_trace()
        assert trace is not None
        assert trace.steps == []

    def test_llm_call_callback_creates_llm_step(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            run_id = uuid4()

            handler.on_chat_model_start(
                serialized={},
                messages=[[]],
                run_id=run_id,
                invocation_params={"model_name": "gpt-4.1"},
            )
            handler.on_llm_end(
                response=_make_llm_response(text="Paris is the capital.", prompt_tokens=20, completion_tokens=10),
                run_id=run_id,
            )

            trace = handler.build_trace()

        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_LLM_CALL
        assert step.args is not None
        assert step.args["model"] == "gpt-4.1"
        assert step.result is not None
        assert step.result["completion"] == "Paris is the capital."
        assert step.result["input_tokens"] == 20
        assert step.result["output_tokens"] == 10

    def test_tool_call_creates_tool_step(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            run_id = uuid4()

            handler.on_tool_start(
                serialized={"name": "get_weather"},
                input_str='{"city": "Paris"}',
                run_id=run_id,
            )
            handler.on_tool_end(
                output='{"temp": 22}',
                run_id=run_id,
            )

            trace = handler.build_trace()

        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_TOOL_CALL
        assert step.name == "get_weather"
        assert step.args is not None
        assert step.args["input"] == '{"city": "Paris"}'
        assert step.result is not None
        assert step.result["output"] == '{"temp": 22}'

    def test_token_accumulation_across_multiple_llm_calls(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()

            # First LLM call: 20 + 10 = 30
            run_id_1 = uuid4()
            handler.on_chat_model_start(serialized={}, messages=[[]], run_id=run_id_1, invocation_params={})
            handler.on_llm_end(response=_make_llm_response(prompt_tokens=20, completion_tokens=10), run_id=run_id_1)

            # Second LLM call: 30 + 15 = 45
            run_id_2 = uuid4()
            handler.on_chat_model_start(serialized={}, messages=[[]], run_id=run_id_2, invocation_params={})
            handler.on_llm_end(response=_make_llm_response(prompt_tokens=30, completion_tokens=15), run_id=run_id_2)

            trace = handler.build_trace()

        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 75  # 30 + 45

    def test_model_name_extraction_from_invocation_params(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            run_id = uuid4()

            handler.on_chat_model_start(
                serialized={},
                messages=[[]],
                run_id=run_id,
                invocation_params={"model_name": "gpt-4.1-mini"},
            )
            handler.on_llm_end(response=_make_llm_response(), run_id=run_id)

            trace = handler.build_trace()

        assert trace.metadata is not None
        assert trace.metadata.model == "gpt-4.1-mini"

    def test_model_name_fallback_to_llm_output(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            run_id = uuid4()

            handler.on_chat_model_start(
                serialized={},
                messages=[[]],
                run_id=run_id,
                invocation_params={},
            )
            handler.on_llm_end(
                response=_make_llm_response(model_name="gpt-4.1"),
                run_id=run_id,
            )

            trace = handler.build_trace()

        assert trace.metadata is not None
        assert trace.metadata.model == "gpt-4.1"

    def test_agent_id_passed_through_to_trace(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler(agent_id="weather-agent")
            trace = handler.build_trace()
        assert trace.agent_id == "weather-agent"

    def test_tool_error_creates_step_with_error_result(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            run_id = uuid4()

            handler.on_tool_start(
                serialized={"name": "get_weather"},
                input_str="Paris",
                run_id=run_id,
            )
            handler.on_tool_error(
                error=ValueError("API rate limit exceeded"),
                run_id=run_id,
            )

            trace = handler.build_trace()

        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_TOOL_CALL
        assert step.name == "get_weather"
        assert step.result is not None
        assert "API rate limit exceeded" in step.result["error"]

    def test_double_build_trace_raises(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            handler.build_trace()
            with pytest.raises(RuntimeError, match="build_trace.*already called"):
                handler.build_trace()

    def test_root_chain_input_mapping(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            run_id = uuid4()

            handler.on_chain_start(
                serialized={},
                inputs={"input": "What is the weather in Paris?"},
                run_id=run_id,
                parent_run_id=None,
            )
            handler.on_chain_end(
                outputs={"output": "It is 22C in Paris."},
                run_id=run_id,
                parent_run_id=None,
            )

            trace = handler.build_trace()

        assert trace.input is not None
        assert trace.input["message"] == "What is the weather in Paris?"
        assert trace.output["message"] == "It is 22C in Paris."

    def test_non_root_chain_does_not_overwrite_input(self) -> None:
        with _langchain_available():
            handler = LangChainCallbackHandler()
            root_id = uuid4()
            child_id = uuid4()

            handler.on_chain_start(
                serialized={},
                inputs={"input": "root question"},
                run_id=root_id,
                parent_run_id=None,
            )
            handler.on_chain_start(
                serialized={},
                inputs={"input": "child question"},
                run_id=child_id,
                parent_run_id=root_id,
            )

            trace = handler.build_trace()

        assert trace.input is not None
        assert trace.input["message"] == "root question"

    def test_full_agent_sequence(self) -> None:
        """Simulate a complete agent run: chain start -> llm -> tool -> llm -> chain end."""
        with _langchain_available():
            handler = LangChainCallbackHandler(agent_id="test-agent")
            root_id = uuid4()

            # Root chain start
            handler.on_chain_start(
                serialized={},
                inputs={"input": "What is 2+2?"},
                run_id=root_id,
                parent_run_id=None,
            )

            # First LLM call (decides to use calculator)
            llm_id_1 = uuid4()
            handler.on_chat_model_start(
                serialized={},
                messages=[[]],
                run_id=llm_id_1,
                invocation_params={"model_name": "gpt-4.1"},
            )
            handler.on_llm_end(
                response=_make_llm_response(text="I'll calculate that.", prompt_tokens=15, completion_tokens=8),
                run_id=llm_id_1,
            )

            # Tool call
            tool_id = uuid4()
            handler.on_tool_start(
                serialized={"name": "calculator"},
                input_str="2+2",
                run_id=tool_id,
            )
            handler.on_tool_end(output="4", run_id=tool_id)

            # Second LLM call (final answer)
            llm_id_2 = uuid4()
            handler.on_chat_model_start(
                serialized={},
                messages=[[]],
                run_id=llm_id_2,
                invocation_params={"model_name": "gpt-4.1"},
            )
            handler.on_llm_end(
                response=_make_llm_response(text="2+2 equals 4.", prompt_tokens=25, completion_tokens=5),
                run_id=llm_id_2,
            )

            # Root chain end
            handler.on_chain_end(
                outputs={"output": "2+2 equals 4."},
                run_id=root_id,
                parent_run_id=None,
            )

            trace = handler.build_trace()

        assert trace.agent_id == "test-agent"
        assert trace.input is not None
        assert trace.input["message"] == "What is 2+2?"
        assert trace.output["message"] == "2+2 equals 4."
        assert len(trace.steps) == 3
        assert trace.steps[0].type == STEP_LLM_CALL
        assert trace.steps[1].type == STEP_TOOL_CALL
        assert trace.steps[1].name == "calculator"
        assert trace.steps[2].type == STEP_LLM_CALL
        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 53  # (15+8) + (25+5)
        assert trace.metadata.model == "gpt-4.1"


class TestLangChainAdapter:
    """Tests for the LangChainAdapter context manager."""

    def test_capture_context_manager_builds_trace(self) -> None:
        with _langchain_available():
            adapter = LangChainAdapter(agent_id="ctx-agent")
            with adapter.capture() as handler:
                run_id = uuid4()
                handler.on_chat_model_start(
                    serialized={},
                    messages=[[]],
                    run_id=run_id,
                    invocation_params={"model_name": "gpt-4.1"},
                )
                handler.on_llm_end(response=_make_llm_response(), run_id=run_id)

        assert adapter.trace is not None
        assert adapter.trace.agent_id == "ctx-agent"
        assert len(adapter.trace.steps) == 1

    def test_trace_is_none_before_capture(self) -> None:
        with _langchain_available():
            adapter = LangChainAdapter()
        assert adapter.trace is None

    def test_capture_with_no_events_returns_empty_trace(self) -> None:
        with _langchain_available():
            adapter = LangChainAdapter()
            with adapter.capture():
                pass

        assert adapter.trace is not None
        assert adapter.trace.steps == []
