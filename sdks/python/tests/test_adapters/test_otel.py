"""Tests for the OTelAdapter."""

from __future__ import annotations

from collections.abc import Generator
from contextlib import contextmanager
from unittest.mock import MagicMock, patch

import pytest

from attest._proto.types import (
    STEP_AGENT_CALL,
    STEP_LLM_CALL,
    STEP_RETRIEVAL,
    STEP_TOOL_CALL,
)
from attest.adapters.otel import OTelAdapter, _require_numeric_attr, to_otel_spans
from attest.trace import TraceBuilder


def _make_span(
    name: str,
    attrs: dict[str, object],
    *,
    trace_id: int = 0xDEADBEEF12345678DEADBEEF12345678,
    span_id: int = 0x1234567890ABCDEF,
    parent_span_id: int | None = None,
    start_time: int = 1_000_000_000,
    end_time: int = 2_000_000_000,
) -> MagicMock:
    """Build a minimal mock ReadableSpan."""
    span = MagicMock()
    span.name = name
    span.attributes = attrs
    span.start_time = start_time
    span.end_time = end_time

    ctx = MagicMock()
    ctx.trace_id = trace_id
    ctx.span_id = span_id
    span.context = ctx

    if parent_span_id is None:
        span.parent = None
    else:
        parent = MagicMock()
        parent.span_id = parent_span_id
        span.parent = parent

    return span


@contextmanager
def _otel_available() -> Generator[None, None, None]:
    """Patch _require_otel to be a no-op (simulates otel being installed)."""
    with patch("attest.adapters.otel._require_otel"):
        yield


class TestOTelAdapterImportGuard:
    """Verify ImportError when opentelemetry is not installed."""

    def test_raises_import_error_when_otel_missing(self) -> None:
        # _require_otel is NOT patched here — it will raise ImportError
        # because opentelemetry-sdk is not installed in the test environment.
        with pytest.raises(ImportError, match="Install otel extras"):
            OTelAdapter.from_spans([])  # type: ignore[arg-type]


class TestOTelAdapterFromSpans:
    """Tests for OTelAdapter.from_spans() with mocked spans.

    All tests patch _require_otel to bypass the opentelemetry install check.
    """

    def test_empty_spans_returns_trace(self) -> None:
        with _otel_available():
            trace = OTelAdapter.from_spans([])
        assert trace is not None
        assert trace.steps == []

    def test_llm_call_span_becomes_llm_step(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.request.model": "gpt-4.1",
                "gen_ai.completion": "Hello world",
                "gen_ai.usage.input_tokens": 10,
                "gen_ai.usage.output_tokens": 5,
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_LLM_CALL
        assert step.args is not None
        assert step.args.get("model") == "gpt-4.1"
        assert step.args.get("operation") == "chat"
        assert step.result is not None
        assert step.result.get("completion") == "Hello world"

    def test_tool_call_span_becomes_tool_step(self) -> None:
        span = _make_span(
            "tool_call",
            {
                "gen_ai.tool.name": "search_web",
                "gen_ai.tool.parameters": '{"query": "Paris"}',
                "gen_ai.tool.output": '{"results": []}',
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert len(trace.steps) == 1
        step = trace.steps[0]
        assert step.type == STEP_TOOL_CALL
        assert step.name == "search_web"

    def test_output_message_from_last_llm_completion(self) -> None:
        span = _make_span(
            "completion",
            {
                "gen_ai.operation.name": "completion",
                "gen_ai.completion": "Paris is the answer.",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.output.get("message") == "Paris is the answer."

    def test_latency_computed_from_root_span(self) -> None:
        # 1 second = 1_000_000_000 ns → 1000 ms
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "hi"},
            start_time=0,
            end_time=1_000_000_000,
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.metadata is not None
        assert trace.metadata.latency_ms == 1000

    def test_token_accumulation(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.completion": "ok",
                "gen_ai.usage.input_tokens": 50,
                "gen_ai.usage.output_tokens": 25,
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.metadata is not None
        assert trace.metadata.total_tokens == 75

    def test_model_extracted_from_response_model(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.response.model": "gpt-4.1-mini",
                "gen_ai.completion": "response",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.metadata is not None
        assert trace.metadata.model == "gpt-4.1-mini"

    def test_model_falls_back_to_request_model(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.request.model": "gpt-4.1",
                "gen_ai.completion": "response",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.metadata is not None
        assert trace.metadata.model == "gpt-4.1"

    def test_trace_id_derived_from_otel_trace_id(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
            trace_id=0xAABBCCDD11223344AABBCCDD11223344,
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.trace_id.startswith("otel_")

    def test_multiple_spans_multiple_steps(self) -> None:
        llm_span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.completion": "I'll search for that.",
            },
            start_time=0,
            end_time=500_000_000,
        )
        tool_span = _make_span(
            "tool_call",
            {
                "gen_ai.tool.name": "search",
                "gen_ai.tool.output": "results",
            },
            start_time=500_000_000,
            end_time=800_000_000,
            parent_span_id=0x1234567890ABCDEF,
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([llm_span, tool_span])
        assert len(trace.steps) == 2
        assert trace.steps[0].type == STEP_LLM_CALL
        assert trace.steps[1].type == STEP_TOOL_CALL

    def test_unknown_span_skipped(self) -> None:
        span = _make_span("http.request", {"http.method": "GET", "http.url": "https://example.com"})
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps == []

    def test_agent_id_set_on_trace(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span], agent_id="my-agent")
        assert trace.agent_id == "my-agent"

    def test_step_duration_metadata(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "hi"},
            start_time=0,
            end_time=200_000_000,  # 200ms
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].metadata is not None
        assert trace.steps[0].metadata.get("duration_ms") == 200

    def test_tool_name_from_attribute(self) -> None:
        span = _make_span(
            "some_span_name",
            {"gen_ai.tool.name": "my_tool"},
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].name == "my_tool"

    def test_from_spans_with_agent_id(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "response"},
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span], agent_id="inst-agent")
        assert trace.agent_id == "inst-agent"


class TestOTelAdapterGenAiOperationCoverage:
    """New OTel GenAI operation names: text_completion, embeddings, invoke_agent, execute_tool."""

    def test_text_completion_classified_as_llm_call(self) -> None:
        span = _make_span(
            "text_completion gpt-4.1",
            {
                "gen_ai.operation.name": "text_completion",
                "gen_ai.request.model": "gpt-4.1",
                "gen_ai.prompt": "Once upon a time",
                "gen_ai.completion": "there was a llama.",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].type == STEP_LLM_CALL
        assert trace.steps[0].args is not None
        assert trace.steps[0].args.get("operation") == "text_completion"

    def test_embeddings_classified_as_llm_call(self) -> None:
        span = _make_span(
            "embeddings text-embedding-3-small",
            {
                "gen_ai.operation.name": "embeddings",
                "gen_ai.request.model": "text-embedding-3-small",
                "gen_ai.usage.input_tokens": 8,
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].type == STEP_LLM_CALL
        assert trace.steps[0].args is not None
        assert trace.steps[0].args.get("operation") == "embeddings"

    def test_execute_tool_classified_as_tool_call(self) -> None:
        span = _make_span(
            "execute_tool web_search",
            {
                "gen_ai.operation.name": "execute_tool",
                "gen_ai.tool.name": "web_search",
                "gen_ai.tool.call.id": "call_abc",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].type == STEP_TOOL_CALL
        assert trace.steps[0].name == "web_search"
        assert trace.steps[0].args is not None
        assert trace.steps[0].args.get("call_id") == "call_abc"

    def test_invoke_agent_classified_as_agent_call(self) -> None:
        span = _make_span(
            "invoke_agent researcher",
            {
                "gen_ai.operation.name": "invoke_agent",
                "gen_ai.agent.id": "agent-001",
                "gen_ai.agent.name": "researcher",
                "gen_ai.agent.description": "Research assistant",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].type == STEP_AGENT_CALL
        assert trace.steps[0].name == "researcher"
        assert trace.steps[0].args is not None
        assert trace.steps[0].args.get("agent_id") == "agent-001"
        assert trace.steps[0].args.get("description") == "Research assistant"

    def test_retrieval_classified_via_operation_name(self) -> None:
        span = _make_span(
            "retrieval",
            {
                "gen_ai.operation.name": "retrieval",
                "gen_ai.retrieval.query": "Capital of France",
                "gen_ai.retrieval.source": "vector_store",
                "gen_ai.retrieval.documents.count": 3,
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].type == STEP_RETRIEVAL
        assert trace.steps[0].args is not None
        assert trace.steps[0].args.get("query") == "Capital of France"
        assert trace.steps[0].args.get("source") == "vector_store"
        assert trace.steps[0].result is not None
        assert trace.steps[0].result.get("documents_count") == 3

    def test_retrieval_classified_via_attribute_family(self) -> None:
        # Producer omits operation.name but emits gen_ai.retrieval.* attrs.
        span = _make_span(
            "vector-search",
            {
                "gen_ai.retrieval.query": "Paris",
                "gen_ai.retrieval.documents.count": 5,
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps[0].type == STEP_RETRIEVAL


class TestOTelAdapterAgentHierarchy:
    """gen_ai.agent.* attributes propagate onto Step.agent_id / agent_role."""

    def test_llm_call_inherits_agent_attributes(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.completion": "ok",
                "gen_ai.agent.id": "agent-42",
                "gen_ai.agent.name": "support_bot",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        step = trace.steps[0]
        assert step.agent_id == "agent-42"
        assert step.agent_role == "support_bot"

    def test_tool_call_inherits_agent_attributes(self) -> None:
        span = _make_span(
            "execute_tool",
            {
                "gen_ai.operation.name": "execute_tool",
                "gen_ai.tool.name": "calculator",
                "gen_ai.agent.id": "agent-7",
                "gen_ai.agent.name": "math_agent",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        step = trace.steps[0]
        assert step.agent_id == "agent-7"
        assert step.agent_role == "math_agent"

    def test_invoke_agent_step_records_agent_identity(self) -> None:
        span = _make_span(
            "invoke_agent",
            {
                "gen_ai.operation.name": "invoke_agent",
                "gen_ai.agent.id": "agent-99",
                "gen_ai.agent.name": "delegator",
            },
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        step = trace.steps[0]
        assert step.type == STEP_AGENT_CALL
        assert step.agent_id == "agent-99"
        assert step.agent_role == "delegator"

    def test_missing_agent_attributes_yield_none(self) -> None:
        span = _make_span(
            "chat",
            {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
        )
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        step = trace.steps[0]
        assert step.agent_id is None
        assert step.agent_role is None


class TestOTelAdapterClassifierStrictness:
    """Span name fallback is narrow: arbitrary substrings no longer match."""

    def test_arbitrary_span_name_with_chat_substring_does_not_match(self) -> None:
        # Old behavior: `"chat" in name.lower()` matched "rich-chat-history".
        # New behavior: only first token equality matches.
        span = _make_span("rich-chat-history", {"http.method": "GET"})
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert trace.steps == []

    def test_first_token_chat_classified_as_llm_call(self) -> None:
        # OTel legacy span name format: `chat <model>`.
        span = _make_span("chat gpt-4.1", {})
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert len(trace.steps) == 1
        assert trace.steps[0].type == STEP_LLM_CALL

    def test_completion_attribute_alone_classifies_as_llm(self) -> None:
        # Producer that emits gen_ai.completion but omits operation.name.
        span = _make_span("anonymous-span", {"gen_ai.completion": "result"})
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert len(trace.steps) == 1
        assert trace.steps[0].type == STEP_LLM_CALL

    def test_agent_attribute_without_operation_classifies_as_agent(self) -> None:
        span = _make_span("anonymous-span", {"gen_ai.agent.name": "researcher"})
        with _otel_available():
            trace = OTelAdapter.from_spans([span])
        assert len(trace.steps) == 1
        assert trace.steps[0].type == STEP_AGENT_CALL


class TestRequireNumericAttr:
    """Tests for _require_numeric_attr helper used to read OTel usage counters."""

    def test_missing_key_returns_zero(self) -> None:
        assert _require_numeric_attr({}, "gen_ai.usage.input_tokens") == 0

    def test_none_value_returns_zero(self) -> None:
        assert (
            _require_numeric_attr({"gen_ai.usage.input_tokens": None}, "gen_ai.usage.input_tokens")
            == 0
        )

    def test_int_value_returned_unchanged(self) -> None:
        assert (
            _require_numeric_attr({"gen_ai.usage.input_tokens": 42}, "gen_ai.usage.input_tokens")
            == 42
        )

    def test_float_value_truncated_to_int(self) -> None:
        assert (
            _require_numeric_attr({"gen_ai.usage.input_tokens": 3.7}, "gen_ai.usage.input_tokens")
            == 3
        )

    def test_bool_value_rejected(self) -> None:
        with pytest.raises(TypeError, match="must be numeric"):
            _require_numeric_attr({"gen_ai.usage.input_tokens": True}, "gen_ai.usage.input_tokens")

    def test_sequence_value_rejected(self) -> None:
        with pytest.raises(TypeError, match="must be numeric"):
            _require_numeric_attr(
                {"gen_ai.usage.input_tokens": [1, 2]}, "gen_ai.usage.input_tokens"
            )

    def test_string_value_rejected(self) -> None:
        with pytest.raises(TypeError, match="must be numeric"):
            _require_numeric_attr({"gen_ai.usage.input_tokens": "5"}, "gen_ai.usage.input_tokens")

    def test_error_message_names_offending_key_and_type(self) -> None:
        with pytest.raises(TypeError) as exc:
            _require_numeric_attr(
                {"gen_ai.usage.output_tokens": [1.0]}, "gen_ai.usage.output_tokens"
            )
        assert "gen_ai.usage.output_tokens" in str(exc.value)
        assert "list" in str(exc.value)


class TestFromOtelSpanDictsValidation:
    """Strict validation of span dict shape: malformed input fails loud."""

    def test_empty_input_yields_empty_trace(self) -> None:
        with _otel_available():
            trace = OTelAdapter.from_otel_span_dicts([])
        assert trace.steps == []

    def test_invalid_trace_id_hex_raises(self) -> None:
        bad_dict = {
            "name": "chat",
            "trace_id": "not-hex-zzz",
            "span_id": "0000000000000001",
            "attributes": {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
            "start_time_unix_nano": 0,
            "end_time_unix_nano": 1_000_000,
        }
        with _otel_available(), pytest.raises(ValueError, match="trace_id"):
            OTelAdapter.from_otel_span_dicts([bad_dict])

    def test_non_string_trace_id_raises(self) -> None:
        bad_dict = {
            "name": "chat",
            "trace_id": 12345,
            "span_id": "0000000000000001",
            "attributes": {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
            "start_time_unix_nano": 0,
            "end_time_unix_nano": 1_000_000,
        }
        with _otel_available(), pytest.raises(ValueError, match="trace_id"):
            OTelAdapter.from_otel_span_dicts([bad_dict])

    def test_invalid_parent_span_id_raises(self) -> None:
        bad_dict = {
            "name": "chat",
            "trace_id": "deadbeefdeadbeefdeadbeefdeadbeef",
            "span_id": "0000000000000002",
            "parent_span_id": "not-hex",
            "attributes": {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
            "start_time_unix_nano": 0,
            "end_time_unix_nano": 1_000_000,
        }
        with _otel_available(), pytest.raises(ValueError, match="parent_span_id"):
            OTelAdapter.from_otel_span_dicts([bad_dict])

    def test_non_mapping_attributes_raises(self) -> None:
        bad_dict = {
            "name": "chat",
            "trace_id": "deadbeefdeadbeefdeadbeefdeadbeef",
            "span_id": "0000000000000001",
            "attributes": ["not", "a", "dict"],
            "start_time_unix_nano": 0,
            "end_time_unix_nano": 1_000_000,
        }
        with _otel_available(), pytest.raises(ValueError, match="attributes"):
            OTelAdapter.from_otel_span_dicts([bad_dict])

    def test_missing_trace_id_treated_as_empty(self) -> None:
        # Missing/empty trace_id is allowed (some producers omit it on
        # in-process spans). The resulting Trace just won't carry an
        # otel_-prefixed trace id.
        dict_span = {
            "name": "chat",
            "span_id": "0000000000000001",
            "attributes": {"gen_ai.operation.name": "chat", "gen_ai.completion": "ok"},
            "start_time_unix_nano": 0,
            "end_time_unix_nano": 1_000_000,
        }
        with _otel_available():
            trace = OTelAdapter.from_otel_span_dicts([dict_span])
        assert len(trace.steps) == 1


class TestExtractLlmStepTokenStrictness:
    """_extract_llm_step routes token attrs through _require_numeric_attr."""

    def test_non_numeric_input_tokens_raises_typed_error(self) -> None:
        span = _make_span(
            "chat",
            {
                "gen_ai.operation.name": "chat",
                "gen_ai.completion": "ok",
                "gen_ai.usage.input_tokens": ["bad"],
                "gen_ai.usage.output_tokens": 5,
            },
        )
        with _otel_available(), pytest.raises(TypeError, match="must be numeric"):
            OTelAdapter.from_spans([span])


class TestToOtelSpansExport:
    """to_otel_spans produces OTLP-compatible dicts with gen_ai.* attributes."""

    def test_empty_trace_yields_empty_spans(self) -> None:
        trace = TraceBuilder().set_input(prompt="hi").set_output(message="ok").build()
        assert to_otel_spans(trace) == []

    def test_llm_step_emits_chat_span_with_model(self) -> None:
        trace = (
            TraceBuilder()
            .add_llm_call(
                name="chat gpt-4.1",
                args={"model": "gpt-4.1"},
                result={
                    "completion": "hello",
                    "input_tokens": 12,
                    "output_tokens": 4,
                    "model": "gpt-4.1",
                },
                metadata={"duration_ms": 150},
            )
            .set_output(message="hello")
            .build()
        )
        spans = to_otel_spans(trace)
        assert len(spans) == 1
        attrs = spans[0]["attributes"]
        assert attrs["gen_ai.operation.name"] == "chat"
        assert attrs["gen_ai.request.model"] == "gpt-4.1"
        assert attrs["gen_ai.response.model"] == "gpt-4.1"
        assert attrs["gen_ai.completion"] == "hello"
        assert attrs["gen_ai.usage.input_tokens"] == 12
        assert attrs["gen_ai.usage.output_tokens"] == 4
        # Duration encoded in nanoseconds
        assert spans[0]["end_time_unix_nano"] - spans[0]["start_time_unix_nano"] == 150_000_000

    def test_tool_step_emits_execute_tool_span(self) -> None:
        trace = (
            TraceBuilder()
            .add_tool_call(
                name="web_search",
                args={"call_id": "c1", "parameters": {"q": "Paris"}},
                result={"output": "results"},
            )
            .set_output(message="done")
            .build()
        )
        spans = to_otel_spans(trace)
        assert spans[0]["attributes"]["gen_ai.operation.name"] == "execute_tool"
        assert spans[0]["attributes"]["gen_ai.tool.name"] == "web_search"
        assert spans[0]["attributes"]["gen_ai.tool.call.id"] == "c1"

    def test_agent_attributes_round_trip_via_step_fields(self) -> None:
        trace = (
            TraceBuilder()
            .add_llm_call(
                name="chat",
                args={"model": "gpt-4.1"},
                result={"completion": "ok"},
                agent_id="agent-42",
                agent_role="support",
            )
            .set_output(message="ok")
            .build()
        )
        attrs = to_otel_spans(trace)[0]["attributes"]
        assert attrs["gen_ai.agent.id"] == "agent-42"
        assert attrs["gen_ai.agent.name"] == "support"

    def test_operation_override_preserved(self) -> None:
        # Producer ingested an embeddings span; export should preserve op name.
        trace = (
            TraceBuilder()
            .add_llm_call(
                name="embeddings text-embedding-3-small",
                args={"operation": "embeddings", "model": "text-embedding-3-small"},
                result={},
            )
            .set_output(message="")
            .build()
        )
        attrs = to_otel_spans(trace)[0]["attributes"]
        assert attrs["gen_ai.operation.name"] == "embeddings"

    def test_parent_span_id_chain(self) -> None:
        # First span is root (no parent); subsequent spans share root as parent.
        trace = (
            TraceBuilder()
            .add_llm_call(
                name="chat", args={"model": "gpt-4.1"}, result={"completion": "calling tool"}
            )
            .add_tool_call(name="search", args={}, result={"output": "x"})
            .set_output(message="x")
            .build()
        )
        spans = to_otel_spans(trace)
        assert spans[0]["parent_span_id"] is None
        assert spans[1]["parent_span_id"] == spans[0]["span_id"]


class TestOtelRoundTrip:
    """to_otel_spans → OTelAdapter.from_otel_span_dicts preserves step structure."""

    def test_round_trip_preserves_step_types(self) -> None:
        original = (
            TraceBuilder()
            .add_llm_call(
                name="chat gpt-4.1",
                args={"model": "gpt-4.1"},
                result={"completion": "hi", "input_tokens": 5, "output_tokens": 2},
                metadata={"duration_ms": 100},
            )
            .add_tool_call(
                name="search",
                args={"call_id": "c1", "parameters": {"q": "Paris"}},
                result={"output": "results"},
                metadata={"duration_ms": 50},
            )
            .set_output(message="hi")
            .build()
        )
        spans = to_otel_spans(original)
        with _otel_available():
            roundtripped = OTelAdapter.from_otel_span_dicts(spans)

        assert [s.type for s in roundtripped.steps] == [STEP_LLM_CALL, STEP_TOOL_CALL]
        assert roundtripped.steps[1].name == "search"
        assert roundtripped.steps[0].result is not None
        assert roundtripped.steps[0].result.get("completion") == "hi"
        assert roundtripped.steps[0].result.get("input_tokens") == 5

    def test_round_trip_preserves_agent_hierarchy(self) -> None:
        original = (
            TraceBuilder()
            .add_llm_call(
                name="chat",
                args={"model": "gpt-4.1"},
                result={"completion": "ok"},
                agent_id="agent-42",
                agent_role="support",
            )
            .set_output(message="ok")
            .build()
        )
        spans = to_otel_spans(original)
        with _otel_available():
            roundtripped = OTelAdapter.from_otel_span_dicts(spans)
        assert roundtripped.steps[0].agent_id == "agent-42"
        assert roundtripped.steps[0].agent_role == "support"

    def test_round_trip_preserves_invoke_agent_step(self) -> None:
        from attest._proto.types import Step

        original = (
            TraceBuilder()
            .add_step(
                Step(
                    type=STEP_AGENT_CALL,
                    name="researcher",
                    args={
                        "agent_id": "agent-99",
                        "agent_name": "researcher",
                        "description": "Research bot",
                    },
                    result={"completion": "found it"},
                    agent_id="agent-99",
                    agent_role="researcher",
                )
            )
            .set_output(message="found it")
            .build()
        )
        spans = to_otel_spans(original)
        with _otel_available():
            roundtripped = OTelAdapter.from_otel_span_dicts(spans)
        assert roundtripped.steps[0].type == STEP_AGENT_CALL
        assert roundtripped.steps[0].agent_id == "agent-99"

    def test_round_trip_preserves_retrieval_step(self) -> None:
        original = (
            TraceBuilder()
            .add_retrieval(
                name="vector_search",
                args={"query": "Paris", "source": "vector_store"},
                result={"documents_count": 3},
            )
            .set_output(message="ok")
            .build()
        )
        spans = to_otel_spans(original)
        with _otel_available():
            roundtripped = OTelAdapter.from_otel_span_dicts(spans)
        assert roundtripped.steps[0].type == STEP_RETRIEVAL
        assert roundtripped.steps[0].args is not None
        assert roundtripped.steps[0].args.get("query") == "Paris"
        assert roundtripped.steps[0].result is not None
        assert roundtripped.steps[0].result.get("documents_count") == 3
