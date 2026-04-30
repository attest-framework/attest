"""Tests for the LlamaIndex instrumentation adapter."""

from __future__ import annotations

import sys
from collections.abc import Generator
from contextlib import contextmanager
from unittest.mock import MagicMock, patch

import pytest

from attest._proto.types import STEP_LLM_CALL, STEP_RETRIEVAL, STEP_TOOL_CALL


@contextmanager
def _llamaindex_available() -> Generator[None, None, None]:
    """Patch _require_llamaindex to be a no-op (simulates llama_index being installed)."""
    with patch("attest.adapters.llamaindex._require_llamaindex"):
        yield


def _make_llm_start_event(model: str = "gpt-4.1") -> MagicMock:
    """Build a synthetic LLMChatStartEvent."""
    event = MagicMock()
    type(event).__name__ = "LLMChatStartEvent"
    event.model_dict = {"model": model}
    event.messages = []
    return event


def _make_llm_end_event(
    completion: str = "Hello world",
    input_tokens: int = 10,
    output_tokens: int = 5,
    tool_calls: list[dict[str, object]] | None = None,
) -> MagicMock:
    """Build a synthetic LLMChatEndEvent."""
    event = MagicMock()
    type(event).__name__ = "LLMChatEndEvent"

    response = MagicMock()
    response.__str__ = lambda self: completion
    response.raw = {"usage": {"prompt_tokens": input_tokens, "completion_tokens": output_tokens}}

    message = MagicMock()
    message.additional_kwargs = {"tool_calls": tool_calls or []}
    response.message = message

    event.response = response
    return event


def _make_retrieval_start_event(query: str = "what is attest?") -> MagicMock:
    """Build a synthetic RetrievalStartEvent."""
    event = MagicMock()
    type(event).__name__ = "RetrievalStartEvent"
    event.str_or_query_bundle = query
    return event


def _make_retrieval_end_event(
    nodes: list[dict[str, object]] | None = None,
) -> MagicMock:
    """Build a synthetic RetrievalEndEvent."""
    event = MagicMock()
    type(event).__name__ = "RetrievalEndEvent"

    if nodes is None:
        nodes = [{"text": "Attest is a testing framework.", "score": 0.95, "node_id": "n1"}]

    mock_nodes = []
    for n in nodes:
        node = MagicMock()
        node.text = n.get("text", "")
        node.score = n.get("score", 0.0)
        node.node_id = n.get("node_id", "")
        mock_nodes.append(node)

    event.nodes = mock_nodes
    return event


class TestLlamaIndexImportGuard:
    """Verify ImportError when llama_index is not installed."""

    def test_raises_import_error_when_llamaindex_missing(self) -> None:
        from attest.adapters.llamaindex import _require_llamaindex

        with pytest.raises(ImportError, match="Install llamaindex extras"):
            _require_llamaindex()


class TestLlamaIndexHandler:
    """Tests for LlamaIndexInstrumentationHandler with mocked events."""

    def test_empty_handler_returns_valid_trace(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            trace = handler.build_trace()
        assert trace is not None
        assert trace.steps == []
        assert trace.output == {"message": ""}

    def test_llm_end_event_creates_llm_step(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            start = _make_llm_start_event(model="gpt-4.1")
            end = _make_llm_end_event(
                completion="Paris is the capital.", input_tokens=20, output_tokens=10
            )
            handler._handle_event(start)
            handler._handle_event(end)
            trace = handler.build_trace()

        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_LLM_CALL
        assert step.name == "chat_completion"
        assert step.args is not None
        assert step.args["model"] == "gpt-4.1"
        assert step.result is not None
        assert step.result["completion"] == "Paris is the capital."
        assert step.result["input_tokens"] == 20
        assert step.result["output_tokens"] == 10

    def test_retrieval_events_create_retrieval_step(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            start = _make_retrieval_start_event(query="what is attest?")
            end = _make_retrieval_end_event(
                nodes=[
                    {"text": "Attest is a framework.", "score": 0.95, "node_id": "n1"},
                    {"text": "It tests agents.", "score": 0.80, "node_id": "n2"},
                ]
            )
            handler._handle_event(start)
            handler._handle_event(end)
            trace = handler.build_trace()

        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_RETRIEVAL
        assert step.name == "retrieve"
        assert step.args is not None
        assert step.args["query"] == "what is attest?"
        assert step.result is not None
        assert len(step.result["nodes"]) == 2
        assert step.result["nodes"][0]["text"] == "Attest is a framework."
        assert step.result["nodes"][0]["score"] == 0.95

    def test_token_accumulation_across_multiple_llm_calls(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()

            handler._handle_event(_make_llm_start_event())
            handler._handle_event(_make_llm_end_event(input_tokens=50, output_tokens=25))
            handler._handle_event(_make_llm_start_event())
            handler._handle_event(_make_llm_end_event(input_tokens=30, output_tokens=15))

            trace = handler.build_trace()

        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 120  # 50+25+30+15

    def test_model_name_extracted_from_start_event(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            handler._handle_event(_make_llm_start_event(model="gpt-4.1-mini"))
            handler._handle_event(_make_llm_end_event())
            trace = handler.build_trace()

        assert trace.metadata is not None
        assert trace.metadata.model == "gpt-4.1-mini"

    def test_agent_id_passed_to_trace(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler(agent_id="rag-agent")
            trace = handler.build_trace()

        assert trace.agent_id == "rag-agent"

    def test_context_manager_lifecycle(self) -> None:
        """Verify attach/detach are called via context manager."""
        mock_dispatcher = MagicMock()
        mock_dispatcher.event_handlers = []

        mock_base = MagicMock()

        with (
            _llamaindex_available(),
            patch.dict(
                sys.modules,
                {
                    "llama_index": MagicMock(),
                    "llama_index.core": MagicMock(),
                    "llama_index.core.instrumentation": MagicMock(
                        get_dispatcher=MagicMock(return_value=mock_dispatcher),
                    ),
                    "llama_index.core.instrumentation.event_handlers": MagicMock(
                        BaseEventHandler=mock_base,
                    ),
                },
            ),
        ):
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            handler.attach()
            assert mock_dispatcher.add_event_handler.called
            assert handler._handler is not None

            # Simulate detach
            mock_dispatcher.event_handlers = [handler._handler]
            handler.detach()
            assert handler._handler is None

    def test_build_trace_with_query_and_response(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            handler._handle_event(_make_llm_start_event())
            handler._handle_event(_make_llm_end_event(completion="42"))

            trace = handler.build_trace(
                query="what is the meaning of life?",
                response="The answer is 42.",
                latency_ms=500,
                cost_usd=0.005,
            )

        assert trace.input == {"message": "what is the meaning of life?"}
        assert trace.output == {"message": "The answer is 42."}
        assert trace.metadata is not None
        assert trace.metadata.latency_ms == 500
        assert trace.metadata.cost_usd == 0.005

    def test_tool_calls_extracted_from_llm_response(self) -> None:
        tool_calls = [
            {
                "function": {
                    "name": "search_web",
                    "arguments": '{"query": "Paris population"}',
                },
            },
            {
                "function": {
                    "name": "calculate",
                    "arguments": '{"expression": "2+2"}',
                },
            },
        ]

        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            handler._handle_event(_make_llm_start_event())
            handler._handle_event(_make_llm_end_event(tool_calls=tool_calls))
            trace = handler.build_trace()

        # 1 llm_call + 2 tool_calls = 3 steps
        assert len(trace.steps) == 3
        assert trace.steps[0].type == STEP_LLM_CALL
        assert trace.steps[1].type == STEP_TOOL_CALL
        assert trace.steps[1].name == "search_web"
        assert trace.steps[1].args == {"query": "Paris population"}
        assert trace.steps[2].type == STEP_TOOL_CALL
        assert trace.steps[2].name == "calculate"
        assert trace.steps[2].args == {"expression": "2+2"}

    def test_multiple_llm_calls_produce_multiple_steps(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            handler._handle_event(_make_llm_start_event(model="gpt-4.1"))
            handler._handle_event(_make_llm_end_event(completion="first"))
            handler._handle_event(_make_llm_start_event(model="gpt-4.1"))
            handler._handle_event(_make_llm_end_event(completion="second"))
            trace = handler.build_trace()

        assert len(trace.steps) == 2
        assert trace.steps[0].result is not None
        assert trace.steps[0].result["completion"] == "first"
        assert trace.steps[1].result is not None
        assert trace.steps[1].result["completion"] == "second"

    def test_output_defaults_to_last_llm_completion(self) -> None:
        with _llamaindex_available():
            from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler

            handler = LlamaIndexInstrumentationHandler()
            handler._handle_event(_make_llm_start_event())
            handler._handle_event(_make_llm_end_event(completion="final answer"))
            trace = handler.build_trace()

        assert trace.output == {"message": "final answer"}
